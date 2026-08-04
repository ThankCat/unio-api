// Package adminauth 提供 admin 管理端的认证身份与认证器。
//
// 单管理员极简版：账号口令是唯一登录凭证，不做用户表、JWT 或 RBAC。两个认证器分工明确——
// StaticCredentialAuthenticator 只在登录入口校验固定用户名与口令；
// SessionAuthenticator 校验其余全部 admin 端点携带的会话 token。
//
// 会话 token 由服务端在登录成功时随机签发并存于 Redis（见 platform/adminsession），
// 不再使用配置里的静态预共享 token：静态 token 永不过期、无法吊销，一旦泄露只能人工轮换。
// 账号口令来自环境变量，不写入源码。
//
// admin 认证与客户 API key 认证（core/auth）、console 用户认证严格隔离，
// 不共用 principal、错误码或 context key。
package adminauth

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"errors"
	"strings"

	"github.com/ThankCat/unio-gateway/internal/platform/failure"
)

// SubjectAdmin 是单管理员模式下固定的 principal subject。
const SubjectAdmin = "admin"

var (
	// ErrMissingToken 表示请求未携带会话 token。
	ErrMissingToken = errors.New("admin session token missing")

	// ErrSessionExpired 表示会话 token 不存在或已过期。
	ErrSessionExpired = errors.New("admin session expired")

	// ErrInvalidCredentials 表示登录用户名或口令不匹配。
	// 两种情况共用同一个错误，避免调用方据此判断用户名是否存在。
	ErrInvalidCredentials = errors.New("admin credentials invalid")
)

// Principal 表示已通过 admin 认证的调用者。
//
// 单管理员极简版只携带稳定 subject 标识，后续接入真实账号体系时再扩展身份字段。
type Principal struct {
	// Subject 是 admin 调用者的稳定标识；单管理员模式固定为 SubjectAdmin。
	Subject string
}

// SessionValidator 定义认证器校验会话所需的最小能力，由 platform/adminsession 实现。
type SessionValidator interface {
	Validate(ctx context.Context, token string) (bool, error)
}

// SessionAuthenticator 用 Redis 会话校验 admin 请求携带的 token。
type SessionAuthenticator struct {
	sessions SessionValidator
}

// NewSessionAuthenticator 创建会话认证器。
func NewSessionAuthenticator(sessions SessionValidator) (*SessionAuthenticator, error) {
	if sessions == nil {
		return nil, failure.New(
			failure.CodeConfigMissing,
			failure.WithMessage("admin session store is required"),
		)
	}

	return &SessionAuthenticator{sessions: sessions}, nil
}

// AuthenticateAdmin 校验会话 token 并返回管理员身份。
//
// 三种结果严格区分：缺 token 返回 missing_token；token 无对应会话返回 session_expired；
// 会话存储故障原样上抛 store_failed，绝不降级成「过期」——否则 Redis 抖动会表现为
// 管理员被反复登出，掩盖真正的基础设施问题。
func (a *SessionAuthenticator) AuthenticateAdmin(ctx context.Context, token string) (*Principal, error) {
	if token == "" {
		return nil, failure.Wrap(
			failure.CodeAdminAuthMissingToken,
			ErrMissingToken,
			failure.WithMessage(ErrMissingToken.Error()),
		)
	}

	valid, err := a.sessions.Validate(ctx, token)
	if err != nil {
		return nil, err
	}
	if !valid {
		return nil, failure.Wrap(
			failure.CodeAdminAuthSessionExpired,
			ErrSessionExpired,
			failure.WithMessage(ErrSessionExpired.Error()),
		)
	}

	return &Principal{Subject: SubjectAdmin}, nil
}

// StaticCredentialAuthenticator 用固定用户名与口令认证登录请求。
//
// 只服务登录入口；换取会话 token 后的其余请求由 SessionAuthenticator 校验。
type StaticCredentialAuthenticator struct {
	username [sha256.Size]byte
	password [sha256.Size]byte
}

// NewStaticCredentialAuthenticator 创建用户名口令认证器。
//
// 任一项为空表示 ADMIN_USERNAME / ADMIN_PASSWORD 未配置，返回 config_missing，
// 由启动流程尽早失败，避免以空口令对外提供登录入口。
func NewStaticCredentialAuthenticator(username, password string) (*StaticCredentialAuthenticator, error) {
	if strings.TrimSpace(username) == "" {
		return nil, failure.New(
			failure.CodeConfigMissing,
			failure.WithMessage("ADMIN_USERNAME is required"),
		)
	}
	if password == "" {
		return nil, failure.New(
			failure.CodeConfigMissing,
			failure.WithMessage("ADMIN_PASSWORD is required"),
		)
	}

	return &StaticCredentialAuthenticator{
		username: sha256.Sum256([]byte(username)),
		password: sha256.Sum256([]byte(password)),
	}, nil
}

// AuthenticateCredentials 校验用户名与口令，通过则返回管理员身份。
//
// 两项先各自 SHA-256 再做常量时间比较：定长摘要让比较耗时不随输入长度变化，
// 避免口令长度经由计时侧信道泄露。两项结果用按位与合并而非短路布尔，
// 保证用户名错误时仍执行口令比较，耗时不因哪一项先失配而不同。
func (a *StaticCredentialAuthenticator) AuthenticateCredentials(_ context.Context, username, password string) (*Principal, error) {
	gotUser := sha256.Sum256([]byte(username))
	gotPass := sha256.Sum256([]byte(password))

	userMatch := subtle.ConstantTimeCompare(gotUser[:], a.username[:])
	passMatch := subtle.ConstantTimeCompare(gotPass[:], a.password[:])

	if userMatch&passMatch != 1 {
		return nil, failure.Wrap(
			failure.CodeAdminAuthInvalidCredentials,
			ErrInvalidCredentials,
			failure.WithMessage(ErrInvalidCredentials.Error()),
		)
	}

	return &Principal{Subject: SubjectAdmin}, nil
}
