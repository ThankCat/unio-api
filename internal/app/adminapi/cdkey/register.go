// Package cdkey exposes the authenticated Admin CDKEY management endpoints.
package cdkey

import (
	"context"

	"github.com/go-chi/chi/v5"
	"go.uber.org/zap"

	admincdkey "github.com/ThankCat/unio-gateway/internal/service/admin/cdkey"
)

// Service is the HTTP-facing subset of the Admin CDKEY service.
type Service interface {
	List(context.Context, admincdkey.ListParams) (admincdkey.ListResult, error)
	Generate(context.Context, admincdkey.GenerateParams) (admincdkey.GenerateResult, error)
	Summary(context.Context, admincdkey.ListParams) (admincdkey.Summary, error)
	ListRedemptions(context.Context, admincdkey.ListParams) (admincdkey.RedemptionsResult, error)
	Revoke(context.Context, int64) error
	Delete(context.Context, int64) error
	BulkRevoke(context.Context, []int64) (admincdkey.BulkResult, error)
	BulkDelete(context.Context, []int64) (admincdkey.BulkResult, error)
	BulkRevokeSelection(context.Context, admincdkey.Selection) (admincdkey.BulkResult, error)
	BulkDeleteSelection(context.Context, admincdkey.Selection) (admincdkey.BulkResult, error)
	Export(context.Context, admincdkey.ExportParams) ([]admincdkey.ExportRow, error)
}

type Deps struct {
	Service Service
	// Logger receives a redacted audit event for every successful export. The
	// plaintext key is never included in this event.
	Logger *zap.Logger
}

func Register(r chi.Router, d Deps) {
	if d.Service == nil {
		return
	}
	h := &handler{service: d.Service, logger: d.Logger}
	// Static paths must be registered before /{id} for readability and routing tests.
	r.Get("/cdkeys/summary", h.summary)
	r.Get("/cdkeys/redemptions", h.redemptions)
	r.Post("/cdkeys/batches", h.generate)
	r.Post("/cdkeys/bulk-revoke", h.bulkRevoke)
	r.Post("/cdkeys/bulk-delete", h.bulkDelete)
	r.Post("/cdkeys/exports", h.export)
	r.Get("/cdkeys", h.list)
	r.Post("/cdkeys/{id}/revoke", h.revoke)
	r.Delete("/cdkeys/{id}", h.delete)
}

type handler struct {
	service Service
	logger  *zap.Logger
}
