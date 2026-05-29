// Package tracing 提供 OpenTelemetry trace 的初始化、全局装配和便捷 Tracer 获取。
//
// 设计原则：
//   - 默认关闭（opt-in）。未启用或未配置 endpoint 时不安装全局 provider，
//     otel 默认返回 no-op tracer，全链路的 Start/End 调用都是零成本空操作。
//   - 启用时使用 OTLP HTTP exporter，并设置 W3C TraceContext + Baggage 传播器，
//     使 correlation 在 HTTP 入站、gateway、adapter 之间通过 context 串联。
//   - 不在 span 上记录用户 prompt、API key、credential 等敏感内容。
package tracing

import (
	"context"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"

	"github.com/ThankCat/unio-api/internal/platform/failure"
)

// tracerName 是本项目所有 span 使用的 instrumentation scope 名称。
const tracerName = "github.com/ThankCat/unio-api"

// Options 保存初始化 trace provider 所需的参数。
type Options struct {
	// Enabled 为 false 时不安装全局 provider，全链路 tracing 为 no-op。
	Enabled bool

	// Endpoint 是 OTLP HTTP collector 地址（host:port），为空时同样视为关闭。
	Endpoint string

	// Insecure 为 true 时使用明文 HTTP 连接 collector（本地/内网常用）。
	Insecure bool

	// ServiceName 写入 resource，用于在后端区分服务。
	ServiceName string

	// SampleRatio 是基于 TraceID 的采样比例，1 表示全采样。
	SampleRatio float64
}

// Provider 持有底层 SDK tracer provider，未启用时 tp 为 nil。
type Provider struct {
	tp *sdktrace.TracerProvider
}

// Setup 根据 opts 初始化全局 trace provider 和传播器。
//
// 未启用或未配置 endpoint 时返回一个空 Provider，Shutdown 为安全空操作，
// 且不修改 otel 全局状态（保持默认 no-op tracer）。
func Setup(ctx context.Context, opts Options) (*Provider, error) {
	if !opts.Enabled || opts.Endpoint == "" {
		return &Provider{}, nil
	}

	exporterOpts := []otlptracehttp.Option{otlptracehttp.WithEndpoint(opts.Endpoint)}
	if opts.Insecure {
		exporterOpts = append(exporterOpts, otlptracehttp.WithInsecure())
	}

	exporter, err := otlptracehttp.New(ctx, exporterOpts...)
	if err != nil {
		return nil, failure.Wrap(
			failure.CodeObservabilityTracerInitFailed,
			err,
			failure.WithMessage("create otlp trace exporter"),
		)
	}

	res := resource.NewSchemaless(attribute.String("service.name", opts.ServiceName))

	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exporter),
		sdktrace.WithResource(res),
		sdktrace.WithSampler(sdktrace.ParentBased(sdktrace.TraceIDRatioBased(opts.SampleRatio))),
	)

	otel.SetTracerProvider(tp)
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{},
		propagation.Baggage{},
	))

	return &Provider{tp: tp}, nil
}

// Shutdown 刷新并关闭 trace provider；未启用时安全返回 nil。
func (p *Provider) Shutdown(ctx context.Context) error {
	if p == nil || p.tp == nil {
		return nil
	}

	return p.tp.Shutdown(ctx)
}

// Tracer 返回项目统一 Tracer；未安装全局 provider 时返回 otel 默认 no-op tracer。
func Tracer() trace.Tracer {
	return otel.Tracer(tracerName)
}
