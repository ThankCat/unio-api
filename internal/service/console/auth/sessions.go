package auth

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"

	consoleservice "github.com/ThankCat/unio-gateway/internal/service/console"
)

const (
	accessTokenType  = "access"
	refreshTokenType = "refresh"
)

type tokenClaims struct {
	SessionID string `json:"sid"`
	TokenType string `json:"token_type"`
	jwt.RegisteredClaims
}

// TokenPair 是写入安全浏览器 Cookie 的访问与刷新 JWT 令牌对。
type TokenPair struct {
	AccessToken  string
	RefreshToken string
	AccessTTL    time.Duration
	RefreshTTL   time.Duration
	SessionID    string
	UserUID      string
}

// SessionManager 在 Redis 中创建、轮换和吊销 Console 会话。
type SessionManager struct {
	redis      redis.Cmdable
	keyNS      string
	secret     []byte
	accessTTL  time.Duration
	refreshTTL time.Duration
	now        func() time.Time
}

// NewSessionManager 创建基于 Redis 的 JWT 会话管理器。
func NewSessionManager(redisClient redis.Cmdable, keyNS, secret string, accessTTL, refreshTTL time.Duration) (*SessionManager, error) {
	if redisClient == nil {
		return nil, errors.New("console sessions require redis")
	}
	if len(secret) < 32 {
		return nil, errors.New("CONSOLE_AUTH_SECRET must contain at least 32 bytes")
	}
	return &SessionManager{
		redis:      redisClient,
		keyNS:      keyNS,
		secret:     []byte(secret),
		accessTTL:  accessTTL,
		refreshTTL: refreshTTL,
		now:        time.Now,
	}, nil
}

// Create 存储刷新会话并返回首个令牌对。
func (m *SessionManager) Create(ctx context.Context, userUID string) (TokenPair, *consoleservice.Error) {
	if _, err := uuid.Parse(userUID); err != nil {
		return TokenPair{}, &consoleservice.Error{Code: CodeSessionInvalid, Message: "The user identifier is invalid.", Status: 401, Cause: err}
	}
	sid := uuid.NewString()
	refreshJTI := uuid.NewString()
	if err := m.redis.HSet(ctx, m.sessionKey(sid), map[string]any{
		"user_uid":    userUID,
		"refresh_jti": refreshJTI,
	}).Err(); err != nil {
		return TokenPair{}, requestUnavailable("create refresh session", err)
	}
	if err := m.redis.Expire(ctx, m.sessionKey(sid), m.refreshTTL).Err(); err != nil {
		return TokenPair{}, requestUnavailable("expire refresh session", err)
	}
	pipe := m.redis.Pipeline()
	pipe.SAdd(ctx, m.userSessionsKey(userUID), sid)
	pipe.Expire(ctx, m.userSessionsKey(userUID), m.refreshTTL)
	if _, err := pipe.Exec(ctx); err != nil {
		_ = m.redis.Del(ctx, m.sessionKey(sid)).Err()
		return TokenPair{}, requestUnavailable("index refresh session", err)
	}
	return m.issuePair(userUID, sid, refreshJTI)
}

// rotateRefreshScript 保证每个刷新 JTI 仅使用一次，并在同一原子操作中延长对应 Redis 会话。
var rotateRefreshScript = redis.NewScript(`
local current = redis.call('HGET', KEYS[1], 'refresh_jti')
local user_uid = redis.call('HGET', KEYS[1], 'user_uid')
if not current or not user_uid or current ~= ARGV[1] or user_uid ~= ARGV[3] then
  return 0
end
redis.call('HSET', KEYS[1], 'refresh_jti', ARGV[2])
redis.call('PEXPIRE', KEYS[1], ARGV[4])
return 1
`)

// Refresh 原子轮换刷新令牌并延长 Redis 会话。
func (m *SessionManager) Refresh(ctx context.Context, rawToken string) (TokenPair, *consoleservice.Error) {
	claims, err := m.parse(rawToken, refreshTokenType)
	if err != nil {
		return TokenPair{}, &consoleservice.Error{Code: CodeRefreshTokenInvalid, Message: "The refresh token is invalid, expired, or revoked.", Status: 401, Cause: err}
	}
	if _, err := uuid.Parse(claims.Subject); err != nil {
		return TokenPair{}, &consoleservice.Error{Code: CodeRefreshTokenInvalid, Message: "The refresh token is invalid, expired, or revoked.", Status: 401}
	}
	newJTI := uuid.NewString()
	result, err := rotateRefreshScript.Run(
		ctx,
		m.redis,
		[]string{m.sessionKey(claims.SessionID)},
		claims.ID,
		newJTI,
		claims.Subject,
		m.refreshTTL.Milliseconds(),
	).Int64()
	if err != nil {
		return TokenPair{}, requestUnavailable("rotate refresh session", err)
	}
	if result != 1 {
		return TokenPair{}, &consoleservice.Error{Code: CodeRefreshTokenInvalid, Message: "The refresh token is invalid, expired, or revoked.", Status: 401}
	}
	_ = m.redis.Expire(ctx, m.userSessionsKey(claims.Subject), m.refreshTTL).Err()
	return m.issuePair(claims.Subject, claims.SessionID, newJTI)
}

