package appsettings

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"
)

const (
	// AuthVerificationRateLimitsKey 标识 Console 验证码限流配置。
	AuthVerificationRateLimitsKey = "auth.verification_rate_limits"
	// AuthPasswordLoginRateLimitsKey 标识 Console 密码登录限流配置。
	AuthPasswordLoginRateLimitsKey = "auth.password_login_rate_limits"
)

// RateLimitRule 定义滚动窗口内允许的最大请求数。
type RateLimitRule struct {
	WindowSeconds int64 `json:"window_seconds"`
	Limit         int64 `json:"limit"`
}

// VerificationRateLimitSettings 是验证码发送和验证的全部动态限流规则。
type VerificationRateLimitSettings struct {
	SendEmailPurpose   []RateLimitRule `json:"send_email_purpose"`
	SendEmailAll       []RateLimitRule `json:"send_email_all"`
	SendIPPurpose      []RateLimitRule `json:"send_ip_purpose"`
	SendIPAll          []RateLimitRule `json:"send_ip_all"`
	VerifyEmailPurpose []RateLimitRule `json:"verify_email_purpose"`
	VerifyIPAll        []RateLimitRule `json:"verify_ip_all"`
}

// PasswordLoginRateLimitSettings 限制密码失败次数，但不会建立攻击者可用于锁定他人账户的
// 账户级限制。
type PasswordLoginRateLimitSettings struct {
	EmailIP []RateLimitRule `json:"email_ip"`
	IP      []RateLimitRule `json:"ip"`
}

// DefaultPasswordLoginRateLimitSettings 返回代码内置的兜底限流配置。
func DefaultPasswordLoginRateLimitSettings() PasswordLoginRateLimitSettings {
	return PasswordLoginRateLimitSettings{
		EmailIP: []RateLimitRule{
			{WindowSeconds: int64((15 * time.Minute).Seconds()), Limit: 5},
			{WindowSeconds: int64((24 * time.Hour).Seconds()), Limit: 20},
		},
		IP: []RateLimitRule{
			{WindowSeconds: int64((15 * time.Minute).Seconds()), Limit: 30},
			{WindowSeconds: int64((24 * time.Hour).Seconds()), Limit: 200},
		},
	}
}

// DefaultVerificationRateLimitSettings 返回代码内置的兜底限流配置。
func DefaultVerificationRateLimitSettings() VerificationRateLimitSettings {
	return VerificationRateLimitSettings{
		SendEmailPurpose: []RateLimitRule{
			{WindowSeconds: 30, Limit: 1},
			{WindowSeconds: int64((15 * time.Minute).Seconds()), Limit: 5},
			{WindowSeconds: int64((24 * time.Hour).Seconds()), Limit: 20},
		},
		SendEmailAll: []RateLimitRule{
			{WindowSeconds: int64((15 * time.Minute).Seconds()), Limit: 8},
			{WindowSeconds: int64((24 * time.Hour).Seconds()), Limit: 30},
		},
		SendIPPurpose: []RateLimitRule{
			{WindowSeconds: int64((10 * time.Minute).Seconds()), Limit: 20},
			{WindowSeconds: int64((24 * time.Hour).Seconds()), Limit: 100},
		},
		SendIPAll: []RateLimitRule{
			{WindowSeconds: int64((10 * time.Minute).Seconds()), Limit: 30},
			{WindowSeconds: int64((24 * time.Hour).Seconds()), Limit: 200},
		},
		VerifyEmailPurpose: []RateLimitRule{
			{WindowSeconds: int64((15 * time.Minute).Seconds()), Limit: 15},
			{WindowSeconds: int64((24 * time.Hour).Seconds()), Limit: 30},
		},
		VerifyIPAll: []RateLimitRule{
			{WindowSeconds: int64((10 * time.Minute).Seconds()), Limit: 60},
			{WindowSeconds: int64((24 * time.Hour).Seconds()), Limit: 300},
		},
	}
}

// DecodeVerificationRateLimitSettings 严格解码并校验每组验证码限流规则。
func DecodeVerificationRateLimitSettings(raw json.RawMessage) (VerificationRateLimitSettings, error) {
	var value VerificationRateLimitSettings
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&value); err != nil {
		return VerificationRateLimitSettings{}, fmt.Errorf("decode verification rate limits: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return VerificationRateLimitSettings{}, errors.New("verification rate limits contain trailing JSON")
	}
	groups := map[string][]RateLimitRule{
		"send_email_purpose":   value.SendEmailPurpose,
		"send_email_all":       value.SendEmailAll,
		"send_ip_purpose":      value.SendIPPurpose,
		"send_ip_all":          value.SendIPAll,
		"verify_email_purpose": value.VerifyEmailPurpose,
		"verify_ip_all":        value.VerifyIPAll,
	}
	for name, rules := range groups {
		if len(rules) == 0 || len(rules) > 8 {
			return VerificationRateLimitSettings{}, fmt.Errorf("%s must contain 1 to 8 rules", name)
		}
		seenWindows := make(map[int64]struct{}, len(rules))
		for _, rule := range rules {
			if rule.WindowSeconds <= 0 || rule.WindowSeconds > int64((7*24*time.Hour).Seconds()) {
				return VerificationRateLimitSettings{}, fmt.Errorf("%s window_seconds must be between 1 and 604800", name)
			}
			if rule.Limit <= 0 || rule.Limit > 1_000_000 {
				return VerificationRateLimitSettings{}, fmt.Errorf("%s limit must be between 1 and 1000000", name)
			}
			if _, duplicate := seenWindows[rule.WindowSeconds]; duplicate {
				return VerificationRateLimitSettings{}, fmt.Errorf("%s contains duplicate windows", name)
			}
			seenWindows[rule.WindowSeconds] = struct{}{}
		}
	}
	return value, nil
}

