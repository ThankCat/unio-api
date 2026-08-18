package auth

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"math/big"
	"net/netip"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"

	"github.com/ThankCat/unio-gateway/internal/service/appsettings"
	consoleservice "github.com/ThankCat/unio-gateway/internal/service/console"
)

const (
	challengeTTL    = 10 * time.Minute
	reservationTTL  = 30 * time.Second
	maxCodeAttempts = 5
)

// Purpose identifies the workflow allowed to consume a verification challenge.
type Purpose string

const (
	// PurposeRegister verifies ownership during account registration.
	PurposeRegister Purpose = "register"
	// PurposeLogin verifies ownership during email-code login.
	PurposeLogin Purpose = "login"
	// PurposePasswordReset verifies ownership during password reset.
	PurposePasswordReset Purpose = "password_reset"
)

// ParsePurpose validates a public verification-purpose value.
func ParsePurpose(raw string) (Purpose, *consoleservice.Error) {
	purpose := Purpose(raw)
	switch purpose {
	case PurposeRegister, PurposeLogin, PurposePasswordReset:
		return purpose, nil
	default:
		return "", &consoleservice.Error{Code: CodeInvalidPurpose, Message: "The verification purpose is invalid.", Param: "purpose", Status: 422}
	}
}

// Challenge is the public metadata returned after issuing a verification code.
type Challenge struct {
	ID          string `json:"challenge_id"`
	ExpiresIn   int64  `json:"expires_in"`
	ResendAfter int64  `json:"resend_after"`
}

// Reservation identifies an atomically reserved verification challenge.
type Reservation struct {
	ChallengeID   string
	ReservationID string
}

type watchRedis interface {
	redis.Cmdable
	Watch(ctx context.Context, fn func(*redis.Tx) error, keys ...string) error
}

// VerificationStore persists verification challenges and rolling counters in Redis.
type VerificationStore struct {
	redis     watchRedis
	keyNS     string
	secret    []byte
	fixedCode string
	settings  *appsettings.SettingsStore
	now       func() time.Time
}

// NewVerificationStore creates a Redis-backed verification store.
func NewVerificationStore(
	redisClient watchRedis,
	keyNS string,
	secret string,
	fixedCode string,
	settings *appsettings.SettingsStore,
) (*VerificationStore, error) {
	if redisClient == nil {
		return nil, errors.New("verification challenges require redis")
	}
	if len(secret) < 32 {
		return nil, errors.New("CONSOLE_AUTH_SECRET must contain at least 32 bytes")
	}
	return &VerificationStore{
		redis:     redisClient,
		keyNS:     keyNS,
		secret:    []byte(secret),
		fixedCode: fixedCode,
		settings:  settings,
		now:       time.Now,
	}, nil
}

// rollingWindowScript checks every applicable window before recording the event,
// so one request is either admitted to all counters or rejected by all of them.
var rollingWindowScript = redis.NewScript(`
local now = tonumber(ARGV[1])
local member = ARGV[2]
local worst_retry = 0
for i = 1, #KEYS do
  local window = tonumber(ARGV[2 + (i - 1) * 2 + 1])
  local limit = tonumber(ARGV[2 + (i - 1) * 2 + 2])
  redis.call('ZREMRANGEBYSCORE', KEYS[i], '-inf', now - window)
  local count = redis.call('ZCARD', KEYS[i])
  if count >= limit then
    local oldest = redis.call('ZRANGE', KEYS[i], 0, 0, 'WITHSCORES')
    local retry = 1
    if oldest[2] then
      retry = math.max(1, math.ceil((tonumber(oldest[2]) + window - now) / 1000))
    end
    if retry > worst_retry then worst_retry = retry end
  end
end
if worst_retry > 0 then return {0, worst_retry} end
for i = 1, #KEYS do
  local window = tonumber(ARGV[2 + (i - 1) * 2 + 1])
  redis.call('ZADD', KEYS[i], now, member)
  redis.call('PEXPIRE', KEYS[i], window + 60000)
end
return {1, 0}
`)

type counterRule struct {
	key    string
	window time.Duration
	limit  int64
}

