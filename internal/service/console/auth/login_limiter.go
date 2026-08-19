package auth

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

	"github.com/ThankCat/unio-gateway/internal/service/appsettings"
	consoleservice "github.com/ThankCat/unio-gateway/internal/service/console"
)

// PasswordLoginLimiter 分别按邮箱与 IP 组合、IP 限制密码失败次数。
// 这里刻意不按邮箱单独锁定，避免第三方通过重复尝试禁用他人账户。
type PasswordLoginLimiter struct {
	redis    redis.Cmdable
	keyNS    string
	secret   []byte
	settings *appsettings.SettingsStore
	now      func() time.Time
}

// NewPasswordLoginLimiter 创建基于 Redis 的密码失败限流器。
func NewPasswordLoginLimiter(
	redisClient redis.Cmdable,
	keyNS string,
	secret string,
	settings *appsettings.SettingsStore,
) (*PasswordLoginLimiter, error) {
	if redisClient == nil {
		return nil, errors.New("password login limiter requires redis")
	}
	if len(secret) < 32 {
		return nil, errors.New("CONSOLE_AUTH_SECRET must contain at least 32 bytes")
	}
	return &PasswordLoginLimiter{
		redis:    redisClient,
		keyNS:    keyNS,
		secret:   []byte(secret),
		settings: settings,
		now:      time.Now,
	}, nil
}

var checkPasswordLoginLimitsScript = redis.NewScript(`
local now = tonumber(ARGV[1])
local worst_retry = 0
for i = 1, #KEYS do
  local window = tonumber(ARGV[1 + (i - 1) * 2 + 1])
  local limit = tonumber(ARGV[1 + (i - 1) * 2 + 2])
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
return {1, 0}
`)

// Check 在任一适用失败窗口达到上限时拒绝密码尝试。
func (l *PasswordLoginLimiter) Check(ctx context.Context, email, ip string) *consoleservice.Error {
	rules := l.rules(ctx, email, ip)
	keys, args := passwordLimitScriptInput(l.now(), rules, false)
	result, err := checkPasswordLoginLimitsScript.Run(ctx, l.redis, keys, args...).Result()
	if err != nil {
		return requestUnavailable("check password login rate limits", err)
	}
	return passwordLimitResult(result)
}

// RecordFailure 在所有适用窗口中原子记录一次密码失败。
func (l *PasswordLoginLimiter) RecordFailure(ctx context.Context, email, ip string) *consoleservice.Error {
	rules := l.rules(ctx, email, ip)
	keys, args := passwordLimitScriptInput(l.now(), rules, true)
	result, err := rollingWindowScript.Run(ctx, l.redis, keys, args...).Result()
	if err != nil {
		return requestUnavailable("record password login failure", err)
	}
	return passwordLimitResult(result)
}

// ResetEmailIP 仅清除登录成功的邮箱与来源 IP 组合计数。
// IP 维度计数继续保留，避免一次成功登录重置整个来源保护。
func (l *PasswordLoginLimiter) ResetEmailIP(ctx context.Context, email, ip string) *consoleservice.Error {
	limits := appsettings.AuthPasswordLoginRateLimits(ctx, l.settings)
	prefix := l.counterPrefix("email_ip", l.identifier("email_ip", email+"\x00"+l.normalizedIP(ip)))
	rules := appendRules(nil, prefix, limits.EmailIP)
	keys := make([]string, 0, len(rules))
	for _, rule := range rules {
		keys = append(keys, rule.key)
	}
	if len(keys) == 0 {
		return nil
	}
	if err := l.redis.Del(ctx, keys...).Err(); err != nil {
		return requestUnavailable("reset password login failures", err)
	}
	return nil
}

func (l *PasswordLoginLimiter) rules(ctx context.Context, email, ip string) []counterRule {
	limits := appsettings.AuthPasswordLoginRateLimits(ctx, l.settings)
	normalizedIP := l.normalizedIP(ip)
	emailIPID := l.identifier("email_ip", email+"\x00"+normalizedIP)
	rules := appendRules(nil, l.counterPrefix("email_ip", emailIPID), limits.EmailIP)
	for _, ipValue := range loginIPValues(normalizedIP) {
		ipID := l.identifier("ip", ipValue)
		rules = appendRules(rules, l.counterPrefix("ip", ipID), limits.IP)
	}
	return rules
}

func passwordLimitScriptInput(now time.Time, rules []counterRule, includeMember bool) ([]string, []any) {
	keys := make([]string, 0, len(rules))
	args := make([]any, 0, 2+len(rules)*2)
	args = append(args, now.UnixMilli())
	if includeMember {
		args = append(args, uuid.NewString())
	}
	for _, rule := range rules {
		keys = append(keys, rule.key)
		args = append(args, rule.window.Milliseconds(), rule.limit)
	}
	return keys, args
}

func passwordLimitResult(result any) *consoleservice.Error {
	values, ok := result.([]any)
	if !ok || len(values) != 2 {
		return requestUnavailable("decode password login rate-limit result", fmt.Errorf("unexpected result %#v", result))
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
		Code:       CodePasswordLoginRateLimited,
		Message:    "Too many password login attempts. Please try again later.",
		Status:     429,
		RetryAfter: retryAfter,
	}
}

func (l *PasswordLoginLimiter) normalizedIP(ip string) string {
	ip = strings.TrimSpace(ip)
	if ip == "" {
		return "unknown"
	}
	if address, err := netip.ParseAddr(ip); err == nil {
		return address.String()
	}
	return ip
}

func loginIPValues(ip string) []string {
	values := []string{ip}
	if address, err := netip.ParseAddr(ip); err == nil && address.Is6() {
		values = append(values, netip.PrefixFrom(address, 64).Masked().String())
	}
	return values
}

func (l *PasswordLoginLimiter) identifier(kind, value string) string {
	mac := hmac.New(sha256.New, l.secret)
	_, _ = fmt.Fprintf(mac, "password-login-identifier\x00%s\x00%s", kind, value)
	return hex.EncodeToString(mac.Sum(nil))
}

func (l *PasswordLoginLimiter) counterPrefix(dimension, subject string) string {
	return fmt.Sprintf("%s:console:auth:rate:password:%s:%s", l.keyNS, dimension, subject)
}