// AuthVerificationRateLimits 读取当前配置；配置不可用时返回安全默认值。
func AuthVerificationRateLimits(ctx context.Context, store *SettingsStore) VerificationRateLimitSettings {
	defaults := DefaultVerificationRateLimitSettings()
	if store == nil {
		return defaults
	}
	value, err := DecodeVerificationRateLimitSettings(store.Raw(ctx, AuthVerificationRateLimitsKey))
	if err != nil {
		return defaults
	}
	return value
}

// DecodePasswordLoginRateLimitSettings 严格解码每组密码登录限流规则。
func DecodePasswordLoginRateLimitSettings(raw json.RawMessage) (PasswordLoginRateLimitSettings, error) {
	var value PasswordLoginRateLimitSettings
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&value); err != nil {
		return PasswordLoginRateLimitSettings{}, fmt.Errorf("decode password login rate limits: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return PasswordLoginRateLimitSettings{}, errors.New("password login rate limits contain trailing JSON")
	}
	for name, rules := range map[string][]RateLimitRule{"email_ip": value.EmailIP, "ip": value.IP} {
		if err := validateRateLimitRules(name, rules); err != nil {
			return PasswordLoginRateLimitSettings{}, err
		}
	}
	return value, nil
}

// AuthPasswordLoginRateLimits 读取当前配置；配置不可用时返回安全默认值。
func AuthPasswordLoginRateLimits(ctx context.Context, store *SettingsStore) PasswordLoginRateLimitSettings {
	defaults := DefaultPasswordLoginRateLimitSettings()
	if store == nil {
		return defaults
	}
	value, err := DecodePasswordLoginRateLimitSettings(store.Raw(ctx, AuthPasswordLoginRateLimitsKey))
	if err != nil {
		return defaults
	}
	return value
}

func authPasswordLoginRateLimitsDefinition() Definition {
	defaultJSON, err := json.Marshal(DefaultPasswordLoginRateLimitSettings())
	if err != nil {
		panic(err)
	}
	return Definition{
		Key:         AuthPasswordLoginRateLimitsKey,
		Category:    "auth",
		Label:       "密码登录失败限流",
		Description: "密码登录失败的邮箱与 IP 组合、IP 滚动窗口阈值；保存后由 console-server 秒级读取。",
		HotReload:   true,
		Default:     defaultJSON,
		Validate: func(raw json.RawMessage) error {
			_, err := DecodePasswordLoginRateLimitSettings(raw)
			return err
		},
	}
}

func validateRateLimitRules(name string, rules []RateLimitRule) error {
	if len(rules) == 0 || len(rules) > 8 {
		return fmt.Errorf("%s must contain 1 to 8 rules", name)
	}
	seenWindows := make(map[int64]struct{}, len(rules))
	for _, rule := range rules {
		if rule.WindowSeconds <= 0 || rule.WindowSeconds > int64((7*24*time.Hour).Seconds()) {
			return fmt.Errorf("%s window_seconds must be between 1 and 604800", name)
		}
		if rule.Limit <= 0 || rule.Limit > 1_000_000 {
			return fmt.Errorf("%s limit must be between 1 and 1000000", name)
		}
		if _, duplicate := seenWindows[rule.WindowSeconds]; duplicate {
			return fmt.Errorf("%s contains duplicate windows", name)
		}
		seenWindows[rule.WindowSeconds] = struct{}{}
	}
	return nil
}

func authVerificationRateLimitsDefinition() Definition {
	defaultJSON, err := json.Marshal(DefaultVerificationRateLimitSettings())
	if err != nil {
		panic(err)
	}
	return Definition{
		Key:         AuthVerificationRateLimitsKey,
		Category:    "auth",
		Label:       "验证码限流",
		Description: "验证码发送和验证的邮箱、IP、用途滚动窗口阈值；保存后由 console-server 秒级读取。",
		HotReload:   true,
		Default:     defaultJSON,
		Validate: func(raw json.RawMessage) error {
			_, err := DecodeVerificationRateLimitSettings(raw)
			return err
		},
	}
}
