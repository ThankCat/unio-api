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

// Inventory 是渠道模型清单的完整视图。
//
// 2026-09-05 起不再输出「快照/验证已过期」类展示事实：过期判定曾按渠道 config_revision 比对，而任何一次
// 渠道编辑（超时、优先级、代理……）都会提升修订号，绝大多数与「模型能不能用」无关，导致页面长期挂着
// 过期横幅、诱导运营反复花钱重验。发现与验证都是运营主动发起的动作，页面永远展示「最近一次结果 + 时间」，
// 重跑时机由人决定；执行期的 stale_revision 守卫（run 排队期间配置变化即取消，防止旧凭据/旧 origin 探测
// 后归因错误）保留不变。
type Inventory struct {
	Channel         InventoryChannel
	LatestDiscovery *Run
	Snapshot        *Run
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
		Protocol: primaryProtocol(contextRow.Protocols), AdapterKey: contextRow.AdapterKey,
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

// inventoryItemPending 判定「待处理」：未绑定、绑定停用、或从未有过成功验证。
// 验证结果不随配置修订过期（见 Inventory 注释），一次成功即长期有效，直到运营主动重验。
func inventoryItemPending(item InventoryItem) bool {
	if item.DiscoveryState != "discovered" || len(item.Bindings) == 0 {
		return true
	}
	for _, binding := range item.Bindings {
		if binding.Status == "disabled" || binding.Verification == nil ||
			binding.Verification.Status != "succeeded" {
			return true
		}
	}
	return false
}

func verificationKey(modelID int64, upstream string) string {
	return strings.Join([]string{strings.TrimSpace(upstream), strconv.FormatInt(modelID, 10)}, "\x00")
}

// primaryProtocol 取渠道的主协议（protocols 数组首项）。
//
// 探测与验活只需要一个协议形态：同一把上游凭据在 openai 和 anthropic 入口下能用的模型是同一批，
// 上游可用性也不随入口形态变化。所以多协议渠道用主协议探一次即可，不必逐协议重复。
func primaryProtocol(protocols []string) string {
	if len(protocols) == 0 {
		return ""
	}
	return protocols[0]
}
