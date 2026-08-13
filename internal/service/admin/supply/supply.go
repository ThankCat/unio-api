// Package supply 实现 ADR-0018 的结构供给联动基础设施：
//
//   - 结构支撑串行化：所有改变某个 Model 结构支撑事实的写事务，先按 model_id 升序
//     FOR UPDATE 锁定 Model 行，再在锁内计算影响或校验支撑；
//   - 影响计算：收缩操作（停用/解除 Binding、停用 Channel 实体、移除 Route Channel、
//     手动停用 Model）反查失去最后结构支撑的 enabled Offering；
//   - 影响指纹：由影响预览的相关状态集合计算 sha256 指纹，确认请求必须携带一致指纹；
//   - 确认契约：需要二次确认的操作返回 ConfirmationRequired（HTTP 层渲染为 409）。
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

// route_model_offerings.disabled_reason 受控枚举（ADR-0018 按触发操作细分）。
const (
	// ReasonManualUnselected 管理员在 Route 中主动取消勾选该 Model 与协议组合。
	ReasonManualUnselected = "manual_unselected"
	// ReasonModelDisabled Model 被手动或自动全局停用。
	ReasonModelDisabled = "model_disabled"
	// ReasonBindingDisabled Route 内最后一条同模型、同协议 enabled Binding 被停用或解除。
	ReasonBindingDisabled = "binding_disabled"
	// ReasonChannelDisabled 承载 Route 内最后结构支撑的 Channel 实体被停用。
	ReasonChannelDisabled = "channel_disabled"
	// ReasonRouteChannelRemoved Channel 被移出 Route 池，使 Offering 失去最后结构支撑。
	ReasonRouteChannelRemoved = "route_channel_removed"
	// ReasonChannelProtocolChanged Channel protocol 修改使 Offering 失去最后同协议结构支撑。
	ReasonChannelProtocolChanged = "channel_protocol_changed"
	// ReasonMigrationBackfill 一次切换迁移按当时事实回填。
	ReasonMigrationBackfill = "migration_backfill"
)

// Offering 状态。
const (
	OfferingEnabled  = "enabled"
	OfferingDisabled = "disabled"
)

// AffectedOffering 是影响预览中将停止（或恢复）售卖的一条 Route+Model+协议组合。
type AffectedOffering struct {
	RouteID          int64
	RouteName        string
	RouteStatus      string
	ModelID          int64
	PublicModelID    string
	ModelDisplayName string
	IngressProtocol  string
}

// Impact 是一次供给写操作在 Model 锁内计算出的影响预览。
type Impact struct {
	// Kind 标识操作类型，参与指纹计算，避免同一影响集合跨操作复用指纹。
	Kind string
	// AffectedOfferings 是将被自动停用（或批量恢复时将启用）的 Offering 集合。
	AffectedOfferings []AffectedOffering
	// ModelWillDisable 表示目标是全局最后一条 enabled Binding，Model 将自动停用。
	ModelWillDisable bool
	// RemainingEnabledBindings 是排除本次失效目标后全局剩余的 enabled Binding 数
	// （仅 Binding 层操作有意义）。
	RemainingEnabledBindings int64
	// CascadeEnabledBindings/CascadeChannels/CascadeProviders 是 Model 停用级联预览计数。
	CascadeEnabledBindings int64
	CascadeChannels        int64
	CascadeProviders       int64
}

// RequiresConfirmation 判断影响是否达到 ADR-0018 的二次确认门槛：
// 至少一条 Offering 将被自动停用，或 Model 将自动停用。
func (im Impact) RequiresConfirmation() bool {
	return len(im.AffectedOfferings) > 0 || im.ModelWillDisable
}

// Fingerprint 由影响预览的相关状态集合计算稳定指纹。指纹只覆盖预览内容本身：
// 并发变化若不改变影响集合，则无需重新确认；改变影响集合必然改变指纹。
func (im Impact) Fingerprint() string {
	lines := make([]string, 0, len(im.AffectedOfferings))
	for _, ao := range im.AffectedOfferings {
		lines = append(lines, fmt.Sprintf(
			"offering|%d|%s|%d|%s|%s",
			ao.RouteID, ao.RouteStatus, ao.ModelID, ao.IngressProtocol, ao.PublicModelID,
		))
	}
	sort.Strings(lines)
	header := fmt.Sprintf(
		"kind=%s|model_will_disable=%t|remaining_enabled_bindings=%d|cascade_bindings=%d|cascade_channels=%d|cascade_providers=%d",
		im.Kind, im.ModelWillDisable, im.RemainingEnabledBindings,
		im.CascadeEnabledBindings, im.CascadeChannels, im.CascadeProviders,
	)
	sum := sha256.Sum256([]byte(header + "\n" + strings.Join(lines, "\n")))
	return hex.EncodeToString(sum[:])
}

// Confirmation 是写请求携带的二次确认参数（confirm_supply_impact + expected_impact_fingerprint）。
type Confirmation struct {
	Confirm             bool
	ExpectedFingerprint string
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
	if c.Confirm && c.ExpectedFingerprint == im.Fingerprint() {
		return nil
	}
	return &ConfirmationRequired{Code: code, Message: message, Impact: im}
}

