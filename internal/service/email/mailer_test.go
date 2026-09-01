package email

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/ThankCat/unio-gateway/internal/platform/store/sqlc"
	"github.com/ThankCat/unio-gateway/internal/service/appsettings"
)

func TestRenderMessageLocalesAndEscaping(t *testing.T) {
	subject, html, text, err := renderMessage(KindVerificationRegister, "zh-CN", "638214", 10)
	if err != nil {
		t.Fatalf("render zh register: %v", err)
	}
	if subject != "完成你的 UnioAPI 注册" {
		t.Fatalf("unexpected zh subject: %q", subject)
	}
	if !strings.Contains(html, "638214") || !strings.Contains(html, "10 分钟内有效") {
		t.Fatalf("zh body missing code or expiry copy")
	}
	if !strings.Contains(html, "#f17462") {
		t.Fatalf("body missing brand accent color")
	}
	// 纯文本 alternative 必须包含验证码且不含 HTML 标签。
	if !strings.Contains(text, "638214") || strings.Contains(text, "<") {
		t.Fatalf("plain text part missing code or contains markup: %q", text)
	}

	subject, html, _, err = renderMessage(KindVerificationLogin, "fr", "424242", 10)
	if err != nil {
		t.Fatalf("render fallback locale: %v", err)
	}
	if subject != "Your UnioAPI sign-in code" {
		t.Fatalf("unsupported locale should fall back to English, got %q", subject)
	}
	if !strings.Contains(html, "Valid for 10 minutes") {
		t.Fatalf("en body missing expiry copy")
	}

	// 普通变量默认 HTML 转义（Blueprint 模板边界）。
	_, html, _, err = renderMessage(KindVerificationPasswordReset, "en", `<b>&"</b>`, 10)
	if err != nil {
		t.Fatalf("render escaped code: %v", err)
	}
	if strings.Contains(html, "<b>") {
		t.Fatalf("code variable must be HTML-escaped")
	}

	// 测试邮件无验证码面板。
	_, html, _, err = renderMessage(KindTest, "zh", "", 0)
	if err != nil {
		t.Fatalf("render test mail: %v", err)
	}
	if strings.Contains(html, "letter-spacing:6px") {
		t.Fatalf("test mail should not contain the code panel")
	}
}

func TestRenderPasswordVerificationMessages(t *testing.T) {
	tests := []struct {
		name        string
		kind        MessageKind
		locale      string
		wantSubject string
		wantText    string
	}{
		{name: "set zh", kind: KindVerificationPasswordSet, locale: "zh-CN", wantSubject: "设置你的 UnioAPI 密码", wantText: "仅限本次密码设置"},
		{name: "change en", kind: KindVerificationPasswordChange, locale: "en", wantSubject: "Change your UnioAPI password", wantText: "For this password change only"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			subject, html, text, err := renderMessage(tt.kind, tt.locale, "638214", 10)
			if err != nil {
				t.Fatal(err)
			}
			if subject != tt.wantSubject || !strings.Contains(html, "638214") || !strings.Contains(text, tt.wantText) {
				t.Fatalf("unexpected rendered message: subject=%q html=%q text=%q", subject, html, text)
			}
		})
	}
}

// fakeSettingsQueries 让 SettingsStore 读到指定的 email.smtp 配置（绕过 Redis/DB）。
type fakeSettingsQueries struct {
	raw []byte
}

func (f *fakeSettingsQueries) GetAppSetting(context.Context, string) ([]byte, error) {
	return f.raw, nil
}
func (f *fakeSettingsQueries) GetAppSettingRecord(context.Context, string) (sqlc.GetAppSettingRecordRow, error) {
	return sqlc.GetAppSettingRecordRow{}, errors.New("not implemented")
}
func (f *fakeSettingsQueries) UpsertAppSetting(context.Context, sqlc.UpsertAppSettingParams) error {
	return errors.New("not implemented")
}
func (f *fakeSettingsQueries) SeedAppSetting(context.Context, sqlc.SeedAppSettingParams) error {
	return errors.New("not implemented")
}

