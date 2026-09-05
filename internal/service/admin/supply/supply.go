// Package supply 实现供给影响预览与显式联合操作基础设施。
//
// 供给的根是 Model：模型 enabled 即对外可售，其背后必定有至少一条可用渠道——
// 这条不变量是整个包存在的理由。任何会让某个模型失去最后一条支撑的写操作，都不能默默执行，
// 否则客户会拿到「列表里有、一调 503」的结果。
//
// 因此每个收缩型写操作都走同一套流程：
//   - 串行化：先按 model_id 升序 FOR UPDATE 锁定受影响的 Model 行，避免并发写各自看到过期事实；
//   - 影响预览：在锁内反查哪些 enabled 模型会失去最后支撑；
//   - 影响指纹：对预览内容取 sha256，确认请求必须携带一致指纹，保证管理员看到的就是即将发生的；
//   - 确认契约：需要确认时返回 ConfirmationRequired（HTTP 层渲染为 409），
//     管理员明确选择要一并停用哪些模型；空选择合法且是默认值（即接受这些模型转为 503）。
package supply

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/ThankCat/unio-gateway/internal/platform/store/sqlc"
)

// models.disabled_reason 受控枚举，与数据库 CHECK 约束保持一致。
const (
	// ReasonManualDelisted 管理员主动下架该模型。
	ReasonManualDelisted = "manual_delisted"
	// ReasonBindingDisabled 该模型最后一条渠道绑定被停用或解除。
	ReasonBindingDisabled = "binding_disabled"
	// ReasonChannelDisabled 该模型最后一条可用渠道被停用。
	ReasonChannelDisabled = "channel_disabled"
	// ReasonPriceDisabled 该模型最后一条可解析售价被撤销。
	ReasonPriceDisabled = "price_disabled"
)

// AffectedModel 是影响预览中将失去供给的一个模型。
type AffectedModel struct {
	ModelID          int64
	PublicModelID    string
	ModelDisplayName string
	// KeptResult 是只执行目标层操作、保留模型 enabled 时对新请求的预期结果。
	KeptResult string
	// SelectedResult 是同时停用该模型时对新请求的预期结果。
	SelectedResult string
}

// ModelSelection 是联合操作中由管理员明确选择一并停用的模型。
type ModelSelection struct {
	ModelID int64
}

// Impact 是一次供给写操作在 Model 锁内计算出的影响预览。
// AffectedModels 只表示潜在影响范围，不表示这些模型会被自动修改。
type Impact struct {
	// Kind 标识操作类型，参与指纹计算，避免同一影响集合跨操作复用指纹。
	Kind string
	// AffectedModels 是本次操作可能改变客户结果的模型集合。
	AffectedModels []AffectedModel
	// RemainingEnabledBindings 是排除本次失效目标后全局剩余的 enabled Binding 数
	// （仅 Binding 层操作有意义）。
	RemainingEnabledBindings int64
	// EnabledBindings/Channels/Providers 是当前影响范围的只读统计，不代表会被级联修改。
	EnabledBindings int64
	Channels        int64
	Providers       int64
}

// RequiresConfirmation 判断是否存在需要管理员确认的客户影响。
func (im Impact) RequiresConfirmation() bool {
	return len(im.AffectedModels) > 0
}

// Fingerprint 由影响预览的相关状态集合计算稳定指纹。指纹只覆盖预览内容本身：
// 并发变化若不改变影响集合，则无需重新确认；改变影响集合必然改变指纹。
func (im Impact) Fingerprint() string {
	lines := make([]string, 0, len(im.AffectedModels))
	for _, am := range im.AffectedModels {
		lines = append(lines, fmt.Sprintf(
			"model|%d|%s|%s|%s",
			am.ModelID, am.PublicModelID, am.KeptResult, am.SelectedResult,
		))
	}
	sort.Strings(lines)
	header := fmt.Sprintf(
		"kind=%s|remaining_enabled_bindings=%d|enabled_bindings=%d|channels=%d|providers=%d",
		im.Kind, im.RemainingEnabledBindings, im.EnabledBindings, im.Channels, im.Providers,
	)
	sum := sha256.Sum256([]byte(header + "\n" + strings.Join(lines, "\n")))
	return hex.EncodeToString(sum[:])
}

