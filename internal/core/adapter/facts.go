package adapter

import (
	"github.com/ThankCat/unio-gateway/internal/core/servicetier"
	"github.com/ThankCat/unio-gateway/internal/core/usage"
)

// FinishClass 是协议无关的稳定结束分类。
//
// 它用于跨模块业务逻辑（审计、metrics）；provider 原始 finish reason 另存于
// FinishFacts.RawReason，不直接驱动跨模块判断。
type FinishClass string

const (
	// FinishStop 表示正常结束。
	FinishStop FinishClass = "stop"

	// FinishLength 表示达到长度上限。
	FinishLength FinishClass = "length"

	// FinishToolUse 表示因调用工具而结束。
	FinishToolUse FinishClass = "tool_use"

	// FinishContentFilter 表示被内容安全策略截断。
	FinishContentFilter FinishClass = "content_filter"

	// FinishRefusal 表示模型拒绝回答。
	FinishRefusal FinishClass = "refusal"

	// FinishPause 表示需要客户继续的暂停（如 Anthropic pause_turn）。
	FinishPause FinishClass = "pause"

	// FinishOther 表示无法归入上述稳定类别的结束原因。
	FinishOther FinishClass = "other"
)

// FinishFacts 表示一次响应的结束事实。
type FinishFacts struct {
	// Class 是稳定结束分类。
	Class FinishClass

	// RawReason 是 provider 原始 finish reason，仅用于审计。
	RawReason string
}

// ResponseFacts 是 adapter 在同一次上游响应解析中产生的协议无关账务审计事实。
//
// 客户协议响应与 ResponseFacts 是同一次解析的两条输出轨道；settlement、recovery 和审计
// 只消费 ResponseFacts，不反向解析公开响应，也不重新解析上游 body。
type ResponseFacts struct {
	// UpstreamProtocol 是上游协议族（如 openai、anthropic）。
	UpstreamProtocol string

	// UpstreamResponseID 是 provider 返回的响应标识，与返回给客户的 response id 分列。
	UpstreamResponseID string

	// UpstreamModel 是 channel_models 映射后的 provider 实际模型。
	UpstreamModel string

	// Finish 是结束事实。
	Finish FinishFacts

	// Usage 是协议无关用量事实。
	Usage usage.Facts

	// UsageSource 是用量事实来源轨道，用于 settlement 幂等校验。
	UsageSource usage.Source

	// UsageMappingVersion 标记 usage 映射规则版本，便于历史账务复算与回归。
	UsageMappingVersion string

	// ServiceTier 是同一次上游响应解析得到的实际档位及 Standard 回落来源。
	// 它绝不从客户请求意图推断；Fast 价格缺失导致的最终回落由 settlement 在副本上补充。
	ServiceTier servicetier.Response

	// Metadata 是本次上游调用的可审计元信息（HTTP 状态码、上游 request id）。
	Metadata UpstreamMetadata

	// AccountUsage 是订阅账号 wire（Codex）随响应头带回的用量窗口事实；其他 wire 恒为 nil。
	// lifecycle 据此更新账号用量快照并执行阈值暂停，settlement 不消费它。
	AccountUsage *AccountUsageFacts
}

// AccountUsageFacts 是订阅账号的用量窗口观测（来自 x-codex-* 响应头）。
//
// primary = 5h 窗口、secondary = 7d 窗口（实测口径；Sub2API 源码把两者弄反，勿照抄其映射）。
// 任一窗口都可能缺失：Business Premium 无 5h 窗口、Enterprise/Edu 弹性额度无固定限，
// 消费方必须容忍 Present=false，不得按 0% 或 100% 臆断。
type AccountUsageFacts struct {
	PlanType  string
	Primary   AccountUsageWindowFacts
	Secondary AccountUsageWindowFacts
}

// AccountUsageWindowFacts 是单个用量窗口的观测值。
type AccountUsageWindowFacts struct {
	Present bool
	// UsedPercent 是窗口已用百分比（0-100，上游口径）。
	UsedPercent float64
	// WindowMinutes 是窗口长度（分钟）。
	WindowMinutes int64
	// ResetAtUnix 是窗口重置的绝对时刻（Unix 秒）。付费即时重置会让它变小，
	// 消费方必须按「最近一次观测覆盖」处理，不能只增不减。
	ResetAtUnix int64
	// ResetAfterSeconds 是距重置的相对秒数（观测时刻起算）。
	ResetAfterSeconds int64
}

// StreamOutcome 是流式调用结束后 adapter 返回给 lifecycle 的最终事实。
//
// Facts 为 nil 表示本次流没有产生可靠的最终事实（例如首包前失败或无 final usage 的风险路径），
// 由 lifecycle 按 release / risk_exposure 规则处理。
type StreamOutcome struct {
	Facts *ResponseFacts
}