// LockModels 按 model_id 升序对给定 Model 行取得 FOR UPDATE 锁（结构支撑串行化）。
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
// Route 级反查失去最后结构支撑的 Offering + 全局判断是否为最后一条 enabled Binding。
// 调用方必须先确认目标 Binding 当前为 enabled，并已锁定该 Model。
func BindingImpact(ctx context.Context, q *sqlc.Queries, channelID, modelID int64) (Impact, error) {
	rows, err := q.ListOfferingsLosingSupport(ctx, sqlc.ListOfferingsLosingSupportParams{
		ChannelID: channelID,
		ModelID:   pgtype.Int8{Int64: modelID, Valid: true},
	})
	if err != nil {
		return Impact{}, fmt.Errorf("compute binding route impact: %w", err)
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
		AffectedOfferings:        affectedFromLosingSupport(rows),
		ModelWillDisable:         remaining == 0,
		RemainingEnabledBindings: remaining,
	}, nil
}

// ChannelImpact 在锁内计算停用 Channel 实体的影响：该 Channel 全部 enabled Binding 承载的
// 结构支撑同时失效。Binding 行不改写，不参与 Model 自动停用判断（ADR-0018 全局影响计算）。
func ChannelImpact(ctx context.Context, q *sqlc.Queries, channelID int64) (Impact, error) {
	rows, err := q.ListOfferingsLosingSupport(ctx, sqlc.ListOfferingsLosingSupportParams{
		ChannelID: channelID,
		ModelID:   pgtype.Int8{},
	})
	if err != nil {
		return Impact{}, fmt.Errorf("compute channel route impact: %w", err)
	}
	return Impact{
		Kind:              "channel_disable",
		AffectedOfferings: affectedFromLosingSupport(rows),
	}, nil
}

// ModelImpact 在锁内计算手动停用 Model 的影响：全部 enabled Offering 与级联 Binding 计数。
func ModelImpact(ctx context.Context, q *sqlc.Queries, modelID int64) (Impact, error) {
	offerings, err := q.ListEnabledOfferingsForModel(ctx, modelID)
	if err != nil {
		return Impact{}, fmt.Errorf("list enabled offerings for model: %w", err)
	}
	counts, err := q.ModelDisableImpactCounts(ctx, modelID)
	if err != nil {
		return Impact{}, fmt.Errorf("count model disable cascade: %w", err)
	}
	affected := make([]AffectedOffering, 0, len(offerings))
	for _, row := range offerings {
		affected = append(affected, AffectedOffering{
			RouteID:          row.RouteID,
			RouteName:        row.RouteName,
			RouteStatus:      row.RouteStatus,
			ModelID:          row.ModelID,
			PublicModelID:    row.PublicModelID,
			ModelDisplayName: row.ModelDisplayName,
			IngressProtocol:  row.IngressProtocol,
		})
	}
	return Impact{
		Kind:                   "model_disable",
		AffectedOfferings:      affected,
		ModelWillDisable:       true,
		CascadeEnabledBindings: counts.EnabledBindings,
		CascadeChannels:        counts.Channels,
		CascadeProviders:       counts.Providers,
	}, nil
}

// DisableOfferings 把影响集合内的 Offering 逐条置 disabled 并记录原因。
// 必须与影响计算处于同一事务、同一 Model 锁内；锁保证集合在提交前不会漂移。
func DisableOfferings(ctx context.Context, q *sqlc.Queries, offerings []AffectedOffering, reason string) error {
	for _, ao := range offerings {
		affected, err := q.DisableRouteModelOffering(ctx, sqlc.DisableRouteModelOfferingParams{
			Reason:          pgtype.Text{String: reason, Valid: true},
			RouteID:         ao.RouteID,
			ModelID:         ao.ModelID,
			IngressProtocol: ao.IngressProtocol,
		})
		if err != nil {
			return fmt.Errorf("disable route model offering: %w", err)
		}
		if affected == 0 {
			return fmt.Errorf("offering route=%d model=%d protocol=%s drifted during supply transaction",
				ao.RouteID, ao.ModelID, ao.IngressProtocol)
		}
	}
	return nil
}

// CascadeModelDisable 在锁内执行 Model 停用级联：Model 行、全部 enabled Binding 与
// 全部 enabled Offering（原因统一 model_disabled）在同一事务内原子完成。
func CascadeModelDisable(ctx context.Context, q *sqlc.Queries, modelID int64) error {
	if _, err := q.DisableModelSupply(ctx, modelID); err != nil {
		return fmt.Errorf("disable model: %w", err)
	}
	if _, err := q.DisableEnabledBindingsForModel(ctx, modelID); err != nil {
		return fmt.Errorf("disable model bindings: %w", err)
	}
	if _, err := q.DisableEnabledOfferingsForModel(ctx, modelID); err != nil {
		return fmt.Errorf("disable model offerings: %w", err)
	}
	return nil
}

func affectedFromLosingSupport(rows []sqlc.ListOfferingsLosingSupportRow) []AffectedOffering {
	out := make([]AffectedOffering, 0, len(rows))
	for _, row := range rows {
		out = append(out, AffectedOffering{
			RouteID:          row.RouteID,
			RouteName:        row.RouteName,
			RouteStatus:      row.RouteStatus,
			ModelID:          row.ModelID,
			PublicModelID:    row.PublicModelID,
			ModelDisplayName: row.ModelDisplayName,
			IngressProtocol:  row.IngressProtocol,
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
