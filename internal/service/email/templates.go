// Package email 提供验证码批次的同步 SMTP 发信能力：内置中英文模板渲染、有界提交与发送记录落库。
//
// 边界（Blueprint 邮件投递中心，2026-09-01 修订）：
//   - 同步发送，不建队列、不自动重试；失败由调用方决定反馈（验证码 → 可重试错误）。
//   - 每次提交完成后（无论成败）写入 email_messages 发送记录，含完整 HTML 正文。
//   - status=sent 只代表 SMTP 服务商接受提交，不代表进入收件箱。
package email

import (
	"fmt"
	"html/template"
	"strings"
	"time"
)

// MessageKind 是发送记录的邮件类型（email_messages.email_type 取值）。
type MessageKind string

const (
	// KindVerificationRegister 注册邮箱验证。
	KindVerificationRegister MessageKind = "verification_register"
	// KindVerificationLogin 邮箱验证码登录。
	KindVerificationLogin MessageKind = "verification_login"
	// KindVerificationPasswordReset 密码重置。
	KindVerificationPasswordReset MessageKind = "verification_password_reset"
	// KindVerificationPasswordSet 首次设置密码。
	KindVerificationPasswordSet MessageKind = "verification_password_set"
	// KindVerificationPasswordChange 修改已有密码。
	KindVerificationPasswordChange MessageKind = "verification_password_change"
	// KindTest Admin 系统设置面板触发的测试邮件。
	KindTest MessageKind = "test"
)

// LocaleZH / LocaleEN 是模板支持的语言；语言缺失或不受支持时统一回退英文（Blueprint 约定）。
const (
	LocaleZH = "zh"
	LocaleEN = "en"
)

// NormalizeLocale 把任意语言标输入收敛为受支持的模板语言。
func NormalizeLocale(raw string) string {
	if strings.HasPrefix(strings.ToLower(strings.TrimSpace(raw)), "zh") {
		return LocaleZH
	}
	return LocaleEN
}

// bodyTemplate 是邮件安全 HTML 骨架：单栏、表格布局、内联样式、品牌珊瑚 #f17462，
// 与 unio-blueprint/docs/architecture/email-template-previews.html 的视觉一致。
// 所有变量经 html/template 默认转义。
var bodyTemplate = template.Must(template.New("email").Parse(`<!doctype html>
<html>
<body style="margin:0;padding:24px 12px;background-color:#eef1f4;">
<table role="presentation" width="100%" cellpadding="0" cellspacing="0" border="0"><tr><td align="center">
<table role="presentation" cellpadding="0" cellspacing="0" border="0" style="width:100%;max-width:560px;background-color:#ffffff;border:1px solid #dde3e8;border-radius:6px;">
<tr><td style="padding:18px 24px;border-bottom:1px solid #eef1f3;font-family:-apple-system,'PingFang SC','Microsoft YaHei',Arial,sans-serif;font-size:16px;font-weight:800;letter-spacing:2px;color:#17232d;">UnioAPI</td></tr>
<tr><td style="padding:26px 24px;font-family:-apple-system,'PingFang SC','Microsoft YaHei',Arial,sans-serif;">
<div style="font-size:20px;font-weight:700;color:#17232d;">{{.Title}}</div>
<p style="margin:14px 0 0;color:#4f5b65;font-size:14px;line-height:1.75;">{{.Intro}}</p>
{{if .Code}}<table role="presentation" width="100%" cellpadding="0" cellspacing="0" border="0" style="margin-top:20px;"><tr>
<td style="padding:18px 20px;background-color:#f7f9fa;border:1px solid #e1e8ec;border-left:3px solid #f17462;">
<div style="font-family:Menlo,Consolas,monospace;font-size:32px;font-weight:800;letter-spacing:6px;color:#17232d;">{{.Code}}</div>
<div style="margin-top:8px;color:#6d7882;font-size:12px;">{{.CodeMeta}}</div>
</td></tr></table>{{end}}
<p style="margin:20px 0 0;color:#8a949d;font-size:12px;line-height:1.7;">{{.Fine}}</p>
</td></tr>
<tr><td style="padding:14px 24px;border-top:1px solid #eef1f3;color:#8a949d;font-size:11px;font-family:-apple-system,'PingFang SC','Microsoft YaHei',Arial,sans-serif;">{{.Footer}} © {{.Year}} UnioAPI</td></tr>
</table>
</td></tr></table>
</body>
</html>
`))

