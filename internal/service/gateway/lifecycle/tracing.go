package lifecycle

import (
	"context"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	"github.com/ThankCat/unio-gateway/internal/platform/observability/tracing"
)

// StartGatewaySpan 启动一个 gateway 业务 span。
//
// 协议无关共享 lifecycle 能力；tracing 未启用时返回 no-op span，开销可忽略。
func StartGatewaySpan(ctx context.Context, name string, attrs ...attribute.KeyValue) (context.Context, trace.Span) {
	return tracing.Tracer().Start(ctx, name, trace.WithAttributes(attrs...))
}

// EndGatewaySpan 结束 span，并在出错时记录错误和错误状态。
// 只记录稳定错误身份，不把上游原始 body 或敏感内容写入 span。
func EndGatewaySpan(span trace.Span, err error) {
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, "")
	}

	span.End()
}

// UpstreamSpanAttrs 构造上游调用 span 的非敏感属性。
func UpstreamSpanAttrs(providerID int64, channelID int64, model string) []attribute.KeyValue {
	return []attribute.KeyValue{
		attribute.String("provider", MetricsID(providerID)),
		attribute.String("channel", MetricsID(channelID)),
		attribute.String("model", model),
	}
}

// EndSettlementSpan 结束结算 span；recovery 已接管的失败不视为 span 错误。
//
// 协议无关：OpenAI chat 与 Anthropic messages 共享同一套 settlement recovery 语义
// （见 IsChatSettlementRecoveryScheduled）；结算失败但 recovery job 已接管时不应让 span
// 看起来是普通错误，否则会污染 SLO/告警。
func EndSettlementSpan(span trace.Span, err error) {
	if err != nil && !IsChatSettlementRecoveryScheduled(err) {
		span.RecordError(err)
		span.SetStatus(codes.Error, "")
	}

	span.End()
}