// Confirmation 是写请求携带的二次确认参数。SelectedModels 为空表示只修改目标层。
type Confirmation struct {
	Confirm             bool
	ExpectedFingerprint string
	SelectedModels      []ModelSelection
}

// ConfirmationRequired 表示操作需要管理员携带影响指纹二次确认；HTTP 层渲染为 409。
type ConfirmationRequired struct {
	Code    string
	Message string
	Impact  Impact
}

// Error 返回安全的确认提示摘要。
func (e *ConfirmationRequired) Error() string { return e.Message }

// Authorize 在锁内比对确认参数与重算影响：影响未达确认门槛直接放行；
// 携带一致指纹的确认放行；否则返回 ConfirmationRequired（含最新预览）。
func Authorize(im Impact, code, message string, c Confirmation) error {
	if !im.RequiresConfirmation() {
		return nil
	}
	if !c.Confirm || c.ExpectedFingerprint != im.Fingerprint() {
		return &ConfirmationRequired{Code: code, Message: message, Impact: im}
	}
	allowed := make(map[int64]struct{}, len(im.AffectedModels))
	for _, am := range im.AffectedModels {
		allowed[am.ModelID] = struct{}{}
	}
	for _, selected := range c.SelectedModels {
		if _, ok := allowed[selected.ModelID]; !ok {
			return &ConfirmationRequired{Code: code, Message: message, Impact: im}
		}
	}
	return nil
}

// LockModels 按 model_id 升序对给定 Model 行取得 FOR UPDATE 锁（供给变更串行化）。
// 空集合是 no-op。调用方必须已处于事务中。
func LockModels(ctx context.Context, q *sqlc.Queries, modelIDs []int64) error {
	ids := dedupeSorted(modelIDs)
	if len(ids) == 0 {
		return nil
	}
	if _, err := q.LockModelsForSupplyChange(ctx, ids); err != nil {
		return fmt.Errorf("lock models for supply change: %w", err)
	}
	return nil
}

// LockModelsForChannel 聚合某 Channel 全部 enabled Binding 的 Model 并锁定，
// 返回锁定的 Model 集合（升序）。Channel 实体停用/归档前置使用。
func LockModelsForChannel(ctx context.Context, q *sqlc.Queries, channelID int64) ([]int64, error) {
	modelIDs, err := q.ListEnabledBindingModelIDsForChannel(ctx, channelID)
	if err != nil {
		return nil, fmt.Errorf("list enabled binding models for channel: %w", err)
	}
	if err := LockModels(ctx, q, modelIDs); err != nil {
		return nil, err
	}
	return modelIDs, nil
}

// BindingImpact 在锁内计算停用/解除一条 enabled Binding 的影响：
// 反查会失去最后一条配置支撑的模型，并全局判断这是否是该模型的最后一条 enabled Binding。
// 调用方必须先确认目标 Binding 当前为 enabled，并已锁定该 Model。
func BindingImpact(ctx context.Context, q *sqlc.Queries, channelID, modelID int64) (Impact, error) {
	rows, err := q.ListModelsLosingConfiguredSupply(ctx, sqlc.ListModelsLosingConfiguredSupplyParams{
		ChannelID: channelID,
		ModelID:   pgtype.Int8{Int64: modelID, Valid: true},
	})
	if err != nil {
		return Impact{}, fmt.Errorf("compute binding supply impact: %w", err)
	}
	remaining, err := q.CountOtherEnabledBindingsForModel(ctx, sqlc.CountOtherEnabledBindingsForModelParams{
		ModelID:          modelID,
		ExcludeChannelID: channelID,
	})
	if err != nil {
		return Impact{}, fmt.Errorf("count remaining enabled bindings: %w", err)
	}
	return Impact{
		Kind:                     "channel_model_disable",
		AffectedModels:           affectedFromLosingConfigured(rows),
		RemainingEnabledBindings: remaining,
	}, nil
}

