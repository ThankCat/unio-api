// Package modelprice 编排 admin 管理端的模型定价（model_prices）读写（DEC-026）。
//
// 一条价格行同时承载三件事：基准价（也是成本基数，DEC-031）、售价折扣、绝对售价。
// 折扣与绝对售价是两套独立实体，可以同行共存；绝对售价是折扣的整组覆盖，不能混算。
// 客户最终售价 = 绝对售价整组非空时用绝对售价，否则基准价 × 售价折扣。
// 允许只有基准价、没有售价的草稿行；那种行不可售（不能启用、不能调用）。
// 创建走三个 intent：配基准价、按折扣定价、按绝对售价。改哪边就新开窗口，另一套售价从当前生效行复制。
// 设计约束（沿用 channelprice 口径）：
//   - 金额只填明确数值、绝不用 float；DTO 层用十进制字符串承载，避免精度丢失。
//   - 价格不可改金额：账务（price_snapshots）按事实快照引用历史价；改价靠「新建一条 + 关闭旧窗口」。
//     售价折扣与绝对售价同样如此——它们与基准价同行，改动一律走新窗口。
//   - 同一 model 的启用窗口不可重叠，否则结算取基准价有歧义。
//   - 毛利由数据库守卫在提交时兜底（售价低于任一渠道成本即拒绝）；草稿行售价不可解析时跳过守卫。
package modelprice

import (
	"context"
	"errors"
	"math/big"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/ThankCat/unio-gateway/internal/platform/failure"
	"github.com/ThankCat/unio-gateway/internal/platform/store/sqlc"
	"github.com/ThankCat/unio-gateway/internal/service/admin/supply"
)

const (
	// StatusEnabled 表示基准价启用（参与结算取价）。
	StatusEnabled = "enabled"
	// StatusDisabled 表示基准价停用。
	StatusDisabled = "disabled"

	// PricingUnitPer1MTokens 是当前唯一支持的计价单位。
	PricingUnitPer1MTokens = "per_1m_tokens"

	// IntentBase 配基准价：新窗口写入新基准价，售价从当前生效行复制（没有则留空）。
	IntentBase = "base"
	// IntentSaleDiscount 按折扣定价：复制当前基准价与当前绝对售价，写入新折扣。
	IntentSaleDiscount = "sale_discount"
	// IntentSaleAbsolute 按绝对售价：复制当前基准价与当前折扣，写入新绝对售价。
	IntentSaleAbsolute = "sale_absolute"
)

// Store 定义模型基准价管理所需的存储能力。
type Store interface {
	LookupModelByID(ctx context.Context, id int64) (sqlc.Model, error)
	GetModelPrice(ctx context.Context, id int64) (sqlc.ModelPrice, error)
	ListModelPricesByModel(ctx context.Context, modelID int64) ([]sqlc.ListModelPricesByModelRow, error)
	ListEnabledModelPriceWindows(ctx context.Context, arg sqlc.ListEnabledModelPriceWindowsParams) ([]sqlc.ListEnabledModelPriceWindowsRow, error)
	CreateModelPrice(ctx context.Context, arg sqlc.CreateModelPriceParams) (sqlc.CreateModelPriceRow, error)
	UpdateModelPriceWindow(ctx context.Context, arg sqlc.UpdateModelPriceWindowParams) (sqlc.ModelPrice, error)
}

// SalePriceVector 是对外绝对售价的一组单价：整组给齐或整组留空
// （DB 侧由 ck_model_prices_sale_all_or_none / ck_model_price_tiers_sale_all_or_none 保证）。
// 必填两项用 string，可选分项为空时计费回退到这两项。
type SalePriceVector struct {
	UncachedInputPrice         string
	CacheReadInputPrice        *string
	CacheCreation5mInputPrice  *string
	CacheCreation1hInputPrice  *string
	CacheCreation30mInputPrice *string
	OutputPrice                string
	ReasoningOutputPrice       *string
}

// ModelPrice 是 admin 视角的模型定价事实；金额以十进制字符串承载，可空项用 *string。
type ModelPrice struct {
	ID                          int64
	ModelID                     int64
	ModelExternalID             string
	ModelDisplayName            string
	Currency                    string
	PricingUnit                 string
	UncachedInputPrice          string
	CacheReadInputPrice         *string
	CacheCreation5mInputPrice   *string
	CacheCreation1hInputPrice   *string
	CacheCreation30mInputPrice  *string
	OutputPrice                 string
	ReasoningOutputPrice        *string
	SaleDiscount                *string
	SalePrices                  *SalePriceVector
	SaleConfigured              bool
	LongContextEnabled          bool
	LongContextThreshold        *int64
	LongContextInputMultiplier  *string
	LongContextOutputMultiplier *string
	FastPriceStatus             string
	FastPrices                  *FastPrice
	FastPriceReference          *FastPriceReference
	Status                      string
	EffectiveFrom               time.Time
	EffectiveTo                 *time.Time
	CreatedAt                   time.Time
	UpdatedAt                   time.Time
	// Warnings 是不阻断写入、但管理员应当知道的后果，例如窗口到期后无接续售价。
	// 只在写响应里出现，不落库。
	Warnings []string
}

// FastPrice 是同一模型价格窗口下已经持久化的 Fast 精确售价。
// Fast 与 Standard 绑在同一套售价实体上：走绝对售价时两边都必须有绝对售价；
// 走折扣时两边都按各自基准价 × 售价折扣。不允许一档绝对、另一档折扣。
type FastPrice struct {
	ServiceTierID              int64
	UncachedInputPrice         string
	CacheReadInputPrice        *string
	CacheCreation5mInputPrice  *string
	CacheCreation1hInputPrice  *string
	CacheCreation30mInputPrice *string
	OutputPrice                string
	ReasoningOutputPrice       *string
	SalePrices                 *SalePriceVector
	ReferenceSource            *string
	ReferenceCheckedAt         *time.Time
}

// FastPriceReference 是版本化的 OpenAI 官方参考价，只用于 Admin 填表，不参与运行时计费。
type FastPriceReference struct {
	Currency                   string
	PricingUnit                string
	UncachedInputPrice         string
	CacheReadInputPrice        *string
	CacheCreation5mInputPrice  *string
	CacheCreation1hInputPrice  *string
	CacheCreation30mInputPrice *string
	OutputPrice                string
	ReasoningOutputPrice       *string
	Source                     string
	CheckedAt                  time.Time
}

// FastPriceInput 是新价格窗口可选的 Fast 精确售价。
type FastPriceInput struct {
	UncachedInputPrice         string
	CacheReadInputPrice        *string
	CacheCreation5mInputPrice  *string
	CacheCreation1hInputPrice  *string
	CacheCreation30mInputPrice *string
	OutputPrice                string
	ReasoningOutputPrice       *string
	SalePrices                 *SalePriceVector
	ReferenceSource            *string
	ReferenceCheckedAt         *time.Time
}

