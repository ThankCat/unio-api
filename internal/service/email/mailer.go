package email

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	mail "github.com/wneessen/go-mail"
	"go.uber.org/zap"

	"github.com/ThankCat/unio-gateway/internal/platform/store/sqlc"
	"github.com/ThankCat/unio-gateway/internal/service/appsettings"
)

// 注意：不要给 SendCloud 附加 X-SMTPAPI 头（曾用于按封关闭追踪注入）。实测该头会触发
// SendCloud 后端重组邮件，其重组器破坏 multipart/alternative 结构（顶层 Content-Type 丢失，
// 收件端渲染出原始 MIME 源码并被判垃圾邮件）；折行形式则直接被静默丢弃（invalid XSMTP-API）。
// 退订注入等追踪行为只能依赖服务商控制台配置或其工单支持关闭。

// ErrNotConfigured 表示 SMTP 尚未启用或配置不完整；调用方决定回退行为
// （验证码流程在 dev 固定验证码模式下跳过发送，否则向用户返回可重试错误）。
var ErrNotConfigured = errors.New("email: smtp is not configured")

const (
	// defaultSendTimeout 是一次 SMTP 提交的整体上限（连接 + TLS + 认证 + 提交）。
	// 同步发送路径持有用户请求，必须有界，禁止无上限等待（Blueprint 边界）。
	defaultSendTimeout = 10 * time.Second
	// recordTimeout 是发送记录落库的独立超时：发送超时或取消不应吞掉事实记录。
	recordTimeout = 5 * time.Second
	// errorSummaryLimit 限制错误摘要长度，防止超长上游报错撑爆记录行。
	errorSummaryLimit = 500
	// codeExpiresMinutes 与 Console 验证码挑战 TTL（10 分钟）保持一致，仅用于邮件文案。
	codeExpiresMinutes = 10
)

// Recorder 是发送记录落库所需的最小存储能力（由 *sqlc.Queries 实现）。
type Recorder interface {
	InsertEmailMessage(ctx context.Context, arg sqlc.InsertEmailMessageParams) (int64, error)
}

// Mailer 执行同步 SMTP 发送：现读系统配置（热更新）、渲染内置模板、有界提交、成败都落发送记录。
type Mailer struct {
	settings *appsettings.SettingsStore
	recorder Recorder
	logger   *zap.Logger

	sendTimeout time.Duration
	now         func() time.Time
}

// NewMailer 创建发信器。
func NewMailer(settings *appsettings.SettingsStore, recorder Recorder, logger *zap.Logger) *Mailer {
	if logger == nil {
		logger = zap.NewNop()
	}
	return &Mailer{
		settings:    settings,
		recorder:    recorder,
		logger:      logger,
		sendTimeout: defaultSendTimeout,
		now:         time.Now,
	}
}

// VerificationCodeMail 描述一封验证码邮件。
type VerificationCodeMail struct {
	Recipient string
	Kind      MessageKind
	Code      string
	Locale    string
}

// Result 是一次发送的结构化结果（测试邮件端点直接回显）。
type Result struct {
	Status       string `json:"status"` // sent | failed
	ErrorSummary string `json:"error_summary,omitempty"`
	DurationMs   int64  `json:"duration_ms"`
	RecordID     int64  `json:"record_id,omitempty"`
}

// SendVerificationCode 同步发送一封验证码邮件；SMTP 未启用返回 ErrNotConfigured，
// 提交失败返回错误（已落 failed 记录），成功返回 nil（已落 sent 记录）。
func (m *Mailer) SendVerificationCode(ctx context.Context, in VerificationCodeMail) error {
	switch in.Kind {
	case KindVerificationRegister,
		KindVerificationLogin,
		KindVerificationPasswordReset,
		KindVerificationPasswordSet,
		KindVerificationPasswordChange:
	default:
		return fmt.Errorf("email: kind %q is not a verification code mail", in.Kind)
	}
	result, err := m.send(ctx, in.Kind, in.Recipient, in.Locale, in.Code)
	if err != nil {
		return err
	}
	if result.Status != "sent" {
		return fmt.Errorf("email: smtp submit failed: %s", result.ErrorSummary)
	}
	return nil
}

// SendTestMail 用当前已保存配置向指定收件人发送测试邮件（记录 email_type=test）。
// SMTP 提交失败不作为 error 返回，而是编码在 Result 中，便于面板直接展示原因。
func (m *Mailer) SendTestMail(ctx context.Context, recipient, locale string) (Result, error) {
	return m.send(ctx, KindTest, recipient, locale, "")
}

