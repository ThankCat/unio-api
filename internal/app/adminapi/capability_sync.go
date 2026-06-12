package adminapi

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	corecap "github.com/ThankCat/unio-api/internal/core/capability"
	"github.com/ThankCat/unio-api/internal/core/modelcatalog"
	"github.com/ThankCat/unio-api/internal/platform/httpx"
)

// CapabilitySyncService 定义 adminapi 触发/展示 models.dev 同步所需的最小能力（M5）。
type CapabilitySyncService interface {
	ListJobs(ctx context.Context, limit int32) ([]corecap.SyncJob, error)
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
	Inserted            int      `json:"inserted"`
	Updated             int      `json:"updated"`
	Skipped             int      `json:"skipped"`
	Removed             int      `json:"removed"`
	CapabilitiesSeeded  int      `json:"capabilities_seeded"`
	ManualConflicts     []string `json:"manual_conflicts"`
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
	var limit int32
	if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil {
			limit = int32(n)
		}
	}

	jobs, err := h.service.ListJobs(r.Context(), limit)
	if err != nil {
		writeServiceError(w, err)
		return
	}

	dtos := make([]syncJobDTO, 0, len(jobs))
	for _, job := range jobs {
		dtos = append(dtos, toSyncJobDTO(job))
	}
	writeData(w, http.StatusOK, dtos)
}

func (h *capabilitySyncHandler) trigger(w http.ResponseWriter, r *http.Request) {
	var req triggerSyncRequest
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		writeServiceError(w, err)
		return
	}

	result, err := h.service.Trigger(r.Context(), req.DryRun)
	if err != nil {
		writeServiceError(w, err)
		return
	}

	writeData(w, http.StatusOK, toSyncResultDTO(result))
}

func toSyncJobDTO(j corecap.SyncJob) syncJobDTO {
	return syncJobDTO{
		ID:         j.ID,
		Source:     string(j.Source),
		Status:     string(j.Status),
		StartedAt:  rfc3339Ptr(j.StartedAt),
		FinishedAt: rfc3339Ptr(j.FinishedAt),
		Stats:      j.Stats,
		ErrorText:  j.ErrorText,
		CreatedAt:  j.CreatedAt.UTC().Format(time.RFC3339),
	}
}

func toSyncResultDTO(r modelcatalog.Result) syncResultDTO {
	manualConflicts := r.ManualConflicts
	if manualConflicts == nil {
		manualConflicts = []string{}
	}
	removed := r.RemovedCanonicalIDs
	if removed == nil {
		removed = []string{}
	}
	return syncResultDTO{
		DryRun:              r.DryRun,
		FeedModels:          r.FeedModels,
		Inserted:            r.Inserted,
		Updated:             r.Updated,
		Skipped:             r.Skipped,
		Removed:             r.Removed,
		CapabilitiesSeeded:  r.CapabilitiesSeeded,
		ManualConflicts:     manualConflicts,
		RemovedCanonicalIDs: removed,
		Fingerprint:         r.Fingerprint,
	}
}