// Authenticate 校验访问令牌，并确认其 Redis 会话仍处于活跃状态。
// 用户退出或会话被吊销后，仅凭有效的 JWT 也不能通过认证。
func (m *SessionManager) Authenticate(ctx context.Context, rawToken string) (string, *consoleservice.Error) {
	claims, err := m.parse(rawToken, accessTokenType)
	if err != nil {
		return "", sessionInvalid(err)
	}
	if _, err := uuid.Parse(claims.Subject); err != nil {
		return "", sessionInvalid(err)
	}
	userUID, err := m.redis.HGet(ctx, m.sessionKey(claims.SessionID), "user_uid").Result()
	if errors.Is(err, redis.Nil) || (err == nil && userUID != claims.Subject) {
		return "", sessionInvalid(nil)
	}
	if err != nil {
		return "", requestUnavailable("read authenticated session", err)
	}
	return claims.Subject, nil
}

// Logout 吊销一个刷新会话；格式错误的令牌按已退出处理。
func (m *SessionManager) Logout(ctx context.Context, rawToken string) *consoleservice.Error {
	claims, err := m.parse(rawToken, refreshTokenType)
	if err != nil {
		return nil
	}
	pipe := m.redis.Pipeline()
	pipe.Del(ctx, m.sessionKey(claims.SessionID))
	if _, parseErr := uuid.Parse(claims.Subject); parseErr == nil {
		pipe.SRem(ctx, m.userSessionsKey(claims.Subject), claims.SessionID)
	}
	if _, err := pipe.Exec(ctx); err != nil {
		return requestUnavailable("logout refresh session", err)
	}
	return nil
}

// LogoutAll 吊销访问令牌主体名下的所有会话。
func (m *SessionManager) LogoutAll(ctx context.Context, rawAccessToken string) *consoleservice.Error {
	claims, err := m.parse(rawAccessToken, accessTokenType)
	if err != nil {
		return &consoleservice.Error{Code: CodeSessionInvalid, Message: "The current session is invalid.", Status: 401, Cause: err}
	}
	if _, err := uuid.Parse(claims.Subject); err != nil {
		return &consoleservice.Error{Code: CodeSessionInvalid, Message: "The current session is invalid.", Status: 401}
	}
	return m.RevokeUser(ctx, claims.Subject)
}

// RevokeUser 删除公开用户 ID 索引的全部 Redis 会话。
func (m *SessionManager) RevokeUser(ctx context.Context, userUID string) *consoleservice.Error {
	sessions, err := m.redis.SMembers(ctx, m.userSessionsKey(userUID)).Result()
	if err != nil {
		return requestUnavailable("list user sessions", err)
	}
	pipe := m.redis.Pipeline()
	for _, sid := range sessions {
		pipe.Del(ctx, m.sessionKey(sid))
	}
	pipe.Del(ctx, m.userSessionsKey(userUID))
	if _, err := pipe.Exec(ctx); err != nil {
		return requestUnavailable("logout all sessions", err)
	}
	return nil
}

func (m *SessionManager) issuePair(userUID, sid, refreshJTI string) (TokenPair, *consoleservice.Error) {
	now := m.now().UTC()
	accessJTI := uuid.NewString()
	access, err := m.sign(userUID, sid, accessJTI, accessTokenType, now, m.accessTTL)
	if err != nil {
		return TokenPair{}, requestUnavailable("sign access token", err)
	}
	refresh, err := m.sign(userUID, sid, refreshJTI, refreshTokenType, now, m.refreshTTL)
	if err != nil {
		return TokenPair{}, requestUnavailable("sign refresh token", err)
	}
	return TokenPair{
		AccessToken:  access,
		RefreshToken: refresh,
		AccessTTL:    m.accessTTL,
		RefreshTTL:   m.refreshTTL,
		SessionID:    sid,
		UserUID:      userUID,
	}, nil
}

func (m *SessionManager) sign(userUID, sid, jti, tokenType string, now time.Time, ttl time.Duration) (string, error) {
	claims := tokenClaims{
		SessionID: sid,
		TokenType: tokenType,
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer:    "unio-console",
			Subject:   userUID,
			Audience:  jwt.ClaimStrings{"unio-console"},
			ExpiresAt: jwt.NewNumericDate(now.Add(ttl)),
			IssuedAt:  jwt.NewNumericDate(now),
			NotBefore: jwt.NewNumericDate(now),
			ID:        jti,
		},
	}
	return jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString(m.secret)
}

func (m *SessionManager) parse(rawToken, tokenType string) (tokenClaims, error) {
	var claims tokenClaims
	token, err := jwt.ParseWithClaims(
		rawToken,
		&claims,
		func(token *jwt.Token) (any, error) { return m.secret, nil },
		jwt.WithValidMethods([]string{jwt.SigningMethodHS256.Alg()}),
		jwt.WithIssuer("unio-console"),
		jwt.WithAudience("unio-console"),
		jwt.WithLeeway(5*time.Second),
	)
	if err != nil || token == nil || !token.Valid {
		return tokenClaims{}, fmt.Errorf("validate JWT: %w", err)
	}
	if claims.TokenType != tokenType || claims.SessionID == "" || claims.ID == "" {
		return tokenClaims{}, errors.New("JWT claims are incomplete")
	}
	return claims, nil
}

func (m *SessionManager) sessionKey(sid string) string {
	return fmt.Sprintf("%s:console:auth:session:%s", m.keyNS, sid)
}

func (m *SessionManager) userSessionsKey(userUID string) string {
	return fmt.Sprintf("%s:console:auth:user_sessions:%s", m.keyNS, userUID)
}

func sessionInvalid(cause error) *consoleservice.Error {
	return &consoleservice.Error{
		Code:    CodeSessionInvalid,
		Message: "The current session is invalid.",
		Status:  401,
		Cause:   cause,
	}
}
