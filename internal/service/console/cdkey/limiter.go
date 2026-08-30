package cdkey

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/netip"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"

	consoleservice "github.com/ThankCat/unio-gateway/internal/service/console"
)

// The limits intentionally apply to failed attempts only. A successful
// redemption clears both counters, while an invalid/revoked/already-used key
// consumes one slot in each applicable rolling window.
const (
	DefaultCDKeyRateWindow       = 10 * time.Minute
	DefaultCDKeyUserFailureLimit = int64(10)
	DefaultCDKeyIPFailureLimit   = int64(30)
)

// RateLimiter protects the online CDKEY guess surface. Identifiers are HMACed
// before they become Redis keys so operational tooling cannot recover user IDs
// or source addresses from key names.
type RateLimiter struct {
	redis     redis.Cmdable
	keyNS     string
	secret    []byte
	window    time.Duration
	userLimit int64
	ipLimit   int64
	now       func() time.Time
}

// NewRateLimiter creates the default Console CDKEY failure limiter.
func NewRateLimiter(redisClient redis.Cmdable, keyNS, secret string) (*RateLimiter, error) {
	if redisClient == nil {
		return nil, errors.New("CDKEY rate limiter requires redis")
	}
	if len(secret) < 32 {
		return nil, errors.New("CONSOLE_AUTH_SECRET must contain at least 32 bytes")
	}
	return &RateLimiter{
		redis:     redisClient,
		keyNS:     keyNS,
		secret:    []byte(secret),
		window:    DefaultCDKeyRateWindow,
		userLimit: DefaultCDKeyUserFailureLimit,
		ipLimit:   DefaultCDKeyIPFailureLimit,
		now:       time.Now,
	}, nil
}

// checkCDKeyRateLimitsScript atomically checks all dimensions and, when
// record=true, appends a failure event to every dimension. This is the same
// rolling-window shape used by Console authentication limits.
var checkCDKeyRateLimitsScript = redis.NewScript(`
local now = tonumber(ARGV[1])
local record = tonumber(ARGV[2])
local worst_retry = 0
for i = 1, #KEYS do
  local window = tonumber(ARGV[3 + (i - 1) * 2 + 1])
  local limit = tonumber(ARGV[3 + (i - 1) * 2 + 2])
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
if record == 1 then
  local member = ARGV[3]
  for i = 1, #KEYS do
    local window = tonumber(ARGV[3 + (i - 1) * 2 + 1])
    redis.call('ZADD', KEYS[i], now, member .. ':' .. i)
    redis.call('PEXPIRE', KEYS[i], window + 60000)
  end
end
return {1, 0}
`)

type rateRule struct {
	key    string
	window time.Duration
	limit  int64
}

// Check rejects a request if either the user or source-IP failure window is
// exhausted. It does not consume a slot.
func (l *RateLimiter) Check(ctx context.Context, userID int64, ip string) *consoleservice.Error {
	return l.apply(ctx, userID, ip, false)
}

// RecordFailure atomically checks and records one failed attempt.
func (l *RateLimiter) RecordFailure(ctx context.Context, userID int64, ip string) *consoleservice.Error {
	return l.apply(ctx, userID, ip, true)
}

// Reset clears counters after a successful redemption. A reset failure is
// returned to the caller so the HTTP layer can decide whether to fail closed;
// the Service currently ignores it after the balance transaction commits.
func (l *RateLimiter) Reset(ctx context.Context, userID int64, ip string) *consoleservice.Error {
	keys := l.keys(userID, ip)
	if len(keys) == 0 {
		return nil
	}
	if err := l.redis.Del(ctx, keys...).Err(); err != nil {
		return consoleservice.RequestUnavailable("reset CDKEY rate limits", err)
	}
	return nil
}

func (l *RateLimiter) apply(ctx context.Context, userID int64, ip string, record bool) *consoleservice.Error {
	if l == nil || l.redis == nil {
		return nil
	}
	if userID <= 0 {
		return nil
	}
	rules := l.rules(userID, ip)
	if len(rules) == 0 {
		return nil
	}
	keys := make([]string, 0, len(rules))
	args := make([]any, 0, 3+len(rules)*2)
	args = append(args, l.now().UnixMilli())
	if record {
		args = append(args, int64(1), uuid.NewString())
	} else {
		args = append(args, int64(0), "")
	}
	for _, rule := range rules {
		keys = append(keys, rule.key)
		args = append(args, rule.window.Milliseconds(), rule.limit)
	}
	result, err := checkCDKeyRateLimitsScript.Run(ctx, l.redis, keys, args...).Result()
	if err != nil {
		return consoleservice.RequestUnavailable("check CDKEY rate limits", err)
	}
	values, ok := result.([]any)
	if !ok || len(values) != 2 {
		return consoleservice.RequestUnavailable("decode CDKEY rate-limit result", fmt.Errorf("unexpected result %#v", result))
	}
	allowed, ok := values[0].(int64)
	if !ok {
		return consoleservice.RequestUnavailable("decode CDKEY rate-limit result", fmt.Errorf("unexpected allowed value %#v", values[0]))
	}
	if allowed == 1 {
		return nil
	}
	retryAfter := 1
	if value, ok := values[1].(int64); ok && value > 0 {
		retryAfter = int(value)
	}
	return &consoleservice.Error{
		Code:       CodeCDKeyRateLimit,
		Message:    "Too many CDKEY attempts. Please try again later.",
		Status:     429,
		RetryAfter: retryAfter,
	}
}

func (l *RateLimiter) rules(userID int64, ip string) []rateRule {
	rules := []rateRule{{
		key:    l.counterKey("user", l.identifier("user", formatInt64(userID))),
		window: l.window,
		limit:  l.userLimit,
	}}
	normalizedIP := normalizeRateLimitIP(ip)
	// "unknown" is deliberately not shared as an IP bucket. Requests that
	// arrive without a trusted client address still receive user protection.
	if normalizedIP != "" && normalizedIP != "unknown" {
		rules = append(rules, rateRule{
			key:    l.counterKey("ip", l.identifier("ip", normalizedIP)),
			window: l.window,
			limit:  l.ipLimit,
		})
	}
	return rules
}

func (l *RateLimiter) keys(userID int64, ip string) []string {
	rules := l.rules(userID, ip)
	keys := make([]string, 0, len(rules))
	for _, rule := range rules {
		keys = append(keys, rule.key)
	}
	return keys
}

func (l *RateLimiter) identifier(kind, value string) string {
	mac := hmac.New(sha256.New, l.secret)
	_, _ = fmt.Fprintf(mac, "console-cdkey-rate\x00%s\x00%s", kind, value)
	return hex.EncodeToString(mac.Sum(nil))
}

func (l *RateLimiter) counterKey(dimension, subject string) string {
	return fmt.Sprintf("%s:console:cdkey:rate:%s:%s", l.keyNS, dimension, subject)
}

func normalizeRateLimitIP(ip string) string {
	ip = strings.TrimSpace(ip)
	if ip == "" {
		return "unknown"
	}
	if address, err := netip.ParseAddr(strings.Trim(ip, "[]")); err == nil {
		return address.String()
	}
	return ip
}