func (s *VerificationStore) applyLimits(ctx context.Context, rules []counterRule) *consoleservice.Error {
	keys := make([]string, 0, len(rules))
	args := make([]any, 0, 2+len(rules)*2)
	args = append(args, s.now().UnixMilli(), uuid.NewString())
	for _, rule := range rules {
		keys = append(keys, rule.key)
		args = append(args, rule.window.Milliseconds(), rule.limit)
	}
	result, err := rollingWindowScript.Run(ctx, s.redis, keys, args...).Result()
	if err != nil {
		return requestUnavailable("apply verification rate limits", err)
	}
	values, ok := result.([]any)
	if !ok || len(values) != 2 {
		return requestUnavailable("decode verification rate-limit result", fmt.Errorf("unexpected result %#v", result))
	}
	allowed, _ := values[0].(int64)
	if allowed == 1 {
		return nil
	}
	retryAfter := 1
	if value, ok := values[1].(int64); ok && value > 0 {
		retryAfter = int(value)
	}
	return &consoleservice.Error{
		Code:       CodeVerificationRateLimited,
		Message:    "Too many requests. Please try again later.",
		Status:     429,
		RetryAfter: retryAfter,
	}
}

// issueChallengeScript supersedes the previous challenge and publishes its
// replacement without exposing an interval in which both are current.
var issueChallengeScript = redis.NewScript(`
local old = redis.call('GET', KEYS[1])
if old then
  redis.call('HSET', ARGV[1] .. old, 'status', 'superseded')
end
redis.call('HSET', KEYS[2],
  'purpose', ARGV[2],
  'email_hmac', ARGV[3],
  'code_digest', ARGV[4],
  'issued_at_ms', ARGV[5],
  'attempt_count', 0,
  'status', 'active')
redis.call('PEXPIRE', KEYS[2], ARGV[6])
redis.call('SET', KEYS[1], ARGV[7], 'PX', ARGV[6])
return old or ''
`)

// Issue applies send limits and stores a new purpose-bound challenge.
func (s *VerificationStore) Issue(ctx context.Context, email string, purpose Purpose, ip string) (Challenge, *consoleservice.Error) {
	limits := appsettings.AuthVerificationRateLimits(ctx, s.settings)
	emailID := s.identifier("email", email)
	ipIDs := s.ipIdentifiers(ip)
	rules := make([]counterRule, 0, 16)
	rules = appendRules(rules, s.counterPrefix("send", "email_purpose", string(purpose)+":"+emailID), limits.SendEmailPurpose)
	rules = appendRules(rules, s.counterPrefix("send", "email_all", emailID), limits.SendEmailAll)
	for _, ipID := range ipIDs {
		rules = appendRules(rules, s.counterPrefix("send", "ip_purpose", string(purpose)+":"+ipID), limits.SendIPPurpose)
		rules = appendRules(rules, s.counterPrefix("send", "ip_all", ipID), limits.SendIPAll)
	}
	if err := s.applyLimits(ctx, rules); err != nil {
		return Challenge{}, err
	}

	challengeID := "vch_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	code, err := s.newCode()
	if err != nil {
		return Challenge{}, requestUnavailable("generate verification code", err)
	}
	digest := s.codeDigest(challengeID, purpose, email, code)
	now := s.now().UnixMilli()
	currentKey := s.currentKey(purpose, emailID)
	challengeKey := s.challengeKey(challengeID)
	if _, err := issueChallengeScript.Run(
		ctx,
		s.redis,
		[]string{currentKey, challengeKey},
		s.challengePrefix(),
		string(purpose),
		emailID,
		digest,
		now,
		challengeTTL.Milliseconds(),
		challengeID,
	).Result(); err != nil {
		return Challenge{}, requestUnavailable("store verification challenge", err)
	}
	return Challenge{ID: challengeID, ExpiresIn: int64(challengeTTL.Seconds()), ResendAfter: 30}, nil
}

