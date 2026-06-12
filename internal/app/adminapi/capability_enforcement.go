package adminapi

import (
	"context"
	"net/http"
	"time"

	capsvc "github.com/ThankCat/unio-api/internal/service/admin/capability"
)

// CapabilityEnforcementService 定义 adminapi 展示 enforce 状态与 observe 分布所需的最小能力（M5）。
type CapabilityEnforcementService interface {
	Modes() []capsvc.EnforcementMode
	ObserveSummary(ctx context.Context, from, to *time.Time) ([]capsvc.ResultCount, error)
}

// enforcementDTO 是 capability enforce 只读状态响应体。
// source=deploy_env 提示这是 admin 进程读到的部署 env 快照，翻 enforce 仍需改 gateway env 重启。
type enforcementDTO struct {
	Source   string                  `json:"source"`
	Note     string                  `json:"note"`
	Surfaces []enforcementSurfaceDTO `json:"surfaces"`
}

type enforcementSurfaceDTO struct {
	Surface   string `json:"surface"`
	Operation string `json:"operation"`
	EnvVar    string `json:"env_var"`
	Mode      string `json:"mode"`
}

// observeSummaryDTO 是 observe 期 capability 判定分布响应体。
type observeSummaryDTO struct {
	From    *string            `json:"from"`
	To      *string            `json:"to"`
	Results []observeResultDTO `json:"results"`
}

type observeResultDTO struct {
	Result *string `json:"result"`
	Total  int64   `json:"total"`
}

type capabilityEnforcementHandler struct {
	service CapabilityEnforcementService
}

func (h *capabilityEnforcementHandler) get(w http.ResponseWriter, _ *http.Request) {
	modes := h.service.Modes()
	surfaces := make([]enforcementSurfaceDTO, 0, len(modes))
	for _, m := range modes {
		surfaces = append(surfaces, enforcementSurfaceDTO{
			Surface:   m.Surface,
			Operation: m.Operation,
			EnvVar:    m.EnvVar,
			Mode:      enforceModeLabel(m.Enforced),
		})
	}

	writeData(w, http.StatusOK, enforcementDTO{
		Source:   "deploy_env",
		Note:     "reflects admin-server env; flipping enforce requires gateway env change + restart",
		Surfaces: surfaces,
	})
}

func (h *capabilityEnforcementHandler) observeSummary(w http.ResponseWriter, r *http.Request) {
	from, err := optionalTimeQuery(r, "from")
	if err != nil {
		writeServiceError(w, err)
		return
	}
	to, err := optionalTimeQuery(r, "to")
	if err != nil {
		writeServiceError(w, err)
		return
	}

	results, err := h.service.ObserveSummary(r.Context(), from, to)
	if err != nil {
		writeServiceError(w, err)
		return
	}

	dtos := make([]observeResultDTO, 0, len(results))
	for _, rc := range results {
		dtos = append(dtos, observeResultDTO{Result: rc.Result, Total: rc.Total})
	}

	writeData(w, http.StatusOK, observeSummaryDTO{
		From:    rfc3339Ptr(from),
		To:      rfc3339Ptr(to),
		Results: dtos,
	})
}

func enforceModeLabel(enforced bool) string {
	if enforced {
		return "enforce"
	}
	return "observe"
}
