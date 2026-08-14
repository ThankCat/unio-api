// Package supply 实现 ADR-0019 的供给影响预览与显式联合操作基础设施：
//
//   - 结构支撑串行化：所有改变某个 Model 结构支撑事实的写事务，先按 model_id 升序
//     FOR UPDATE 锁定 Model 行，再在锁内计算影响或校验支撑；
//   - 影响计算：状态变化前反查可能失去配置支撑或运行候选的 enabled Offering；
//   - 影响指纹：由影响预览的相关状态集合计算 sha256 指纹，确认请求必须携带一致指纹；
//   - 确认契约：需要二次确认的操作返回 ConfirmationRequired（HTTP 层渲染为 409），
//     确认请求必须明确携带需要跨层修改的 Offering；空选择合法且是默认值。
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

// route_model_offerings.disabled_reason 受控枚举。ADR-0018 遗留原因仅用于历史解释，
// 新联合操作使用 manual_unselected、binding_disabled 或 model_delisted。
const (
	// ReasonManualUnselected 管理员在 Route 中主动取消勾选该 Model 与协议组合。
	ReasonManualUnselected = "manual_unselected"
	// ReasonModelDisabled 旧版 Model 级联停用留下的历史原因。
	ReasonModelDisabled = "model_disabled"
	// ReasonBindingDisabled Route 内最后一条同模型、同协议 enabled Binding 被停用或解除。
	ReasonBindingDisabled = "binding_disabled"
	// ReasonChannelDisabled 旧版 Channel 级联停用留下的历史原因。
	ReasonChannelDisabled = "channel_disabled"
	// ReasonRouteChannelRemoved 旧版 Route 换池级联留下的历史原因。
	ReasonRouteChannelRemoved = "route_channel_removed"
	// ReasonChannelProtocolChanged 旧版 Channel 协议变更级联留下的历史原因。
	ReasonChannelProtocolChanged = "channel_protocol_changed"
	// ReasonMigrationBackfill 一次切换迁移按当时事实回填。
	ReasonMigrationBackfill = "migration_backfill"
	// ReasonModelDelisted 管理员通过全局下架联合操作明确停止该 Offering。
	ReasonModelDelisted = "model_delisted"
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
	// KeptResult 是只执行目标层操作、保留 Offering 时对新请求的预期结果。
	KeptResult string
	// SelectedResult 是同时停止该 Offering 时对新请求的预期结果。
	SelectedResult string
}

// OfferingSelection 是联合操作中由管理员明确选择的一条 Offering。
type OfferingSelection struct {
	RouteID         int64
	ModelID         int64
	IngressProtocol string
}

// Impact 是一次供给写操作在 Model 锁内计算出的影响预览。AffectedOfferings 只表示
// 潜在影响范围，不表示这些 Offering 会被自动修改。
type Impact struct {
	// Kind 标识操作类型，参与指纹计算，避免同一影响集合跨操作复用指纹。
	Kind string
	// AffectedOfferings 是本次操作可能改变客户结果的 Offering 集合。
	AffectedOfferings []AffectedOffering
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
	return len(im.AffectedOfferings) > 0
}

// Fingerprint 由影响预览的相关状态集合计算稳定指纹。指纹只覆盖预览内容本身：
// 并发变化若不改变影响集合，则无需重新确认；改变影响集合必然改变指纹。
func (im Impact) Fingerprint() string {
	lines := make([]string, 0, len(im.AffectedOfferings))
	for _, ao := range im.AffectedOfferings {
		lines = append(lines, fmt.Sprintf(
			"offering|%d|%s|%d|%s|%s|%s|%s",
			ao.RouteID, ao.RouteStatus, ao.ModelID, ao.IngressProtocol, ao.PublicModelID,
			ao.KeptResult, ao.SelectedResult,
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

// Confirmation 是写请求携带的二次确认参数。SelectedOfferings 为空表示只修改目标层。
type Confirmation struct {
	Confirm             bool
	ExpectedFingerprint string
	SelectedOfferings   []OfferingSelection
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
	allowed := make(map[offeringKey]struct{}, len(im.AffectedOfferings))
	for _, ao := range im.AffectedOfferings {
		allowed[offeringKey{ao.RouteID, ao.ModelID, ao.IngressProtocol}] = struct{}{}
	}
	for _, selected := range c.SelectedOfferings {
		if _, ok := allowed[offeringKey{selected.RouteID, selected.ModelID, selected.IngressProtocol}]; !ok {
			return &ConfirmationRequired{Code: code, Message: message, Impact: im}
		}
	}
	return nil
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
		RemainingEnabledBindings: remaining,
	}, nil
}

// ChannelImpact 在锁内计算暂停 Channel 流量后可能失去最后基础运行候选的 Offering。
// Binding、Model 与 Offering 配置行均不改写。
func ChannelImpact(ctx context.Context, q *sqlc.Queries, channelID int64) (Impact, error) {
	rows, err := q.ListOfferingsLosingRuntimeChannel(ctx, channelID)
	if err != nil {
		return Impact{}, fmt.Errorf("compute channel route impact: %w", err)
	}
	return Impact{
		Kind:              "channel_disable",
		AffectedOfferings: affectedFromLosingRuntime(rows),
	}, nil
}

// ModelImpact 在锁内计算全局暂停 Model 的客户影响：全部 enabled Offering 与供给范围统计。
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
			KeptResult:       "404",
			SelectedResult:   "404",
		})
	}
	return Impact{
		Kind:              "model_disable",
		AffectedOfferings: affected,
		EnabledBindings:   counts.EnabledBindings,
		Channels:          counts.Channels,
		Providers:         counts.Providers,
	}, nil
}

// DisableSelectedOfferings 只把管理员明确选择且属于最新影响范围的 Offering 置 disabled。
// 必须与影响计算处于同一事务、同一 Model 锁内；锁保证集合在提交前不会漂移。
func DisableSelectedOfferings(ctx context.Context, q *sqlc.Queries, im Impact, c Confirmation, reason string) error {
	available := make(map[offeringKey]AffectedOffering, len(im.AffectedOfferings))
	for _, ao := range im.AffectedOfferings {
		available[offeringKey{ao.RouteID, ao.ModelID, ao.IngressProtocol}] = ao
	}
	seen := make(map[offeringKey]struct{}, len(c.SelectedOfferings))
	for _, selected := range c.SelectedOfferings {
		key := offeringKey{selected.RouteID, selected.ModelID, selected.IngressProtocol}
		if _, duplicate := seen[key]; duplicate {
			continue
		}
		seen[key] = struct{}{}
		ao, ok := available[key]
		if !ok {
			return fmt.Errorf("selected offering is outside the current impact")
		}
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
			KeptResult:       "503",
			SelectedResult:   "404",
		})
	}
	return out
}

func affectedFromLosingRuntime(rows []sqlc.ListOfferingsLosingRuntimeChannelRow) []AffectedOffering {
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
			KeptResult:       "503",
			SelectedResult:   "404",
		})
	}
	return out
}

type offeringKey struct {
	routeID  int64
	modelID  int64
	protocol string
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
