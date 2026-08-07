package channel

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/ThankCat/unio-gateway/internal/app/adminapi/adminhttp"
	"github.com/ThankCat/unio-gateway/internal/platform/httpx"
	"github.com/ThankCat/unio-gateway/internal/service/admin/channelmodelinventory"
	"github.com/ThankCat/unio-gateway/internal/service/admin/modelcatalog"
)

// ChannelModelInventoryService 定义渠道模型发现、清单与验证 API 的最小能力。
type ChannelModelInventoryService interface {
	CreateDiscovery(ctx context.Context, channelID int64, source string) (channelmodelinventory.Run, error)
	GetDiscovery(ctx context.Context, channelID, runID int64) (channelmodelinventory.Run, error)
	ListDiscoveries(ctx context.Context, channelID int64, limit, offset int32) (channelmodelinventory.RunPage, error)
	GetInventory(ctx context.Context, channelID int64) (channelmodelinventory.Inventory, error)
	CreateVerification(ctx context.Context, channelID int64, targets []channelmodelinventory.VerificationTarget, source string) (channelmodelinventory.VerificationResult, error)
	GetVerification(ctx context.Context, channelID, runID int64) (channelmodelinventory.VerificationResult, error)
	BindBatch(ctx context.Context, channelID int64, inputs []channelmodelinventory.BatchBindingInput) ([]channelmodelinventory.BindingResult, error)
	AdoptAndBind(ctx context.Context, in modelcatalog.AdoptAndBindInput) (channelmodelinventory.BindingResult, error)
}

type channelModelInventoryHandler struct {
	service ChannelModelInventoryService
}

type createDiscoveryRequest struct {
	Source string `json:"source"`
}

func (h *channelModelInventoryHandler) createDiscovery(w http.ResponseWriter, r *http.Request) {
	channelID, err := adminhttp.PathID(r)
	if err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}
	var req createDiscoveryRequest
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}
	if req.Source == "" {
		req.Source = channelmodelinventory.DiscoverySourceManual
	}
	run, err := h.service.CreateDiscovery(r.Context(), channelID, req.Source)
	if err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}
	adminhttp.WriteData(w, http.StatusAccepted, toInventoryRunDTO(run))
}

func (h *channelModelInventoryHandler) getDiscovery(w http.ResponseWriter, r *http.Request) {
	channelID, err := adminhttp.PathID(r)
	if err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}
	runID, err := adminhttp.PathInt64(r, "runId")
	if err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}
	run, err := h.service.GetDiscovery(r.Context(), channelID, runID)
	if err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}
	adminhttp.WriteData(w, http.StatusOK, toInventoryRunDTO(run))
}

func (h *channelModelInventoryHandler) listDiscoveries(w http.ResponseWriter, r *http.Request) {
	channelID, err := adminhttp.PathID(r)
	if err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}
	page := adminhttp.ParsePage(r)
	result, err := h.service.ListDiscoveries(r.Context(), channelID, page.Limit(), page.Offset())
	if err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}
	items := make([]inventoryRunDTO, 0, len(result.Items))
	for _, run := range result.Items {
		items = append(items, toInventoryRunDTO(run))
	}
	adminhttp.WriteList(w, http.StatusOK, items, page, result.Total)
}

func (h *channelModelInventoryHandler) inventory(w http.ResponseWriter, r *http.Request) {
	channelID, err := adminhttp.PathID(r)
	if err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}
	result, err := h.service.GetInventory(r.Context(), channelID)
	if err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}
	adminhttp.WriteData(w, http.StatusOK, toInventoryDTO(result))
}

type createVerificationRequest struct {
	Source   string  `json:"source"`
	ModelIDs []int64 `json:"model_ids"`
	Targets  []struct {
		ModelID       int64  `json:"model_id"`
		UpstreamModel string `json:"upstream_model"`
	} `json:"targets"`
}

