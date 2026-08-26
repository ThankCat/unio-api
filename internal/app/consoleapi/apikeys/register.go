// Package apikeys 暴露 Console 侧的 API 密钥自助管理接口。
//
// 归属由 handler 强制：UserID 一律取自会话 principal，从不接受请求体或查询参数里的
// user_id。这样即便 service 层将来漏了一处校验，也不可能从 HTTP 层越权。
package apikeys

import (
	"context"

	"github.com/go-chi/chi/v5"

	"github.com/ThankCat/unio-gateway/internal/app/consoleapi/transport"
	consoleservice "github.com/ThankCat/unio-gateway/internal/service/console"
	consoleapikeys "github.com/ThankCat/unio-gateway/internal/service/console/apikeys"
)

// Service 定义 HTTP 适配层依赖的密钥管理能力。
type Service interface {
	List(context.Context, consoleapikeys.ListParams) ([]consoleapikeys.Key, int64, *consoleservice.Error)
	Summary(context.Context, int64, consoleapikeys.Window) (consoleapikeys.Summary, *consoleservice.Error)
	Get(context.Context, consoleapikeys.GetParams) (consoleapikeys.Detail, *consoleservice.Error)
	Create(context.Context, consoleapikeys.CreateParams) (consoleapikeys.CreatedKey, *consoleservice.Error)
	Update(context.Context, consoleapikeys.UpdateParams) (consoleapikeys.Key, *consoleservice.Error)
	Revoke(context.Context, int64, int64) (consoleapikeys.Key, *consoleservice.Error)
	Delete(context.Context, int64, int64) *consoleservice.Error
}

var _ Service = (*consoleapikeys.Service)(nil)

// Deps 包含密钥管理 HTTP 适配层的依赖。
type Deps struct {
	Service     Service
	ErrorWriter transport.ErrorWriter
}

// Register 将密钥管理路由挂载到 /api-keys。
func Register(r chi.Router, deps Deps) {
	h := &handler{
		service:     deps.Service,
		errorWriter: deps.ErrorWriter,
	}
	r.Route("/api-keys", func(r chi.Router) {
		r.Get("/", h.list)
		r.Post("/", h.create)
		r.Get("/summary", h.summary)
		r.Get("/{id}", h.get)
		r.Patch("/{id}", h.update)
		r.Post("/{id}/revoke", h.revoke)
		r.Delete("/{id}", h.remove)
	})
}

type handler struct {
	service     Service
	errorWriter transport.ErrorWriter
}
