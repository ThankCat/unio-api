package appsettings

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/mail"
	"strings"
)

// EmailSMTPKey 是通用 SMTP 发信配置的运行时配置项（验证码批次，
// docs/changes/2026-09-01-email-verification-code）。
//
// 配置可读可写：Admin 专用面板回显当前全部字段（含密码，与渠道凭据同权的产品决策）；
// DedicatedControl 使其不出现在通用设置面板，统一走 typed 接口。改后免重启热生效。
const EmailSMTPKey = "email.smtp"

// EmailTLSImplicit / EmailTLSSTARTTLS 是允许的 TLS 策略。不提供明文降级选项：
// 生产环境禁止未经加密的 SMTP 凭据传输（Blueprint 邮件投递中心边界）。
const (
	EmailTLSImplicit = "implicit"
	EmailTLSSTARTTLS = "starttls"
)

// EmailSMTPConfig 是 SMTP 发信配置的完整形状（持久化 JSON 与运行时读取共用）。
type EmailSMTPConfig struct {
	// Enabled 为 false 时同步发送路径返回「邮件服务未配置」，不尝试连接。
	Enabled bool `json:"enabled"`
	// Host 是 SMTP 服务器地址（不含端口）。
	Host string `json:"host"`
	// Port 是 SMTP 端口（隐式 TLS 常用 465，STARTTLS 常用 587）。
	Port int `json:"port"`
	// Username / Password 是 SMTP 认证凭据；留空表示服务器不要求认证。
	Username string `json:"username"`
	Password string `json:"password"`
	// TLSPolicy 是传输加密策略：implicit（连接即 TLS）或 starttls（明文握手后强制升级）。
	TLSPolicy string `json:"tls_policy"`
	// SenderName / SenderAddress 组成发件人，如 "UnioAPI <noreply@example.com>"。
	SenderName    string `json:"sender_name"`
	SenderAddress string `json:"sender_address"`
}

// DefaultEmailSMTPConfig 返回代码内置的默认值（未启用）。
func DefaultEmailSMTPConfig() EmailSMTPConfig {
	return EmailSMTPConfig{
		Enabled:       false,
		Host:          "",
		Port:          465,
		Username:      "",
		Password:      "",
		TLSPolicy:     EmailTLSImplicit,
		SenderName:    "UnioAPI",
		SenderAddress: "",
	}
}

// ValidateEmailSMTPConfig 校验一份 SMTP 配置是否可保存。
// 未启用时允许留空 host/发件地址（先存草稿），启用时必须齐备。
func ValidateEmailSMTPConfig(cfg EmailSMTPConfig) error {
	if cfg.Port < 1 || cfg.Port > 65535 {
		return fmt.Errorf("port must be between 1 and 65535, got %d", cfg.Port)
	}
	switch cfg.TLSPolicy {
	case EmailTLSImplicit, EmailTLSSTARTTLS:
	default:
		return fmt.Errorf("invalid tls_policy %q (want implicit|starttls)", cfg.TLSPolicy)
	}
	if address := strings.TrimSpace(cfg.SenderAddress); address != "" {
		parsed, err := mail.ParseAddress(address)
		if err != nil || parsed.Address != address {
			return fmt.Errorf("sender_address %q is not a valid email address", cfg.SenderAddress)
		}
	}
	if cfg.Enabled {
		if strings.TrimSpace(cfg.Host) == "" {
			return errors.New("host is required when smtp is enabled")
		}
		if strings.TrimSpace(cfg.SenderAddress) == "" {
			return errors.New("sender_address is required when smtp is enabled")
		}
	}
	return nil
}

// DecodeEmailSMTPConfig 严格解码并校验 SMTP 配置。
func DecodeEmailSMTPConfig(raw json.RawMessage) (EmailSMTPConfig, error) {
	var value EmailSMTPConfig
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&value); err != nil {
		return EmailSMTPConfig{}, fmt.Errorf("decode email smtp config: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return EmailSMTPConfig{}, errors.New("email smtp config contains trailing JSON")
	}
	if err := ValidateEmailSMTPConfig(value); err != nil {
		return EmailSMTPConfig{}, err
	}
	return value, nil
}

// EmailSMTP 读取当前生效的 SMTP 配置（含密码；本地缓存 → Redis → DB，失败回退默认未启用）。
func EmailSMTP(ctx context.Context, store *SettingsStore) EmailSMTPConfig {
	defaults := DefaultEmailSMTPConfig()
	if store == nil {
		return defaults
	}
	value, err := DecodeEmailSMTPConfig(store.Raw(ctx, EmailSMTPKey))
	if err != nil {
		return defaults
	}
	return value
}

// SetEmailSMTP 校验并写入 SMTP 配置（写 DB + 刷新 Redis/本地，跨进程秒级生效）。
func SetEmailSMTP(ctx context.Context, store *SettingsStore, cfg EmailSMTPConfig) error {
	if err := ValidateEmailSMTPConfig(cfg); err != nil {
		return err
	}
	raw, err := json.Marshal(cfg)
	if err != nil {
		return err
	}
	return store.Set(ctx, EmailSMTPKey, raw)
}

func emailSMTPDefinition() Definition {
	defaultJSON, err := json.Marshal(DefaultEmailSMTPConfig())
	if err != nil {
		panic(err)
	}
	return Definition{
		Key:      EmailSMTPKey,
		Category: "email",
		Label:    "SMTP 发信配置",
		Description: "验证码等事务邮件的通用 SMTP 配置（地址/端口/认证/TLS 策略/发件人）。" +
			"通过 Admin 系统设置「邮件」面板读写，改后免重启热生效；密码与渠道凭据同权明文存储。",
		HotReload: true,
		// 密码随配置对象持久化，通用设置面板不适合回显/编辑该对象，统一走专用 typed 接口。
		DedicatedControl: true,
		Default:          defaultJSON,
		Validate: func(raw json.RawMessage) error {
			_, err := DecodeEmailSMTPConfig(raw)
			return err
		},
	}
}
