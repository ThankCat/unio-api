package lifecycle

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/ThankCat/unio-gateway/internal/core/adapter"
	"github.com/ThankCat/unio-gateway/internal/core/auth"
	"github.com/ThankCat/unio-gateway/internal/core/billing"
	"github.com/ThankCat/unio-gateway/internal/core/ledger"
	"github.com/ThankCat/unio-gateway/internal/core/requestlog"
	"github.com/ThankCat/unio-gateway/internal/core/usage"
	"github.com/ThankCat/unio-gateway/internal/platform/failure"
	"github.com/ThankCat/unio-gateway/internal/platform/store/sqlc"
)

// ChatTxBeginner 定义 chat settlement 开启数据库事务所需能力。
type ChatTxBeginner interface {
	Begin(ctx context.Context) (pgx.Tx, error)
}

// ChatLedgerCapturer 定义 chat settlement 确认扣费或释放冻结余额所需能力。
type ChatLedgerCapturer interface {
	CaptureWithQueries(ctx context.Context, queries *sqlc.Queries, params ledger.CaptureParams) (ledger.Reservation, error)
	ReleaseWithQueries(ctx context.Context, queries *sqlc.Queries, params ledger.ReleaseParams) (ledger.Reservation, error)
}

// ChatBillingCalculator 定义 chat settlement 计算请求金额所需能力。
type ChatBillingCalculator interface {
	CalculateCustomerCharge(facts usage.Facts, price billing.CustomerPriceSnapshot) (billing.CustomerCharge, error)
	CalculateProviderCost(facts usage.Facts, cost billing.ProviderCostSnapshot) (billing.ProviderCost, error)
}

// ChatSettlementService 负责 chat 请求成功后的 usage、price snapshot 和 ledger 结算。
type ChatSettlementService struct {
	db                ChatTxBeginner
	queries           *sqlc.Queries
	billingCalculator ChatBillingCalculator
	ledgerCapturer    ChatLedgerCapturer
}

// NewChatSettlementService 创建 chat 请求结算 service。
func NewChatSettlementService(db ChatTxBeginner, queries *sqlc.Queries, billingCalculator ChatBillingCalculator, ledgerCapturer ChatLedgerCapturer) *ChatSettlementService {
	if db == nil {
		panic("gateway: chat settlement tx beginner is required")
	}
	if queries == nil {
		panic("gateway: chat settlement queries is required")
	}
	if billingCalculator == nil {
		panic("gateway: chat billing calculator is required")
	}
	if ledgerCapturer == nil {
		panic("gateway: chat ledger capturer is required")
	}

	return &ChatSettlementService{
		db:                db,
		queries:           queries,
		billingCalculator: billingCalculator,
		ledgerCapturer:    ledgerCapturer,
	}
}

// ChatSettlementExecutor 定义 chat 成功后提交 usage、price snapshot 和 ledger 结算事务的能力。
type ChatSettlementExecutor interface {
	SettleSuccessfulChat(ctx context.Context, params ChatSettlementParams) error
}

// ChatSettlementParams 表示一次成功 chat 请求结算所需的事实。
// 非流式与流式都只消费 adapter 同次解析产生的不可变 ResponseFacts。
type ChatSettlementParams struct {
	RequestRecord       requestlog.RequestRecord
	AttemptRecord       requestlog.AttemptRecord
	Principal           *auth.APIKeyPrincipal
	Authorization       ChatAuthorization
	ResponseProtocol    requestlog.Protocol
	ResponseID          string
	ResponseModelID     string
	ResponseStartedAt   *time.Time
	RequestFinalStatus  requestlog.RequestStatus
	AttemptFinalStatus  requestlog.AttemptStatus
	ErrorCode           string
	ErrorMessage        string
	InternalErrorDetail string
	ModelDBID           int64
	FinalProviderID     int64
	FinalChannelID      int64
	// ChannelPriceID 是中标候选在路由/授权时锁定的 channel_prices 绝对成本覆盖行 ID（P1-3 / DEC-027）。
	// settlement 优先按此 ID 取价计费，降低「授权后管理员停用/改窗口」导致结算价漂移到另一行的竞态。
	// 0 表示无覆盖（走倍率路径，见下三个 pin）或未透传（旧数据，此时回退按 attemptStart 重查）。
	ChannelPriceID int64
	// CostBaseModelPriceID/ChannelCostMultiplierID/ChannelRechargeFactorID 是倍率路径的成本来源 pin（DEC-031）：
	// 路由/授权锁定的成本基数行（model_prices）+ 价格倍率行 + 充值倍率行 id，透传至此。settlement 按这些不可改行
	// 确定性重算成本，防「授权后改倍率」漂移。DEC-031：成本基数复用 model_prices，故 CostBaseModelPriceID == 售价侧
	// ModelPriceID（同一基准价行）。覆盖路径下三者为 0；充值倍率未配时 ChannelRechargeFactorID=0（按 1.0）。
	CostBaseModelPriceID    int64
	ChannelCostMultiplierID int64
	ChannelRechargeFactorID int64
	// SalePrice 是客户最终售价快照 = 模型基准价 × 线路倍率（DEC-026），路由时算好并透传到结算；
	// 同一请求所有候选共享、不随命中哪条渠道变。此处为短上下文牌价；若 LongContextPolicy 触发则结算前再缩放。
	SalePrice billing.CustomerPriceSnapshot
	// PriceRatio 是算 SalePrice 用的线路倍率（routes.price_ratio），随 SalePrice 一起快照进 price_snapshots，
	// 供请求详情/列表恒显示结算当时的倍率与倒推基准价（不随后续改倍率漂移）。
	PriceRatio pgtype.Numeric
	// LongContextPolicy 来自售价所用 model_prices 窗口；结算按真实 usage 输入合计决定是否放大售价与成本。
	LongContextPolicy billing.LongContextPolicy
	Facts             adapter.ResponseFacts
}

// settlementCostPins 是结算成本来源 pin（DEC-031）：路由/授权锁定的行 id，透传到结算/恢复。
type settlementCostPins struct {
	ChannelPriceID          int64 // 覆盖路径 >0
	CostBaseModelPriceID    int64 // 倍率路径 >0（成本基数 = model_prices.id，DEC-031）
	ChannelCostMultiplierID int64 // 倍率路径 >0
	ChannelRechargeFactorID int64 // 倍率路径且已配 >0（未配按 1.0）
}

// resolvedSettlementCost 是结算解析出的真实成本快照 + 来源事实（供写 cost_snapshots / recovery job）。
type resolvedSettlementCost struct {
	// snapshot 是真实成本单价快照：必填分项已归一；可选分项可为 NULL（计费时回退基价）。
	snapshot billing.ProviderCostSnapshot
	// 覆盖路径：channelPriceID>0，其余为 0 / 无效。倍率路径：三个来源 id + 两个标量置位。
	channelPriceID          int64
	costBaseModelPriceID    int64
	channelCostMultiplierID int64
	costMultiplier          pgtype.Numeric
	channelRechargeFactorID int64
	rechargeFactor          pgtype.Numeric
}

// resolveSettlementCost 解析 settlement 计费应使用的真实成本（DEC-027 倍率 + DEC-031 单基数），优先级：
//  1. 绝对覆盖 pin（channelPriceID）：channel_prices 金额不可变，按 pin 行取值，不受改价竞态影响。
//  2. 倍率 pin（成本基数 model_prices 行 × 价格倍率行 × 充值倍率行）：各行金额/倍率不可变，按 pin 行确定性重算，防改倍率漂移。
//  3. 回退（旧数据 / 缺 pin）：按 attemptStart 时点重查 active 覆盖，无则重查 基准价 × 价格倍率 × 充值倍率。
//
// 与 P1-3 同构：pin 行不可改 ⇒ 同 id ⇒ 同结果。任何一步命中即返回；三者皆无 → 未定价，报 settlement failed。
func resolveSettlementCost(
	ctx context.Context,
	queries *sqlc.Queries,
	channelID int64,
	modelID int64,
	pins settlementCostPins,
	atTime time.Time,
) (resolvedSettlementCost, error) {
	// 1. 覆盖 pin。
	if pins.ChannelPriceID > 0 {
		pinned, err := queries.GetChannelPrice(ctx, pins.ChannelPriceID)
		switch {
		case err == nil:
			if pinned.ChannelID == channelID && pinned.ModelID == modelID {
				return overrideResolvedCost(pinned), nil
			}
		case errors.Is(err, pgx.ErrNoRows):
			// 行已不存在（极少见）：回退。
		default:
			return resolvedSettlementCost{}, failure.Wrap(
				failure.CodeGatewayChatSettlementFailed, err,
				failure.WithMessage("load pinned channel price for chat settlement"),
			)
		}
	}

	// 2. 倍率 pin。
	if pins.CostBaseModelPriceID > 0 && pins.ChannelCostMultiplierID > 0 {
		resolved, ok, err := resolvePinnedMultiplierCost(ctx, queries, channelID, modelID, pins)
		if err != nil {
			return resolvedSettlementCost{}, err
		}
		if ok {
			return resolved, nil
		}
		// pin 行缺失（极少见）：回退。
	}

	// 3. 回退：按 attemptStart 重查（覆盖优先，否则参考成本 × 倍率）。
	return resolveActiveSettlementCost(ctx, queries, channelID, modelID, atTime)
}

// overrideResolvedCost 从绝对成本覆盖行构造解析结果（沿用 channelPriceCostSnapshot 的 numericOrZero 归一）。
func overrideResolvedCost(price sqlc.ChannelPrice) resolvedSettlementCost {
	return resolvedSettlementCost{
		snapshot:       channelPriceCostSnapshot(price),
		channelPriceID: price.ID,
	}
}

