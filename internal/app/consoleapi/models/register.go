// Package models 提供 Console 模型目录的 HTTP 适配层。
//
// 数据来自与 website 共享的 publicmodels 查询服务（标价即结算价），但 DTO 独立定义：
// 两个 surface 的字段各自演进，互不牵连。挂在 RequireAuth 内——目录本身无租户语义，
// 但 console 的一切内容都以登录为前提，与其余页面保持一致的访问边界。
package models

import (
	"context"

	"github.com/go-chi/chi/v5"

	"github.com/ThankCat/unio-gateway/internal/app/consoleapi/transport"
	"github.com/ThankCat/unio-gateway/internal/service/publicmodels"
)

// Service 定义模型目录查询能力。
type Service interface {
	List(ctx context.Context) ([]publicmodels.Model, error)
}

var _ Service = (*publicmodels.Service)(nil)

// Deps 包含模型目录 HTTP 适配层的依赖。
type Deps struct {
	Service     Service
	ErrorWriter transport.ErrorWriter
}

// Register 将模型目录路由挂载到 /models。
func Register(r chi.Router, deps Deps) {
	h := &handler{
		service:     deps.Service,
		errorWriter: deps.ErrorWriter,
	}
	r.Get("/models", h.list)
}

type handler struct {
	service     Service
	errorWriter transport.ErrorWriter
}
