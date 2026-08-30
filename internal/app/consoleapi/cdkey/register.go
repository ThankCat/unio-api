// Package cdkey exposes the single Console wallet CDKEY redemption endpoint.
package cdkey

import (
	"context"

	"github.com/go-chi/chi/v5"

	"github.com/ThankCat/unio-gateway/internal/app/consoleapi/transport"
	consoleservice "github.com/ThankCat/unio-gateway/internal/service/console"
	consolecdkey "github.com/ThankCat/unio-gateway/internal/service/console/cdkey"
)

type Service interface {
	Redeem(context.Context, int64, string) (consolecdkey.Redemption, *consoleservice.Error)
}

// IPService is an optional extension implemented by the concrete service. It
// lets the handler feed the trusted client address into the failed-attempt
// limiter without breaking lightweight test fakes that implement Service.
type IPService interface {
	RedeemWithIP(context.Context, int64, string, string) (consolecdkey.Redemption, *consoleservice.Error)
}

type Deps struct {
	Service     Service
	ErrorWriter transport.ErrorWriter
}

func Register(r chi.Router, d Deps) {
	if d.Service == nil {
		return
	}
	h := &handler{service: d.Service, errorWriter: d.ErrorWriter}
	r.Post("/wallet/cdkey-redemptions", h.redeem)
}

type handler struct {
	service     Service
	errorWriter transport.ErrorWriter
}