// resolvePinnedMultiplierCost 按 pin 行取成本基数（model_prices）+ 价格倍率 + 充值倍率并算真实成本；pin 行缺失返回 ok=false 供回退。
// DEC-031：成本基数复用 model_prices（与售价同源），不再走独立参考成本表。
func resolvePinnedMultiplierCost(
	ctx context.Context,
	queries *sqlc.Queries,
	channelID, modelID int64,
	pins settlementCostPins,
) (resolvedSettlementCost, bool, error) {
	base, err := queries.GetModelPrice(ctx, pins.CostBaseModelPriceID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return resolvedSettlementCost{}, false, nil
		}
		return resolvedSettlementCost{}, false, failure.Wrap(failure.CodeGatewayChatSettlementFailed, err, failure.WithMessage("load pinned cost base model price"))
	}
	mult, err := queries.GetChannelCostMultiplier(ctx, pins.ChannelCostMultiplierID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return resolvedSettlementCost{}, false, nil
		}
		return resolvedSettlementCost{}, false, failure.Wrap(failure.CodeGatewayChatSettlementFailed, err, failure.WithMessage("load pinned channel cost multiplier"))
	}
	// 防脏 id 串价：成本基数须属本 model，价格倍率须属本 channel 且（默认 或 本 model 覆盖）。
	if base.ModelID != modelID || mult.ChannelID != channelID || (mult.ModelID.Valid && mult.ModelID.Int64 != modelID) {
		return resolvedSettlementCost{}, false, nil
	}

	rechargeFactor := oneNumeric()
	rechargeFactorID := int64(0)
	if pins.ChannelRechargeFactorID > 0 {
		crf, err := queries.GetChannelRechargeFactor(ctx, pins.ChannelRechargeFactorID)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return resolvedSettlementCost{}, false, nil
			}
			return resolvedSettlementCost{}, false, failure.Wrap(failure.CodeGatewayChatSettlementFailed, err, failure.WithMessage("load pinned channel recharge factor"))
		}
		if crf.ChannelID != channelID {
			return resolvedSettlementCost{}, false, nil
		}
		rechargeFactor = crf.Factor
		rechargeFactorID = crf.ID
	}

	snapshot, err := scaledMultiplierCostSnapshot(base, mult.Multiplier, rechargeFactor)
	if err != nil {
		return resolvedSettlementCost{}, false, err
	}
	return resolvedSettlementCost{
		snapshot:                snapshot,
		costBaseModelPriceID:    base.ID,
		channelCostMultiplierID: mult.ID,
		costMultiplier:          mult.Multiplier,
		channelRechargeFactorID: rechargeFactorID,
		rechargeFactor:          rechargeFactor,
	}, true, nil
}

// resolveActiveSettlementCost 按 attemptStart 时点重查成本（回退路径）：覆盖优先，否则基准价 × 价格倍率 × 充值倍率。
// DEC-031：成本基数复用 model_prices（FindActiveModelPrice，与售价解析同源），不再走独立参考成本表。
func resolveActiveSettlementCost(
	ctx context.Context,
	queries *sqlc.Queries,
	channelID, modelID int64,
	atTime time.Time,
) (resolvedSettlementCost, error) {
	at := pgtype.Timestamptz{Time: atTime, Valid: true}

	price, err := queries.FindActiveChannelPrice(ctx, sqlc.FindActiveChannelPriceParams{
		ChannelID: channelID, ModelID: modelID, AtTime: at,
	})
	switch {
	case err == nil:
		return overrideResolvedCost(price), nil
	case !errors.Is(err, pgx.ErrNoRows):
		return resolvedSettlementCost{}, failure.Wrap(failure.CodeGatewayChatSettlementFailed, err, failure.WithMessage("find active channel price for chat settlement"))
	}

	base, err := queries.FindActiveModelPrice(ctx, sqlc.FindActiveModelPriceParams{
		ModelID: modelID, AtTime: at,
	})
	if err != nil {
		return resolvedSettlementCost{}, failure.Wrap(failure.CodeGatewayChatSettlementFailed, err, failure.WithMessage("find active cost base model price for chat settlement"))
	}
	mult, err := queries.FindActiveChannelCostMultiplier(ctx, sqlc.FindActiveChannelCostMultiplierParams{
		ChannelID: channelID, ModelID: pgtype.Int8{Int64: modelID, Valid: true}, AtTime: at,
	})
	if err != nil {
		return resolvedSettlementCost{}, failure.Wrap(failure.CodeGatewayChatSettlementFailed, err, failure.WithMessage("find active channel cost multiplier for chat settlement"))
	}

	rechargeFactor := oneNumeric()
	rechargeFactorID := int64(0)
	crf, err := queries.FindActiveChannelRechargeFactor(ctx, sqlc.FindActiveChannelRechargeFactorParams{
		ChannelID: channelID, AtTime: at,
	})
	switch {
	case err == nil:
		rechargeFactor = crf.Factor
		rechargeFactorID = crf.ID
	case !errors.Is(err, pgx.ErrNoRows):
		return resolvedSettlementCost{}, failure.Wrap(failure.CodeGatewayChatSettlementFailed, err, failure.WithMessage("find active channel recharge factor for chat settlement"))
	}

	snapshot, err := scaledMultiplierCostSnapshot(base, mult.Multiplier, rechargeFactor)
	if err != nil {
		return resolvedSettlementCost{}, err
	}
	return resolvedSettlementCost{
		snapshot:                snapshot,
		costBaseModelPriceID:    base.ID,
		channelCostMultiplierID: mult.ID,
		costMultiplier:          mult.Multiplier,
		channelRechargeFactorID: rechargeFactorID,
		rechargeFactor:          rechargeFactor,
	}, nil
}

// scaledMultiplierCostSnapshot 基准价（model_prices）× 价格倍率 × 充值倍率 → 真实成本单价。
// DEC-031：成本基数复用 model_prices，经 billing.ModelPriceToProviderCost 映射为成本向量（*_price → *_cost，1:1）。
// 必填分项（uncached/output）空值归一为 0；可选分项保留 NULL，供 CalculateProviderCost 回退到基价
// （cache_* → uncached，reasoning → output）。切勿把可选分项写成 0，否则会变成「显式免费」少计成本。
func scaledMultiplierCostSnapshot(base sqlc.ModelPrice, priceMultiplier, rechargeFactor pgtype.Numeric) (billing.ProviderCostSnapshot, error) {
	scaled, err := billing.ScaleProviderCostByFactors(costBaseSnapshot(base), priceMultiplier, rechargeFactor)
	if err != nil {
		return billing.ProviderCostSnapshot{}, failure.Wrap(
			failure.CodeGatewayChatSettlementFailed, err,
			failure.WithMessage("scale provider cost by channel multiplier and recharge factor"),
		)
	}
	return normalizeCostSnapshotRequiredRates(scaled), nil
}

// costBaseSnapshot 把模型基准价行映射成 ProviderCostSnapshot（保留 NULL 分项，供缩放后再归一）。
// DEC-031：基准价 = 成本基数，*_price 列一一对应 *_cost（复用 billing.ModelPriceToProviderCost 保持单一映射）。
func costBaseSnapshot(base sqlc.ModelPrice) billing.ProviderCostSnapshot {
	return billing.ModelPriceToProviderCost(billing.CustomerPriceSnapshot{
		Currency:                base.Currency,
		PricingUnit:             base.PricingUnit,
		UncachedInputPrice:      base.UncachedInputPrice,
		CacheReadInputPrice:     base.CacheReadInputPrice,
		CacheWrite5mInputPrice:  base.CacheWrite5mInputPrice,
		CacheWrite1hInputPrice:  base.CacheWrite1hInputPrice,
		CacheWrite30mInputPrice: base.CacheWrite30mInputPrice,
		OutputPrice:             base.OutputPrice,
		ReasoningOutputPrice:    base.ReasoningOutputPrice,
		FormulaVersion:          billing.FormulaVersionV1,
	})
}

// normalizeCostSnapshotRequiredRates 仅归一必填成本单价（uncached/output）；可选分项保持 NULL 以启用回退计价。
func normalizeCostSnapshotRequiredRates(c billing.ProviderCostSnapshot) billing.ProviderCostSnapshot {
	c.UncachedInputCost = numericOrZero(c.UncachedInputCost)
	c.OutputCost = numericOrZero(c.OutputCost)
	return c
}

// oneNumeric 返回 1.0 的 NUMERIC（充值倍率未配置时的缺省，名义即真实）。
func oneNumeric() pgtype.Numeric {
	return pgtype.Numeric{Int: big.NewInt(1), Exp: 0, Valid: true}
}

// ValidateChatSettlementFacts 校验 adapter 交给 settlement 的不可变事实。
// recovery job 创建与正式 settlement 共用；导出供 chatcompletions recovery 在 Step 3 迁移前调用。
func ValidateChatSettlementFacts(params ChatSettlementParams) error {
	facts := params.Facts
	if !facts.UsageSource.Valid() {
		return failure.New(
			failure.CodeGatewayChatSettlementFailed,
			failure.WithMessage("chat settlement usage source is invalid"),
			failure.WithField("usage_source", string(facts.UsageSource)),
		)
	}
	if !facts.Usage.Valid() {
		return failure.New(
			failure.CodeGatewayChatSettlementFailed,
			failure.WithMessage("chat settlement usage facts are invalid"),
		)
	}
	if params.ResponseProtocol == "" || params.ResponseID == "" ||
		facts.UpstreamProtocol == "" || facts.UpstreamResponseID == "" ||
		facts.UpstreamModel == "" || facts.UsageMappingVersion == "" ||
		facts.Metadata.StatusCode < 100 || facts.Metadata.StatusCode > 599 {
		return failure.New(
			failure.CodeGatewayChatSettlementFailed,
			failure.WithMessage("chat settlement response facts are incomplete"),
		)
	}
	if !validSettlementFinishClass(facts.Finish.Class) {
		return failure.New(
			failure.CodeGatewayChatSettlementFailed,
			failure.WithMessage("chat settlement finish class is invalid"),
			failure.WithField("finish_class", string(facts.Finish.Class)),
		)
	}

	return nil
}

func validSettlementFinishClass(class adapter.FinishClass) bool {
	switch class {
	case adapter.FinishStop,
		adapter.FinishLength,
		adapter.FinishToolUse,
		adapter.FinishContentFilter,
		adapter.FinishRefusal,
		adapter.FinishPause,
		adapter.FinishOther:
		return true
	default:
		return false
	}
}

