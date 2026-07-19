package route

import (
	"context"
	"net/http"

	"github.com/ThankCat/unio-gateway/internal/app/adminapi/adminhttp"
	"github.com/ThankCat/unio-gateway/internal/service/admin/routingtrace"
)

type RoutingTraceService interface {
	ListByRoute(context.Context, int64, int32, int32) ([]routingtrace.Decision, int64, error)
}

type routingTraceHandler struct {
	service RoutingTraceService
}

func (h *routingTraceHandler) list(w http.ResponseWriter, r *http.Request) {
	routeID, err := adminhttp.PathID(r)
	if err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}
	page := adminhttp.ParsePage(r)
	rows, total, err := h.service.ListByRoute(r.Context(), routeID, page.Limit(), page.Offset())
	if err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}
	out := make([]adminhttp.RoutingDecisionDTO, 0, len(rows))
	for _, row := range rows {
		out = append(out, adminhttp.NewRoutingDecisionDTO(row))
	}
	adminhttp.WriteList(w, http.StatusOK, out, page, total)
}