type bodyData struct {
	Title    string
	Intro    string
	Code     string
	CodeMeta string
	Fine     string
	Footer   string
	Year     int
}

// copyDeck 是一类邮件在单一语言下的全部文案。
type copyDeck struct {
	Subject  string
	Title    string
	Intro    string
	CodeMeta string
	Fine     string
	Footer   string
}

// copyDecks[kind][locale]；新增语言或事件在此扩展。
var copyDecks = map[MessageKind]map[string]copyDeck{
	KindVerificationRegister: {
		LocaleZH: {
			Subject:  "完成你的 UnioAPI 注册",
			Title:    "欢迎来到 UnioAPI",
			Intro:    "请使用下面的验证码完成邮箱验证。",
			CodeMeta: "%d 分钟内有效 · 仅限本次注册",
			Fine:     "如果不是你本人操作，请忽略这封邮件。我们不会要求你通过邮件提供密码。",
			Footer:   "这是一封自动发送的邮件，请不要直接回复。",
		},
		LocaleEN: {
			Subject:  "Complete your UnioAPI registration",
			Title:    "Welcome to UnioAPI",
			Intro:    "Use the code below to verify your email address.",
			CodeMeta: "Valid for %d minutes · For this registration only",
			Fine:     "If you did not request this, you can ignore this email. UnioAPI will never ask for your password by email.",
			Footer:   "This is an automated message. Please do not reply directly.",
		},
	},
	KindVerificationLogin: {
		LocaleZH: {
			Subject:  "你的 UnioAPI 登录验证码",
			Title:    "确认这次登录",
			Intro:    "这是你请求的登录验证码，输入它即可继续访问 UnioAPI。",
			CodeMeta: "%d 分钟内有效 · 只可使用一次",
			Fine:     "如果不是你本人操作，请立即忽略这封邮件，并检查账户安全。",
			Footer:   "这是一封自动发送的邮件，请不要直接回复。",
		},
		LocaleEN: {
			Subject:  "Your UnioAPI sign-in code",
			Title:    "Confirm this sign-in",
			Intro:    "Here is the sign-in code you requested. Enter it to continue to UnioAPI.",
			CodeMeta: "Valid for %d minutes · Single use",
			Fine:     "If you did not request this, ignore the message and review your account security.",
			Footer:   "This is an automated message. Please do not reply directly.",
		},
	},
	KindVerificationPasswordReset: {
		LocaleZH: {
			Subject:  "重置你的 UnioAPI 密码",
			Title:    "设置一个新密码",
			Intro:    "请使用下面的验证码验证邮箱，然后设置新密码。",
			CodeMeta: "%d 分钟内有效 · 仅限本次密码重置",
			Fine:     "如果不是你本人操作，请忽略这封邮件。我们不会要求你通过邮件提供密码。",
			Footer:   "这是一封自动发送的邮件，请不要直接回复。",
		},
		LocaleEN: {
			Subject:  "Reset your UnioAPI password",
			Title:    "Set a new password",
			Intro:    "Use the code below to verify your email before setting a new password.",
			CodeMeta: "Valid for %d minutes · For this password reset only",
			Fine:     "If you did not request this, you can ignore this email. UnioAPI will never ask for your password by email.",
			Footer:   "This is an automated message. Please do not reply directly.",
		},
	},
	KindVerificationPasswordSet: {
		LocaleZH: {
			Subject:  "设置你的 UnioAPI 密码",
			Title:    "设置账户密码",
			Intro:    "请使用下面的验证码确认本次密码设置。",
			CodeMeta: "%d 分钟内有效 · 仅限本次密码设置",
			Fine:     "如果不是你本人操作，请忽略这封邮件。我们不会要求你通过邮件提供密码。",
			Footer:   "这是一封自动发送的邮件，请不要直接回复。",
		},
		LocaleEN: {
			Subject:  "Set your UnioAPI password",
			Title:    "Set an account password",
			Intro:    "Use the code below to confirm this password setup.",
			CodeMeta: "Valid for %d minutes · For this password setup only",
			Fine:     "If you did not request this, you can ignore this email. UnioAPI will never ask for your password by email.",
			Footer:   "This is an automated message. Please do not reply directly.",
		},
	},
	KindVerificationPasswordChange: {
		LocaleZH: {
			Subject:  "修改你的 UnioAPI 密码",
			Title:    "确认密码修改",
			Intro:    "请使用下面的验证码确认本次密码修改。",
			CodeMeta: "%d 分钟内有效 · 仅限本次密码修改",
			Fine:     "如果不是你本人操作，请忽略这封邮件并检查账户安全。我们不会要求你通过邮件提供密码。",
			Footer:   "这是一封自动发送的邮件，请不要直接回复。",
		},
		LocaleEN: {
			Subject:  "Change your UnioAPI password",
			Title:    "Confirm your password change",
			Intro:    "Use the code below to confirm this password change.",
			CodeMeta: "Valid for %d minutes · For this password change only",
			Fine:     "If you did not request this, ignore the message and review your account security. UnioAPI will never ask for your password by email.",
			Footer:   "This is an automated message. Please do not reply directly.",
		},
	},
	KindTest: {
		LocaleZH: {
			Subject: "UnioAPI SMTP 测试邮件",
			Title:   "SMTP 配置测试",
			Intro:   "这是一封来自 UnioAPI 的测试邮件。收到它即表示当前 SMTP 配置可以正常发信。",
			Fine:    "本邮件由管理员在系统设置中手动触发，与正式业务邮件使用相同发送链路。",
			Footer:  "这是一封测试邮件。",
		},
		LocaleEN: {
			Subject: "UnioAPI SMTP test email",
			Title:   "SMTP configuration test",
			Intro:   "This is a test email from UnioAPI. Receiving it means the current SMTP configuration can deliver mail.",
			Fine:    "This email was triggered manually by an administrator from system settings and uses the same delivery path as production mail.",
			Footer:  "This is a test email.",
		},
	},
}