// createSettlementUsageRecord 保存协议无关 usage facts。
func createSettlementUsageRecord(ctx context.Context, queries *sqlc.Queries, requestRecordID int64, facts adapter.ResponseFacts) (sqlc.UsageRecord, error) {
	u := facts.Usage
	return queries.CreateUsageRecord(ctx, sqlc.CreateUsageRecordParams{
		RequestRecordID:               requestRecordID,
		UncachedInputTokens:           u.UncachedInputTokens.Value,
		UncachedInputTokensState:      string(u.UncachedInputTokens.State),
		CacheReadInputTokens:          u.CacheReadInputTokens.Value,
		CacheReadInputTokensState:     string(u.CacheReadInputTokens.State),
		CacheWrite5mInputTokens:       u.CacheWrite5mInputTokens.Value,
		CacheWrite5mInputTokensState:  string(u.CacheWrite5mInputTokens.State),
		CacheWrite1hInputTokens:       u.CacheWrite1hInputTokens.Value,
		CacheWrite1hInputTokensState:  string(u.CacheWrite1hInputTokens.State),
		CacheWrite30mInputTokens:      u.CacheWrite30mInputTokens.Value,
		CacheWrite30mInputTokensState: string(u.CacheWrite30mInputTokens.State),
		OutputTokensTotal:             u.OutputTokensTotal.Value,
		OutputTokensTotalState:        string(u.OutputTokensTotal.State),
		ReasoningOutputTokens:         u.ReasoningOutputTokens.Value,
		ReasoningOutputTokensState:    string(u.ReasoningOutputTokens.State),
		UsageSource:                   string(facts.UsageSource),
		UsageMappingVersion:           facts.UsageMappingVersion,
	})
}

// createSettlementUsageLineItems 保存已登记的附加计量事实。
func createSettlementUsageLineItems(ctx context.Context, queries *sqlc.Queries, usageRecordID int64, items []usage.MeteredItem) error {
	for _, item := range items {
		if _, err := queries.CreateUsageLineItem(ctx, sqlc.CreateUsageLineItemParams{
			UsageRecordID: usageRecordID,
			Kind:          string(item.Kind),
			Quantity:      item.Quantity,
		}); err != nil {
			return failure.Wrap(
				failure.CodeGatewayChatSettlementFailed,
				err,
				failure.WithMessage("create usage line item"),
			)
		}
	}

	return nil
}

// UpstreamRequestIDPtr 把上游 request id 字符串转成可选指针，空串视为上游未提供。
func UpstreamRequestIDPtr(requestID string) *string {
	if requestID == "" {
		return nil
	}

	return &requestID
}

// injectedSettlementFailure 仅用于本地账单 E2E 故障注入。
//
// P2-6：故障注入开关只在以 `-tags billing_e2e` 构建的二进制里读取 env，生产构建恒为 false，
// 杜绝「生产误设 env 导致每次结算都失败」。"always" 让每次 raw settlement 失败，用于驱动
// recovery 重试耗尽 → dead → 风险敞口收口（REC-02）。"once" 在 recoverable 包裹器层处理（REC-01）。
func injectedSettlementFailure() error {
	if faultInjectSettlementAlways() {
		return failure.New(
			failure.CodeGatewayChatSettlementFailed,
			failure.WithMessage("billing e2e injected settlement failure (always)"),
		)
	}
	return nil
}

