package auth

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"

	consoleservice "github.com/ThankCat/unio-gateway/internal/service/console"
)

const passwordResetGrantTTL = 10 * time.Minute

// PasswordResetGrant 是邮箱所有权验证通过后签发的短期一次性凭证。
type PasswordResetGrant struct {
	Token     string `json:"reset_token"`
	ExpiresIn int64  `json:"expires_in"`
}

type passwordResetGrantReservation struct {
	Key           string
	ReservationID string
	UserUID       string
}

var issuePasswordResetGrantScript = redis.NewScript(`
if redis.call('HGET', KEYS[1], 'status') ~= 'reserved' then return 0 end
if redis.call('HGET', KEYS[1], 'reservation_id') ~= ARGV[1] then return 0 end
if redis.call('EXISTS', KEYS[3]) ~= 0 then return -1 end
redis.call('HSET', KEYS[1], 'status', 'consumed')
redis.call('HDEL', KEYS[1], 'reservation_id', 'reserved_at_ms')
if redis.call('GET', KEYS[2]) == ARGV[2] then
  redis.call('DEL', KEYS[2])
end
redis.call('HSET', KEYS[3],
  'status', 'active',
  'user_uid', ARGV[3],
  'issued_at_ms', ARGV[4])
redis.call('PEXPIRE', KEYS[3], ARGV[5])
return 1
`)

// IssuePasswordResetGrant 消费已预占的密码重置挑战，并原子替换为短期重置凭证。
func (s *VerificationStore) IssuePasswordResetGrant(
	ctx context.Context,
	email string,
	reservation Reservation,
	userUID string,
) (PasswordResetGrant, *consoleservice.Error) {
	tokenBytes := make([]byte, 32)
	if _, err := rand.Read(tokenBytes); err != nil {
		return PasswordResetGrant{}, requestUnavailable("generate password reset credential", err)
	}
	token := "prt_" + base64.RawURLEncoding.EncodeToString(tokenBytes)
	emailID := s.identifier("email", email)
	result, err := issuePasswordResetGrantScript.Run(
		ctx,
		s.redis,
		[]string{
			s.challengeKey(reservation.ChallengeID),
			s.currentKey(PurposePasswordReset, emailID),
			s.passwordResetGrantKey(token),
		},
		reservation.ReservationID,
		reservation.ChallengeID,
		userUID,
		s.now().UnixMilli(),
		passwordResetGrantTTL.Milliseconds(),
	).Int64()
	if err != nil {
		return PasswordResetGrant{}, requestUnavailable("issue password reset credential", err)
	}
	if result == 0 {
		return PasswordResetGrant{}, challengeUnavailable()
	}
	if result != 1 {
		return PasswordResetGrant{}, requestUnavailable("issue password reset credential", fmt.Errorf("credential key collision"))
	}
	return PasswordResetGrant{Token: token, ExpiresIn: int64(passwordResetGrantTTL.Seconds())}, nil
}

var reservePasswordResetGrantScript = redis.NewScript(`
local status = redis.call('HGET', KEYS[1], 'status')
if status == 'reserved' then
  local reserved_at = tonumber(redis.call('HGET', KEYS[1], 'reserved_at_ms') or '0')
  if tonumber(ARGV[2]) - reserved_at <= tonumber(ARGV[3]) then return '' end
  status = 'active'
end
if status ~= 'active' then return '' end
local user_uid = redis.call('HGET', KEYS[1], 'user_uid')
if not user_uid then return '' end
redis.call('HSET', KEYS[1],
  'status', 'reserved',
  'reservation_id', ARGV[1],
  'reserved_at_ms', ARGV[2])
return user_uid
`)

// ReservePasswordResetGrant 预占重置凭证，使其在明确提交或释放前无法被并发请求使用。
func (s *VerificationStore) ReservePasswordResetGrant(
	ctx context.Context,
	token string,
) (passwordResetGrantReservation, *consoleservice.Error) {
	if !validPasswordResetToken(token) {
		return passwordResetGrantReservation{}, passwordResetTokenUnavailable()
	}
	key := s.passwordResetGrantKey(token)
	reservationID := uuid.NewString()
	userUID, err := reservePasswordResetGrantScript.Run(
		ctx,
		s.redis,
		[]string{key},
		reservationID,
		s.now().UnixMilli(),
		reservationTTL.Milliseconds(),
	).Text()
	if err != nil && err != redis.Nil {
		return passwordResetGrantReservation{}, requestUnavailable("reserve password reset credential", err)
	}
	if userUID == "" || err == redis.Nil {
		return passwordResetGrantReservation{}, passwordResetTokenUnavailable()
	}
	return passwordResetGrantReservation{Key: key, ReservationID: reservationID, UserUID: userUID}, nil
}

var finishPasswordResetGrantScript = redis.NewScript(`
if redis.call('HGET', KEYS[1], 'status') ~= 'reserved' then return 0 end
if redis.call('HGET', KEYS[1], 'reservation_id') ~= ARGV[1] then return 0 end
if ARGV[2] == 'consumed' then
  redis.call('DEL', KEYS[1])
else
  redis.call('HSET', KEYS[1], 'status', 'active')
  redis.call('HDEL', KEYS[1], 'reservation_id', 'reserved_at_ms')
end
return 1
`)

// CommitPasswordResetGrant 永久消费已预占的重置凭证。
func (s *VerificationStore) CommitPasswordResetGrant(ctx context.Context, reservation passwordResetGrantReservation) *consoleservice.Error {
	return s.finishPasswordResetGrant(ctx, reservation, "consumed")
}

// ReleasePasswordResetGrant 在密码更新失败且账户状态未改变时释放预占凭证，使其可再次使用。
func (s *VerificationStore) ReleasePasswordResetGrant(ctx context.Context, reservation passwordResetGrantReservation) *consoleservice.Error {
	return s.finishPasswordResetGrant(ctx, reservation, "active")
}

func (s *VerificationStore) finishPasswordResetGrant(
	ctx context.Context,
	reservation passwordResetGrantReservation,
	status string,
) *consoleservice.Error {
	result, err := finishPasswordResetGrantScript.Run(
		ctx,
		s.redis,
		[]string{reservation.Key},
		reservation.ReservationID,
		status,
	).Int64()
	if err != nil {
		return requestUnavailable("finish password reset credential", err)
	}
	if result != 1 {
		return passwordResetTokenUnavailable()
	}
	return nil
}

func validPasswordResetToken(token string) bool {
	if !strings.HasPrefix(token, "prt_") || len(token) != 47 {
		return false
	}
	_, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(token, "prt_"))
	return err == nil
}

func (s *VerificationStore) passwordResetGrantKey(token string) string {
	tokenID := s.identifier("password-reset-token", token)
	return fmt.Sprintf("%s:console:auth:password_reset_grant:%s", s.keyNS, tokenID)
}