// ChannelBulkImpact 在锁内计算解除/停用某 Channel 全部 enabled Binding 的影响。
// 与 BindingImpact 的区别只在范围：这里目标渠道上所有绑定同时失效。
func ChannelBulkImpact(ctx context.Context, q *sqlc.Queries, channelID int64) (Impact, error) {
	rows, err := q.ListModelsLosingConfiguredSupply(ctx, sqlc.ListModelsLosingConfiguredSupplyParams{
		ChannelID: channelID,
	})
	if err != nil {
		return Impact{}, fmt.Errorf("compute channel binding supply impact: %w", err)
	}
	return Impact{
		Kind:           "channel_bindings_disable",
		AffectedModels: affectedFromLosingConfigured(rows),
	}, nil
}

// ChannelImpact 在锁内计算暂停 Channel 流量后可能失去最后运行候选的模型。
// Binding 与 Model 配置行均不改写。
func ChannelImpact(ctx context.Context, q *sqlc.Queries, channelID int64) (Impact, error) {
	rows, err := q.ListModelsLosingRuntimeSupply(ctx, channelID)
	if err != nil {
		return Impact{}, fmt.Errorf("compute channel runtime impact: %w", err)
	}
	return Impact{
		Kind:           "channel_disable",
		AffectedModels: affectedFromLosingRuntime(rows),
	}, nil
}

// ModelImpact 在锁内计算全局下架 Model 的客户影响与供给范围统计。
// 目标模型自身就是唯一受影响对象，所以这里不需要反查：下架即 404。
func ModelImpact(ctx context.Context, q *sqlc.Queries, modelID int64, publicModelID, displayName string) (Impact, error) {
	counts, err := q.ModelDisableImpactCounts(ctx, modelID)
	if err != nil {
		return Impact{}, fmt.Errorf("count model disable impact: %w", err)
	}
	return Impact{
		Kind: "model_disable",
		AffectedModels: []AffectedModel{{
			ModelID:          modelID,
			PublicModelID:    publicModelID,
			ModelDisplayName: displayName,
			KeptResult:       "404",
			SelectedResult:   "404",
		}},
		EnabledBindings: counts.EnabledBindings,
		Channels:        counts.Channels,
		Providers:       counts.Providers,
	}, nil
}

// PriceImpact 在锁内计算撤掉一条价格窗口的影响：该模型是否因此失去可解析售价。
// excludePriceID 为 nil 表示当前窗口整组被替换（Create 的 replace_overlapping_enabled 路径）。
// 调用方必须已锁定该 Model。
func PriceImpact(ctx context.Context, q *sqlc.Queries, modelID int64, excludePriceID *int64) (Impact, error) {
	exclude := pgtype.Int8{}
	if excludePriceID != nil {
		exclude = pgtype.Int8{Int64: *excludePriceID, Valid: true}
	}
	rows, err := q.ListModelsLosingSalePrice(ctx, sqlc.ListModelsLosingSalePriceParams{
		ModelID:        modelID,
		ExcludePriceID: exclude,
	})
	if err != nil {
		return Impact{}, fmt.Errorf("compute model price supply impact: %w", err)
	}
	out := make([]AffectedModel, 0, len(rows))
	for _, row := range rows {
		out = append(out, AffectedModel{
			ModelID:          row.ModelID,
			PublicModelID:    row.PublicModelID,
			ModelDisplayName: row.ModelDisplayName,
			// 价格侧不给「保留」选项，两个字段同为 404：确认即下架。
			KeptResult:     "404",
			SelectedResult: "404",
		})
	}
	return Impact{Kind: "model_price_disable", AffectedModels: out}, nil
}

// DisableAffectedModels 停用影响范围内的全部模型，不看管理员的勾选。
//
// 与 DisableSelectedModels 的差别是产品口径而非实现细节：渠道故障会自己恢复，
// 所以那边允许保留模型 enabled 等渠道回来；撤价不会自己恢复，保留只能让模型
// 一直停在「列表里有、一调失败」的状态，因此这里强制下架。
// 必须与影响计算处于同一事务、同一 Model 锁内。
func DisableAffectedModels(ctx context.Context, q *sqlc.Queries, im Impact, reason string) error {
	for _, am := range im.AffectedModels {
		affected, err := q.DisableModelSupply(ctx, sqlc.DisableModelSupplyParams{
			ID:     am.ModelID,
			Reason: pgtype.Text{String: reason, Valid: true},
		})
		if err != nil {
			return fmt.Errorf("disable model supply: %w", err)
		}
		if affected == 0 {
			return fmt.Errorf("model %d drifted during supply transaction", am.ModelID)
		}
	}
	return nil
}