// SettleSuccessfulChat 对一次成功的 chat 请求执行结算。
func (s *ChatSettlementService) SettleSuccessfulChat(ctx context.Context, params ChatSettlementParams) error {
	if err := injectedSettlementFailure(); err != nil {
		return err
	}

	tx, err := s.db.Begin(ctx)
	if err != nil {
		return failure.Wrap(
			failure.CodeGatewayChatSettlementFailed,
			err,
			failure.WithMessage("begin chat settlement transaction"),
		)
	}
	defer func() {
		_ = tx.Rollback(ctx)
	}()

	now := time.Now()
	txQueries := s.queries.WithTx(tx)

	lockedRequest, err := txQueries.GetRequestRecordForUpdate(ctx, params.RequestRecord.ID)
	if err != nil {
		return failure.Wrap(
			failure.CodeGatewayChatSettlementFailed,
			err,
			failure.WithMessage("lock request record for chat settlement"),
		)
	}

	if err := ValidateChatSettlementFacts(params); err != nil {
		return err
	}

	switch requestlog.RequestStatus(lockedRequest.Status) {
	case requestlog.RequestStatusRunning:
		// running 是唯一允许首次执行 settlement 的状态。

	case requestlog.RequestStatusSucceeded:
		// 已成功的 request 不能再次写 usage/snapshot/ledger。
		// 只有既有结算事实和本次重放参数完全一致，才视为幂等成功。
		if err := s.ensureIdempotentSuccessfulChat(ctx, txQueries, lockedRequest, params); err != nil {
			return err
		}

		if err := tx.Commit(ctx); err != nil {
			return failure.Wrap(
				failure.CodeGatewayChatSettlementFailed,
				err,
				failure.WithMessage("commit idempotent chat settlement replay"),
			)
		}
		return nil

	default:
		return failure.New(
			failure.CodeGatewayChatSettlementFailed,
			failure.WithMessage("request status does not allow chat settlement"),
			failure.WithField("request_status", lockedRequest.Status),
		)
	}

	txRequestLog := requestlog.NewStore(txQueries)
	facts := params.Facts
	requestFinalStatus := params.RequestFinalStatus
	if requestFinalStatus == "" {
		requestFinalStatus = requestlog.RequestStatusSucceeded
	}
	attemptFinalStatus := params.AttemptFinalStatus
	if attemptFinalStatus == "" {
		attemptFinalStatus = requestlog.AttemptStatusSucceeded
	}

	// 从 adapter response metadata 写入真实 upstream status code 和 request id，
	// 用于渠道审计和 observability，而不是固定写 200/NULL。
	attemptSuccessParams := requestlog.MarkAttemptSucceededParams{
		ID:                    params.AttemptRecord.ID,
		UpstreamResponseID:    facts.UpstreamResponseID,
		UpstreamResponseModel: facts.UpstreamModel,
		UpstreamFinishReason:  facts.Finish.RawReason,
		FinishClass:           string(facts.Finish.Class),
		UpstreamStatusCode:    facts.Metadata.StatusCode,
		UpstreamRequestID:     UpstreamRequestIDPtr(facts.Metadata.RequestID),
		ResponseStartedAt:     params.ResponseStartedAt,
		// partial settlement 合成的估算事实不是上游真实 usage：标 final_usage_received=false 作为审计信号。
		FinalUsageReceived:  !facts.UsageSource.IsPartialEstimate(),
		UsageMappingVersion: facts.UsageMappingVersion,
		CompletedAt:         now,
	}
	switch attemptFinalStatus {
	case requestlog.AttemptStatusSucceeded:
		_, err = txRequestLog.MarkAttemptSucceeded(ctx, attemptSuccessParams)
	case requestlog.AttemptStatusFailed:
		_, err = txRequestLog.MarkSettledAttemptFailed(ctx, requestlog.MarkSettledAttemptFailedParams{
			MarkAttemptSucceededParams: attemptSuccessParams,
			ErrorCode:                  params.ErrorCode,
			ErrorMessage:               params.ErrorMessage,
			InternalErrorDetail:        params.InternalErrorDetail,
		})
	case requestlog.AttemptStatusCanceled:
		_, err = txRequestLog.MarkSettledAttemptCanceled(ctx, requestlog.MarkSettledAttemptCanceledParams{
			MarkAttemptSucceededParams: attemptSuccessParams,
			ErrorCode:                  params.ErrorCode,
			ErrorMessage:               params.ErrorMessage,
			InternalErrorDetail:        params.InternalErrorDetail,
		})
	default:
		err = failure.New(
			failure.CodeGatewayChatSettlementFailed,
			failure.WithMessage("unsupported settled attempt final status"),
			failure.WithField("attempt_status", string(attemptFinalStatus)),
		)
	}
	if err != nil {
		return failure.Wrap(
			failure.CodeGatewayChatSettlementFailed,
			err,
			failure.WithMessage("create usage record"),
		)
	}

	usageRecord, err := createSettlementUsageRecord(ctx, txQueries, params.RequestRecord.ID, facts)
	if err != nil {
		return failure.Wrap(
			failure.CodeGatewayChatSettlementFailed,
			err,
			failure.WithMessage("create usage record"),
		)
	}

	if err := createSettlementUsageLineItems(ctx, txQueries, usageRecord.ID, facts.Usage.ServerToolUsage); err != nil {
		return err
	}

	// DEC-027：解析真实成本（绝对覆盖 pin > 倍率 pin > attemptStart 回退），一次取回成本快照 + 来源 pin。
	// 收入不沿用 authorization 锁价；authorization 只负责保守冻结。
	cost, err := resolveSettlementCost(
		ctx,
		txQueries,
		params.FinalChannelID,
		params.ModelDBID,
		settlementCostPins{
			ChannelPriceID:          params.ChannelPriceID,
			CostBaseModelPriceID:    params.CostBaseModelPriceID,
			ChannelCostMultiplierID: params.ChannelCostMultiplierID,
			ChannelRechargeFactorID: params.ChannelRechargeFactorID,
		},
		params.AttemptRecord.StartedAt,
	)
	if err != nil {
		return err
	}

	// 收入快照：客户售价 = 模型基准价 × 线路倍率（DEC-026），由路由透传（params.SalePrice），不随命中哪条渠道变。
	// price_id：覆盖路径指向命中的 channel_prices 行；倍率路径无该行 → 写 NULL（列可空、FK 对 NULL 豁免）。
	// 长上下文：按真实 usage 输入合计判定是否放大售价/成本（与上游 GPT-5.4+ / sub2api 对齐）。
	inputTokenSum := billing.LongContextInputTokenSum(facts.Usage)
	salePrice, longContextApplied, err := billing.ApplyLongContextToCustomerPrice(
		params.SalePrice,
		params.LongContextPolicy,
		inputTokenSum,
	)
	if err != nil {
		return failure.Wrap(
			failure.CodeGatewayChatSettlementFailed,
			err,
			failure.WithMessage("apply long-context multiplier to customer sale price"),
		)
	}
	snapshot, err := txQueries.CreatePriceSnapshot(ctx, sqlc.CreatePriceSnapshotParams{
		RequestRecordID:         params.RequestRecord.ID,
		PriceID:                 nullableInt8(cost.channelPriceID),
		Currency:                salePrice.Currency,
		PricingUnit:             salePrice.PricingUnit,
		UncachedInputPrice:      salePrice.UncachedInputPrice,
		CacheReadInputPrice:     salePrice.CacheReadInputPrice,
		CacheWrite5mInputPrice:  salePrice.CacheWrite5mInputPrice,
		CacheWrite1hInputPrice:  salePrice.CacheWrite1hInputPrice,
		CacheWrite30mInputPrice: salePrice.CacheWrite30mInputPrice,
		OutputPrice:             salePrice.OutputPrice,
		ReasoningOutputPrice:    salePrice.ReasoningOutputPrice,
		FormulaVersion:          billing.FormulaVersionV1,
		PriceRatio:              params.PriceRatio,
		LongContextApplied:      longContextApplied,
	})
	if err != nil {
		return err
	}

	// 计算用户本次请求的花费（按客户售价 = 基准 × 倍率，必要时再 × 长上下文倍率）。
	charge, err := s.billingCalculator.CalculateCustomerCharge(facts.Usage, billing.CustomerPriceSnapshot{
		Currency:                snapshot.Currency,
		PricingUnit:             snapshot.PricingUnit,
		UncachedInputPrice:      snapshot.UncachedInputPrice,
		CacheReadInputPrice:     snapshot.CacheReadInputPrice,
		CacheWrite5mInputPrice:  snapshot.CacheWrite5mInputPrice,
		CacheWrite1hInputPrice:  snapshot.CacheWrite1hInputPrice,
		CacheWrite30mInputPrice: snapshot.CacheWrite30mInputPrice,
		OutputPrice:             snapshot.OutputPrice,
		ReasoningOutputPrice:    snapshot.ReasoningOutputPrice,
		FormulaVersion:          snapshot.FormulaVersion,
	})
	if err != nil {
		return err
	}

	// 成本：解析出的真实成本单价（覆盖值 或 参考价×价格倍率×充值倍率）；某分项为空按 0 入账（毛利偏保守）。
	// 长上下文触发时同步放大成本向量（与售价同一策略，避免毛利被虚假抬高）。
	costSnapshot, costLongContextApplied, err := billing.ApplyLongContextToProviderCost(
		cost.snapshot,
		params.LongContextPolicy,
		inputTokenSum,
	)
	if err != nil {
		return failure.Wrap(
			failure.CodeGatewayChatSettlementFailed,
			err,
			failure.WithMessage("apply long-context multiplier to provider cost"),
		)
	}
	if costLongContextApplied != longContextApplied {
		return failure.New(
			failure.CodeGatewayChatSettlementFailed,
			failure.WithMessage("long-context applied mismatch between sale price and provider cost"),
		)
	}
	providerCost, err := s.billingCalculator.CalculateProviderCost(facts.Usage, costSnapshot)
	if err != nil {
		return failure.Wrap(
			failure.CodeGatewayChatSettlementFailed,
			err,
			failure.WithMessage("calculate provider cost for chat settlement"),
		)
	}

	// 写入成本快照：覆盖路径 cost_price_id 置位、倍率列 NULL；倍率路径反之（cost_price_id NULL + 来源 id/标量置位）。
	_, err = txQueries.CreateCostSnapshot(ctx, sqlc.CreateCostSnapshotParams{
		RequestRecordID:              params.RequestRecord.ID,
		CostPriceID:                  nullableInt8(cost.channelPriceID),
		CostBaseModelPriceID:         nullableInt8(cost.costBaseModelPriceID),
		ChannelCostMultiplierID:      nullableInt8(cost.channelCostMultiplierID),
		CostMultiplier:               cost.costMultiplier,
		ChannelRechargeFactorID:      nullableInt8(cost.channelRechargeFactorID),
		RechargeFactor:               cost.rechargeFactor,
		ProviderID:                   params.FinalProviderID,
		ChannelID:                    params.FinalChannelID,
		ModelID:                      params.ModelDBID,
		UpstreamModel:                params.AttemptRecord.UpstreamModel,
		Currency:                     costSnapshot.Currency,
		PricingUnit:                  costSnapshot.PricingUnit,
		UncachedInputCost:            costSnapshot.UncachedInputCost,
		CacheReadInputCost:           costSnapshot.CacheReadInputCost,
		CacheWrite5mInputCost:        costSnapshot.CacheWrite5mInputCost,
		CacheWrite1hInputCost:        costSnapshot.CacheWrite1hInputCost,
		CacheWrite30mInputCost:       costSnapshot.CacheWrite30mInputCost,
		OutputCost:                   costSnapshot.OutputCost,
		ReasoningOutputCost:          costSnapshot.ReasoningOutputCost,
		UncachedInputCostAmount:      providerCost.UncachedInputCostAmount,
		CacheReadInputCostAmount:     providerCost.CacheReadInputCostAmount,
		CacheWrite5mInputCostAmount:  providerCost.CacheWrite5mInputCostAmount,
		CacheWrite1hInputCostAmount:  providerCost.CacheWrite1hInputCostAmount,
		CacheWrite30mInputCostAmount: providerCost.CacheWrite30mInputCostAmount,
		OutputCostAmount:             providerCost.OutputCostAmount,
		ReasoningOutputCostAmount:    providerCost.ReasoningOutputCostAmount,
		TotalCostAmount:              providerCost.TotalCostAmount,
		FormulaVersion:               providerCost.FormulaVersion,
		LongContextApplied:           longContextApplied,
	})
	if err != nil {
		return failure.Wrap(
			failure.CodeGatewayChatSettlementFailed,
			err,
			failure.WithMessage("create chat cost snapshot"),
		)
	}

	reservationID := params.Authorization.ReservationID

	// ledger_entries.amount 要求大于 0；零金额请求保留 usage 和 price snapshot，但不写余额流水。
	if numericIsZero(charge.Amount) {
		_, err := s.ledgerCapturer.ReleaseWithQueries(ctx, txQueries, ledger.ReleaseParams{
			RequestRecordID: params.RequestRecord.ID,
			ReservationID:   &reservationID,
		})
		if err != nil {
			return err
		}
	} else {
		reservation, err := s.ledgerCapturer.CaptureWithQueries(ctx, txQueries, ledger.CaptureParams{
			RequestRecordID: params.RequestRecord.ID,
			ReservationID:   &reservationID,
			ActualAmount:    charge.Amount,
			IdempotencyKey:  fmt.Sprintf("chat:settle:%d", params.RequestRecord.ID),
			Reason:          "chat completion settlement",
		})
		if err != nil {
			return err
		}

		// M7 费用上限计数器：按本次实扣金额累加该 Key 累计花费，同事务提交保证与扣费一致；
		// 只在首次结算（running→succeeded）执行，幂等重放走上面的 succeeded 分支不会重复累加。
		if err := txQueries.AddAPIKeySpentTotal(ctx, sqlc.AddAPIKeySpentTotalParams{
			Amount: reservation.CapturedAmount,
			ID:     params.RequestRecord.APIKeyID,
		}); err != nil {
			return failure.Wrap(
				failure.CodeGatewayChatSettlementFailed,
				err,
				failure.WithMessage("add api key spent total"),
			)
		}

		// 真实费用超出冻结额度时的超额二次补扣同样是用户真实承担的花费，一并计入费用上限累计口径。
		if !numericIsZero(reservation.OverageCapturedAmount) {
			if err := txQueries.AddAPIKeySpentTotal(ctx, sqlc.AddAPIKeySpentTotalParams{
				Amount: reservation.OverageCapturedAmount,
				ID:     params.RequestRecord.APIKeyID,
			}); err != nil {
				return failure.Wrap(
					failure.CodeGatewayChatSettlementFailed,
					err,
					failure.WithMessage("add api key overage spent total"),
				)
			}
		}
	}

	requestSuccessParams := requestlog.MarkRequestSucceededParams{
		ID:                params.RequestRecord.ID,
		ResponseModelID:   params.ResponseModelID,
		ResponseProtocol:  params.ResponseProtocol,
		ResponseID:        params.ResponseID,
		FinalProviderID:   params.FinalProviderID,
		FinalChannelID:    params.FinalChannelID,
		ResponseStartedAt: params.ResponseStartedAt,
		CompletedAt:       now,
	}
	switch requestFinalStatus {
	case requestlog.RequestStatusSucceeded:
		_, err = txRequestLog.MarkRequestSucceeded(ctx, requestSuccessParams)
	case requestlog.RequestStatusFailed:
		_, err = txRequestLog.MarkSettledRequestFailed(ctx, requestlog.MarkSettledRequestFailedParams{
			MarkRequestSucceededParams: requestSuccessParams,
			ErrorCode:                  params.ErrorCode,
			ErrorMessage:               params.ErrorMessage,
			InternalErrorDetail:        params.InternalErrorDetail,
		})
	case requestlog.RequestStatusCanceled:
		_, err = txRequestLog.MarkSettledRequestCanceled(ctx, requestlog.MarkSettledRequestCanceledParams{
			MarkRequestSucceededParams: requestSuccessParams,
			ErrorCode:                  params.ErrorCode,
			ErrorMessage:               params.ErrorMessage,
			InternalErrorDetail:        params.InternalErrorDetail,
		})
	default:
		err = failure.New(
			failure.CodeGatewayChatSettlementFailed,
			failure.WithMessage("unsupported settled request final status"),
			failure.WithField("request_status", string(requestFinalStatus)),
		)
	}
	if err != nil {
		return err
	}

	if err := tx.Commit(ctx); err != nil {
		return failure.Wrap(
			failure.CodeGatewayChatSettlementFailed,
			err,
			failure.WithMessage("commit chat settlement transaction"),
		)
	}

	return nil
}

