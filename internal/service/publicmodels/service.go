// Package publicmodels 提供「公开模型目录」查询：console 模型页与 website 模型页共享的数据源。
//
// 只读、无租户语义：目录内容对两个 surface 完全一致（enabled 且有当前生效基准价的模型，
// 含展示元数据、双档官方牌价与解析后售价、长上下文阶梯、能力声明）。HTTP 层各自持有
// 独立 DTO 与缓存策略，本包只负责把数据库事实解析成领域对象。
//
// 售价解析复用计费系统的 billing.ResolveCustomerPrice（绝对售价整组优先，否则基准价 ×
// 行级倍率）——目录标价与实际结算价永远同源，不存在第二套口径。
package publicmodels

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/ThankCat/unio-gateway/internal/core/billing"
	"github.com/ThankCat/unio-gateway/internal/platform/store/sqlc"
	"github.com/ThankCat/unio-gateway/internal/service/admin/opsutil"
)

// Store 是公开目录所需的只读存储能力（由 *sqlc.Queries 满足）。
type Store interface {
	ListPublicModels(ctx context.Context) ([]sqlc.ListPublicModelsRow, error)
	ListPublicModelCapabilities(ctx context.Context) ([]sqlc.ListPublicModelCapabilitiesRow, error)
	ListPublicModelPriceWindows(ctx context.Context, since pgtype.Timestamptz) ([]sqlc.ListPublicModelPriceWindowsRow, error)
}

// PriceVector 是一组按分项展开的单价（十进制字符串，USD / 1M tokens）；nil 表示该分项未定价。
type PriceVector struct {
	UncachedInput    *string
	CacheRead        *string
	CacheCreation5m  *string
	CacheCreation1h  *string
	CacheCreation30m *string
	Output           *string
	ReasoningOutput  *string
}

// PriceGroup 是某个服务档位的「官方牌价 + 对客售价」对照。
type PriceGroup struct {
	List PriceVector
	Sale PriceVector
}

// LongContext 是长上下文阶梯：输入合计超阈值后整单按倍率计价。
type LongContext struct {
	ThresholdTokens  int64
	InputMultiplier  string
	OutputMultiplier string
}

// Capability 是一条能力声明。
type Capability struct {
	Key          string
	SupportLevel string
}

// Model 是公开目录的一个条目。
type Model struct {
	ModelID     string
	DisplayName string
	// Lab 即 models.owned_by（与 model_labs.slug 同值），是厂商筛选与图标的稳定 key。
	Lab             string
	Family          string
	Description     string
	KnowledgeCutoff string
	Currency        string

	ContextWindowTokens *int64
	MaxOutputTokens     *int64
	ReleaseDate         *time.Time

	Standard PriceGroup
	// Fast 为 nil 表示该模型未配置 priority 档。
	Fast *PriceGroup
	// SaleRatio 仅在倍率定价路径下非空（如 "0.2"）；绝对售价路径由前端按 Sale/List 自行算比。
	SaleRatio *string

	LongContext  *LongContext
	Capabilities []Capability

	// LabHasLogo 告知前端 logo 端点是否有内容，避免为无图标厂商发一次注定 404 的请求。
	LabHasLogo         bool
	PriceEffectiveFrom time.Time
}

// Service 提供公开模型目录查询。
type Service struct {
	store Store
}

// NewService 创建公开目录服务。
func NewService(store Store) *Service {
	return &Service{store: store}
}

// List 返回全部在售模型（按厂商、模型 ID 排序）。
func (s *Service) List(ctx context.Context) ([]Model, error) {
	rows, err := s.store.ListPublicModels(ctx)
	if err != nil {
		return nil, fmt.Errorf("list public models: %w", err)
	}
	capRows, err := s.store.ListPublicModelCapabilities(ctx)
	if err != nil {
		return nil, fmt.Errorf("list public model capabilities: %w", err)
	}

	capsByModel := make(map[int64][]Capability, len(rows))
	for _, c := range capRows {
		capsByModel[c.ModelID] = append(capsByModel[c.ModelID], Capability{
			Key:          c.CapabilityKey,
			SupportLevel: c.SupportLevel,
		})
	}

	out := make([]Model, 0, len(rows))
	for _, row := range rows {
		model, err := buildModel(row, capsByModel[row.ID])
		if err != nil {
			return nil, fmt.Errorf("resolve price for model %s: %w", row.ModelID, err)
		}
		out = append(out, model)
	}
	return out, nil
}