// CreateInput 是创建模型价格窗口的入参。Intent 必填，决定哪一半由请求提供、哪一半从当前生效行复制。
type CreateInput struct {
	ModelID                     int64
	Intent                      string
	Currency                    string
	PricingUnit                 string
	UncachedInputPrice          string
	CacheReadInputPrice         *string
	CacheCreation5mInputPrice   *string
	CacheCreation1hInputPrice   *string
	CacheCreation30mInputPrice  *string
	OutputPrice                 string
	ReasoningOutputPrice        *string
	SaleDiscount                *string
	SalePrices                  *SalePriceVector
	LongContextEnabled          bool
	LongContextThreshold        *int64
	LongContextInputMultiplier  *string
	LongContextOutputMultiplier *string
	FastPrices                  *FastPriceInput
	ReplaceOverlappingEnabled   bool
	Status                      string
	EffectiveFrom               time.Time
	EffectiveTo                 *time.Time
	// Confirmation 携带撤价影响指纹；替换窗口导致模型失去可解析售价时必须确认。
	Confirmation supply.Confirmation
}

// UpdateInput 是 PATCH 模型基准价的入参：只改启停状态与生效结束时间（关闭窗口）；金额不可改。
type UpdateInput struct {
	ID          int64
	Status      string
	EffectiveTo *time.Time
	// Confirmation 携带撤价影响指纹。撤掉模型最后一条可解析售价时必须确认。
	Confirmation supply.Confirmation
}

// TxBeginner 提供事务能力（由 pgxpool 满足）。撤价可能让模型失去最后一条可解析售价，
// 那时价格写入与模型下架必须同进同出，否则会留下「已启用、却没法卖」的模型。
type TxBeginner interface {
	Begin(ctx context.Context) (pgx.Tx, error)
}

// Service 编排模型基准价读写。
type Service struct {
	store   Store
	db      TxBeginner
	queries *sqlc.Queries
}

// NewService 创建模型基准价管理服务。db/queries 用于撤价时的供给影响预览与联动下架；
// 只读路径继续走 store。
func NewService(store Store, db TxBeginner, queries *sqlc.Queries) *Service {
	return &Service{store: store, db: db, queries: queries}
}

// withStore 返回一个把全部内部读写重定向到 st 的副本，用于让既有方法在事务内执行。
// *sqlc.Queries 满足 Store，因此事务句柄可直接传入。
func (s *Service) withStore(st Store) *Service {
	return &Service{store: st, db: s.db, queries: s.queries}
}

// List 列出某 model 下全部基准价（含历史与停用）；model 不存在返回 not_found。
func (s *Service) List(ctx context.Context, modelID int64) ([]ModelPrice, error) {
	if modelID <= 0 {
		return nil, invalidArgument("model_id", "model id must be positive")
	}
	if _, err := s.store.LookupModelByID(ctx, modelID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, notFound("model not found")
		}
		return nil, storeFailed(err, "load model")
	}

	rows, err := s.store.ListModelPricesByModel(ctx, modelID)
	if err != nil {
		return nil, storeFailed(err, "list model prices")
	}

	prices := make([]ModelPrice, 0, len(rows))
	for _, row := range rows {
		prices = append(prices, toModelPriceFromRow(row))
	}

	return prices, nil
}

