package system

import (
	"context"
	"net/http"

	"github.com/ThankCat/unio-gateway/internal/app/adminapi/adminhttp"
	"github.com/ThankCat/unio-gateway/internal/service/admin/runtimediagnostics"
)

type RuntimeDiagnosticsService interface {
	Get(ctx context.Context) (runtimediagnostics.Diagnostics, error)
}

type runtimeDiagnosticsHandler struct {
	service RuntimeDiagnosticsService
}

type runtimeDiagnosticsDTO struct {
	Readiness         readinessDTO         `json:"readiness"`
	RuntimeStateEpoch stateEpochDTO        `json:"runtime_state_epoch"`
	Operations        operationFamiliesDTO `json:"operations"`
}

type readinessDTO struct {
	Ready  bool   `json:"ready"`
	Reason string `json:"reason"`
}

type stateEpochDTO struct {
	State    string `json:"state"`
	Revision int64  `json:"revision"`
	Match    bool   `json:"match"`
}

type operationFamiliesDTO struct {
	EndpointRouting operationSummaryDTO `json:"endpoint_routing"`
	RuntimeControl  operationSummaryDTO `json:"runtime_control"`
}

type operationSummaryDTO struct {
	NonterminalCount int64  `json:"nonterminal_count"`
	OldestAgeSeconds *int64 `json:"oldest_age_seconds"`
}

func (h *runtimeDiagnosticsHandler) get(w http.ResponseWriter, r *http.Request) {
	diagnostics, err := h.service.Get(r.Context())
	if err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}
	adminhttp.WriteData(w, http.StatusOK, toRuntimeDiagnosticsDTO(diagnostics))
}

func toRuntimeDiagnosticsDTO(diagnostics runtimediagnostics.Diagnostics) runtimeDiagnosticsDTO {
	return runtimeDiagnosticsDTO{
		Readiness: readinessDTO{
			Ready: diagnostics.Readiness.Ready, Reason: diagnostics.Readiness.Reason,
		},
		RuntimeStateEpoch: stateEpochDTO{
			State:    diagnostics.RuntimeStateEpoch.State,
			Revision: diagnostics.RuntimeStateEpoch.Revision,
			Match:    diagnostics.RuntimeStateEpoch.Match,
		},
		Operations: operationFamiliesDTO{
			EndpointRouting: toOperationSummaryDTO(diagnostics.Operations.EndpointRouting),
			RuntimeControl:  toOperationSummaryDTO(diagnostics.Operations.RuntimeControl),
		},
	}
}

func toOperationSummaryDTO(summary runtimediagnostics.OperationSummary) operationSummaryDTO {
	return operationSummaryDTO{
		NonterminalCount: summary.NonterminalCount,
		OldestAgeSeconds: summary.OldestAgeSeconds,
	}
}