// FinalizeDeadChatSettlement 收口一条「补偿任务已 dead、但请求仍停留在 running」的资金/状态残留。
//
// settlement 永久失败、补偿任务耗尽自动重试后会进入 dead，但此前没有任何路径把请求从 running 推进到
// 终态，也没有释放冻结余额——请求会永远显示「进行中」，用户余额也被永久冻结。本方法在单事务内：
//  1. 锁请求记录，仅当其仍为 running 才继续（幂等闸门：已被正常结算或其他路径收口则直接返回）；
//  2. 释放冻结余额并记平台风险敞口异常（与 stream_settlement_failed_after_upstream_success 同语义：
//     上游可能已产生成本但无可靠结算，平台承担、不向用户扣费）；
//  3. 把请求原子推进到 failed。
//
// 以「请求仍为 running」为闸门，崩溃后由 worker 下个 tick 安全重放。
func (s *ChatSettlementService) FinalizeDeadChatSettlement(ctx context.Context, job sqlc.SettlementRecoveryJob) error {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return failure.Wrap(
			failure.CodeGatewayChatSettlementFailed,
			err,
			failure.WithMessage("begin dead chat settlement finalize transaction"),
		)
	}
	defer func() {
		_ = tx.Rollback(ctx)
	}()

	txQueries := s.queries.WithTx(tx)

	lockedRequest, err := txQueries.GetRequestRecordForUpdate(ctx, job.RequestRecordID)
	if err != nil {
		return failure.Wrap(
			failure.CodeGatewayChatSettlementFailed,
			err,
			failure.WithMessage("lock request record for dead chat settlement finalize"),
		)
	}

	// 幂等闸门：只有仍停留在 running 的请求才需要收口；其余终态说明已被正常结算 / 其他路径处理。
	if requestlog.RequestStatus(lockedRequest.Status) != requestlog.RequestStatusRunning {
		return nil
	}

	if err := s.releaseDeadSettlementReservation(ctx, txQueries, job); err != nil {
		return err
	}

	txRequestLog := requestlog.NewStore(txQueries)
	_, err = txRequestLog.MarkRequestFailed(ctx, requestlog.MarkRequestFailedParams{
		ID:                  job.RequestRecordID,
		ErrorCode:           string(failure.CodeGatewayChatSettlementFailed),
		ErrorMessage:        BaseSafeRequestLogErrorMessage(string(failure.CodeGatewayChatSettlementFailed)),
		InternalErrorDetail: "settlement recovery job exhausted retries; frozen balance released and request finalized as failed",
		CompletedAt:         time.Now(),
	})
	if err != nil {
		return failure.Wrap(
			failure.CodeGatewayChatSettlementFailed,
			err,
			failure.WithMessage("mark request failed for dead chat settlement finalize"),
		)
	}

	if err := tx.Commit(ctx); err != nil {
		return failure.Wrap(
			failure.CodeGatewayChatSettlementFailed,
			err,
			failure.WithMessage("commit dead chat settlement finalize transaction"),
		)
	}

	return nil
}

// releaseDeadSettlementReservation 在 finalize 事务内释放冻结余额并记平台风险敞口异常。
//
// reservation 已 released 时 ReleaseWithQueries 幂等返回；CreateLedgerRiskExposureException 按
// reservation_id ON CONFLICT 幂等，故整体可安全重放。reservation 缺失（理论不该发生：补偿任务必有
// reservation）时不阻断请求收口。reservation 已 captured 在本路径不可能出现——capture 与
// MarkRequestSucceeded 同事务提交，请求若 captured 则不会是 running（已被上层闸门拦下）。
func (s *ChatSettlementService) releaseDeadSettlementReservation(ctx context.Context, txQueries *sqlc.Queries, job sqlc.SettlementRecoveryJob) error {
	reservationID := job.ReservationID
	released, err := s.ledgerCapturer.ReleaseWithQueries(ctx, txQueries, ledger.ReleaseParams{
		RequestRecordID: job.RequestRecordID,
		ReservationID:   &reservationID,
	})
	if err != nil {
		if failure.CodeOf(err) == failure.CodeLedgerReservationNotFound {
			return nil
		}
		return err
	}

	_, err = txQueries.CreateLedgerRiskExposureException(ctx, sqlc.CreateLedgerRiskExposureExceptionParams{
		UserID:          released.UserID,
		RequestRecordID: released.RequestRecordID,
		ReservationID:   released.ID,
		PlatformAmount:  released.AuthorizedAmount,
		Currency:        released.Currency,
		ReasonCode:      "settlement_recovery_exhausted",
		Reason:          "settlement recovery job exhausted retries after upstream success without reliable settlement",
	})
	if err != nil {
		return failure.Wrap(
			failure.CodeGatewayChatSettlementFailed,
			err,
			failure.WithMessage("record risk exposure for dead chat settlement finalize"),
		)
	}

	return nil
}

// FinalizeOrphanReservation 收口一条「进程崩溃遗留的孤儿预授权」：请求永久停留 running、冻结余额永不释放。
//
// 这类残留发生在 gateway 在 PreAuthorize 之后、settlement/补偿任务建立之前崩溃：既没有正常结算路径，
// 也没有 settlement_recovery_job 兜底（调用方查询已用 NOT EXISTS 排除有补偿任务者，与该 worker 严格互补）。
// 本方法在单事务内：
//  1. 锁请求记录，仅当其仍为 running 才继续（幂等闸门：已被其他路径收口则直接返回）；
//  2. 释放冻结余额（用户不扣费），并记一条 risk_exposure 异常作为「可能已产生上游成本」的上界敞口，便于观测追查；
//  3. 把请求原子推进到 failed。
//
// 以「请求仍为 running」为闸门，崩溃后下个 tick 安全重放；多 worker 并发由请求记录行锁串行化。
func (s *ChatSettlementService) FinalizeOrphanReservation(ctx context.Context, reservation sqlc.LedgerReservation) error {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return failure.Wrap(
			failure.CodeGatewayRequestOrphanReclaimed,
			err,
			failure.WithMessage("begin orphan reservation finalize transaction"),
		)
	}
	defer func() {
		_ = tx.Rollback(ctx)
	}()

	txQueries := s.queries.WithTx(tx)

	lockedRequest, err := txQueries.GetRequestRecordForUpdate(ctx, reservation.RequestRecordID)
	if err != nil {
		return failure.Wrap(
			failure.CodeGatewayRequestOrphanReclaimed,
			err,
			failure.WithMessage("lock request record for orphan reservation finalize"),
		)
	}

	// 幂等闸门：只有仍停留在 running 的请求才需要收口；其余终态说明已被其他路径处理。
	if requestlog.RequestStatus(lockedRequest.Status) != requestlog.RequestStatusRunning {
		return nil
	}

	reservationID := reservation.ID
	released, err := s.ledgerCapturer.ReleaseWithQueries(ctx, txQueries, ledger.ReleaseParams{
		RequestRecordID: reservation.RequestRecordID,
		ReservationID:   &reservationID,
	})
	if err != nil {
		// 已被释放/不存在则视作已收口，幂等返回；其余（如已 captured 冲突）上抛由 worker 重试或告警。
		if failure.CodeOf(err) == failure.CodeLedgerReservationNotFound {
			return nil
		}
		return failure.Wrap(
			failure.CodeGatewayRequestOrphanReclaimed,
			err,
			failure.WithMessage("release orphan reservation"),
		)
	}

	_, err = txQueries.CreateLedgerRiskExposureException(ctx, sqlc.CreateLedgerRiskExposureExceptionParams{
		UserID:          released.UserID,
		RequestRecordID: released.RequestRecordID,
		ReservationID:   released.ID,
		PlatformAmount:  released.AuthorizedAmount,
		Currency:        released.Currency,
		ReasonCode:      "orphan_reservation_swept",
		Reason:          "process crash left an authorized reservation with a stuck running request; frozen balance released and request finalized as failed",
	})
	if err != nil {
		return failure.Wrap(
			failure.CodeGatewayRequestOrphanReclaimed,
			err,
			failure.WithMessage("record risk exposure for orphan reservation finalize"),
		)
	}

	txRequestLog := requestlog.NewStore(txQueries)
	_, err = txRequestLog.MarkRequestFailed(ctx, requestlog.MarkRequestFailedParams{
		ID:                  reservation.RequestRecordID,
		ErrorCode:           string(failure.CodeGatewayRequestOrphanReclaimed),
		ErrorMessage:        BaseSafeRequestLogErrorMessage(string(failure.CodeGatewayRequestOrphanReclaimed)),
		InternalErrorDetail: "orphan authorized reservation swept by worker; frozen balance released and request finalized as failed",
		CompletedAt:         time.Now(),
	})
	if err != nil {
		return failure.Wrap(
			failure.CodeGatewayRequestOrphanReclaimed,
			err,
			failure.WithMessage("mark request failed for orphan reservation finalize"),
		)
	}

	if err := tx.Commit(ctx); err != nil {
		return failure.Wrap(
			failure.CodeGatewayRequestOrphanReclaimed,
			err,
			failure.WithMessage("commit orphan reservation finalize transaction"),
		)
	}

	return nil
}