// renderMessage 渲染主题、HTML 正文与纯文本正文。code 为空表示无验证码面板（测试邮件）。
// 纯文本 alternative 是事务邮件的标准做法，可降低被邮箱客户端归类为营销/推广的概率。
func renderMessage(kind MessageKind, locale, code string, expiresMinutes int) (subject, html, text string, err error) {
	locales, ok := copyDecks[kind]
	if !ok {
		return "", "", "", fmt.Errorf("email: unknown message kind %q", kind)
	}
	deck, ok := locales[NormalizeLocale(locale)]
	if !ok {
		deck = locales[LocaleEN]
	}

	codeMeta := ""
	if code != "" && deck.CodeMeta != "" {
		codeMeta = fmt.Sprintf(deck.CodeMeta, expiresMinutes)
	}
	var out strings.Builder
	renderErr := bodyTemplate.Execute(&out, bodyData{
		Title:    deck.Title,
		Intro:    deck.Intro,
		Code:     code,
		CodeMeta: codeMeta,
		Fine:     deck.Fine,
		Footer:   deck.Footer,
		Year:     time.Now().Year(),
	})
	if renderErr != nil {
		return "", "", "", fmt.Errorf("email: render %s body: %w", kind, renderErr)
	}

	var plain strings.Builder
	plain.WriteString(deck.Title + "\n\n" + deck.Intro + "\n")
	if code != "" {
		plain.WriteString("\n" + code + "\n" + codeMeta + "\n")
	}
	plain.WriteString("\n" + deck.Fine + "\n\n" + deck.Footer + "\n")

	return deck.Subject, out.String(), plain.String(), nil
}
