package system

import (
	"context"
	"errors"
	"net/http"
	"strconv"

	"github.com/ThankCat/unio-gateway/internal/app/adminapi/adminhttp"
	"github.com/ThankCat/unio-gateway/internal/platform/failure"
	"github.com/ThankCat/unio-gateway/internal/platform/httpx"
	admingatewaylogging "github.com/ThankCat/unio-gateway/internal/service/admin/gatewaylogging"
	"github.com/ThankCat/unio-gateway/internal/service/appsettings"
)

const staticAdminOperatorID int64 = 0

type GatewayLoggingService interface {
	Get(context.Context) (admingatewaylogging.Snapshot, error)
	Start(context.Context, int, string, int64) (admingatewaylogging.Snapshot, error)
	Stop(context.Context, int64) (admingatewaylogging.Snapshot, error)
	ListLogs(context.Context, admingatewaylogging.LogQuery) (admingatewaylogging.LogList, error)
}

type gatewayLoggingHandler struct {
	service GatewayLoggingService
}

type startGatewayDebugRequest struct {
	DurationMinutes int    `json:"duration_minutes"`
	Reason          string `json:"reason"`
}

func (h *gatewayLoggingHandler) get(w http.ResponseWriter, r *http.Request) {
	snapshot, err := h.service.Get(r.Context())
	if err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}
	adminhttp.WriteData(w, http.StatusOK, snapshot)
}

func (h *gatewayLoggingHandler) start(w http.ResponseWriter, r *http.Request) {
	var request startGatewayDebugRequest
	if err := httpx.DecodeJSON(w, r, &request); err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}
	snapshot, err := h.service.Start(
		r.Context(), request.DurationMinutes, request.Reason, staticAdminOperatorID,
	)
	if err != nil {
		if errors.Is(err, appsettings.ErrGatewayDebugRequestInvalid) {
			adminhttp.WriteServiceError(w, failure.New(
				failure.CodeAdminInvalidArgument,
				failure.WithMessage(err.Error()),
				failure.WithField("field", "debug_session"),
			))
			return
		}
		adminhttp.WriteServiceError(w, err)
		return
	}
	adminhttp.WriteData(w, http.StatusOK, snapshot)
}

func (h *gatewayLoggingHandler) stop(w http.ResponseWriter, r *http.Request) {
	snapshot, err := h.service.Stop(r.Context(), staticAdminOperatorID)
	if err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}
	adminhttp.WriteData(w, http.StatusOK, snapshot)
}

func (h *gatewayLoggingHandler) listLogs(w http.ResponseWriter, r *http.Request) {
	limit := 0
	if raw := r.URL.Query().Get("limit"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil {
			adminhttp.WriteServiceError(w, adminhttp.InvalidRequestField("limit", "limit must be an integer"))
			return
		}
		limit = parsed
	}
	result, err := h.service.ListLogs(r.Context(), admingatewaylogging.LogQuery{
		Window:    r.URL.Query().Get("range"),
		Level:     r.URL.Query().Get("level"),
		Type:      r.URL.Query().Get("type"),
		Event:     r.URL.Query().Get("event"),
		RelatedID: r.URL.Query().Get("related_id"),
		Search:    r.URL.Query().Get("search"),
		Limit:     limit,
	})
	if err != nil {
		if errors.Is(err, admingatewaylogging.ErrLogQueryInvalid) {
			adminhttp.WriteServiceError(w, failure.New(
				failure.CodeAdminInvalidArgument,
				failure.WithMessage(err.Error()),
				failure.WithField("field", "log_query"),
			))
			return
		}
		adminhttp.WriteServiceError(w, err)
		return
	}
	adminhttp.WriteData(w, http.StatusOK, result)
}