func (h *channelModelInventoryHandler) createVerification(w http.ResponseWriter, r *http.Request) {
	channelID, err := adminhttp.PathID(r)
	if err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}
	var req createVerificationRequest
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}
	if req.Source == "" {
		req.Source = channelmodelinventory.VerificationSourceManual
	}
	targets := make([]channelmodelinventory.VerificationTarget, 0, len(req.ModelIDs)+len(req.Targets))
	for _, modelID := range req.ModelIDs {
		targets = append(targets, channelmodelinventory.VerificationTarget{ModelID: modelID})
	}
	for _, target := range req.Targets {
		targets = append(targets, channelmodelinventory.VerificationTarget{
			ModelID: target.ModelID, UpstreamModel: target.UpstreamModel,
		})
	}
	result, err := h.service.CreateVerification(r.Context(), channelID, targets, req.Source)
	if err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}
	adminhttp.WriteData(w, http.StatusAccepted, toVerificationResultDTO(result))
}

func (h *channelModelInventoryHandler) getVerification(w http.ResponseWriter, r *http.Request) {
	channelID, err := adminhttp.PathID(r)
	if err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}
	runID, err := adminhttp.PathInt64(r, "runId")
	if err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}
	result, err := h.service.GetVerification(r.Context(), channelID, runID)
	if err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}
	adminhttp.WriteData(w, http.StatusOK, toVerificationResultDTO(result))
}

type batchBindingRequest struct {
	Bindings []struct {
		ModelID       int64  `json:"model_id"`
		UpstreamModel string `json:"upstream_model"`
	} `json:"bindings"`
}

func (h *channelModelInventoryHandler) bindBatch(w http.ResponseWriter, r *http.Request) {
	channelID, err := adminhttp.PathID(r)
	if err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}
	var req batchBindingRequest
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}
	inputs := make([]channelmodelinventory.BatchBindingInput, 0, len(req.Bindings))
	for _, binding := range req.Bindings {
		inputs = append(inputs, channelmodelinventory.BatchBindingInput{
			ModelID: binding.ModelID, UpstreamModel: binding.UpstreamModel,
		})
	}
	bindings, err := h.service.BindBatch(r.Context(), channelID, inputs)
	if err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}
	adminhttp.WriteData(w, http.StatusCreated, toBindingResultDTOs(bindings))
}

type adoptAndBindRequest struct {
	CanonicalID              string  `json:"canonical_id"`
	ModelID                  string  `json:"model_id"`
	DisplayName              string  `json:"display_name"`
	OwnedBy                  string  `json:"owned_by"`
	UpstreamModel            string  `json:"upstream_model"`
	MaxOutputTokens          *int64  `json:"max_output_tokens"`
	ContextWindowTokens      *int64  `json:"context_window_tokens"`
	InputPriceUSDPerMTokens  *string `json:"input_price_usd_per_million_tokens"`
	OutputPriceUSDPerMTokens *string `json:"output_price_usd_per_million_tokens"`
	ReleaseDate              *string `json:"release_date"`
	Capabilities             []struct {
		CapabilityKey string          `json:"capability_key"`
		SupportLevel  string          `json:"support_level"`
		Limits        json.RawMessage `json:"limits"`
	} `json:"capabilities"`
}

func (h *channelModelInventoryHandler) adoptAndBind(w http.ResponseWriter, r *http.Request) {
	channelID, err := adminhttp.PathID(r)
	if err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}
	var req adoptAndBindRequest
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}
	var releaseDate *time.Time
	if req.ReleaseDate != nil && strings.TrimSpace(*req.ReleaseDate) != "" {
		parsed, parseErr := time.Parse("2006-01-02", strings.TrimSpace(*req.ReleaseDate))
		if parseErr != nil {
			adminhttp.WriteServiceError(w, adminhttp.InvalidRequestField("release_date", "release_date must be YYYY-MM-DD"))
			return
		}
		releaseDate = &parsed
	}
	caps := make([]modelcatalog.CapabilityHint, 0, len(req.Capabilities))
	for _, capability := range req.Capabilities {
		caps = append(caps, modelcatalog.CapabilityHint{
			Key: capability.CapabilityKey, SupportLevel: capability.SupportLevel, Limits: capability.Limits,
		})
	}
	binding, err := h.service.AdoptAndBind(r.Context(), modelcatalog.AdoptAndBindInput{
		ChannelID: channelID, UpstreamModel: req.UpstreamModel,
		Adopt: modelcatalog.AdoptInput{
			CanonicalID: req.CanonicalID, ModelID: req.ModelID, DisplayName: req.DisplayName,
			OwnedBy: req.OwnedBy, Status: "disabled", MaxOutputTokens: req.MaxOutputTokens,
			ContextWindowTokens:      req.ContextWindowTokens,
			InputPriceUSDPerMTokens:  trimStringPtr(req.InputPriceUSDPerMTokens),
			OutputPriceUSDPerMTokens: trimStringPtr(req.OutputPriceUSDPerMTokens),
			ReleaseDate:              releaseDate, Capabilities: caps,
		},
	})
	if err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}
	adminhttp.WriteData(w, http.StatusCreated, toBindingResultDTO(binding))
}