// Reserve validates a code and atomically reserves its challenge for one workflow.
func (s *VerificationStore) Reserve(
	ctx context.Context,
	email string,
	purpose Purpose,
	ip string,
	challengeID string,
	code string,
) (Reservation, *consoleservice.Error) {
	if len(code) != 6 {
		return Reservation{}, &consoleservice.Error{Code: CodeVerificationCodeFormatInvalid, Message: "The verification code must contain exactly six digits.", Param: "code", Status: 422}
	}
	for _, ch := range code {
		if ch < '0' || ch > '9' {
			return Reservation{}, &consoleservice.Error{Code: CodeVerificationCodeFormatInvalid, Message: "The verification code must contain exactly six digits.", Param: "code", Status: 422}
		}
	}
	limits := appsettings.AuthVerificationRateLimits(ctx, s.settings)
	emailID := s.identifier("email", email)
	rules := appendRules(nil, s.counterPrefix("verify", "email_purpose", string(purpose)+":"+emailID), limits.VerifyEmailPurpose)
	for _, ipID := range s.ipIdentifiers(ip) {
		rules = appendRules(rules, s.counterPrefix("verify", "ip_all", ipID), limits.VerifyIPAll)
	}
	if err := s.applyLimits(ctx, rules); err != nil {
		return Reservation{}, err
	}

	challengeKey := s.challengeKey(challengeID)
	currentKey := s.currentKey(purpose, emailID)
	reservationID := uuid.NewString()
	for attempt := 0; attempt < 4; attempt++ {
		var resultErr *consoleservice.Error
		err := s.redis.Watch(ctx, func(tx *redis.Tx) error {
			fields, err := tx.HGetAll(ctx, challengeKey).Result()
			if err != nil {
				return err
			}
			if len(fields) == 0 {
				resultErr = challengeUnavailable()
				return nil
			}
			currentID, err := tx.Get(ctx, currentKey).Result()
			if err != nil && !errors.Is(err, redis.Nil) {
				return err
			}
			if currentID != challengeID || fields["purpose"] != string(purpose) || fields["email_hmac"] != emailID {
				resultErr = challengeUnavailable()
				return nil
			}
			status := fields["status"]
			if status == "reserved" {
				reservedAt, _ := strconv.ParseInt(fields["reserved_at_ms"], 10, 64)
				if s.now().UnixMilli()-reservedAt <= reservationTTL.Milliseconds() {
					resultErr = challengeUnavailable()
					return nil
				}
				status = "active"
			}
			if status == "exhausted" {
				resultErr = &consoleservice.Error{Code: CodeVerificationAttemptsExhausted, Message: "The verification challenge has no attempts remaining.", Status: 409}
				return nil
			}
			if status != "active" {
				resultErr = challengeUnavailable()
				return nil
			}
			attemptCount, _ := strconv.Atoi(fields["attempt_count"])
			if attemptCount >= maxCodeAttempts {
				resultErr = &consoleservice.Error{Code: CodeVerificationAttemptsExhausted, Message: "The verification challenge has no attempts remaining.", Status: 409}
				return nil
			}
			gotDigest := s.codeDigest(challengeID, purpose, email, code)
			if !hmac.Equal([]byte(gotDigest), []byte(fields["code_digest"])) {
				attemptCount++
				updates := map[string]any{"attempt_count": attemptCount}
				if attemptCount >= maxCodeAttempts {
					updates["status"] = "exhausted"
				}
				_, err = tx.TxPipelined(ctx, func(pipe redis.Pipeliner) error {
					pipe.HSet(ctx, challengeKey, updates)
					return nil
				})
				if err != nil {
					return err
				}
				if attemptCount >= maxCodeAttempts {
					resultErr = &consoleservice.Error{Code: CodeVerificationAttemptsExhausted, Message: "The verification challenge has no attempts remaining.", Status: 409}
				} else {
					resultErr = &consoleservice.Error{Code: CodeVerificationCodeInvalid, Message: "The verification code is incorrect.", Param: "code", Status: 422}
				}
				return nil
			}
			_, err = tx.TxPipelined(ctx, func(pipe redis.Pipeliner) error {
				pipe.HSet(ctx, challengeKey, map[string]any{
					"status":         "reserved",
					"reservation_id": reservationID,
					"reserved_at_ms": s.now().UnixMilli(),
				})
				return nil
			})
			return err
		}, challengeKey, currentKey)
		if errors.Is(err, redis.TxFailedErr) {
			continue
		}
		if err != nil {
			return Reservation{}, requestUnavailable("reserve verification challenge", err)
		}
		if resultErr != nil {
			return Reservation{}, resultErr
		}
		return Reservation{ChallengeID: challengeID, ReservationID: reservationID}, nil
	}
	return Reservation{}, requestUnavailable("reserve verification challenge", redis.TxFailedErr)
}

