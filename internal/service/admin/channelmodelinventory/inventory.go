package channelmodelinventory

import (
	"context"
	"errors"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/ThankCat/unio-gateway/internal/platform/store/sqlc"
)

type Inventory struct {
	Channel         InventoryChannel
	LatestDiscovery *Run
	Snapshot        *Run
	SnapshotStale   bool
	DiscoveredCount int
	BindingCount    int
	NewCount        int
	PendingCount    int
	Items           []InventoryItem
}

type InventoryChannel struct {
	ID           int64
	Name         string
	Status       string
	Protocol     string
	AdapterKey   string
	ProviderID   int64
	ProviderSlug string
}

type InventoryItem struct {
	UpstreamModel     string
	OwnedBy           string
	UpstreamCreatedAt *time.Time
	DiscoveryState    string
	Bindings          []InventoryBinding
	Match             InventoryMatch
}

type InventoryBinding struct {
	ID                 int64
	ModelID            int64
	ModelExternalID    string
	ModelDisplayName   string
	ModelStatus        string
	UpstreamModel      string
	Status             string
	AdoptedCanonicalID string
	Verification       *InventoryVerification
}

type InventoryVerification struct {
	ItemID      int64
	RunID       int64
	Status      string
	Current     bool
	HTTPStatus  int32
	ErrorCode   string
	Message     string
	LatencyMs   *int64
	CompletedAt *time.Time
}

type InventoryMatch struct {
	Kind              string
	ExactModel        *InventoryModelCandidate
	CatalogCandidates []InventoryCatalogCandidate
}

type InventoryModelCandidate struct {
	ID          int64
	ModelID     string
	DisplayName string
	Status      string
	CanonicalID string
}

type InventoryCatalogCandidate struct {
	CanonicalID     string
	Lab             string
	DisplayName     string
	RemovedUpstream bool
	AdoptedModels   []InventoryModelCandidate
}

func (s *Service) GetInventory(ctx context.Context, channelID int64) (Inventory, error) {
	contextRow, err := s.queries.GetChannelModelInventoryContext(ctx, channelID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Inventory{}, notFound("channel not found")
		}
		return Inventory{}, storeFailed(err, "get channel model inventory context")
	}
	result := Inventory{Channel: InventoryChannel{
		ID: contextRow.ChannelID, Name: contextRow.ChannelName, Status: contextRow.ChannelStatus,
		Protocol: contextRow.Protocol, AdapterKey: contextRow.AdapterKey,
		ProviderID: contextRow.ProviderID, ProviderSlug: contextRow.ProviderSlug,
	}}

	if row, err := s.queries.GetLatestChannelModelDiscoveryRun(ctx, channelID); err == nil {
		run := discoveryRun(row)
		result.LatestDiscovery = &run
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return Inventory{}, storeFailed(err, "get latest channel model discovery")
	}

	discovered := make(map[string]sqlc.ChannelModelDiscoveryItem)
	if row, err := s.queries.GetLatestSuccessfulChannelModelDiscoveryRun(ctx, channelID); err == nil {
		run := discoveryRun(row)
		result.Snapshot = &run
		result.SnapshotStale = row.ChannelConfigRevision != contextRow.ConfigRevision ||
			row.ProviderOriginRevision != contextRow.OriginRevision || row.ProviderStatusRevision != contextRow.StatusRevision
		items, listErr := s.queries.ListChannelModelDiscoveryItems(ctx, row.ID)
		if listErr != nil {
			return Inventory{}, storeFailed(listErr, "list channel model discovery snapshot")
		}
		for _, item := range items {
			discovered[item.UpstreamModel] = item
		}
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return Inventory{}, storeFailed(err, "get successful channel model discovery")
	}

	bindings, err := s.queries.ListChannelModelInventoryBindings(ctx, channelID)
	if err != nil {
		return Inventory{}, storeFailed(err, "list channel model inventory bindings")
	}
	verificationRows, err := s.queries.ListLatestChannelModelVerificationItems(ctx, channelID)
	if err != nil {
		return Inventory{}, storeFailed(err, "list channel model verification facts")
	}
	verifications := make(map[string]sqlc.ListLatestChannelModelVerificationItemsRow, len(verificationRows))
	for _, row := range verificationRows {
		verifications[verificationKey(row.ModelID, row.UpstreamModel)] = row
	}

	rowsByUpstream := make(map[string]*InventoryItem)
	for upstream, item := range discovered {
		copy := item
		rowsByUpstream[upstream] = &InventoryItem{
			UpstreamModel: upstream, OwnedBy: textValue(copy.OwnedBy),
			UpstreamCreatedAt: timeValue(copy.UpstreamCreatedAt), DiscoveryState: "discovered",
		}
	}
	for _, binding := range bindings {
		item := rowsByUpstream[binding.UpstreamModel]
		if item == nil {
			item = &InventoryItem{UpstreamModel: binding.UpstreamModel, DiscoveryState: "not_seen"}
			rowsByUpstream[binding.UpstreamModel] = item
		}
		view := InventoryBinding{
			ID: binding.ID, ModelID: binding.ModelID, ModelExternalID: binding.ModelExternalID,
			ModelDisplayName: binding.ModelDisplayName, ModelStatus: binding.ModelStatus,
			UpstreamModel: binding.UpstreamModel, Status: binding.Status,
			AdoptedCanonicalID: textValue(binding.AdoptedCanonicalID),
		}
		if verification, ok := verifications[verificationKey(binding.ModelID, binding.UpstreamModel)]; ok {
			latency := int64(0)
			var latencyPtr *int64
			if verification.LatencyMs.Valid {
				latency = verification.LatencyMs.Int64
				latencyPtr = &latency
			}
			view.Verification = &InventoryVerification{
				ItemID: verification.ID, RunID: verification.RunID, Status: verification.Status,
				Current: verification.ChannelConfigRevision == contextRow.ConfigRevision &&
					verification.ProviderOriginRevision == contextRow.OriginRevision &&
					verification.ProviderStatusRevision == contextRow.StatusRevision,
				HTTPStatus: verification.HttpStatus, ErrorCode: textValue(verification.ErrorCode),
				Message: textValue(verification.Message), LatencyMs: latencyPtr,
				CompletedAt: timeValue(verification.CompletedAt),
			}
		}
		item.Bindings = append(item.Bindings, view)
	}

	upstreamModels := make([]string, 0, len(rowsByUpstream))
	for upstream := range rowsByUpstream {
		upstreamModels = append(upstreamModels, upstream)
	}
	sort.Strings(upstreamModels)
	matchesByUpstream := make(map[string][]sqlc.ListChannelModelInventoryMatchesRow)
	if len(upstreamModels) > 0 {
		matchRows, matchErr := s.queries.ListChannelModelInventoryMatches(ctx, upstreamModels)
		if matchErr != nil {
			return Inventory{}, storeFailed(matchErr, "match channel model inventory")
		}
		for _, match := range matchRows {
			matchesByUpstream[match.UpstreamModel] = append(matchesByUpstream[match.UpstreamModel], match)
		}
	}

	for _, upstream := range upstreamModels {
		item := rowsByUpstream[upstream]
		item.Match = buildInventoryMatch(item.Bindings, matchesByUpstream[upstream])
		result.Items = append(result.Items, *item)
		if item.DiscoveryState == "discovered" {
			result.DiscoveredCount++
		}
		result.BindingCount += len(item.Bindings)
		if item.DiscoveryState == "discovered" && len(item.Bindings) == 0 {
			result.NewCount++
		}
		if inventoryItemPending(*item) {
			result.PendingCount++
		}
	}
	return result, nil
}