type inventoryRunDTO struct {
	ID                     int64   `json:"id"`
	ChannelID              int64   `json:"channel_id"`
	Source                 string  `json:"source"`
	Status                 string  `json:"status"`
	ChannelConfigRevision  int64   `json:"channel_config_revision"`
	ProviderOriginRevision int64   `json:"provider_origin_revision"`
	ProviderStatusRevision int64   `json:"provider_status_revision"`
	AttemptCount           int32   `json:"attempt_count"`
	TotalCount             int32   `json:"total_count"`
	SucceededCount         int32   `json:"succeeded_count"`
	FailedCount            int32   `json:"failed_count"`
	WarningCode            *string `json:"warning_code"`
	ErrorCode              *string `json:"error_code"`
	Message                *string `json:"message"`
	CreatedAt              string  `json:"created_at"`
	StartedAt              *string `json:"started_at"`
	CompletedAt            *string `json:"completed_at"`
}

func toInventoryRunDTO(run channelmodelinventory.Run) inventoryRunDTO {
	return inventoryRunDTO{
		ID: run.ID, ChannelID: run.ChannelID, Source: run.Source, Status: run.Status,
		ChannelConfigRevision: run.ChannelConfigRevision, ProviderOriginRevision: run.ProviderOriginRevision,
		ProviderStatusRevision: run.ProviderStatusRevision, AttemptCount: run.AttemptCount,
		TotalCount: run.TotalCount, SucceededCount: run.SucceededCount, FailedCount: run.FailedCount,
		WarningCode: stringPtr(run.WarningCode), ErrorCode: stringPtr(run.ErrorCode), Message: stringPtr(run.Message),
		CreatedAt: run.CreatedAt.UTC().Format(time.RFC3339), StartedAt: formatTimePtr(run.StartedAt),
		CompletedAt: formatTimePtr(run.CompletedAt),
	}
}

type inventoryDTO struct {
	Channel         inventoryChannelDTO `json:"channel"`
	LatestDiscovery *inventoryRunDTO    `json:"latest_discovery"`
	Snapshot        *inventoryRunDTO    `json:"snapshot"`
	SnapshotStale   bool                `json:"snapshot_stale"`
	Stats           inventoryStatsDTO   `json:"stats"`
	Items           []inventoryItemDTO  `json:"items"`
}

type inventoryChannelDTO struct {
	ID           int64  `json:"id"`
	Name         string `json:"name"`
	Status       string `json:"status"`
	Protocol     string `json:"protocol"`
	AdapterKey   string `json:"adapter_key"`
	ProviderID   int64  `json:"provider_id"`
	ProviderSlug string `json:"provider_slug"`
}

type inventoryStatsDTO struct {
	Discovered int `json:"discovered"`
	Bindings   int `json:"bindings"`
	New        int `json:"new"`
	Pending    int `json:"pending"`
}

type inventoryItemDTO struct {
	UpstreamModel     string                `json:"upstream_model"`
	OwnedBy           string                `json:"owned_by"`
	UpstreamCreatedAt *string               `json:"upstream_created_at"`
	DiscoveryState    string                `json:"discovery_state"`
	Bindings          []inventoryBindingDTO `json:"bindings"`
	Match             inventoryMatchDTO     `json:"match"`
}

type inventoryBindingDTO struct {
	ID                 int64                     `json:"id"`
	ModelID            int64                     `json:"model_id"`
	ModelExternalID    string                    `json:"model_external_id"`
	ModelDisplayName   string                    `json:"model_display_name"`
	ModelStatus        string                    `json:"model_status"`
	UpstreamModel      string                    `json:"upstream_model"`
	Status             string                    `json:"status"`
	AdoptedCanonicalID string                    `json:"adopted_canonical_id"`
	Verification       *inventoryVerificationDTO `json:"verification"`
}