// ensureIdempotentSuccessfulChat 校验重复 settlement 是否等价于第一次成功结算。
func (s *ChatSettlementService) ensureIdempotentSuccessfulChat(ctx context.Context, queries *sqlc.Queries, request sqlc.RequestRecord, params ChatSettlementParams) error {
	if err := ensureSettlementRequestMatches(request, params); err != nil {
		return err
	}

	if err := ensureSettlementAttemptMatches(ctx, queries, params); err != nil {
		return err
	}

	usageRecord, err := queries.GetUsageRecordByRequest(ctx, request.ID)
	if err != nil {
		return failure.Wrap(
			failure.CodeGatewayChatSettlementFailed,
			err,
			failure.WithMessage("lookup idempotent chat settlement usage"),
		)
	}

	if err := ensureSettlementUsageMatches(usageRecord, params.Facts); err != nil {
		return err
	}

	lineItems, err := queries.ListUsageLineItemsByUsageRecord(ctx, usageRecord.ID)
	if err != nil {
		return failure.Wrap(
			failure.CodeGatewayChatSettlementFailed,
			err,
			failure.WithMessage("lookup idempotent chat settlement usage line items"),
		)
	}
	if err := ensureSettlementUsageLineItemsMatch(lineItems, params.Facts.Usage.ServerToolUsage); err != nil {
		return err
	}

	// 阶段 15：收入快照来自结算时命中渠道的售价（非 authorization 锁价），幂等校验改为
	// 「按存储的 price_snapshot 重算费用并与 ledger 实扣金额比对」，price_snapshot 本身即为权威事实。
	snapshot, err := queries.GetPriceSnapshotByRequest(ctx, request.ID)
	if err != nil {
		return failure.Wrap(
			failure.CodeGatewayChatSettlementFailed,
			err,
			failure.WithMessage("lookup idempotent chat settlement price snapshot"),
		)
	}

	billingUsage := settlementUsageFactsFromRecord(usageRecord, lineItems)

	charge, err := s.billingCalculator.CalculateCustomerCharge(billingUsage, billing.CustomerPriceSnapshot{
		Currency:                snapshot.Currency,
		PricingUnit:             snapshot.PricingUnit,
		UncachedInputPrice:      snapshot.UncachedInputPrice,
		CacheReadInputPrice:     snapshot.CacheReadInputPrice,
		CacheWrite5mInputPrice:  snapshot.CacheWrite5mInputPrice,
		CacheWrite1hInputPrice:  snapshot.CacheWrite1hInputPrice,
		CacheWrite30mInputPrice: snapshot.CacheWrite30mInputPrice,
		OutputPrice:             snapshot.OutputPrice,
		ReasoningOutputPrice:    snapshot.ReasoningOutputPrice,
		FormulaVersion:          snapshot.FormulaVersion,
	})
	if err != nil {
		return failure.Wrap(
			failure.CodeGatewayChatSettlementFailed,
			err,
			failure.WithMessage("calculate idempotent chat settlement amount"),
		)
	}

	costSnapshot, err := queries.GetCostSnapshotByRequest(ctx, request.ID)
	if err != nil {
		return failure.Wrap(
			failure.CodeGatewayChatSettlementFailed,
			err,
			failure.WithMessage("lookup idempotent chat settlement cost snapshot"),
		)
	}

	providerCost, err := s.billingCalculator.CalculateProviderCost(billingUsage, billing.ProviderCostSnapshot{
		Currency:               costSnapshot.Currency,
		PricingUnit:            costSnapshot.PricingUnit,
		UncachedInputCost:      costSnapshot.UncachedInputCost,
		CacheReadInputCost:     costSnapshot.CacheReadInputCost,
		CacheWrite5mInputCost:  costSnapshot.CacheWrite5mInputCost,
		CacheWrite1hInputCost:  costSnapshot.CacheWrite1hInputCost,
		CacheWrite30mInputCost: costSnapshot.CacheWrite30mInputCost,
		OutputCost:             costSnapshot.OutputCost,
		ReasoningOutputCost:    costSnapshot.ReasoningOutputCost,
		FormulaVersion:         costSnapshot.FormulaVersion,
	})
	if err != nil {
		return failure.Wrap(
			failure.CodeGatewayChatSettlementFailed,
			err,
			failure.WithMessage("calculate idempotent chat settlement provider cost"),
		)
	}

	if err := ensureSettlementCostSnapshotMatches(costSnapshot, params, providerCost); err != nil {
		return err
	}

	reservation, err := queries.GetLedgerReservationByRequestRecordID(ctx, request.ID)
	if err != nil {
		return failure.Wrap(
			failure.CodeGatewayChatSettlementFailed,
			err,
			failure.WithMessage("lookup idempotent chat settlement reservation"),
		)
	}

	if err := ensureSettlementReservationBaseMatches(reservation, request, params.Authorization); err != nil {
		return err
	}

	if numericIsZero(charge.Amount) {
		return ensureSettlementReleasedReservationMatches(ctx, queries, reservation)
	}

	return ensureSettlementCapturedReservationMatches(ctx, queries, reservation, charge.Amount)
}

// ensureSettlementRequestMatches 校验成功 request 终态是否属于本次 settlement 参数。
func ensureSettlementRequestMatches(request sqlc.RequestRecord, params ChatSettlementParams) error {
	if request.ID != params.RequestRecord.ID ||
		request.UserID != params.RequestRecord.UserID ||
		request.ApiKeyID != params.RequestRecord.APIKeyID {
		return ChatSettlementIdempotencyConflict("request identity mismatch")
	}
	if !requiredTextMatches(request.ResponseModelID, params.ResponseModelID) {
		return ChatSettlementIdempotencyConflict("response model mismatch")
	}
	if !requiredTextMatches(request.ResponseProtocol, string(params.ResponseProtocol)) {
		return ChatSettlementIdempotencyConflict("response protocol mismatch")
	}
	if !requiredTextMatches(request.ResponseID, params.ResponseID) {
		return ChatSettlementIdempotencyConflict("response id mismatch")
	}
	if !requiredInt8Matches(request.FinalProviderID, params.FinalProviderID) {
		return ChatSettlementIdempotencyConflict("final provider mismatch")
	}
	if !requiredInt8Matches(request.FinalChannelID, params.FinalChannelID) {
		return ChatSettlementIdempotencyConflict("final channel mismatch")
	}

	return nil

}

// channelPriceCostSnapshot 把 channel_prices 行的成本列映射成 ProviderCostSnapshot。
// 必填列（uncached/output）空值→0；可选列保留 NULL，由 billing 回退到基价（与客户售价侧一致）。
func channelPriceCostSnapshot(p sqlc.ChannelPrice) billing.ProviderCostSnapshot {
	return billing.ProviderCostSnapshot{
		Currency:               p.Currency,
		PricingUnit:            p.PricingUnit,
		UncachedInputCost:      numericOrZero(p.UncachedInputCost),
		CacheReadInputCost:     p.CacheReadInputCost,
		CacheWrite5mInputCost:  p.CacheWrite5mInputCost,
		CacheWrite1hInputCost:  p.CacheWrite1hInputCost,
		CacheWrite30mInputCost: p.CacheWrite30mInputCost,
		OutputCost:             numericOrZero(p.OutputCost),
		ReasoningOutputCost:    p.ReasoningOutputCost,
		FormulaVersion:         billing.FormulaVersionV1,
	}
}

// numericOrZero 把空/非有限 NUMERIC 归一为 0（成本未知按 0 入账）。
func numericOrZero(v pgtype.Numeric) pgtype.Numeric {
	if v.Valid && !v.NaN && v.InfinityModifier == pgtype.Finite && v.Int != nil {
		return v
	}
	return pgtype.Numeric{Int: big.NewInt(0), Exp: 0, Valid: true}
}

// nullableInt8 把行 id 转成可空 BIGINT 参数：0 → SQL NULL（该来源不适用，如倍率路径的 cost_price_id）。
func nullableInt8(id int64) pgtype.Int8 {
	if id == 0 {
		return pgtype.Int8{Valid: false}
	}
	return pgtype.Int8{Int64: id, Valid: true}
}

// int8OrZero 把可空 BIGINT 列读回 int64：NULL → 0（该来源不适用）。
func int8OrZero(v pgtype.Int8) int64 {
	if !v.Valid {
		return 0
	}
	return v.Int64
}

// ensureSettlementCostSnapshotMatches 校验请求级成本快照是否和本次 settlement 参数、自身重算金额一致。
func ensureSettlementCostSnapshotMatches(snapshot sqlc.CostSnapshot, params ChatSettlementParams, cost billing.ProviderCost) error {
	if snapshot.RequestRecordID != params.RequestRecord.ID ||
		snapshot.ProviderID != params.FinalProviderID ||
		snapshot.ChannelID != params.FinalChannelID ||
		snapshot.ModelID != params.ModelDBID {
		return ChatSettlementIdempotencyConflict("cost snapshot route mismatch")
	}

	if snapshot.UpstreamModel != params.AttemptRecord.UpstreamModel {
		return ChatSettlementIdempotencyConflict("cost snapshot upstream model mismatch")
	}

	// DEC-031：成本来源必须可辨——覆盖路径有 cost_price_id，倍率路径有 成本基数(model_price) id + 价格倍率 id。
	hasOverride := snapshot.CostPriceID.Valid && snapshot.CostPriceID.Int64 > 0
	hasMultiplier := snapshot.CostBaseModelPriceID.Valid && snapshot.CostBaseModelPriceID.Int64 > 0 &&
		snapshot.ChannelCostMultiplierID.Valid && snapshot.ChannelCostMultiplierID.Int64 > 0
	if !hasOverride && !hasMultiplier {
		return ChatSettlementIdempotencyConflict("cost snapshot source mismatch")
	}

	if snapshot.Currency != cost.Currency ||
		snapshot.FormulaVersion != cost.FormulaVersion ||
		snapshot.PricingUnit != billing.PricingUnitPer1MTokens {
		return ChatSettlementIdempotencyConflict("cost snapshot metadata mismatch")
	}

	if !chatSettlementSameNumeric(snapshot.UncachedInputCostAmount, cost.UncachedInputCostAmount) ||
		!chatSettlementSameNumeric(snapshot.CacheReadInputCostAmount, cost.CacheReadInputCostAmount) ||
		!chatSettlementSameNumeric(snapshot.CacheWrite5mInputCostAmount, cost.CacheWrite5mInputCostAmount) ||
		!chatSettlementSameNumeric(snapshot.CacheWrite1hInputCostAmount, cost.CacheWrite1hInputCostAmount) ||
		!chatSettlementSameNumeric(snapshot.CacheWrite30mInputCostAmount, cost.CacheWrite30mInputCostAmount) ||
		!chatSettlementSameNumeric(snapshot.OutputCostAmount, cost.OutputCostAmount) ||
		!chatSettlementSameNumeric(snapshot.ReasoningOutputCostAmount, cost.ReasoningOutputCostAmount) ||
		!chatSettlementSameNumeric(snapshot.TotalCostAmount, cost.TotalCostAmount) {
		return ChatSettlementIdempotencyConflict("cost snapshot amount mismatch")
	}

	return nil
}