type fakeRecorder struct {
	inserted []sqlc.InsertEmailMessageParams
}

func (f *fakeRecorder) InsertEmailMessage(_ context.Context, arg sqlc.InsertEmailMessageParams) (int64, error) {
	f.inserted = append(f.inserted, arg)
	return int64(len(f.inserted)), nil
}

func settingsStoreWith(t *testing.T, cfg appsettings.EmailSMTPConfig) *appsettings.SettingsStore {
	t.Helper()
	raw, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return appsettings.NewSettingsStore(&fakeSettingsQueries{raw: raw}, nil, "test", appsettings.DefaultRegistry(), nil)
}

func TestSendVerificationCodeNotConfigured(t *testing.T) {
	store := settingsStoreWith(t, appsettings.DefaultEmailSMTPConfig())
	recorder := &fakeRecorder{}
	mailer := NewMailer(store, recorder, nil)

	err := mailer.SendVerificationCode(context.Background(), VerificationCodeMail{
		Recipient: "user@example.com",
		Kind:      KindVerificationRegister,
		Code:      "123456",
		Locale:    "zh",
	})
	if !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("disabled smtp should return ErrNotConfigured, got %v", err)
	}
	if len(recorder.inserted) != 0 {
		t.Fatalf("unconfigured send must not create records, got %d", len(recorder.inserted))
	}
}

func TestSendVerificationCodeRecordsFailure(t *testing.T) {
	store := settingsStoreWith(t, appsettings.EmailSMTPConfig{
		Enabled:       true,
		Host:          "127.0.0.1",
		Port:          1, // 未监听端口：连接立即被拒绝，无需真实 SMTP。
		TLSPolicy:     appsettings.EmailTLSImplicit,
		SenderName:    "UnioAPI",
		SenderAddress: "noreply@example.com",
	})
	recorder := &fakeRecorder{}
	mailer := NewMailer(store, recorder, nil)
	mailer.sendTimeout = 2 * time.Second

	err := mailer.SendVerificationCode(context.Background(), VerificationCodeMail{
		Recipient: "user@example.com",
		Kind:      KindVerificationLogin,
		Code:      "123456",
		Locale:    "en",
	})
	if err == nil {
		t.Fatal("submit to closed port should fail")
	}
	if len(recorder.inserted) != 1 {
		t.Fatalf("failed send must create exactly one record, got %d", len(recorder.inserted))
	}
	row := recorder.inserted[0]
	if row.Status != "failed" || !row.ErrorSummary.Valid || row.ErrorSummary.String == "" {
		t.Fatalf("failure record must carry status=failed and error summary, got %+v", row)
	}
	if row.EmailType != string(KindVerificationLogin) || row.Recipient != "user@example.com" {
		t.Fatalf("record identity mismatch: %+v", row)
	}
	if row.SentAt.Valid {
		t.Fatalf("failed record must not carry sent_at")
	}
	if !strings.Contains(row.BodyHtml, "123456") {
		t.Fatalf("record must keep the rendered body")
	}
}

func TestSendTestMailEncodesFailureInResult(t *testing.T) {
	store := settingsStoreWith(t, appsettings.EmailSMTPConfig{
		Enabled:       true,
		Host:          "127.0.0.1",
		Port:          1,
		TLSPolicy:     appsettings.EmailTLSImplicit,
		SenderName:    "UnioAPI",
		SenderAddress: "noreply@example.com",
	})
	recorder := &fakeRecorder{}
	mailer := NewMailer(store, recorder, nil)
	mailer.sendTimeout = 2 * time.Second

	result, err := mailer.SendTestMail(context.Background(), "ops@example.com", "zh")
	if err != nil {
		t.Fatalf("test mail smtp failure must be encoded in result, got error %v", err)
	}
	if result.Status != "failed" || result.ErrorSummary == "" {
		t.Fatalf("unexpected result: %+v", result)
	}
	if len(recorder.inserted) != 1 || recorder.inserted[0].EmailType != string(KindTest) {
		t.Fatalf("test mail must record email_type=test, got %+v", recorder.inserted)
	}
}