// Create 按 intent 创建一条价格窗口：改哪边就新开窗口，另一半从当前生效行复制。
func (s *Service) Create(ctx context.Context, in CreateInput) (ModelPrice, error) {
	if in.ModelID <= 0 {
		return ModelPrice{}, invalidArgument("model_id", "model_id must be positive")
	}
	if err := validateIntent(in.Intent); err != nil {
		return ModelPrice{}, err
	}
	if err := validateStatus(in.Status); err != nil {
		return ModelPrice{}, err
	}
	if in.ReplaceOverlappingEnabled && in.Status != StatusEnabled {
		return ModelPrice{}, invalidArgument("replace_overlapping_enabled", "replacement requires enabled status")
	}
	if in.EffectiveFrom.IsZero() {
		return ModelPrice{}, invalidArgument("effective_from", "effective_from is required")
	}
	if in.EffectiveTo != nil && !in.EffectiveTo.After(in.EffectiveFrom) {
		return ModelPrice{}, invalidArgument("effective_to", "effective_to must be after effective_from")
	}

	if s.db == nil || s.queries == nil {
		return ModelPrice{}, storeFailed(errors.New("supply linkage dependencies are unavailable"), "create model price")
	}

	tx, err := s.db.Begin(ctx)
	if err != nil {
		return ModelPrice{}, storeFailed(err, "begin model price create transaction")
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := s.queries.WithTx(tx)

	// 锁内构建草稿：intent 要从当前生效行复制另一半售价，锁外读会与并发撤价竞态。
	if err := supply.LockModels(ctx, q, []int64{in.ModelID}); err != nil {
		return ModelPrice{}, storeFailed(err, "lock model for price create")
	}

	result, err := s.withStore(q).createInLock(ctx, q, in)
	if err != nil {
		return ModelPrice{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return ModelPrice{}, storeFailed(err, "commit model price create transaction")
	}
	return result, nil
}

// createInLock 是 Create 的写入主体，调用方负责事务与 Model 锁。
// supplyQ 为 nil 表示不做供给联动（fake store 单测验证 intent 复制时走这条）。
func (s *Service) createInLock(ctx context.Context, supplyQ *sqlc.Queries, in CreateInput) (ModelPrice, error) {
	draft, err := s.buildCreateDraft(ctx, in)
	if err != nil {
		return ModelPrice{}, err
	}

	if in.Status == StatusEnabled && !in.ReplaceOverlappingEnabled {
		if err := s.ensureNoOverlap(ctx, in.ModelID, 0, in.EffectiveFrom, in.EffectiveTo); err != nil {
			return ModelPrice{}, err
		}
	}

	// 替换会停掉当前全部重叠窗口。新窗口自己可售就没有空档；否则等同撤价。
	var impact supply.Impact
	if supplyQ != nil && in.ReplaceOverlappingEnabled && !draftSellable(draft) {
		impact, err = supply.PriceImpact(ctx, supplyQ, in.ModelID, nil)
		if err != nil {
			return ModelPrice{}, storeFailed(err, "compute model price replace impact")
		}
		if err := supply.Authorize(impact,
			"model_price_disable_confirmation_required",
			"新窗口没有配售价，替换后该模型没有可解析售价；确认后会连同模型一并下架",
			in.Confirmation,
		); err != nil {
			return ModelPrice{}, err
		}
	}

	row, err := s.store.CreateModelPrice(ctx, draft.params)
	if err != nil {
		return ModelPrice{}, storeFailed(err, "create model price")
	}

	result := toModelPriceFromCreateRow(row)
	result.ModelExternalID = draft.model.ModelID
	result.ModelDisplayName = draft.model.DisplayName
	result.FastPriceReference = officialFastPriceReference(draft.model.ModelID)

	if supplyQ == nil {
		return result, nil
	}
	if impact.RequiresConfirmation() {
		if err := supply.DisableAffectedModels(ctx, supplyQ, impact, supply.ReasonPriceDisabled); err != nil {
			return ModelPrice{}, storeFailed(err, "delist models losing sale price")
		}
	}
	warnings, err := expiryWarnings(ctx, supplyQ, in.ModelID, row.ID, in.Status, in.EffectiveTo)
	if err != nil {
		return ModelPrice{}, err
	}
	result.Warnings = warnings
	return result, nil
}

// draftSellable 判断待写入窗口自身是否可售：折扣或绝对售价有其一即可。
func draftSellable(draft createDraft) bool {
	return draft.params.SaleDiscount.Valid || draft.params.SaleUncachedInputPrice.Valid
}

type createDraft struct {
	model  sqlc.Model
	params sqlc.CreateModelPriceParams
}

func validateIntent(intent string) error {
	switch intent {
	case IntentBase, IntentSaleDiscount, IntentSaleAbsolute:
		return nil
	case "":
		return invalidArgument("intent", "intent is required (base, sale_discount, or sale_absolute)")
	default:
		return invalidArgument("intent", "intent must be base, sale_discount, or sale_absolute")
	}
}

func (s *Service) lookupModel(ctx context.Context, modelID int64) (sqlc.Model, error) {
	model, err := s.store.LookupModelByID(ctx, modelID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return sqlc.Model{}, invalidArgument("model_id", "model not found")
		}
		return sqlc.Model{}, storeFailed(err, "load model")
	}
	return model, nil
}

func (s *Service) currentEnabledPrice(ctx context.Context, modelID int64) (*sqlc.ListModelPricesByModelRow, error) {
	rows, err := s.store.ListModelPricesByModel(ctx, modelID)
	if err != nil {
		return nil, storeFailed(err, "list model prices")
	}
	now := time.Now()
	for i := range rows {
		row := &rows[i]
		if row.Status != StatusEnabled {
			continue
		}
		if row.EffectiveFrom.Time.After(now) {
			continue
		}
		if row.EffectiveTo.Valid && !row.EffectiveTo.Time.After(now) {
			continue
		}
		return row, nil
	}
	return nil, nil
}

func (s *Service) buildCreateDraft(ctx context.Context, in CreateInput) (createDraft, error) {
	switch in.Intent {
	case IntentBase:
		return s.buildBaseDraft(ctx, in)
	case IntentSaleDiscount:
		return s.buildSaleDiscountDraft(ctx, in)
	case IntentSaleAbsolute:
		return s.buildSaleAbsoluteDraft(ctx, in)
	default:
		return createDraft{}, invalidArgument("intent", "intent must be base, sale_discount, or sale_absolute")
	}
}

func (s *Service) buildBaseDraft(ctx context.Context, in CreateInput) (createDraft, error) {
	currency := strings.TrimSpace(in.Currency)
	if currency == "" {
		return createDraft{}, invalidArgument("currency", "currency is required")
	}
	if in.PricingUnit != PricingUnitPer1MTokens {
		return createDraft{}, invalidArgument("pricing_unit", "pricing_unit must be \"per_1m_tokens\"")
	}
	amounts, err := parseModelPriceAmounts(in)
	if err != nil {
		return createDraft{}, err
	}
	longContext, err := parseLongContextConfig(in)
	if err != nil {
		return createDraft{}, err
	}
	fastPrice, err := parseFastPriceConfig(in.FastPrices)
	if err != nil {
		return createDraft{}, err
	}
	fastPrice.sale = saleVectorAmounts{}

	model, err := s.lookupModel(ctx, in.ModelID)
	if err != nil {
		return createDraft{}, err
	}
	current, err := s.currentEnabledPrice(ctx, in.ModelID)
	if err != nil {
		return createDraft{}, err
	}

	var saleDiscount pgtype.Numeric
	var sale saleVectorAmounts
	if current != nil {
		saleDiscount = current.SaleDiscount
		sale = saleVectorFromRow(*current)
		if fastPrice.configured {
			fastPrice.sale = fastSaleVectorFromRow(*current)
		}
	}

	return createDraft{
		model:  model,
		params: createParams(in, currency, in.PricingUnit, amounts, longContext, fastPrice, saleDiscount, sale),
	}, nil
}

func (s *Service) buildSaleDiscountDraft(ctx context.Context, in CreateInput) (createDraft, error) {
	saleDiscount, err := parseOptionalPositiveMultiplier("sale_discount", in.SaleDiscount)
	if err != nil {
		return createDraft{}, err
	}
	if !saleDiscount.Valid {
		return createDraft{}, invalidArgument("sale_discount", "sale_discount is required")
	}

	model, err := s.lookupModel(ctx, in.ModelID)
	if err != nil {
		return createDraft{}, err
	}
	current, err := s.requireCurrentBase(ctx, in.ModelID)
	if err != nil {
		return createDraft{}, err
	}

	fastPrice := fastBaseFromRow(*current)
	fastPrice.sale = fastSaleVectorFromRow(*current)
	return createDraft{
		model: model,
		params: createParams(
			in,
			current.Currency,
			current.PricingUnit,
			amountsFromRow(*current),
			longContextFromRow(*current),
			fastPrice,
			saleDiscount,
			saleVectorFromRow(*current),
		),
	}, nil
}

func (s *Service) buildSaleAbsoluteDraft(ctx context.Context, in CreateInput) (createDraft, error) {
	sale, err := parseSaleVector("sale_prices", in.SalePrices)
	if err != nil {
		return createDraft{}, err
	}
	if !sale.configured {
		return createDraft{}, invalidArgument("sale_prices", "sale_prices is required")
	}

	model, err := s.lookupModel(ctx, in.ModelID)
	if err != nil {
		return createDraft{}, err
	}
	current, err := s.requireCurrentBase(ctx, in.ModelID)
	if err != nil {
		return createDraft{}, err
	}

	fastPrice := fastBaseFromRow(*current)
	if fastPrice.configured {
		var saleIn *SalePriceVector
		if in.FastPrices != nil {
			saleIn = in.FastPrices.SalePrices
		}
		fastSale, err := parseSaleVector("fast_prices.sale_prices", saleIn)
		if err != nil {
			return createDraft{}, err
		}
		if !fastSale.configured {
			return createDraft{}, invalidArgument(
				"fast_prices.sale_prices",
				"fast tier requires its own sale_prices when absolute sale prices are configured",
			)
		}
		fastPrice.sale = fastSale
	}

	return createDraft{
		model: model,
		params: createParams(
			in,
			current.Currency,
			current.PricingUnit,
			amountsFromRow(*current),
			longContextFromRow(*current),
			fastPrice,
			current.SaleDiscount,
			sale,
		),
	}, nil
}

func (s *Service) requireCurrentBase(ctx context.Context, modelID int64) (*sqlc.ListModelPricesByModelRow, error) {
	current, err := s.currentEnabledPrice(ctx, modelID)
	if err != nil {
		return nil, err
	}
	if current == nil {
		return nil, invalidArgument("intent", "sale pricing requires an effective base price")
	}
	return current, nil
}

func createParams(
	in CreateInput,
	currency, pricingUnit string,
	amounts modelPriceAmounts,
	longContext longContextConfig,
	fastPrice fastPriceConfig,
	saleDiscount pgtype.Numeric,
	sale saleVectorAmounts,
) sqlc.CreateModelPriceParams {
	return sqlc.CreateModelPriceParams{
		ModelID:                            in.ModelID,
		Currency:                           currency,
		PricingUnit:                        pricingUnit,
		UncachedInputPrice:                 amounts.uncachedInputPrice,
		CacheReadInputPrice:                amounts.cacheReadInputPrice,
		CacheCreation5mInputPrice:          amounts.cacheCreation5mInputPrice,
		CacheCreation1hInputPrice:          amounts.cacheCreation1hInputPrice,
		CacheCreation30mInputPrice:         amounts.cacheCreation30mInputPrice,
		OutputPrice:                        amounts.outputPrice,
		ReasoningOutputPrice:               amounts.reasoningOutputPrice,
		SaleDiscount:                       saleDiscount,
		SaleUncachedInputPrice:             sale.uncachedInputPrice,
		SaleCacheReadInputPrice:            sale.cacheReadInputPrice,
		SaleCacheCreation5mInputPrice:      sale.cacheCreation5mInputPrice,
		SaleCacheCreation1hInputPrice:      sale.cacheCreation1hInputPrice,
		SaleCacheCreation30mInputPrice:     sale.cacheCreation30mInputPrice,
		SaleOutputPrice:                    sale.outputPrice,
		SaleReasoningOutputPrice:           sale.reasoningOutputPrice,
		LongContextEnabled:                 longContext.enabled,
		LongContextThreshold:               longContext.threshold,
		LongContextInputMultiplier:         longContext.inputMultiplier,
		LongContextOutputMultiplier:        longContext.outputMultiplier,
		FastUncachedInputPrice:             fastPrice.uncachedInputPrice,
		FastCacheReadInputPrice:            fastPrice.cacheReadInputPrice,
		FastCacheCreation5mInputPrice:      fastPrice.cacheCreation5mInputPrice,
		FastCacheCreation1hInputPrice:      fastPrice.cacheCreation1hInputPrice,
		FastCacheCreation30mInputPrice:     fastPrice.cacheCreation30mInputPrice,
		FastOutputPrice:                    fastPrice.outputPrice,
		FastReasoningOutputPrice:           fastPrice.reasoningOutputPrice,
		FastSaleUncachedInputPrice:         fastPrice.sale.uncachedInputPrice,
		FastSaleCacheReadInputPrice:        fastPrice.sale.cacheReadInputPrice,
		FastSaleCacheCreation5mInputPrice:  fastPrice.sale.cacheCreation5mInputPrice,
		FastSaleCacheCreation1hInputPrice:  fastPrice.sale.cacheCreation1hInputPrice,
		FastSaleCacheCreation30mInputPrice: fastPrice.sale.cacheCreation30mInputPrice,
		FastSaleOutputPrice:                fastPrice.sale.outputPrice,
		FastSaleReasoningOutputPrice:       fastPrice.sale.reasoningOutputPrice,
		FastReferenceSource:                fastPrice.referenceSource,
		FastReferenceCheckedAt:             fastPrice.referenceCheckedAt,
		FastConfigured:                     fastPrice.configured,
		ReplaceOverlappingEnabled:          in.ReplaceOverlappingEnabled,
		Status:                             in.Status,
		EffectiveFrom:                      tsParam(&in.EffectiveFrom),
		EffectiveTo:                        tsParam(in.EffectiveTo),
	}
}

func amountsFromRow(row sqlc.ListModelPricesByModelRow) modelPriceAmounts {
	return modelPriceAmounts{
		uncachedInputPrice:         row.UncachedInputPrice,
		cacheReadInputPrice:        row.CacheReadInputPrice,
		cacheCreation5mInputPrice:  row.CacheCreation5mInputPrice,
		cacheCreation1hInputPrice:  row.CacheCreation1hInputPrice,
		cacheCreation30mInputPrice: row.CacheCreation30mInputPrice,
		outputPrice:                row.OutputPrice,
		reasoningOutputPrice:       row.ReasoningOutputPrice,
	}
}

func longContextFromRow(row sqlc.ListModelPricesByModelRow) longContextConfig {
	return longContextConfig{
		enabled:          row.LongContextEnabled,
		threshold:        row.LongContextThreshold,
		inputMultiplier:  row.LongContextInputMultiplier,
		outputMultiplier: row.LongContextOutputMultiplier,
	}
}

func saleVectorFromRow(row sqlc.ListModelPricesByModelRow) saleVectorAmounts {
	return saleVectorAmounts{
		configured:                 row.SaleUncachedInputPrice.Valid && row.SaleOutputPrice.Valid,
		uncachedInputPrice:         row.SaleUncachedInputPrice,
		cacheReadInputPrice:        row.SaleCacheReadInputPrice,
		cacheCreation5mInputPrice:  row.SaleCacheCreation5mInputPrice,
		cacheCreation1hInputPrice:  row.SaleCacheCreation1hInputPrice,
		cacheCreation30mInputPrice: row.SaleCacheCreation30mInputPrice,
		outputPrice:                row.SaleOutputPrice,
		reasoningOutputPrice:       row.SaleReasoningOutputPrice,
	}
}

func fastSaleVectorFromRow(row sqlc.ListModelPricesByModelRow) saleVectorAmounts {
	return saleVectorAmounts{
		configured:                 row.FastSaleUncachedInputPrice.Valid && row.FastSaleOutputPrice.Valid,
		uncachedInputPrice:         row.FastSaleUncachedInputPrice,
		cacheReadInputPrice:        row.FastSaleCacheReadInputPrice,
		cacheCreation5mInputPrice:  row.FastSaleCacheCreation5mInputPrice,
		cacheCreation1hInputPrice:  row.FastSaleCacheCreation1hInputPrice,
		cacheCreation30mInputPrice: row.FastSaleCacheCreation30mInputPrice,
		outputPrice:                row.FastSaleOutputPrice,
		reasoningOutputPrice:       row.FastSaleReasoningOutputPrice,
	}
}

func fastBaseFromRow(row sqlc.ListModelPricesByModelRow) fastPriceConfig {
	if row.FastServiceTierID <= 0 {
		return fastPriceConfig{}
	}
	return fastPriceConfig{
		configured:                 true,
		uncachedInputPrice:         row.FastUncachedInputPrice,
		cacheReadInputPrice:        row.FastCacheReadInputPrice,
		cacheCreation5mInputPrice:  row.FastCacheCreation5mInputPrice,
		cacheCreation1hInputPrice:  row.FastCacheCreation1hInputPrice,
		cacheCreation30mInputPrice: row.FastCacheCreation30mInputPrice,
		outputPrice:                row.FastOutputPrice,
		reasoningOutputPrice:       row.FastReasoningOutputPrice,
		referenceSource:            row.FastReferenceSource,
		referenceCheckedAt:         row.FastReferenceCheckedAt,
	}
}

// Update 调整窗口/启停：改 effective_to（关闭窗口）与 status；金额不可改。重新启用或延长窗口时复查重叠。
//
// 停用与「把窗口关到当下」对客户是同一件事：模型立刻没有可解析售价。若这是最后一条
// 供价窗口，写入前必须取得管理员确认，确认后在同一事务里连同模型一起下架——
// 只改价格不下架，模型会停在「列表里有、一调失败」的状态。
func (s *Service) Update(ctx context.Context, in UpdateInput) (ModelPrice, error) {
	if in.ID <= 0 {
		return ModelPrice{}, invalidArgument("id", "id must be positive")
	}
	if err := validateStatus(in.Status); err != nil {
		return ModelPrice{}, err
	}
	if s.db == nil || s.queries == nil {
		return ModelPrice{}, storeFailed(errors.New("supply linkage dependencies are unavailable"), "update model price")
	}

	tx, err := s.db.Begin(ctx)
	if err != nil {
		return ModelPrice{}, storeFailed(err, "begin model price update transaction")
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := s.queries.WithTx(tx)
	txService := s.withStore(q)

	// 先读一次只为拿到 model_id：不知道属于哪个模型就无法确定该锁哪一行。
	existing, err := q.GetModelPrice(ctx, in.ID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ModelPrice{}, notFound("model price not found")
		}
		return ModelPrice{}, storeFailed(err, "load model price")
	}
	// 取锁后重读：影响预览必须基于锁内事实，不复用锁外读到的行。
	if err := supply.LockModels(ctx, q, []int64{existing.ModelID}); err != nil {
		return ModelPrice{}, storeFailed(err, "lock model for price update")
	}
	existing, err = q.GetModelPrice(ctx, in.ID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ModelPrice{}, notFound("model price not found")
		}
		return ModelPrice{}, storeFailed(err, "reload model price in transaction")
	}

	if in.EffectiveTo != nil && !in.EffectiveTo.After(existing.EffectiveFrom.Time) {
		return ModelPrice{}, invalidArgument("effective_to", "effective_to must be after effective_from")
	}

	if in.Status == StatusEnabled {
		if err := txService.ensureNoOverlap(ctx, existing.ModelID, existing.ID, existing.EffectiveFrom.Time, in.EffectiveTo); err != nil {
			return ModelPrice{}, err
		}
	}

	var impact supply.Impact
	if stopsSupplyingNow(in) {
		impact, err = supply.PriceImpact(ctx, q, existing.ModelID, &in.ID)
		if err != nil {
			return ModelPrice{}, storeFailed(err, "compute model price disable impact")
		}
		if err := supply.Authorize(impact,
			"model_price_disable_confirmation_required",
			"撤掉这条价格后该模型没有可解析售价；确认后会连同模型一并下架",
			in.Confirmation,
		); err != nil {
			return ModelPrice{}, err
		}
	}

	row, err := q.UpdateModelPriceWindow(ctx, sqlc.UpdateModelPriceWindowParams{
		ID:          in.ID,
		Status:      in.Status,
		EffectiveTo: tsParam(in.EffectiveTo),
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ModelPrice{}, notFound("model price not found")
		}
		return ModelPrice{}, storeFailed(err, "update model price")
	}

	if impact.RequiresConfirmation() {
		if err := supply.DisableAffectedModels(ctx, q, impact, supply.ReasonPriceDisabled); err != nil {
			return ModelPrice{}, storeFailed(err, "delist models losing sale price")
		}
	}

	warnings, err := expiryWarnings(ctx, q, row.ModelID, row.ID, in.Status, in.EffectiveTo)
	if err != nil {
		return ModelPrice{}, err
	}

	rows, listErr := q.ListModelPricesByModel(ctx, row.ModelID)
	if listErr != nil {
		return ModelPrice{}, storeFailed(listErr, "reload model price")
	}
	for _, candidate := range rows {
		if candidate.ID != row.ID {
			continue
		}
		result := toModelPriceFromRow(candidate)
		result.Warnings = warnings
		if err := tx.Commit(ctx); err != nil {
			return ModelPrice{}, storeFailed(err, "commit model price update transaction")
		}
		return result, nil
	}
	return ModelPrice{}, storeFailed(pgx.ErrNoRows, "reload model price")
}