// ensureSettlementReservationBaseMatches 校验 reservation 基础身份和 authorization 事实一致。
func ensureSettlementReservationBaseMatches(reservation sqlc.LedgerReservation, request sqlc.RequestRecord, authorization ChatAuthorization) error {
	if reservation.ID != authorization.ReservationID ||
		reservation.UserID != request.UserID ||
		reservation.RequestRecordID != request.ID ||
		reservation.Currency != authorization.Currency {
		return ChatSettlementIdempotencyConflict("reservation identity mismatch")
	}
	if !chatSettlementSameNumeric(reservation.EstimatedAmount, authorization.EstimatedAmount) ||
		!chatSettlementSameNumeric(reservation.AuthorizedAmount, authorization.AuthorizedAmount) {
		return ChatSettlementIdempotencyConflict("reservation authorization amount mismatch")
	}

	return nil
}

// ensureSettlementReleasedReservationMatches 校验 0 金额 settlement 是否已经释放冻结余额。
func ensureSettlementReleasedReservationMatches(ctx context.Context, queries *sqlc.Queries, reservation sqlc.LedgerReservation) error {
	if reservation.Status != string(ledger.ReservationStatusReleased) {
		return ChatSettlementIdempotencyConflict("reservation release status mismatch")
	}
	if !numericIsZero(reservation.CapturedAmount) ||
		!chatSettlementSameNumeric(reservation.ReleasedAmount, reservation.AuthorizedAmount) ||
		reservation.CaptureLedgerEntryID.Valid {
		return ChatSettlementIdempotencyConflict("released reservation amount mismatch")
	}

	entries, err := queries.ListLedgerEntriesByRequest(ctx, pgtype.Int8{Int64: reservation.RequestRecordID, Valid: true})
	if err != nil {
		return failure.Wrap(
			failure.CodeGatewayChatSettlementFailed,
			err,
			failure.WithMessage("lookup idempotent released settlement ledger entries"),
		)
	}
	for _, entry := range entries {
		if entry.EntryType == string(ledger.EntryTypeDebit) {
			return ChatSettlementIdempotencyConflict("released settlement has debit ledger entry")
		}
	}

	return nil
}

// ensureSettlementCapturedReservationMatches 校验非 0 金额 settlement 是否已经形成扣费流水。
func ensureSettlementCapturedReservationMatches(ctx context.Context, queries *sqlc.Queries, reservation sqlc.LedgerReservation, actualAmount pgtype.Numeric) error {
	if reservation.Status != string(ledger.ReservationStatusCaptured) {
		return ChatSettlementIdempotencyConflict("reservation capture status mismatch")
	}

	capturedAmount := chatSettlementMinNumeric(actualAmount, reservation.AuthorizedAmount)
	if !chatSettlementSameNumeric(reservation.CapturedAmount, capturedAmount) ||
		!chatSettlementNumericDiffMatches(reservation.ReleasedAmount, reservation.AuthorizedAmount, capturedAmount) ||
		!reservation.CaptureLedgerEntryID.Valid {
		return ChatSettlementIdempotencyConflict("captured reservation amount mismatch")
	}

	entry, err := queries.GetLedgerEntryByIdempotencyKey(ctx, fmt.Sprintf("chat:settle:%d", reservation.RequestRecordID))
	if err != nil {
		return failure.Wrap(
			failure.CodeGatewayChatSettlementFailed,
			err,
			failure.WithMessage("lookup idempotent capture ledger entry"),
		)
	}
	if entry.ID != reservation.CaptureLedgerEntryID.Int64 ||
		entry.UserID != reservation.UserID ||
		!entry.RequestRecordID.Valid ||
		entry.RequestRecordID.Int64 != reservation.RequestRecordID ||
		entry.EntryType != string(ledger.EntryTypeDebit) ||
		entry.Currency != reservation.Currency ||
		!chatSettlementSameNumeric(entry.Amount, capturedAmount) {
		return ChatSettlementIdempotencyConflict("capture ledger entry mismatch")
	}

	// 还原首次结算的超额二次补扣：存在独立 overage debit 时校验其身份并取金额，否则为 0。
	overageCaptured := chatSettlementNumericZero()
	overageEntry, err := queries.GetLedgerEntryByIdempotencyKey(ctx, settlementOverageIdempotencyKey(reservation.RequestRecordID))
	if err == nil {
		if overageEntry.UserID != reservation.UserID ||
			!overageEntry.RequestRecordID.Valid ||
			overageEntry.RequestRecordID.Int64 != reservation.RequestRecordID ||
			overageEntry.EntryType != string(ledger.EntryTypeDebit) ||
			overageEntry.Currency != reservation.Currency ||
			!chatSettlementNumericGreaterThan(overageEntry.Amount, chatSettlementNumericZero()) {
			return ChatSettlementIdempotencyConflict("overage ledger entry mismatch")
		}
		overageCaptured = overageEntry.Amount
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return failure.Wrap(
			failure.CodeGatewayChatSettlementFailed,
			err,
			failure.WithMessage("lookup idempotent overage ledger entry"),
		)
	}

	return ensureSettlementWriteOffMatches(ctx, queries, reservation, actualAmount, capturedAmount, overageCaptured)
}

// settlementOverageIdempotencyKey 由 capture 幂等键派生超额补扣 debit 幂等键，与 ledger.overageIdempotencyKey 保持一致。
func settlementOverageIdempotencyKey(requestRecordID int64) string {
	return fmt.Sprintf("chat:settle:%d", requestRecordID) + ":overage"
}

// chatSettlementNumericZero 返回 settlement 幂等校验用的 0 金额 NUMERIC。
func chatSettlementNumericZero() pgtype.Numeric {
	return pgtype.Numeric{Int: big.NewInt(0), Exp: 0, Valid: true}
}

// chatSettlementAddNumeric 精确相加两个有限 NUMERIC（按更小指数对齐），用于还原用户真实承担总额。
func chatSettlementAddNumeric(left pgtype.Numeric, right pgtype.Numeric) pgtype.Numeric {
	leftFinite := left.Valid && !left.NaN && left.InfinityModifier == pgtype.Finite && left.Int != nil
	rightFinite := right.Valid && !right.NaN && right.InfinityModifier == pgtype.Finite && right.Int != nil
	if !leftFinite || !rightFinite {
		return pgtype.Numeric{}
	}

	exp := left.Exp
	if right.Exp < exp {
		exp = right.Exp
	}

	scale := func(value pgtype.Numeric) *big.Int {
		diff := value.Exp - exp
		if diff <= 0 {
			return new(big.Int).Set(value.Int)
		}
		return new(big.Int).Mul(new(big.Int).Set(value.Int), new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(diff)), nil))
	}

	return pgtype.Numeric{Int: new(big.Int).Add(scale(left), scale(right)), Exp: exp, Valid: true}
}

// ChatSettlementIdempotencyConflict 返回重复 settlement 事实不一致的稳定错误。
func ChatSettlementIdempotencyConflict(message string) error {
	return failure.New(
		failure.CodeGatewayChatSettlementIdempotencyConflict,
		failure.WithMessage(message),
	)
}

func requiredTextMatches(value pgtype.Text, want string) bool {
	return value.Valid && value.String == want
}

func requiredInt8Matches(value pgtype.Int8, want int64) bool {
	return value.Valid && value.Int64 == want
}

// ensureSettlementAttemptMatches 校验已成功 attempt 是否和本次 settlement 参数一致。
func ensureSettlementAttemptMatches(ctx context.Context, queries *sqlc.Queries, params ChatSettlementParams) error {
	attempts, err := queries.ListRequestAttemptsByRequest(ctx, params.RequestRecord.ID)
	if err != nil {
		return failure.Wrap(
			failure.CodeGatewayChatSettlementFailed,
			err,
			failure.WithMessage("lookup idempotent chat settlement attempts"),
		)
	}

	for _, attempt := range attempts {
		if attempt.ID != params.AttemptRecord.ID {
			continue
		}

		if attempt.RequestRecordID != params.RequestRecord.ID ||
			attempt.ProviderID != params.FinalProviderID ||
			attempt.ChannelID != params.FinalChannelID {
			return ChatSettlementIdempotencyConflict("attempt route mismatch")
		}
		if attempt.Status != string(requestlog.AttemptStatusSucceeded) {
			return ChatSettlementIdempotencyConflict("attempt status mismatch")
		}
		if attempt.AdapterKey != params.AttemptRecord.AdapterKey ||
			attempt.UpstreamModel != params.AttemptRecord.UpstreamModel ||
			attempt.UpstreamProtocol != params.Facts.UpstreamProtocol {
			return ChatSettlementIdempotencyConflict("attempt upstream request mismatch")
		}
		if !requiredTextMatches(attempt.UpstreamResponseID, params.Facts.UpstreamResponseID) {
			return ChatSettlementIdempotencyConflict("attempt upstream response id mismatch")
		}
		if !requiredTextMatches(attempt.UpstreamResponseModel, params.Facts.UpstreamModel) {
			return ChatSettlementIdempotencyConflict("attempt upstream response model mismatch")
		}
		if !requiredTextMatches(attempt.UpstreamFinishReason, params.Facts.Finish.RawReason) ||
			!requiredTextMatches(attempt.FinishClass, string(params.Facts.Finish.Class)) {
			return ChatSettlementIdempotencyConflict("attempt finish facts mismatch")
		}
		if !requiredInt4Matches(attempt.UpstreamStatusCode, int32(params.Facts.Metadata.StatusCode)) {
			return ChatSettlementIdempotencyConflict("attempt upstream status mismatch")
		}
		if !optionalTextMatches(attempt.UpstreamRequestID, UpstreamRequestIDPtr(params.Facts.Metadata.RequestID)) {
			return ChatSettlementIdempotencyConflict("attempt upstream request id mismatch")
		}
		if !attempt.FinalUsageReceived ||
			!requiredTextMatches(attempt.UsageMappingVersion, params.Facts.UsageMappingVersion) {
			return ChatSettlementIdempotencyConflict("attempt usage mapping mismatch")
		}

		return nil
	}

	return ChatSettlementIdempotencyConflict("settlement attempt not found")
}

