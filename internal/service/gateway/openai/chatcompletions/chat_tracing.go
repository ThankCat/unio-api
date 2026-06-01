package gateway

import (
	"context"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	"github.com/ThankCat/unio-api/internal/platform/observability/tracing"
)

// startGatewaySpan 启动一个 gateway 业务 span。
// tracing 未启用时返回 no-op span，开销可忽略。
func startGatewaySpan(ctx context.Context, name string, attrs ...attribute.KeyValue) (context.Context, trace.Span) {
	return tracing.Tracer().Start(ctx, name, trace.WithAttributes(attrs...))
}

// endGatewaySpan 结束 span，并在出错时记录错误和错误状态。
// 只记录稳定错误身份，不把上游原始 body 或敏感内容写入 span。
func endGatewaySpan(span trace.Span, err error) {
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, "")
	}

	span.End()
}

// endSettlementSpan 结束结算 span；recovery 已接管的失败不视为 span 错误。
func endSettlementSpan(span trace.Span, err error) {
	if err != nil && !IsChatSettlementRecoveryScheduled(err) {
		span.RecordError(err)
		span.SetStatus(codes.Error, "")
	}

	span.End()
}

// upstreamSpanAttrs 构造上游调用 span 的非敏感属性。
func upstreamSpanAttrs(providerID int64, channelID int64, model string) []attribute.KeyValue {
	return []attribute.KeyValue{
		attribute.String("provider", metricsID(providerID)),
		attribute.String("channel", metricsID(channelID)),
		attribute.String("model", model),
	}
}