// stopsSupplyingNow 判断这次写入是否让该窗口此刻起停止供价。
// 停用是显式的；把 effective_to 改到当下或过去与停用等价，客户侧看不出区别。
// effective_to 落在未来只是预约到期，那条路由 expiryWarnings 提示，不在这里阻断。
func stopsSupplyingNow(in UpdateInput) bool {
	if in.Status == StatusDisabled {
		return true
	}
	return in.EffectiveTo != nil && !in.EffectiveTo.After(time.Now())
}

// expiryWarnings 检查窗口到期后是否还有接续的可售窗口。
//
// 窗口自然到期不经过任何管理员操作，二次确认在那条路上没有触发时机，
// 所以只能在设定到期时间的这一刻把话说在前面。
func expiryWarnings(ctx context.Context, q *sqlc.Queries, modelID, priceID int64, status string, effectiveTo *time.Time) ([]string, error) {
	if status != StatusEnabled || effectiveTo == nil || !effectiveTo.After(time.Now()) {
		return nil, nil
	}
	hasSuccessor, err := q.ModelHasSuccessorSalePriceWindow(ctx, sqlc.ModelHasSuccessorSalePriceWindowParams{
		ModelID:        modelID,
		ExcludePriceID: priceID,
		ExpiresAt:      pgtype.Timestamptz{Time: *effectiveTo, Valid: true},
	})
	if err != nil {
		return nil, storeFailed(err, "check successor sale price window")
	}
	if hasSuccessor {
		return nil, nil
	}
	return []string{
		"该窗口到期后没有接续的可售价格，模型会在那一刻失去售价并开始调用失败；请提前配好下一个窗口。",
	}, nil
}