// DisableSelectedModels 只停用管理员明确选择且属于最新影响范围的模型。
// 必须与影响计算处于同一事务、同一 Model 锁内；锁保证集合在提交前不会漂移。
func DisableSelectedModels(ctx context.Context, q *sqlc.Queries, im Impact, c Confirmation, reason string) error {
	available := make(map[int64]AffectedModel, len(im.AffectedModels))
	for _, am := range im.AffectedModels {
		available[am.ModelID] = am
	}
	seen := make(map[int64]struct{}, len(c.SelectedModels))
	for _, selected := range c.SelectedModels {
		if _, duplicate := seen[selected.ModelID]; duplicate {
			continue
		}
		seen[selected.ModelID] = struct{}{}
		am, ok := available[selected.ModelID]
		if !ok {
			return fmt.Errorf("selected model is outside the current impact")
		}
		affected, err := q.DisableModelSupply(ctx, sqlc.DisableModelSupplyParams{
			ID:     am.ModelID,
			Reason: pgtype.Text{String: reason, Valid: true},
		})
		if err != nil {
			return fmt.Errorf("disable model supply: %w", err)
		}
		if affected == 0 {
			return fmt.Errorf("model %d drifted during supply transaction", am.ModelID)
		}
	}
	return nil
}

// EnsureRuntimeSupply 是启用模型的前置守卫：没有可用渠道、没有生效基准价、
// 或没有可解析售价，都不允许 enabled，否则「enabled 即可调用」当场被打破。
// 必须在 Model 锁内调用。
func EnsureRuntimeSupply(ctx context.Context, q *sqlc.Queries, modelID int64) error {
	supported, err := q.ModelHasRuntimeSupply(ctx, modelID)
	if err != nil {
		return fmt.Errorf("check model runtime supply: %w", err)
	}
	if supported {
		return nil
	}
	hasBase, err := q.ModelHasEffectiveBasePrice(ctx, modelID)
	if err != nil {
		return fmt.Errorf("check model base price: %w", err)
	}
	if !hasBase {
		return ErrNoBasePrice
	}
	hasSale, err := q.ModelHasResolvableSalePrice(ctx, modelID)
	if err != nil {
		return fmt.Errorf("check model sale price: %w", err)
	}
	if !hasSale {
		return ErrNoSalePrice
	}
	return ErrNoRuntimeSupply
}

// ErrNoRuntimeSupply 表示模型当前没有任何可用渠道，不能启用。
var ErrNoRuntimeSupply = fmt.Errorf("model has no usable channel")

// ErrNoBasePrice 表示模型没有生效基准价，不能启用。
var ErrNoBasePrice = fmt.Errorf("model has no effective base price")

// ErrNoSalePrice 表示模型有基准价但没有折扣或绝对售价，不能启用。
var ErrNoSalePrice = fmt.Errorf("model has no resolvable sale price")

func affectedFromLosingConfigured(rows []sqlc.ListModelsLosingConfiguredSupplyRow) []AffectedModel {
	out := make([]AffectedModel, 0, len(rows))
	for _, row := range rows {
		out = append(out, AffectedModel{
			ModelID:          row.ModelID,
			PublicModelID:    row.PublicModelID,
			ModelDisplayName: row.ModelDisplayName,
			KeptResult:       "503",
			SelectedResult:   "404",
		})
	}
	return out
}

func affectedFromLosingRuntime(rows []sqlc.ListModelsLosingRuntimeSupplyRow) []AffectedModel {
	out := make([]AffectedModel, 0, len(rows))
	for _, row := range rows {
		out = append(out, AffectedModel{
			ModelID:          row.ModelID,
			PublicModelID:    row.PublicModelID,
			ModelDisplayName: row.ModelDisplayName,
			KeptResult:       "503",
			SelectedResult:   "404",
		})
	}
	return out
}

func dedupeSorted(ids []int64) []int64 {
	if len(ids) == 0 {
		return nil
	}
	seen := make(map[int64]struct{}, len(ids))
	out := make([]int64, 0, len(ids))
	for _, id := range ids {
		if id <= 0 {
			continue
		}
		if _, dup := seen[id]; dup {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}