func buildInventoryMatch(bindings []InventoryBinding, rows []sqlc.ListChannelModelInventoryMatchesRow) InventoryMatch {
	if len(bindings) > 0 {
		return InventoryMatch{Kind: "bound"}
	}
	match := InventoryMatch{Kind: "none"}
	catalogByID := make(map[string]*InventoryCatalogCandidate)
	for _, row := range rows {
		if match.ExactModel == nil && row.ExactModelID.Valid {
			match.ExactModel = &InventoryModelCandidate{
				ID: row.ExactModelID.Int64, ModelID: textValue(row.ExactModelExternalID),
				DisplayName: textValue(row.ExactModelDisplayName), Status: textValue(row.ExactModelStatus),
				CanonicalID: textValue(row.ExactModelCanonicalID),
			}
		}
		if !row.CatalogCanonicalID.Valid {
			continue
		}
		canonicalID := row.CatalogCanonicalID.String
		candidate := catalogByID[canonicalID]
		if candidate == nil {
			candidate = &InventoryCatalogCandidate{
				CanonicalID: canonicalID, Lab: textValue(row.CatalogLab), DisplayName: textValue(row.CatalogDisplayName),
				RemovedUpstream: row.CatalogRemovedUpstream,
			}
			catalogByID[canonicalID] = candidate
		}
		if row.AdoptedModelID.Valid {
			candidate.AdoptedModels = append(candidate.AdoptedModels, InventoryModelCandidate{
				ID: row.AdoptedModelID.Int64, ModelID: textValue(row.AdoptedModelExternalID),
				DisplayName: textValue(row.AdoptedModelDisplayName), Status: textValue(row.AdoptedModelStatus),
				CanonicalID: canonicalID,
			})
		}
	}
	for _, candidate := range catalogByID {
		match.CatalogCandidates = append(match.CatalogCandidates, *candidate)
	}
	sort.Slice(match.CatalogCandidates, func(i, j int) bool {
		return match.CatalogCandidates[i].CanonicalID < match.CatalogCandidates[j].CanonicalID
	})
	switch {
	case match.ExactModel != nil:
		match.Kind = "local_model"
	case len(match.CatalogCandidates) == 1 && len(match.CatalogCandidates[0].AdoptedModels) == 1:
		match.Kind = "adopted_model"
	case len(match.CatalogCandidates) == 1:
		match.Kind = "catalog"
	case len(match.CatalogCandidates) > 1:
		match.Kind = "ambiguous_catalog"
	}
	return match
}

func inventoryItemPending(item InventoryItem) bool {
	if item.DiscoveryState != "discovered" || len(item.Bindings) == 0 {
		return true
	}
	for _, binding := range item.Bindings {
		if binding.Status == "disabled" || binding.Verification == nil ||
			!binding.Verification.Current || binding.Verification.Status != "succeeded" {
			return true
		}
	}
	return false
}

func verificationKey(modelID int64, upstream string) string {
	return strings.Join([]string{strings.TrimSpace(upstream), strconv.FormatInt(modelID, 10)}, "\x00")
}