// ensureNoOverlap 校验目标窗口与同一 model 现有启用窗口不重叠（半开区间 [from, to)）。
func (s *Service) ensureNoOverlap(ctx context.Context, modelID, excludeID int64, from time.Time, to *time.Time) error {
	windows, err := s.store.ListEnabledModelPriceWindows(ctx, sqlc.ListEnabledModelPriceWindowsParams{
		ModelID:   modelID,
		ExcludeID: excludeID,
	})
	if err != nil {
		return storeFailed(err, "list enabled model price windows")
	}

	for _, w := range windows {
		var existingTo *time.Time
		if w.EffectiveTo.Valid {
			t := w.EffectiveTo.Time
			existingTo = &t
		}
		if windowsOverlap(from, to, w.EffectiveFrom.Time, existingTo) {
			return failure.New(
				failure.CodeAdminPricingWindowOverlap,
				failure.WithMessage("effective window overlaps an existing enabled model price"),
			)
		}
	}

	return nil
}

// windowsOverlap 判断两个半开区间 [aFrom, aTo) 与 [bFrom, bTo) 是否相交；nil 结束时间表示 +∞。
func windowsOverlap(aFrom time.Time, aTo *time.Time, bFrom time.Time, bTo *time.Time) bool {
	aStartsBeforeBEnds := bTo == nil || aFrom.Before(*bTo)
	bStartsBeforeAEnds := aTo == nil || bFrom.Before(*aTo)
	return aStartsBeforeBEnds && bStartsBeforeAEnds
}

// modelPriceAmounts 持有解析后的 NUMERIC 基准售价。
type modelPriceAmounts struct {
	uncachedInputPrice         pgtype.Numeric
	cacheReadInputPrice        pgtype.Numeric
	cacheCreation5mInputPrice  pgtype.Numeric
	cacheCreation1hInputPrice  pgtype.Numeric
	cacheCreation30mInputPrice pgtype.Numeric
	outputPrice                pgtype.Numeric
	reasoningOutputPrice       pgtype.Numeric
}

