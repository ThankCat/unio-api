package appsettings

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"
)

const GatewayLoggingDebugSessionKey = "gateway.logging.debug_session"

type GatewayLoggingDebugSessionSetting struct {
	SessionID       string    `json:"session_id"`
	StartedAt       time.Time `json:"started_at"`
	ExpiresAt       time.Time `json:"expires_at"`
	Reason          string    `json:"reason"`
	EnabledByUserID int64     `json:"enabled_by_user_id"`
	Revision        int64     `json:"revision"`
}

func gatewayLoggingDebugSessionDefinition() Definition {
	return Definition{
		Key:              GatewayLoggingDebugSessionKey,
		Category:         "gateway",
		Label:            "Gateway 临时 DEBUG",
		Description:      "Gateway 文件日志临时 DEBUG 会话；最长 60 分钟，到期后各实例本地自动恢复 INFO。",
		HotReload:        true,
		DedicatedControl: true,
		Default:          json.RawMessage(`{"session_id":"","started_at":"1970-01-01T00:00:00Z","expires_at":"1970-01-01T00:00:00Z","reason":"disabled","enabled_by_user_id":0,"revision":0}`),
		Validate: func(raw json.RawMessage) error {
			_, err := DecodeGatewayLoggingDebugSession(raw)
			return err
		},
	}
}

func DecodeGatewayLoggingDebugSession(raw json.RawMessage) (GatewayLoggingDebugSessionSetting, error) {
	var value GatewayLoggingDebugSessionSetting
	if err := json.Unmarshal(raw, &value); err != nil {
		return value, fmt.Errorf("decode %s: %w", GatewayLoggingDebugSessionKey, err)
	}
	if value.Revision < 0 || value.EnabledByUserID < 0 {
		return value, fmt.Errorf("%s revision and enabled_by_user_id must be non-negative", GatewayLoggingDebugSessionKey)
	}
	if value.SessionID == "" {
		return value, nil
	}
	if value.StartedAt.IsZero() || value.StartedAt.After(value.ExpiresAt) {
		return value, fmt.Errorf("%s started_at must not be after expires_at", GatewayLoggingDebugSessionKey)
	}
	if strings.TrimSpace(value.Reason) == "" || utf8.RuneCountInString(value.Reason) > 200 {
		return value, fmt.Errorf("%s reason must contain 1 to 200 characters", GatewayLoggingDebugSessionKey)
	}
	if strings.IndexFunc(value.Reason, func(r rune) bool { return r < 0x20 || r == 0x7f }) >= 0 {
		return value, fmt.Errorf("%s reason contains control characters", GatewayLoggingDebugSessionKey)
	}
	if value.ExpiresAt.IsZero() {
		return value, fmt.Errorf("%s expires_at is required", GatewayLoggingDebugSessionKey)
	}
	return value, nil
}