type inventoryVerificationDTO struct {
	ItemID      int64   `json:"item_id"`
	RunID       int64   `json:"run_id"`
	Status      string  `json:"status"`
	Current     bool    `json:"current"`
	HTTPStatus  int32   `json:"http_status"`
	ErrorCode   *string `json:"error_code"`
	Message     *string `json:"message"`
	LatencyMs   *int64  `json:"latency_ms"`
	CompletedAt *string `json:"completed_at"`
}

type inventoryMatchDTO struct {
	Kind              string                         `json:"kind"`
	ExactModel        *inventoryModelCandidateDTO    `json:"exact_model"`
	CatalogCandidates []inventoryCatalogCandidateDTO `json:"catalog_candidates"`
}

type inventoryModelCandidateDTO struct {
	ID          int64  `json:"id"`
	ModelID     string `json:"model_id"`
	DisplayName string `json:"display_name"`
	Status      string `json:"status"`
	CanonicalID string `json:"canonical_id"`
}

type inventoryCatalogCandidateDTO struct {
	CanonicalID     string                       `json:"canonical_id"`
	Lab             string                       `json:"lab"`
	DisplayName     string                       `json:"display_name"`
	RemovedUpstream bool                         `json:"removed_upstream"`
	AdoptedModels   []inventoryModelCandidateDTO `json:"adopted_models"`
}

func toInventoryDTO(in channelmodelinventory.Inventory) inventoryDTO {
	result := inventoryDTO{
		Channel: inventoryChannelDTO{
			ID: in.Channel.ID, Name: in.Channel.Name, Status: in.Channel.Status, Protocol: in.Channel.Protocol,
			AdapterKey: in.Channel.AdapterKey, ProviderID: in.Channel.ProviderID, ProviderSlug: in.Channel.ProviderSlug,
		},
		SnapshotStale: in.SnapshotStale,
		Stats:         inventoryStatsDTO{Discovered: in.DiscoveredCount, Bindings: in.BindingCount, New: in.NewCount, Pending: in.PendingCount},
		Items:         make([]inventoryItemDTO, 0, len(in.Items)),
	}
	if in.LatestDiscovery != nil {
		dto := toInventoryRunDTO(*in.LatestDiscovery)
		result.LatestDiscovery = &dto
	}
	if in.Snapshot != nil {
		dto := toInventoryRunDTO(*in.Snapshot)
		result.Snapshot = &dto
	}
	for _, item := range in.Items {
		dto := inventoryItemDTO{
			UpstreamModel: item.UpstreamModel, OwnedBy: item.OwnedBy,
			UpstreamCreatedAt: formatTimePtr(item.UpstreamCreatedAt), DiscoveryState: item.DiscoveryState,
			Bindings: make([]inventoryBindingDTO, 0, len(item.Bindings)), Match: toInventoryMatchDTO(item.Match),
		}
		for _, binding := range item.Bindings {
			bindingDTO := inventoryBindingDTO{
				ID: binding.ID, ModelID: binding.ModelID, ModelExternalID: binding.ModelExternalID,
				ModelDisplayName: binding.ModelDisplayName, ModelStatus: binding.ModelStatus,
				UpstreamModel: binding.UpstreamModel, Status: binding.Status,
				AdoptedCanonicalID: binding.AdoptedCanonicalID,
			}
			if binding.Verification != nil {
				bindingDTO.Verification = &inventoryVerificationDTO{
					ItemID: binding.Verification.ItemID, RunID: binding.Verification.RunID,
					Status: binding.Verification.Status, Current: binding.Verification.Current,
					HTTPStatus: binding.Verification.HTTPStatus, ErrorCode: stringPtr(binding.Verification.ErrorCode),
					Message: stringPtr(binding.Verification.Message), LatencyMs: binding.Verification.LatencyMs,
					CompletedAt: formatTimePtr(binding.Verification.CompletedAt),
				}
			}
			dto.Bindings = append(dto.Bindings, bindingDTO)
		}
		result.Items = append(result.Items, dto)
	}
	return result
}