// send 渲染模板、执行一次有界 SMTP 提交，并把结果写入发送记录。
// 返回 error 仅表示「未进入提交流程」（未配置 / 渲染失败）；提交失败编码在 Result 中。
func (m *Mailer) send(ctx context.Context, kind MessageKind, recipient, locale, code string) (Result, error) {
	cfg := appsettings.EmailSMTP(ctx, m.settings)
	if !cfg.Enabled {
		return Result{}, ErrNotConfigured
	}
	if strings.TrimSpace(recipient) == "" {
		return Result{}, errors.New("email: recipient is required")
	}

	normalizedLocale := NormalizeLocale(locale)
	subject, html, text, err := renderMessage(kind, normalizedLocale, code, codeExpiresMinutes)
	if err != nil {
		return Result{}, err
	}

	start := m.now()
	sendErr := m.submit(ctx, cfg, recipient, subject, html, text)
	duration := m.now().Sub(start)

	result := Result{Status: "sent", DurationMs: duration.Milliseconds()}
	if sendErr != nil {
		result.Status = "failed"
		result.ErrorSummary = summarizeError(sendErr)
	}

	result.RecordID = m.record(ctx, kind, cfg.SenderAddress, recipient, subject, html, normalizedLocale, result)

	if sendErr != nil {
		m.logger.Warn("email submit failed",
			zap.String("kind", string(kind)),
			zap.String("recipient", recipient),
			zap.Int64("duration_ms", result.DurationMs),
			zap.String("error_summary", result.ErrorSummary),
		)
	}
	return result, nil
}

// submit 执行一次有界 SMTP 提交。
func (m *Mailer) submit(ctx context.Context, cfg appsettings.EmailSMTPConfig, recipient, subject, html, text string) error {
	options := []mail.Option{
		mail.WithPort(cfg.Port),
		mail.WithTimeout(m.sendTimeout),
	}
	if cfg.TLSPolicy == appsettings.EmailTLSImplicit {
		options = append(options, mail.WithSSL())
	} else {
		// STARTTLS 强制升级；证书校验失败即失败，不降级明文（Blueprint 边界）。
		options = append(options, mail.WithTLSPolicy(mail.TLSMandatory))
	}
	if cfg.Username != "" {
		options = append(options,
			mail.WithSMTPAuth(mail.SMTPAuthAutoDiscover),
			mail.WithUsername(cfg.Username),
			mail.WithPassword(cfg.Password),
		)
	}

	client, err := mail.NewClient(cfg.Host, options...)
	if err != nil {
		return err
	}

	msg := mail.NewMsg()
	if err := msg.FromFormat(cfg.SenderName, cfg.SenderAddress); err != nil {
		return err
	}
	if err := msg.To(recipient); err != nil {
		return err
	}
	msg.Subject(subject)
	// multipart/alternative：纯文本在前、HTML 在后（RFC 2046 推荐顺序），降低推广/垃圾分类概率。
	msg.SetBodyString(mail.TypeTextPlain, text)
	msg.AddAlternativeString(mail.TypeTextHTML, html)

	sendCtx, cancel := context.WithTimeout(ctx, m.sendTimeout)
	defer cancel()
	return client.DialAndSendWithContext(sendCtx, msg)
}

// record 把发送事实写入 email_messages。使用独立于发送超时的短超时 context，
// 发送已超时/取消也不放弃记录；记录失败只告警，不影响发送结果向上返回。
func (m *Mailer) record(
	ctx context.Context,
	kind MessageKind,
	sender, recipient, subject, html, locale string,
	result Result,
) int64 {
	recordCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), recordTimeout)
	defer cancel()

	params := sqlc.InsertEmailMessageParams{
		EmailType:  string(kind),
		Recipient:  recipient,
		Sender:     sender,
		Subject:    subject,
		BodyHtml:   html,
		Status:     result.Status,
		Locale:     locale,
		DurationMs: pgtype.Int4{Int32: int32(result.DurationMs), Valid: true},
	}
	if result.Status == "sent" {
		params.SentAt = pgtype.Timestamptz{Time: m.now(), Valid: true}
	} else {
		params.ErrorSummary = pgtype.Text{String: result.ErrorSummary, Valid: true}
	}

	id, err := m.recorder.InsertEmailMessage(recordCtx, params)
	if err != nil {
		m.logger.Error("email record insert failed",
			zap.String("kind", string(kind)),
			zap.String("recipient", recipient),
			zap.String("status", result.Status),
			zap.Error(err),
		)
		return 0
	}
	return id
}

// summarizeError 生成不含凭据的安全错误摘要（go-mail 错误不回显密码，仅防御性截断）。
func summarizeError(err error) string {
	summary := strings.TrimSpace(err.Error())
	if len(summary) > errorSummaryLimit {
		summary = summary[:errorSummaryLimit]
	}
	return summary
}
