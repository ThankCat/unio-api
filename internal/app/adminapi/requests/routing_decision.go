package requests

import (
	"context"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/ThankCat/unio-gateway/internal/app/adminapi/adminhttp"
	"github.com/ThankCat/unio-gateway/internal/service/admin/routingtrace"
)

type RoutingTraceService interface {
	GetByRequestID(context.Context, string) (routingtrace.Decision, error)
}

type routingTraceHandler struct {
	service RoutingTraceService
}

func (h *routingTraceHandler) get(w http.ResponseWriter, r *http.Request) {
	requestID := strings.TrimSpace(chi.URLParam(r, "requestId"))
	decision, err := h.service.GetByRequestID(r.Context(), requestID)
	if err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}
	adminhttp.WriteData(w, http.StatusOK, adminhttp.NewRoutingDecisionDTO(decision))
}