func toInventoryMatchDTO(match channelmodelinventory.InventoryMatch) inventoryMatchDTO {
	result := inventoryMatchDTO{Kind: match.Kind, CatalogCandidates: make([]inventoryCatalogCandidateDTO, 0, len(match.CatalogCandidates))}
	if match.ExactModel != nil {
		dto := toInventoryModelCandidateDTO(*match.ExactModel)
		result.ExactModel = &dto
	}
	for _, candidate := range match.CatalogCandidates {
		dto := inventoryCatalogCandidateDTO{
			CanonicalID: candidate.CanonicalID, Lab: candidate.Lab, DisplayName: candidate.DisplayName,
			RemovedUpstream: candidate.RemovedUpstream,
			AdoptedModels:   make([]inventoryModelCandidateDTO, 0, len(candidate.AdoptedModels)),
		}
		for _, adopted := range candidate.AdoptedModels {
			dto.AdoptedModels = append(dto.AdoptedModels, toInventoryModelCandidateDTO(adopted))
		}
		result.CatalogCandidates = append(result.CatalogCandidates, dto)
	}
	return result
}

func toInventoryModelCandidateDTO(model channelmodelinventory.InventoryModelCandidate) inventoryModelCandidateDTO {
	return inventoryModelCandidateDTO{
		ID: model.ID, ModelID: model.ModelID, DisplayName: model.DisplayName,
		Status: model.Status, CanonicalID: model.CanonicalID,
	}
}

type verificationResultDTO struct {
	Run   inventoryRunDTO       `json:"run"`
	Items []verificationItemDTO `json:"items"`
}

type verificationItemDTO struct {
	ID                    int64   `json:"id"`
	RunID                 int64   `json:"run_id"`
	ModelID               int64   `json:"model_id"`
	UpstreamModel         string  `json:"upstream_model"`
	Status                string  `json:"status"`
	Success               *bool   `json:"success"`
	HTTPStatus            int32   `json:"http_status"`
	ErrorCode             *string `json:"error_code"`
	Message               *string `json:"message"`
	LatencyMs             *int64  `json:"latency_ms"`
	ProviderProbeRecordID *int64  `json:"provider_probe_record_id"`
	CreatedAt             string  `json:"created_at"`
	CompletedAt           *string `json:"completed_at"`
}

type inventoryBindingResultDTO struct {
	ID               int64  `json:"id"`
	ChannelID        int64  `json:"channel_id"`
	ModelID          int64  `json:"model_id"`
	ModelExternalID  string `json:"model_external_id"`
	ModelDisplayName string `json:"model_display_name"`
	UpstreamModel    string `json:"upstream_model"`
	Status           string `json:"status"`
}

func toBindingResultDTO(binding channelmodelinventory.BindingResult) inventoryBindingResultDTO {
	return inventoryBindingResultDTO{
		ID: binding.ID, ChannelID: binding.ChannelID, ModelID: binding.ModelID,
		ModelExternalID: binding.ModelExternalID, ModelDisplayName: binding.ModelDisplayName,
		UpstreamModel: binding.UpstreamModel, Status: binding.Status,
	}
}

func toBindingResultDTOs(bindings []channelmodelinventory.BindingResult) []inventoryBindingResultDTO {
	result := make([]inventoryBindingResultDTO, 0, len(bindings))
	for _, binding := range bindings {
		result = append(result, toBindingResultDTO(binding))
	}
	return result
}

func toVerificationResultDTO(result channelmodelinventory.VerificationResult) verificationResultDTO {
	out := verificationResultDTO{Run: toInventoryRunDTO(result.Run), Items: make([]verificationItemDTO, 0, len(result.Items))}
	for _, item := range result.Items {
		out.Items = append(out.Items, verificationItemDTO{
			ID: item.ID, RunID: item.RunID, ModelID: item.ModelID, UpstreamModel: item.UpstreamModel,
			Status: item.Status, Success: item.Success, HTTPStatus: item.HTTPStatus,
			ErrorCode: stringPtr(item.ErrorCode), Message: stringPtr(item.Message), LatencyMs: item.LatencyMs,
			ProviderProbeRecordID: item.ProviderProbeRecordID,
			CreatedAt:             item.CreatedAt.UTC().Format(time.RFC3339), CompletedAt: formatTimePtr(item.CompletedAt),
		})
	}
	return out
}

func stringPtr(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}

func formatTimePtr(value *time.Time) *string {
	if value == nil {
		return nil
	}
	formatted := value.UTC().Format(time.RFC3339)
	return &formatted
}

func trimStringPtr(value *string) *string {
	if value == nil {
		return nil
	}
	trimmed := strings.TrimSpace(*value)
	return &trimmed
}
