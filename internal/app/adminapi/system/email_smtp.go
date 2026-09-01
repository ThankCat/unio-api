package system

import (
	"context"
	"net/http"
	"net/mail"
	"strings"

	"github.com/ThankCat/unio-gateway/internal/app/adminapi/adminhttp"
	"github.com/ThankCat/unio-gateway/internal/platform/failure"
	"github.com/ThankCat/unio-gateway/internal/platform/httpx"
	"github.com/ThankCat/unio-gateway/internal/service/appsettings"
	emailsvc "github.com/ThankCat/unio-gateway/internal/service/email"
)

// EmailSMTPService 定义 SMTP 发信配置的读写能力（appsettings.Service 实现）。
// 配置可读可写：GET 回显完整当前配置（含密码，与渠道凭据同权的产品决策）。
type EmailSMTPService interface {
	GetEmailSMTP(ctx context.Context) appsettings.EmailSMTPConfig
	SetEmailSMTP(ctx context.Context, cfg appsettings.EmailSMTPConfig) error
}

// EmailTestMailer 用当前已保存配置发送测试邮件（internal/service/email.Mailer 实现）。
type EmailTestMailer interface {
	SendTestMail(ctx context.Context, recipient, locale string) (emailsvc.Result, error)
}

// emailSMTPConfigDTO 是 SMTP 配置的请求/响应体（与 appsettings.EmailSMTPConfig 字段一致）。
type emailSMTPConfigDTO struct {
	Enabled       bool   `json:"enabled"`
	Host          string `json:"host"`
	Port          int    `json:"port"`
	Username      string `json:"username"`
	Password      string `json:"password"`
	TLSPolicy     string `json:"tls_policy"`
	SenderName    string `json:"sender_name"`
	SenderAddress string `json:"sender_address"`
}

type emailSMTPTestRequest struct {
	Recipient string `json:"recipient"`
}

type emailSMTPHandler struct {
	service EmailSMTPService
	mailer  EmailTestMailer
}

func (h *emailSMTPHandler) get(w http.ResponseWriter, r *http.Request) {
	adminhttp.WriteData(w, http.StatusOK, dtoFromEmailSMTP(h.service.GetEmailSMTP(r.Context())))
}

func (h *emailSMTPHandler) put(w http.ResponseWriter, r *http.Request) {
	var req emailSMTPConfigDTO
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}
	cfg := appsettings.EmailSMTPConfig{
		Enabled:       req.Enabled,
		Host:          strings.TrimSpace(req.Host),
		Port:          req.Port,
		Username:      strings.TrimSpace(req.Username),
		Password:      req.Password,
		TLSPolicy:     strings.TrimSpace(req.TLSPolicy),
		SenderName:    strings.TrimSpace(req.SenderName),
		SenderAddress: strings.TrimSpace(req.SenderAddress),
	}
	if err := h.service.SetEmailSMTP(r.Context(), cfg); err != nil {
		if failure.CodeOf(err) != "" {
			adminhttp.WriteServiceError(w, err)
			return
		}
		adminhttp.WriteServiceError(w, failure.New(
			failure.CodeAdminInvalidArgument,
			failure.WithMessage(err.Error()),
			failure.WithField("field", "email_smtp"),
		))
		return
	}
	adminhttp.WriteData(w, http.StatusOK, dtoFromEmailSMTP(h.service.GetEmailSMTP(r.Context())))
}

// test 用当前已保存配置向指定收件人发送测试邮件（记录 email_type=test）。
// SMTP 提交失败不是 HTTP 错误：结果编码在响应体中，面板直接展示 sent/failed 与原因。
func (h *emailSMTPHandler) test(w http.ResponseWriter, r *http.Request) {
	var req emailSMTPTestRequest
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}
	recipient := strings.TrimSpace(req.Recipient)
	if parsed, err := mail.ParseAddress(recipient); err != nil || parsed.Address != recipient {
		adminhttp.WriteServiceError(w, adminhttp.InvalidRequestField("recipient", "recipient must be a valid email address"))
		return
	}
	result, err := h.mailer.SendTestMail(r.Context(), recipient, emailsvc.LocaleZH)
	if err != nil {
		adminhttp.WriteServiceError(w, failure.New(
			failure.CodeAdminInvalidArgument,
			failure.WithMessage("SMTP 未启用或配置不完整，请先保存并启用配置"),
			failure.WithField("field", "email_smtp"),
		))
		return
	}
	adminhttp.WriteData(w, http.StatusOK, result)
}

func dtoFromEmailSMTP(cfg appsettings.EmailSMTPConfig) emailSMTPConfigDTO {
	return emailSMTPConfigDTO{
		Enabled:       cfg.Enabled,
		Host:          cfg.Host,
		Port:          cfg.Port,
		Username:      cfg.Username,
		Password:      cfg.Password,
		TLSPolicy:     cfg.TLSPolicy,
		SenderName:    cfg.SenderName,
		SenderAddress: cfg.SenderAddress,
	}
}