// saleVectorAmounts 持有解析后的 NUMERIC 绝对售价；未配置时全部为 SQL NULL。
type saleVectorAmounts struct {
	configured                 bool
	uncachedInputPrice         pgtype.Numeric
	cacheReadInputPrice        pgtype.Numeric
	cacheCreation5mInputPrice  pgtype.Numeric
	cacheCreation1hInputPrice  pgtype.Numeric
	cacheCreation30mInputPrice pgtype.Numeric
	outputPrice                pgtype.Numeric
	reasoningOutputPrice       pgtype.Numeric
}

// parseSaleVector 解析一组绝对售价；nil 表示不配（整组 NULL）。
// prefix 用于把字段名还原成请求体里的路径，如 "sale_prices" / "fast_prices.sale_prices"。
func parseSaleVector(prefix string, in *SalePriceVector) (saleVectorAmounts, error) {
	if in == nil {
		return saleVectorAmounts{}, nil
	}
	out := saleVectorAmounts{configured: true}
	var err error
	if out.uncachedInputPrice, err = parseMoney(prefix+".uncached_input_price", in.UncachedInputPrice); err != nil {
		return saleVectorAmounts{}, err
	}
	if out.outputPrice, err = parseMoney(prefix+".output_price", in.OutputPrice); err != nil {
		return saleVectorAmounts{}, err
	}
	if out.cacheReadInputPrice, err = parseOptionalMoney(prefix+".cache_read_input_price", in.CacheReadInputPrice); err != nil {
		return saleVectorAmounts{}, err
	}
	if out.cacheCreation5mInputPrice, err = parseOptionalMoney(prefix+".cache_creation_5m_input_price", in.CacheCreation5mInputPrice); err != nil {
		return saleVectorAmounts{}, err
	}
	if out.cacheCreation1hInputPrice, err = parseOptionalMoney(prefix+".cache_creation_1h_input_price", in.CacheCreation1hInputPrice); err != nil {
		return saleVectorAmounts{}, err
	}
	if out.cacheCreation30mInputPrice, err = parseOptionalMoney(prefix+".cache_creation_30m_input_price", in.CacheCreation30mInputPrice); err != nil {
		return saleVectorAmounts{}, err
	}
	if out.reasoningOutputPrice, err = parseOptionalMoney(prefix+".reasoning_output_price", in.ReasoningOutputPrice); err != nil {
		return saleVectorAmounts{}, err
	}
	return out, nil
}

// saleVectorFromValues 把库里读出的一组 sale_* 还原为向量；未配置（必填两项缺任一）时返回 nil。
func saleVectorFromValues(
	uncached, cacheRead, cacheCreation5m, cacheCreation1h, cacheCreation30m, output, reasoning pgtype.Numeric,
) *SalePriceVector {
	if !uncached.Valid || !output.Valid {
		return nil
	}
	return &SalePriceVector{
		UncachedInputPrice:         numericString(uncached),
		CacheReadInputPrice:        numericPtr(cacheRead),
		CacheCreation5mInputPrice:  numericPtr(cacheCreation5m),
		CacheCreation1hInputPrice:  numericPtr(cacheCreation1h),
		CacheCreation30mInputPrice: numericPtr(cacheCreation30m),
		OutputPrice:                numericString(output),
		ReasoningOutputPrice:       numericPtr(reasoning),
	}
}

type fastPriceConfig struct {
	configured                 bool
	uncachedInputPrice         pgtype.Numeric
	cacheReadInputPrice        pgtype.Numeric
	cacheCreation5mInputPrice  pgtype.Numeric
	cacheCreation1hInputPrice  pgtype.Numeric
	cacheCreation30mInputPrice pgtype.Numeric
	outputPrice                pgtype.Numeric
	reasoningOutputPrice       pgtype.Numeric
	sale                       saleVectorAmounts
	referenceSource            pgtype.Text
	referenceCheckedAt         pgtype.Date
}

func parseFastPriceConfig(in *FastPriceInput) (fastPriceConfig, error) {
	if in == nil {
		return fastPriceConfig{}, nil
	}
	var out fastPriceConfig
	out.configured = true
	var err error
	if out.uncachedInputPrice, err = parseMoney("fast_prices.uncached_input_price", in.UncachedInputPrice); err != nil {
		return fastPriceConfig{}, err
	}
	if out.outputPrice, err = parseMoney("fast_prices.output_price", in.OutputPrice); err != nil {
		return fastPriceConfig{}, err
	}
	if out.cacheReadInputPrice, err = parseOptionalMoney("fast_prices.cache_read_input_price", in.CacheReadInputPrice); err != nil {
		return fastPriceConfig{}, err
	}
	if out.cacheCreation5mInputPrice, err = parseOptionalMoney("fast_prices.cache_creation_5m_input_price", in.CacheCreation5mInputPrice); err != nil {
		return fastPriceConfig{}, err
	}
	if out.cacheCreation1hInputPrice, err = parseOptionalMoney("fast_prices.cache_creation_1h_input_price", in.CacheCreation1hInputPrice); err != nil {
		return fastPriceConfig{}, err
	}
	if out.cacheCreation30mInputPrice, err = parseOptionalMoney("fast_prices.cache_creation_30m_input_price", in.CacheCreation30mInputPrice); err != nil {
		return fastPriceConfig{}, err
	}
	if out.reasoningOutputPrice, err = parseOptionalMoney("fast_prices.reasoning_output_price", in.ReasoningOutputPrice); err != nil {
		return fastPriceConfig{}, err
	}
	if out.sale, err = parseSaleVector("fast_prices.sale_prices", in.SalePrices); err != nil {
		return fastPriceConfig{}, err
	}

	var source string
	if in.ReferenceSource != nil {
		source = strings.TrimSpace(*in.ReferenceSource)
	}
	hasSource := source != ""
	hasDate := in.ReferenceCheckedAt != nil
	if hasSource != hasDate {
		return fastPriceConfig{}, invalidArgument("fast_prices.reference_source", "reference_source and reference_checked_at must be provided together")
	}
	if hasSource {
		out.referenceSource = pgtype.Text{String: source, Valid: true}
		out.referenceCheckedAt = pgtype.Date{Time: *in.ReferenceCheckedAt, Valid: true}
	}
	return out, nil
}

func parseModelPriceAmounts(in CreateInput) (modelPriceAmounts, error) {
	var out modelPriceAmounts
	var err error

	if out.uncachedInputPrice, err = parseMoney("uncached_input_price", in.UncachedInputPrice); err != nil {
		return modelPriceAmounts{}, err
	}
	if out.outputPrice, err = parseMoney("output_price", in.OutputPrice); err != nil {
		return modelPriceAmounts{}, err
	}
	if out.cacheReadInputPrice, err = parseOptionalMoney("cache_read_input_price", in.CacheReadInputPrice); err != nil {
		return modelPriceAmounts{}, err
	}
	if out.cacheCreation5mInputPrice, err = parseOptionalMoney("cache_creation_5m_input_price", in.CacheCreation5mInputPrice); err != nil {
		return modelPriceAmounts{}, err
	}
	if out.cacheCreation1hInputPrice, err = parseOptionalMoney("cache_creation_1h_input_price", in.CacheCreation1hInputPrice); err != nil {
		return modelPriceAmounts{}, err
	}
	if out.cacheCreation30mInputPrice, err = parseOptionalMoney("cache_creation_30m_input_price", in.CacheCreation30mInputPrice); err != nil {
		return modelPriceAmounts{}, err
	}
	if out.reasoningOutputPrice, err = parseOptionalMoney("reasoning_output_price", in.ReasoningOutputPrice); err != nil {
		return modelPriceAmounts{}, err
	}

	return out, nil
}

