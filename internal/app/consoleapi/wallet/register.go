// Package wallet 提供 Console 钱包流水的 HTTP 适配层。
package wallet

import (
	"context"

	"github.com/go-chi/chi/v5"

	"github.com/ThankCat/unio-gateway/internal/app/consoleapi/transport"
	consoleservice "github.com/ThankCat/unio-gateway/internal/service/console"
	consolewallet "github.com/ThankCat/unio-gateway/internal/service/console/wallet"
)

// Service 定义 HTTP 适配层依赖的钱包流水查询能力。
type Service interface {
	List(context.Context, consolewallet.ListParams) ([]consolewallet.Entry, int64, *consoleservice.Error)
}

var _ Service = (*consolewallet.Service)(nil)

// Deps 包含钱包 HTTP 适配层的依赖。
type Deps struct {
	Service     Service
	ErrorWriter transport.ErrorWriter
}

// Register 将钱包路由挂载到 /wallet。
func Register(r chi.Router, deps Deps) {
	h := &handler{
		service:     deps.Service,
		errorWriter: deps.ErrorWriter,
	}
	r.Route("/wallet", func(r chi.Router) {
		r.Get("/transactions", h.transactions)
	})
}

type handler struct {
	service     Service
	errorWriter transport.ErrorWriter
}