func buildModel(row sqlc.ListPublicModelsRow, caps []Capability) (Model, error) {
	standardList := billing.CustomerPriceSnapshot{
		Currency:                   row.Currency,
		PricingUnit:                row.PricingUnit,
		UncachedInputPrice:         row.UncachedInputPrice,
		CacheReadInputPrice:        row.CacheReadInputPrice,
		CacheCreation5mInputPrice:  row.CacheCreation5mInputPrice,
		CacheCreation1hInputPrice:  row.CacheCreation1hInputPrice,
		CacheCreation30mInputPrice: row.CacheCreation30mInputPrice,
		OutputPrice:                row.OutputPrice,
		ReasoningOutputPrice:       row.ReasoningOutputPrice,
	}
	standardOverride := billing.SaleOverride{
		UncachedInputPrice:         row.SaleUncachedInputPrice,
		CacheReadInputPrice:        row.SaleCacheReadInputPrice,
		CacheCreation5mInputPrice:  row.SaleCacheCreation5mInputPrice,
		CacheCreation1hInputPrice:  row.SaleCacheCreation1hInputPrice,
		CacheCreation30mInputPrice: row.SaleCacheCreation30mInputPrice,
		OutputPrice:                row.SaleOutputPrice,
		ReasoningOutputPrice:       row.SaleReasoningOutputPrice,
	}
	standardSale, err := billing.ResolveCustomerPrice(standardList, row.SalePriceRatio, standardOverride)
	if err != nil {
		return Model{}, err
	}

	model := Model{
		ModelID:             row.ModelID,
		DisplayName:         row.DisplayName,
		Lab:                 row.OwnedBy,
		Family:              row.Family,
		Description:         row.Description,
		KnowledgeCutoff:     row.KnowledgeCutoff,
		Currency:            row.Currency,
		ContextWindowTokens: int64Ptr(row.ContextWindowTokens),
		MaxOutputTokens:     int64Ptr(row.MaxOutputTokens),
		ReleaseDate:         datePtr(row.ReleaseDate),
		Standard: PriceGroup{
			List: vectorFromSnapshot(standardList),
			Sale: vectorFromSnapshot(standardSale),
		},
		Capabilities:       caps,
		LabHasLogo:         row.LabHasLogo,
		PriceEffectiveFrom: row.PriceEffectiveFrom.Time,
	}

	// 倍率路径才对外给出 ratio：绝对售价路径下倍率不参与定价，给出去只会误导。
	if !standardOverride.Configured() {
		model.SaleRatio = opsutil.NumericStringPtr(row.SalePriceRatio)
	}

	if row.LongContextEnabled {
		model.LongContext = &LongContext{
			ThresholdTokens:  row.LongContextThreshold.Int64,
			InputMultiplier:  opsutil.NumericString(row.LongContextInputMultiplier),
			OutputMultiplier: opsutil.NumericString(row.LongContextOutputMultiplier),
		}
	}

	if row.FastConfigured {
		fastList := billing.CustomerPriceSnapshot{
			Currency:                   row.Currency,
			PricingUnit:                row.PricingUnit,
			UncachedInputPrice:         row.FastUncachedInputPrice,
			CacheReadInputPrice:        row.FastCacheReadInputPrice,
			CacheCreation5mInputPrice:  row.FastCacheCreation5mInputPrice,
			CacheCreation1hInputPrice:  row.FastCacheCreation1hInputPrice,
			CacheCreation30mInputPrice: row.FastCacheCreation30mInputPrice,
			OutputPrice:                row.FastOutputPrice,
			ReasoningOutputPrice:       row.FastReasoningOutputPrice,
		}
		fastOverride := billing.SaleOverride{
			UncachedInputPrice:         row.FastSaleUncachedInputPrice,
			CacheReadInputPrice:        row.FastSaleCacheReadInputPrice,
			CacheCreation5mInputPrice:  row.FastSaleCacheCreation5mInputPrice,
			CacheCreation1hInputPrice:  row.FastSaleCacheCreation1hInputPrice,
			CacheCreation30mInputPrice: row.FastSaleCacheCreation30mInputPrice,
			OutputPrice:                row.FastSaleOutputPrice,
			ReasoningOutputPrice:       row.FastSaleReasoningOutputPrice,
		}
		fastSale, err := billing.ResolveCustomerPrice(fastList, row.SalePriceRatio, fastOverride)
		if err != nil {
			return Model{}, err
		}
		model.Fast = &PriceGroup{
			List: vectorFromSnapshot(fastList),
			Sale: vectorFromSnapshot(fastSale),
		}
	}

	return model, nil
}

func vectorFromSnapshot(s billing.CustomerPriceSnapshot) PriceVector {
	return PriceVector{
		UncachedInput:    opsutil.NumericStringPtr(s.UncachedInputPrice),
		CacheRead:        opsutil.NumericStringPtr(s.CacheReadInputPrice),
		CacheCreation5m:  opsutil.NumericStringPtr(s.CacheCreation5mInputPrice),
		CacheCreation1h:  opsutil.NumericStringPtr(s.CacheCreation1hInputPrice),
		CacheCreation30m: opsutil.NumericStringPtr(s.CacheCreation30mInputPrice),
		Output:           opsutil.NumericStringPtr(s.OutputPrice),
		ReasoningOutput:  opsutil.NumericStringPtr(s.ReasoningOutputPrice),
	}
}

func int64Ptr(v pgtype.Int8) *int64 {
	if !v.Valid {
		return nil
	}
	out := v.Int64
	return &out
}

func datePtr(v pgtype.Date) *time.Time {
	if !v.Valid {
		return nil
	}
	out := v.Time
	return &out
}
