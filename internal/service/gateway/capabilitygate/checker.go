// Package capabilitygate 提供 routing.CapabilityChecker 的服务层实现。
//
// 它读取 capability Layer 2/3 数据（model_capabilities / channel overrides），调用纯判定
// capability.Evaluate，并在内部发 metric 与结构化审计日志。core/routing 只依赖接口，
// 持有 store/metrics/logger 的实现放在服务层，保持 core 不反向依赖 platform 可观测设施。
//
// 当前为 observe 模式：只记录判定、不影响候选与返回（enforce 拒绝与三协议错误渲染见 TASK-12.08）。
package capabilitygate

import (
	"context"
	"log/slog"

	"github.com/ThankCat/unio-api/internal/core/capability"
	"github.com/ThankCat/unio-api/internal/core/routing"
)

// CapabilityStore 是闸门判定所需的能力数据只读面。
type CapabilityStore interface {
	ListModelCapabilities(ctx context.Context, modelID int64) ([]capability.ModelCapability, error)
	ListChannelOverrides(ctx context.Context, channelID int64) ([]capability.ChannelOverride, error)
}

// CapabilityMetrics 是闸门判定结果计数面（由 *metrics.Metrics 满足）。
type CapabilityMetrics interface {
	IncCapabilityCheck(protocol string, result string)
	IncCapabilityRequired(protocol string, capability string)
	IncCapabilityMissing(protocol string, capability string, scope string)
}

// Checker 是 routing.CapabilityChecker 的服务层实现。
type Checker struct {
	store   CapabilityStore
	metrics CapabilityMetrics
	logger  *slog.Logger
}

// NewChecker 构造 capability 闸门判定器。
//
// store 必填；metrics 为 nil 表示不计数；logger 为 nil 时回退 slog.Default()。
func NewChecker(store CapabilityStore, metrics CapabilityMetrics, logger *slog.Logger) *Checker {
	if store == nil {
		panic("capabilitygate: capability store is required")
	}
	if logger == nil {
		logger = slog.Default()
	}

	return &Checker{store: store, metrics: metrics, logger: logger}
}

// Check 评估一次 routing 的 capability 闸门结论；绝不返回 error，存储异常降级为 result=error 并放行。
func (c *Checker) Check(ctx context.Context, in routing.CapabilityCheckInput) routing.CapabilityObservation {
	observation := routing.CapabilityObservation{Required: in.Required.Keys()}

	// required 指标反映客户实际触发的能力分布，无论判定成败都计数。
	c.recordRequired(in.Protocol, observation.Required)

	modelCaps, err := c.store.ListModelCapabilities(ctx, in.ModelDBID)
	if err != nil {
		return c.degrade(in, observation, "list model capabilities", err)
	}

	channels := make([]capability.ChannelCaps, 0, len(in.ChannelIDs))
	for _, channelID := range in.ChannelIDs {
		overrides, err := c.store.ListChannelOverrides(ctx, channelID)
		if err != nil {
			return c.degrade(in, observation, "list channel overrides", err)
		}
		channels = append(channels, capability.ChannelCaps{ChannelID: channelID, Overrides: overrides})
	}

	// 闸门同时校验 key 存在性与 limited 档位约束（如 reasoning.effort），档位值由 ingress 推断抽取并经 routing 透传。
	eval := capability.Evaluate(modelCaps, channels, in.Required, in.Limits)

	observation.Result = eval.Result
	observation.Provisioned = eval.Provisioned
	observation.MissingModel = eval.MissingModel
	observation.MissingChannel = eval.MissingChannel

	c.record(in.Protocol, string(eval.Result))
	c.recordMissing(in.Protocol, observation.MissingModel, observation.MissingChannel)
	c.log(ctx, in, observation)

	return observation
}

// degrade 在读取能力数据失败时降级：记 result=error、写审计、放行（observe/enforce fail-open）。
func (c *Checker) degrade(in routing.CapabilityCheckInput, observation routing.CapabilityObservation, stage string, err error) routing.CapabilityObservation {
	observation.Result = capability.GateResultError
	c.record(in.Protocol, string(capability.GateResultError))
	c.logger.WarnContext(context.Background(), "capability gate degraded to fail-open",
		slog.String("ingress_protocol", protocolLabel(in.Protocol)),
		slog.Int64("model_db_id", in.ModelDBID),
		slog.String("stage", stage),
		slog.String("error", err.Error()),
	)

	return observation
}

func (c *Checker) record(protocol string, result string) {
	if c.metrics == nil {
		return
	}
	c.metrics.IncCapabilityCheck(protocolLabel(protocol), result)
}

// recordRequired 为每个推断出的所需能力计数一次。
func (c *Checker) recordRequired(protocol string, keys []capability.Key) {
	if c.metrics == nil {
		return
	}
	label := protocolLabel(protocol)
	for _, key := range keys {
		c.metrics.IncCapabilityRequired(label, string(key))
	}
}

// recordMissing 为模型层、channel 层缺失的能力分别按 scope 计数。
func (c *Checker) recordMissing(protocol string, missingModel, missingChannel []capability.Key) {
	if c.metrics == nil {
		return
	}
	label := protocolLabel(protocol)
	for _, key := range missingModel {
		c.metrics.IncCapabilityMissing(label, string(key), "model")
	}
	for _, key := range missingChannel {
		c.metrics.IncCapabilityMissing(label, string(key), "channel")
	}
}

// protocolLabel 把空协议归一成 unknown，避免空 label。
func protocolLabel(protocol string) string {
	if protocol == "" {
		return "unknown"
	}
	return protocol
}

// log 写 observe 审计：would-be 拒绝（model/channel unavailable）记 Warn 供观察期复核，其余记 Debug。
func (c *Checker) log(ctx context.Context, in routing.CapabilityCheckInput, observation routing.CapabilityObservation) {
	attrs := []any{
		slog.String("ingress_protocol", protocolLabel(in.Protocol)),
		slog.Int64("model_db_id", in.ModelDBID),
		slog.Int("candidate_channels", len(in.ChannelIDs)),
		slog.String("capability_result", string(observation.Result)),
		slog.Any("required_capabilities", keyStrings(observation.Required)),
	}

	switch observation.Result {
	case capability.GateResultModelUnavailable:
		attrs = append(attrs, slog.Any("missing_model_capabilities", keyStrings(observation.MissingModel)))
		c.logger.WarnContext(ctx, "capability gate observe: model capability unavailable", attrs...)
	case capability.GateResultChannelUnavailable:
		attrs = append(attrs, slog.Any("missing_channel_capabilities", keyStrings(observation.MissingChannel)))
		c.logger.WarnContext(ctx, "capability gate observe: channel capability unavailable", attrs...)
	default:
		c.logger.DebugContext(ctx, "capability gate observe", attrs...)
	}
}

func keyStrings(keys []capability.Key) []string {
	out := make([]string, len(keys))
	for i, key := range keys {
		out[i] = string(key)
	}
	return out
}
