// Package adminlogin 提供跨 Admin 实例共享的登录尝试限制。
package adminlogin

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/ThankCat/unio-gateway/internal/platform/failure"
)

const incrementScript = `
local source_count = redis.call('INCR', KEYS[1])
if source_count == 1 then
  redis.call('PEXPIRE', KEYS[1], ARGV[1])
end

local account_count = redis.call('INCR', KEYS[2])
if account_count == 1 then
  redis.call('PEXPIRE', KEYS[2], ARGV[1])
end

local source_ttl = redis.call('PTTL', KEYS[1])
if source_ttl < 0 then
  redis.call('PEXPIRE', KEYS[1], ARGV[1])
  source_ttl = tonumber(ARGV[1])
end

local account_ttl = redis.call('PTTL', KEYS[2])
if account_ttl < 0 then
  redis.call('PEXPIRE', KEYS[2], ARGV[1])
  account_ttl = tonumber(ARGV[1])
end

return {source_count, source_ttl, account_count, account_ttl}
`

// Limiter 在 Redis 固定窗口内同时限制单来源和同一账号的登录尝试。
type Limiter struct {
	client       redis.Cmdable
	keyPrefix    string
	sourceLimit  int64
	accountLimit int64
	window       time.Duration
	increment    *redis.Script
}

// NewLimiter 创建登录尝试限制器。配置在启动期已经过正数校验。
func NewLimiter(client redis.Cmdable, keyNamespace string, sourceLimit, accountLimit int, window time.Duration) *Limiter {
	if client == nil {
		panic("adminlogin: redis client is required")
	}
	if sourceLimit <= 0 || accountLimit <= 0 || window <= 0 {
		panic("adminlogin: limits and window must be greater than zero")
	}

	keyNamespace = strings.Trim(keyNamespace, ":")
	if keyNamespace == "" {
		keyNamespace = "unio"
	}

	return &Limiter{
		client:       client,
		keyPrefix:    keyNamespace + ":admin:login:",
		sourceLimit:  int64(sourceLimit),
		accountLimit: int64(accountLimit),
		window:       window,
		increment:    redis.NewScript(incrementScript),
	}
}

// Allow 先占用一次登录尝试。allowed=false 时 retryAfter 表示最晚还需等待多久。
func (l *Limiter) Allow(ctx context.Context, username, remoteAddr string) (allowed bool, retryAfter time.Duration, err error) {
	sourceKey, accountKey := l.keys(username, remoteAddr)
	values, err := l.increment.Run(
		ctx,
		l.client,
		[]string{sourceKey, accountKey},
		l.window.Milliseconds(),
	).Slice()
	if err != nil {
		return false, 0, storeFailure(err, "increment admin login attempt")
	}
	if len(values) != 4 {
		return false, 0, storeFailure(redis.Nil, "invalid admin login limiter result")
	}

	sourceCount, sourceTTL, ok := limiterValues(values[0], values[1])
	if !ok {
		return false, 0, storeFailure(redis.Nil, "decode admin login source limiter result")
	}
	accountCount, accountTTL, ok := limiterValues(values[2], values[3])
	if !ok {
		return false, 0, storeFailure(redis.Nil, "decode admin login account limiter result")
	}

	if sourceCount <= l.sourceLimit && accountCount <= l.accountLimit {
		return true, 0, nil
	}
	if sourceCount > l.sourceLimit {
		retryAfter = sourceTTL
	}
	if accountCount > l.accountLimit && accountTTL > retryAfter {
		retryAfter = accountTTL
	}
	if retryAfter <= 0 {
		retryAfter = l.window
	}
	return false, retryAfter, nil
}

// Reset 在凭据校验成功后清除该来源和账号的失败窗口。
func (l *Limiter) Reset(ctx context.Context, username, remoteAddr string) error {
	sourceKey, accountKey := l.keys(username, remoteAddr)
	if err := l.client.Del(ctx, sourceKey, accountKey).Err(); err != nil {
		return storeFailure(err, "reset admin login attempts")
	}
	return nil
}

func (l *Limiter) keys(username, remoteAddr string) (string, string) {
	account := strings.ToLower(strings.TrimSpace(username))
	source := remoteHost(remoteAddr)
	return l.keyPrefix + "source:" + digest(account+"\x00"+source),
		l.keyPrefix + "account:" + digest(account)
}

func remoteHost(remoteAddr string) string {
	remoteAddr = strings.TrimSpace(remoteAddr)
	if host, _, err := net.SplitHostPort(remoteAddr); err == nil {
		return host
	}
	if remoteAddr == "" {
		return "unknown"
	}
	return remoteAddr
}

func digest(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

func limiterValues(rawCount, rawTTL any) (int64, time.Duration, bool) {
	count, countOK := rawCount.(int64)
	ttlMillis, ttlOK := rawTTL.(int64)
	if !countOK || !ttlOK {
		return 0, 0, false
	}
	return count, time.Duration(ttlMillis) * time.Millisecond, true
}

func storeFailure(err error, message string) error {
	return failure.Wrap(
		failure.CodeAdminAuthLoginRateLimitStoreFailed,
		err,
		failure.WithMessage(message),
	)
}
