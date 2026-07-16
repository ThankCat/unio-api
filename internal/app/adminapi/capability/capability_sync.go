package capability

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/ThankCat/unio-gateway/internal/app/adminapi/adminhttp"

	corecap "github.com/ThankCat/unio-gateway/internal/core/capability"
	"github.com/ThankCat/unio-gateway/internal/core/modelcatalog"
	"github.com/ThankCat/unio-gateway/internal/platform/httpx"
	capsvc "github.com/ThankCat/unio-gateway/internal/service/admin/capability"
)

// CapabilitySyncService 定义 adminapi 触发/展示 models.dev 同步所需的最小能力（M5）。
type CapabilitySyncService interface {
	ListJobs(ctx context.Context, params capsvc.ListJobsParams) ([]corecap.SyncJob, int64, error)
	Trigger(ctx context.Context, dryRun bool) (modelcatalog.Result, error)
}

// syncJobDTO 是一条能力同步任务的审计记录响应体。
type syncJobDTO struct {
	ID         int64           `json:"id"`
	Source     string          `json:"source"`
	Status     string          `json:"status"`
	StartedAt  *string         `json:"started_at"`
	FinishedAt *string         `json:"finished_at"`
	Stats      json.RawMessage `json:"stats"`
	ErrorText  *string         `json:"error_text"`
	CreatedAt  string          `json:"created_at"`
}

// syncResultDTO 是一次同步触发的结果摘要（dry-run 不写库）。
type syncResultDTO struct {
	DryRun              bool     `json:"dry_run"`
	FeedModels          int      `json:"feed_models"`
	Upserted            int      `json:"upserted"`
	Removed             int      `json:"removed"`
	CapabilityHints     int      `json:"capability_hints"`
	RemovedCanonicalIDs []string `json:"removed_canonical_ids"`
	Fingerprint         string   `json:"fingerprint"`
}

type triggerSyncRequest struct {
	DryRun bool `json:"dry_run"`
}

type capabilitySyncHandler struct {
	service CapabilitySyncService
}

func (h *capabilitySyncHandler) listJobs(w http.ResponseWriter, r *http.Request) {
	page := adminhttp.ParsePage(r)
	sort, err := adminhttp.ParseListSort(r, map[string]struct{}{
		"created_at": {},
		"status":     {},
		"source":     {},
	}, "created_at", true)
	if err != nil {
		adminhttp.WriteSortError(w, err)
		return
	}
	field, desc := sort.SQLParams()
	jobs, total, err := h.service.ListJobs(r.Context(), capsvc.ListJobsParams{
		SortField: field,
		SortDesc:  desc,
		Limit:     page.Limit(),
		Offset:    page.Offset(),
	})
	if err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}

	dtos := make([]syncJobDTO, 0, len(jobs))
	for _, job := range jobs {
		dtos = append(dtos, toSyncJobDTO(job))
	}
	adminhttp.WriteList(w, http.StatusOK, dtos, page, total)
}

func (h *capabilitySyncHandler) trigger(w http.ResponseWriter, r *http.Request) {
	var req triggerSyncRequest
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}

	result, err := h.service.Trigger(r.Context(), req.DryRun)
	if err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}

	adminhttp.WriteData(w, http.StatusOK, toSyncResultDTO(result))
}

func toSyncJobDTO(j corecap.SyncJob) syncJobDTO {
	return syncJobDTO{
		ID:         j.ID,
		Source:     string(j.Source),
		Status:     string(j.Status),
		StartedAt:  adminhttp.RFC3339Ptr(j.StartedAt),
		FinishedAt: adminhttp.RFC3339Ptr(j.FinishedAt),
		Stats:      j.Stats,
		ErrorText:  j.ErrorText,
		CreatedAt:  j.CreatedAt.UTC().Format(time.RFC3339),
	}
}

func toSyncResultDTO(r modelcatalog.Result) syncResultDTO {
	removed := r.RemovedCanonicalIDs
	if removed == nil {
		removed = []string{}
	}
	return syncResultDTO{
		DryRun:              r.DryRun,
		FeedModels:          r.FeedModels,
		Upserted:            r.Upserted,
		Removed:             r.Removed,
		CapabilityHints:     r.CapabilityHints,
		RemovedCanonicalIDs: removed,
		Fingerprint:         r.Fingerprint,
	}
}
