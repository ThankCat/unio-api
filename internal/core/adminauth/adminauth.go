// Package adminauth 提供 admin 管理端的认证身份与认证器。
//
// 单管理员极简版：用单个静态 token（ADMIN_API_TOKEN）认证，不做用户表、会话、
// JWT 或 RBAC。admin 认证与客户 API key 认证（core/auth）、console 用户认证严格隔离，
// 不共用 principal、错误码或 context key。
package adminauth

import (
	"context"
	"crypto/subtle"
	"errors"
	"strings"

	"github.com/ThankCat/unio-api/internal/platform/failure"
)

// SubjectAdmin 是单管理员模式下固定的 principal subject。
const SubjectAdmin = "admin"

var (
	// ErrMissingToken 表示请求未携带 admin token。
	ErrMissingToken = errors.New("admin token missing")

	// ErrInvalidToken 表示 admin token 与配置不匹配。
	ErrInvalidToken = errors.New("admin token invalid")
)

// Principal 表示已通过 admin 认证的调用者。
//
// 单管理员极简版只携带稳定 subject 标识，后续接入真实账号体系时再扩展身份字段。
type Principal struct {
	// Subject 是 admin 调用者的稳定标识；单管理员模式固定为 SubjectAdmin。
	Subject string
}

// StaticTokenAuthenticator 用单个静态 token 认证管理员，比对走常量时间避免计时侧信道。
type StaticTokenAuthenticator struct {
	token string
}

// NewStaticTokenAuthenticator 创建静态 token 认证器。
//
// token 为空表示 ADMIN_API_TOKEN 未配置，返回 config_missing，由启动流程尽早失败。
func NewStaticTokenAuthenticator(token string) (*StaticTokenAuthenticator, error) {
	if strings.TrimSpace(token) == "" {
		return nil, failure.New(
			failure.CodeConfigMissing,
			failure.WithMessage("ADMIN_API_TOKEN is required"),
		)
	}

	return &StaticTokenAuthenticator{token: token}, nil
}

// AuthenticateAdmin 校验调用方 token 是否与配置 token 一致，返回管理员身份。
//
// 缺失 token 返回 adminauth_missing_token，不匹配返回 adminauth_invalid_token。
func (a *StaticTokenAuthenticator) AuthenticateAdmin(_ context.Context, token string) (*Principal, error) {
	if token == "" {
		return nil, failure.Wrap(
			failure.CodeAdminAuthMissingToken,
			ErrMissingToken,
			failure.WithMessage(ErrMissingToken.Error()),
		)
	}

	if subtle.ConstantTimeCompare([]byte(token), []byte(a.token)) != 1 {
		return nil, failure.Wrap(
			failure.CodeAdminAuthInvalidToken,
			ErrInvalidToken,
			failure.WithMessage(ErrInvalidToken.Error()),
		)
	}

	return &Principal{Subject: SubjectAdmin}, nil
}