// finishReservationScript applies a reservation result only when its owner still
// matches, preventing an expired worker from consuming a newer reservation.
var finishReservationScript = redis.NewScript(`
if redis.call('HGET', KEYS[1], 'status') ~= 'reserved' then return 0 end
if redis.call('HGET', KEYS[1], 'reservation_id') ~= ARGV[1] then return 0 end
redis.call('HSET', KEYS[1], 'status', ARGV[2])
redis.call('HDEL', KEYS[1], 'reservation_id', 'reserved_at_ms')
if ARGV[2] == 'consumed' and redis.call('GET', KEYS[2]) == ARGV[3] then
  redis.call('DEL', KEYS[2])
end
return 1
`)

// Commit marks a reserved challenge as consumed.
func (s *VerificationStore) Commit(ctx context.Context, email string, purpose Purpose, reservation Reservation) *consoleservice.Error {
	return s.finishReservation(ctx, email, purpose, reservation, "consumed")
}

// Release returns a reserved challenge to the active state after a failed workflow.
func (s *VerificationStore) Release(ctx context.Context, email string, purpose Purpose, reservation Reservation) *consoleservice.Error {
	return s.finishReservation(ctx, email, purpose, reservation, "active")
}

func (s *VerificationStore) finishReservation(
	ctx context.Context,
	email string,
	purpose Purpose,
	reservation Reservation,
	status string,
) *consoleservice.Error {
	emailID := s.identifier("email", email)
	result, err := finishReservationScript.Run(
		ctx,
		s.redis,
		[]string{s.challengeKey(reservation.ChallengeID), s.currentKey(purpose, emailID)},
		reservation.ReservationID,
		status,
		reservation.ChallengeID,
	).Int64()
	if err != nil {
		return requestUnavailable("finish verification reservation", err)
	}
	if result != 1 {
		return challengeUnavailable()
	}
	return nil
}

func appendRules(dst []counterRule, prefix string, rules []appsettings.RateLimitRule) []counterRule {
	for _, rule := range rules {
		dst = append(dst, counterRule{
			key:    fmt.Sprintf("%s:%d", prefix, rule.WindowSeconds),
			window: time.Duration(rule.WindowSeconds) * time.Second,
			limit:  rule.Limit,
		})
	}
	return dst
}

func (s *VerificationStore) newCode() (string, error) {
	if s.fixedCode != "" {
		return s.fixedCode, nil
	}
	n, err := rand.Int(rand.Reader, big.NewInt(1_000_000))
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%06d", n.Int64()), nil
}

func (s *VerificationStore) codeDigest(challengeID string, purpose Purpose, email, code string) string {
	mac := hmac.New(sha256.New, s.secret)
	_, _ = fmt.Fprintf(mac, "verification-code\x00%s\x00%s\x00%s\x00%s", challengeID, purpose, email, code)
	return hex.EncodeToString(mac.Sum(nil))
}

func (s *VerificationStore) identifier(kind, value string) string {
	mac := hmac.New(sha256.New, s.secret)
	_, _ = fmt.Fprintf(mac, "verification-identifier\x00%s\x00%s", kind, value)
	return hex.EncodeToString(mac.Sum(nil))
}

func (s *VerificationStore) ipIdentifiers(ip string) []string {
	values := []string{ip}
	if address, err := netip.ParseAddr(ip); err == nil && address.Is6() {
		values = append(values, netip.PrefixFrom(address, 64).Masked().String())
	}
	out := make([]string, 0, len(values))
	for _, value := range values {
		out = append(out, s.identifier("ip", value))
	}
	return out
}

func (s *VerificationStore) challengePrefix() string {
	return fmt.Sprintf("%s:console:auth:challenge:", s.keyNS)
}

func (s *VerificationStore) challengeKey(id string) string {
	return s.challengePrefix() + id
}

func (s *VerificationStore) currentKey(purpose Purpose, emailID string) string {
	return fmt.Sprintf("%s:console:auth:challenge_current:%s:%s", s.keyNS, purpose, emailID)
}

func (s *VerificationStore) counterPrefix(action, dimension, subject string) string {
	return fmt.Sprintf("%s:console:auth:rate:%s:%s:%s", s.keyNS, action, dimension, subject)
}

func challengeUnavailable() *consoleservice.Error {
	return &consoleservice.Error{
		Code:    CodeVerificationChallengeUnavailable,
		Message: "The verification challenge is missing, expired, or unavailable.",
		Status:  410,
	}
}
