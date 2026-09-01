// Package auth 提供 Console 认证模块的 HTTP 路由、DTO 和协议处理。
package auth

import (
	"context"

	"github.com/go-chi/chi/v5"

	"github.com/ThankCat/unio-gateway/internal/app/consoleapi/transport"
	consoleservice "github.com/ThankCat/unio-gateway/internal/service/console"
	serviceauth "github.com/ThankCat/unio-gateway/internal/service/console/auth"
)

// Service 定义 HTTP 适配层依赖的认证能力。
type Service interface {
	CheckEmail(context.Context, string) *consoleservice.Error
	CheckRegistrationEmail(context.Context, string) *consoleservice.Error
	SendChallenge(context.Context, string, string, string, string) (serviceauth.Challenge, *consoleservice.Error)
	Register(context.Context, string, string, string, string, string, string) (serviceauth.User, serviceauth.TokenPair, *consoleservice.Error)
	PasswordLogin(context.Context, string, string, string, string) (serviceauth.User, serviceauth.TokenPair, *consoleservice.Error)
	EmailCodeLogin(context.Context, string, string, string, string, string) (serviceauth.User, serviceauth.TokenPair, *consoleservice.Error)
	CurrentUser(context.Context, string) (serviceauth.User, *consoleservice.Error)
	AuthenticatePrincipal(context.Context, string) (serviceauth.Principal, *consoleservice.Error)
	VerifyPasswordResetCode(context.Context, string, string, string, string) (serviceauth.PasswordResetGrant, *consoleservice.Error)
	ResetPassword(context.Context, string, string) *consoleservice.Error
	Refresh(context.Context, string) (serviceauth.TokenPair, *consoleservice.Error)
	Logout(context.Context, string) *consoleservice.Error
	LogoutAll(context.Context, string) *consoleservice.Error
	UpdateDisplayName(context.Context, string, string) (serviceauth.User, *consoleservice.Error)
	SendPasswordChallenge(context.Context, string, string, string) (serviceauth.Challenge, *consoleservice.Error)
	UpdatePassword(context.Context, string, string, string, string, string) *consoleservice.Error
	ListSessions(context.Context, string) ([]serviceauth.SessionEntry, *consoleservice.Error)
	RevokeSession(context.Context, string, string) *consoleservice.Error
	LogoutOthers(context.Context, string) *consoleservice.Error
}

// Deps 包含认证 HTTP 适配层的依赖。
type Deps struct {
	CookieDomain string
	CookieSecure bool
	Service      Service
	ErrorWriter  transport.ErrorWriter
}

// Register 将 Console 认证路由挂载到 /auth。
func Register(r chi.Router, deps Deps) {
	h := &handler{
		cookieDomain: deps.CookieDomain,
		cookieSecure: deps.CookieSecure,
		service:      deps.Service,
		errorWriter:  deps.ErrorWriter,
	}
	r.Route("/auth", func(r chi.Router) {
		r.Get("/me", h.currentUser)
		r.Patch("/me", h.updateMe)
		r.Post("/email-checks", h.emailCheck)
		r.Post("/registration-email-checks", h.registrationEmailCheck)
		r.Post("/email-challenges", h.emailChallenge)
		r.Post("/registrations", h.registration)
		r.Post("/password-challenges", h.passwordChallenge)
		r.Put("/password", h.passwordUpdate)
		r.Get("/sessions", h.listSessions)
		r.Delete("/sessions/{sid}", h.revokeSession)
		r.Post("/sessions/password", h.passwordSession)
		r.Post("/sessions/email-code", h.emailCodeSession)
		r.Post("/sessions/refresh", h.refresh)
		r.Post("/sessions/logout", h.logout)
		r.Post("/sessions/logout-all", h.logoutAll)
		r.Post("/sessions/logout-others", h.logoutOthers)
		r.Post("/password-reset-verifications", h.passwordResetVerification)
		r.Post("/password-resets", h.passwordReset)
	})
}

type handler struct {
	cookieDomain string
	cookieSecure bool
	service      Service
	errorWriter  transport.ErrorWriter
}