// ensureSettlementUsageMatches 校验 usage record 是否和本次上游 usage facts 一致。
func ensureSettlementUsageMatches(row sqlc.UsageRecord, facts adapter.ResponseFacts) error {
	u := facts.Usage
	if row.UncachedInputTokens != u.UncachedInputTokens.Value ||
		row.UncachedInputTokensState != string(u.UncachedInputTokens.State) ||
		row.CacheReadInputTokens != u.CacheReadInputTokens.Value ||
		row.CacheReadInputTokensState != string(u.CacheReadInputTokens.State) ||
		row.CacheWrite5mInputTokens != u.CacheWrite5mInputTokens.Value ||
		row.CacheWrite5mInputTokensState != string(u.CacheWrite5mInputTokens.State) ||
		row.CacheWrite1hInputTokens != u.CacheWrite1hInputTokens.Value ||
		row.CacheWrite1hInputTokensState != string(u.CacheWrite1hInputTokens.State) ||
		row.CacheWrite30mInputTokens != u.CacheWrite30mInputTokens.Value ||
		row.CacheWrite30mInputTokensState != string(u.CacheWrite30mInputTokens.State) ||
		row.OutputTokensTotal != u.OutputTokensTotal.Value ||
		row.OutputTokensTotalState != string(u.OutputTokensTotal.State) ||
		row.ReasoningOutputTokens != u.ReasoningOutputTokens.Value ||
		row.ReasoningOutputTokensState != string(u.ReasoningOutputTokens.State) {
		return ChatSettlementIdempotencyConflict("usage mismatch")
	}

	if row.UsageSource != string(facts.UsageSource) ||
		row.UsageMappingVersion != facts.UsageMappingVersion {
		return ChatSettlementIdempotencyConflict("usage source mismatch")
	}

	return nil
}

// ensureSettlementUsageLineItemsMatch 校验受控附加计量事实是否和重放 facts 一致。
func ensureSettlementUsageLineItemsMatch(rows []sqlc.UsageLineItem, items []usage.MeteredItem) error {
	if len(rows) != len(items) {
		return ChatSettlementIdempotencyConflict("usage line item count mismatch")
	}

	want := make(map[usage.MeteredKind]int64, len(items))
	for _, item := range items {
		want[item.Kind] = item.Quantity
	}
	for _, row := range rows {
		quantity, ok := want[usage.MeteredKind(row.Kind)]
		if !ok || quantity != row.Quantity {
			return ChatSettlementIdempotencyConflict("usage line item mismatch")
		}
	}

	return nil
}

// settlementUsageFactsFromRecord 将数据库 usage 行还原为 billing 消费的协议无关 facts。
func settlementUsageFactsFromRecord(row sqlc.UsageRecord, lineItems []sqlc.UsageLineItem) usage.Facts {
	items := make([]usage.MeteredItem, 0, len(lineItems))
	for _, item := range lineItems {
		items = append(items, usage.MeteredItem{
			Kind:     usage.MeteredKind(item.Kind),
			Quantity: item.Quantity,
		})
	}

	return usage.Facts{
		UncachedInputTokens:      usage.TokenCount{Value: row.UncachedInputTokens, State: usage.CountState(row.UncachedInputTokensState)},
		CacheReadInputTokens:     usage.TokenCount{Value: row.CacheReadInputTokens, State: usage.CountState(row.CacheReadInputTokensState)},
		CacheWrite5mInputTokens:  usage.TokenCount{Value: row.CacheWrite5mInputTokens, State: usage.CountState(row.CacheWrite5mInputTokensState)},
		CacheWrite1hInputTokens:  usage.TokenCount{Value: row.CacheWrite1hInputTokens, State: usage.CountState(row.CacheWrite1hInputTokensState)},
		CacheWrite30mInputTokens: usage.TokenCount{Value: row.CacheWrite30mInputTokens, State: usage.CountState(row.CacheWrite30mInputTokensState)},
		OutputTokensTotal:        usage.TokenCount{Value: row.OutputTokensTotal, State: usage.CountState(row.OutputTokensTotalState)},
		ReasoningOutputTokens:    usage.TokenCount{Value: row.ReasoningOutputTokens, State: usage.CountState(row.ReasoningOutputTokensState)},
		ServerToolUsage:          items,
	}
}

// ensureSettlementWriteOffMatches 校验「真实费用 - (冻结内扣费 + 超额二次补扣)」残差由平台核销的事实。
// totalCaptured = capturedAmount + overageCaptured，即用户真实承担总额；残差 > 0 时才应存在 write_off 异常。
func ensureSettlementWriteOffMatches(ctx context.Context, queries *sqlc.Queries, reservation sqlc.LedgerReservation, actualAmount pgtype.Numeric, capturedAmount pgtype.Numeric, overageCaptured pgtype.Numeric) error {
	totalCaptured := chatSettlementAddNumeric(capturedAmount, overageCaptured)
	exception, err := queries.GetLedgerBillingExceptionByReservationID(ctx, reservation.ID)

	if !chatSettlementNumericGreaterThan(actualAmount, totalCaptured) {
		// 二次补扣已覆盖全部超额（或无超额）：不应存在 write_off 异常。
		if err == nil {
			return ChatSettlementIdempotencyConflict("unexpected billing exception")
		}
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		return failure.Wrap(
			failure.CodeGatewayChatSettlementFailed,
			err,
			failure.WithMessage("lookup idempotent billing exception"),
		)
	}

	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ChatSettlementIdempotencyConflict("missing write off exception")
		}
		return failure.Wrap(
			failure.CodeGatewayChatSettlementFailed,
			err,
			failure.WithMessage("lookup idempotent write off exception"),
		)
	}
	if exception.EventType != "write_off" ||
		exception.UserID != reservation.UserID ||
		exception.RequestRecordID != reservation.RequestRecordID ||
		exception.ReservationID != reservation.ID ||
		exception.Currency != reservation.Currency ||
		!chatSettlementSameNumeric(exception.ActualAmount, actualAmount) ||
		!chatSettlementSameNumeric(exception.CapturedAmount, totalCaptured) ||
		!chatSettlementNumericDiffMatches(exception.PlatformAmount, actualAmount, totalCaptured) {
		return ChatSettlementIdempotencyConflict("write off exception mismatch")
	}

	return nil
}

func requiredInt4Matches(value pgtype.Int4, want int32) bool {
	return value.Valid && value.Int32 == want
}

// optionalTextMatches 校验可空 TEXT 列是否与可选字符串一致。
// 两者都缺失视为一致；一有一无或值不同视为不一致。
func optionalTextMatches(value pgtype.Text, want *string) bool {
	if want == nil {
		return !value.Valid
	}

	return value.Valid && value.String == *want
}

func chatSettlementSameNumeric(left pgtype.Numeric, right pgtype.Numeric) bool {
	leftRat, leftOK := chatSettlementNumericRat(left)
	rightRat, rightOK := chatSettlementNumericRat(right)
	if !leftOK || !rightOK {
		return leftOK == rightOK
	}

	return leftRat.Cmp(rightRat) == 0
}

func chatSettlementNumericGreaterThan(left pgtype.Numeric, right pgtype.Numeric) bool {
	cmp, ok := chatSettlementCompareNumeric(left, right)
	return ok && cmp > 0
}

func chatSettlementMinNumeric(left pgtype.Numeric, right pgtype.Numeric) pgtype.Numeric {
	cmp, ok := chatSettlementCompareNumeric(left, right)
	if ok && cmp <= 0 {
		return left
	}

	return right
}

func chatSettlementNumericDiffMatches(value pgtype.Numeric, left pgtype.Numeric, right pgtype.Numeric) bool {
	valueRat, valueOK := chatSettlementNumericRat(value)
	leftRat, leftOK := chatSettlementNumericRat(left)
	rightRat, rightOK := chatSettlementNumericRat(right)
	if !valueOK || !leftOK || !rightOK {
		return false
	}

	return valueRat.Cmp(new(big.Rat).Sub(leftRat, rightRat)) == 0
}

func chatSettlementCompareNumeric(left pgtype.Numeric, right pgtype.Numeric) (int, bool) {
	leftRat, leftOK := chatSettlementNumericRat(left)
	rightRat, rightOK := chatSettlementNumericRat(right)
	if !leftOK || !rightOK {
		return 0, false
	}

	return leftRat.Cmp(rightRat), true
}

func chatSettlementNumericRat(value pgtype.Numeric) (*big.Rat, bool) {
	if !value.Valid || value.NaN || value.InfinityModifier != pgtype.Finite || value.Int == nil {
		return nil, false
	}

	rat := new(big.Rat).SetInt(new(big.Int).Set(value.Int))
	if value.Exp > 0 {
		rat.Mul(rat, new(big.Rat).SetInt(chatSettlementPow10(value.Exp)))
	}
	if value.Exp < 0 {
		rat.Quo(rat, new(big.Rat).SetInt(chatSettlementPow10(-value.Exp)))
	}

	return rat, true
}

func chatSettlementPow10(exp int32) *big.Int {
	return new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(exp)), nil)
}

// numericIsZero 判断 NUMERIC 金额是否表示 0。
func numericIsZero(value pgtype.Numeric) bool {
	if !value.Valid || value.Int == nil {
		return true
	}
	return value.Int.Sign() == 0
}
