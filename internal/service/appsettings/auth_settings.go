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

// AuthVerificationRateLimitsKey identifies the Console verification limit setting.
const AuthVerificationRateLimitsKey = "auth.verification_rate_limits"

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

// DefaultVerificationRateLimitSettings returns the code-owned fallback limits.
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

// DecodeVerificationRateLimitSettings strictly decodes and validates every rule group.
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

// AuthVerificationRateLimits reads the current setting or returns safe defaults.
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