func markSaleConfigured(p ModelPrice) ModelPrice {
	p.SaleConfigured = p.SaleDiscount != nil || p.SalePrices != nil
	return p
}

func toModelPriceFromCreateRow(c sqlc.CreateModelPriceRow) ModelPrice {
	result := ModelPrice{
		ID:                         c.ID,
		ModelID:                    c.ModelID,
		Currency:                   c.Currency,
		PricingUnit:                c.PricingUnit,
		UncachedInputPrice:         numericString(c.UncachedInputPrice),
		CacheReadInputPrice:        numericPtr(c.CacheReadInputPrice),
		CacheCreation5mInputPrice:  numericPtr(c.CacheCreation5mInputPrice),
		CacheCreation1hInputPrice:  numericPtr(c.CacheCreation1hInputPrice),
		CacheCreation30mInputPrice: numericPtr(c.CacheCreation30mInputPrice),
		OutputPrice:                numericString(c.OutputPrice),
		ReasoningOutputPrice:       numericPtr(c.ReasoningOutputPrice),
		SaleDiscount:               numericPtr(c.SaleDiscount),
		SalePrices: saleVectorFromValues(
			c.SaleUncachedInputPrice,
			c.SaleCacheReadInputPrice,
			c.SaleCacheCreation5mInputPrice,
			c.SaleCacheCreation1hInputPrice,
			c.SaleCacheCreation30mInputPrice,
			c.SaleOutputPrice,
			c.SaleReasoningOutputPrice,
		),
		LongContextEnabled:          c.LongContextEnabled,
		LongContextThreshold:        int64Ptr(c.LongContextThreshold),
		LongContextInputMultiplier:  numericPtr(c.LongContextInputMultiplier),
		LongContextOutputMultiplier: numericPtr(c.LongContextOutputMultiplier),
		FastPriceStatus:             fastPriceStatus(c.FastServiceTierID),
		Status:                      c.Status,
		EffectiveFrom:               c.EffectiveFrom.Time,
		EffectiveTo:                 timePtr(c.EffectiveTo),
		CreatedAt:                   c.CreatedAt.Time,
		UpdatedAt:                   c.UpdatedAt.Time,
	}
	result.FastPrices = fastPriceFromValues(
		c.FastServiceTierID,
		c.FastUncachedInputPrice,
		c.FastCacheReadInputPrice,
		c.FastCacheCreation5mInputPrice,
		c.FastCacheCreation1hInputPrice,
		c.FastCacheCreation30mInputPrice,
		c.FastOutputPrice,
		c.FastReasoningOutputPrice,
		saleVectorFromValues(
			c.FastSaleUncachedInputPrice,
			c.FastSaleCacheReadInputPrice,
			c.FastSaleCacheCreation5mInputPrice,
			c.FastSaleCacheCreation1hInputPrice,
			c.FastSaleCacheCreation30mInputPrice,
			c.FastSaleOutputPrice,
			c.FastSaleReasoningOutputPrice,
		),
		c.FastReferenceSource,
		c.FastReferenceCheckedAt,
	)
	return markSaleConfigured(result)
}

func toModelPriceFromRow(c sqlc.ListModelPricesByModelRow) ModelPrice {
	result := ModelPrice{
		ID:                         c.ID,
		ModelID:                    c.ModelID,
		ModelExternalID:            c.ModelExternalID,
		ModelDisplayName:           c.ModelDisplayName,
		Currency:                   c.Currency,
		PricingUnit:                c.PricingUnit,
		UncachedInputPrice:         numericString(c.UncachedInputPrice),
		CacheReadInputPrice:        numericPtr(c.CacheReadInputPrice),
		CacheCreation5mInputPrice:  numericPtr(c.CacheCreation5mInputPrice),
		CacheCreation1hInputPrice:  numericPtr(c.CacheCreation1hInputPrice),
		CacheCreation30mInputPrice: numericPtr(c.CacheCreation30mInputPrice),
		OutputPrice:                numericString(c.OutputPrice),
		ReasoningOutputPrice:       numericPtr(c.ReasoningOutputPrice),
		SaleDiscount:               numericPtr(c.SaleDiscount),
		SalePrices: saleVectorFromValues(
			c.SaleUncachedInputPrice,
			c.SaleCacheReadInputPrice,
			c.SaleCacheCreation5mInputPrice,
			c.SaleCacheCreation1hInputPrice,
			c.SaleCacheCreation30mInputPrice,
			c.SaleOutputPrice,
			c.SaleReasoningOutputPrice,
		),
		LongContextEnabled:          c.LongContextEnabled,
		LongContextThreshold:        int64Ptr(c.LongContextThreshold),
		LongContextInputMultiplier:  numericPtr(c.LongContextInputMultiplier),
		LongContextOutputMultiplier: numericPtr(c.LongContextOutputMultiplier),
		FastPriceStatus:             fastPriceStatus(c.FastServiceTierID),
		FastPriceReference:          officialFastPriceReference(c.ModelExternalID),
		Status:                      c.Status,
		EffectiveFrom:               c.EffectiveFrom.Time,
		EffectiveTo:                 timePtr(c.EffectiveTo),
		CreatedAt:                   c.CreatedAt.Time,
		UpdatedAt:                   c.UpdatedAt.Time,
	}
	result.FastPrices = fastPriceFromValues(
		c.FastServiceTierID,
		c.FastUncachedInputPrice,
		c.FastCacheReadInputPrice,
		c.FastCacheCreation5mInputPrice,
		c.FastCacheCreation1hInputPrice,
		c.FastCacheCreation30mInputPrice,
		c.FastOutputPrice,
		c.FastReasoningOutputPrice,
		saleVectorFromValues(
			c.FastSaleUncachedInputPrice,
			c.FastSaleCacheReadInputPrice,
			c.FastSaleCacheCreation5mInputPrice,
			c.FastSaleCacheCreation1hInputPrice,
			c.FastSaleCacheCreation30mInputPrice,
			c.FastSaleOutputPrice,
			c.FastSaleReasoningOutputPrice,
		),
		c.FastReferenceSource,
		c.FastReferenceCheckedAt,
	)
	return markSaleConfigured(result)
}

func fastPriceStatus(serviceTierID int64) string {
	if serviceTierID > 0 {
		return "configured"
	}
	return "missing"
}

func fastPriceFromValues(
	serviceTierID int64,
	uncached, cacheRead, cacheCreation5m, cacheCreation1h, cacheCreation30m, output, reasoning pgtype.Numeric,
	sale *SalePriceVector,
	referenceSource pgtype.Text,
	referenceCheckedAt pgtype.Date,
) *FastPrice {
	if serviceTierID <= 0 {
		return nil
	}
	return &FastPrice{
		ServiceTierID:              serviceTierID,
		UncachedInputPrice:         numericString(uncached),
		CacheReadInputPrice:        numericPtr(cacheRead),
		CacheCreation5mInputPrice:  numericPtr(cacheCreation5m),
		CacheCreation1hInputPrice:  numericPtr(cacheCreation1h),
		CacheCreation30mInputPrice: numericPtr(cacheCreation30m),
		OutputPrice:                numericString(output),
		ReasoningOutputPrice:       numericPtr(reasoning),
		SalePrices:                 sale,
		ReferenceSource:            textValuePtr(referenceSource),
		ReferenceCheckedAt:         dateValuePtr(referenceCheckedAt),
	}
}

// longContextConfig 是解析后的长上下文阶梯配置（对应 model_prices 四列）。
type longContextConfig struct {
	enabled          bool
	threshold        pgtype.Int8
	inputMultiplier  pgtype.Numeric
	outputMultiplier pgtype.Numeric
}

// parseLongContextConfig 解析长上下文配置：启用时 threshold/倍率必填且 >0；关闭时可保留可选值供展示，或全空。
func parseLongContextConfig(in CreateInput) (longContextConfig, error) {
	var out longContextConfig
	out.enabled = in.LongContextEnabled

	if in.LongContextThreshold != nil {
		if *in.LongContextThreshold <= 0 {
			return longContextConfig{}, invalidArgument("long_context_threshold", "must be a positive integer")
		}
		out.threshold = pgtype.Int8{Int64: *in.LongContextThreshold, Valid: true}
	}
	var err error
	if out.inputMultiplier, err = parseOptionalPositiveMultiplier("long_context_input_multiplier", in.LongContextInputMultiplier); err != nil {
		return longContextConfig{}, err
	}
	if out.outputMultiplier, err = parseOptionalPositiveMultiplier("long_context_output_multiplier", in.LongContextOutputMultiplier); err != nil {
		return longContextConfig{}, err
	}

	if !out.enabled {
		return out, nil
	}
	if !out.threshold.Valid {
		return longContextConfig{}, invalidArgument("long_context_threshold", "is required when long_context_enabled is true")
	}
	if !out.inputMultiplier.Valid {
		return longContextConfig{}, invalidArgument("long_context_input_multiplier", "is required when long_context_enabled is true")
	}
	if !out.outputMultiplier.Valid {
		return longContextConfig{}, invalidArgument("long_context_output_multiplier", "is required when long_context_enabled is true")
	}
	return out, nil
}

// parseOptionalPositiveMultiplier 解析可选正倍率：nil/空 → NULL；否则须为 >0 的十进制。
func parseOptionalPositiveMultiplier(field string, raw *string) (pgtype.Numeric, error) {
	if raw == nil || strings.TrimSpace(*raw) == "" {
		return pgtype.Numeric{Valid: false}, nil
	}
	n, err := parseMoney(field, *raw)
	if err != nil {
		return pgtype.Numeric{}, err
	}
	r, ok := new(big.Rat).SetString(strings.TrimSpace(*raw))
	if !ok || r.Sign() <= 0 {
		return pgtype.Numeric{}, invalidArgument(field, "must be a positive decimal amount")
	}
	return n, nil
}

func int64Ptr(v pgtype.Int8) *int64 {
	if !v.Valid {
		return nil
	}
	out := v.Int64
	return &out
}

func textValuePtr(v pgtype.Text) *string {
	if !v.Valid {
		return nil
	}
	out := v.String
	return &out
}

func dateValuePtr(v pgtype.Date) *time.Time {
	if !v.Valid {
		return nil
	}
	out := v.Time
	return &out
}

func validateStatus(status string) error {
	switch status {
	case StatusEnabled, StatusDisabled:
		return nil
	default:
		return invalidArgument("status", "status must be \"enabled\" or \"disabled\"")
	}
}

// parseMoney 解析必填金额：非负十进制字符串 → pgtype.Numeric。
func parseMoney(field, raw string) (pgtype.Numeric, error) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return pgtype.Numeric{}, invalidArgument(field, "is required")
	}
	r, ok := new(big.Rat).SetString(s)
	if !ok || strings.ContainsAny(s, "eE") {
		return pgtype.Numeric{}, invalidArgument(field, "must be a non-negative decimal amount")
	}
	if r.Sign() < 0 {
		return pgtype.Numeric{}, invalidArgument(field, "must be a non-negative decimal amount")
	}
	var n pgtype.Numeric
	if err := n.Scan(s); err != nil {
		return pgtype.Numeric{}, invalidArgument(field, "invalid decimal amount")
	}
	return n, nil
}

// parseOptionalMoney 解析可选金额：nil/空串 → SQL NULL；否则按必填规则解析。
func parseOptionalMoney(field string, raw *string) (pgtype.Numeric, error) {
	if raw == nil || strings.TrimSpace(*raw) == "" {
		return pgtype.Numeric{Valid: false}, nil
	}
	return parseMoney(field, *raw)
}

// tsParam 把可选时间转成 pgtype.Timestamptz；nil → SQL NULL。
func tsParam(t *time.Time) pgtype.Timestamptz {
	if t == nil {
		return pgtype.Timestamptz{Valid: false}
	}
	return pgtype.Timestamptz{Time: *t, Valid: true}
}

func timePtr(t pgtype.Timestamptz) *time.Time {
	if !t.Valid {
		return nil
	}
	out := t.Time
	return &out
}

// numericString 把 NUMERIC 精确格式化为十进制字符串（不用 float）；NULL/NaN/Inf → "0"。
func numericString(n pgtype.Numeric) string {
	if s := numericPtr(n); s != nil {
		return *s
	}
	return "0"
}

// numericPtr 把 NUMERIC 精确格式化为十进制字符串（不用 float）；NULL/NaN/Inf 返回 nil。
func numericPtr(n pgtype.Numeric) *string {
	if !n.Valid || n.NaN || n.InfinityModifier != pgtype.Finite {
		return nil
	}
	if n.Int == nil {
		zero := "0"
		return &zero
	}

	negative := n.Int.Sign() < 0
	digits := new(big.Int).Abs(n.Int).String()
	exp := int(n.Exp)

	var formatted string
	switch {
	case exp == 0:
		formatted = digits
	case exp > 0:
		formatted = digits + strings.Repeat("0", exp)
	default:
		scale := -exp
		if len(digits) <= scale {
			digits = strings.Repeat("0", scale-len(digits)+1) + digits
		}
		point := len(digits) - scale
		formatted = digits[:point] + "." + digits[point:]
	}

	if negative {
		formatted = "-" + formatted
	}
	return &formatted
}

func invalidArgument(field, message string) error {
	return failure.New(
		failure.CodeAdminInvalidArgument,
		failure.WithMessage(message),
		failure.WithField("field", field),
	)
}

func notFound(message string) error {
	return failure.New(failure.CodeAdminNotFound, failure.WithMessage(message))
}

func storeFailed(cause error, message string) error {
	return failure.Wrap(failure.CodeAdminStoreFailed, cause, failure.WithMessage(message))
}
