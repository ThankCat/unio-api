// Package logfields 提供按请求传播的结构化日志字段。
//
// 设计动机：HTTP 访问日志在中间件最外层写出，但 user/api_key（认证中间件）
// 和 request_id/model/route/provider/channel（gateway）是在更内层才确定的。
// 通过在请求最外层安装一个可变 *Fields 指针并由下游填充，外层日志即可拿到全量字段。
//
// 字段语义：
//   - model：API 模型字符串（CreateRequest，来自请求体 models.model_id）
//   - model_id：模型表数字主键 models.id（CreateAttempt）
//   - route_id：线路数字 ID（CreateRequest，API Key 绑定）
//   - router：线路名 routes.name（CreateAttempt，随候选透传）
//   - provider_id / provider：服务商数字 ID / providers.slug（CreateAttempt）
//   - channel_id / channel：渠道数字 ID / channels.name（CreateAttempt）
//
// 脱敏原则：这里只承载稳定、非敏感的标识与路由维度。
// 绝不承载 API key 明文、credential、上游 Authorization、用户 prompt 等敏感内容。
package logfields

import (
	"context"
	"sync"

	"go.uber.org/zap"
)

// ctxKey 是 Fields 在 context 中的私有 key。
type ctxKey struct{}

// Fields 是一次请求共享的结构化日志字段集合。
// 它在请求最外层创建，由下游中间件和 service 并发安全地填充。
type Fields struct {
	mu sync.Mutex

	traceID       string
	requestID     string
	attemptID     int64
	hasAttemptID  bool
	userID        int64
	apiKeyID      int64
	model         string
	modelID       int64
	hasModelID    bool
	routeID       int64
	hasRouteID    bool
	router        string
	providerID    int64
	hasProviderID bool
	provider      string
	channelID     int64
	hasChannelID  bool
	channel       string

	upstreamTTFTMs        int64
	hasUpstreamTTFT       bool
	gatewayTTFTMs         int64
	hasGatewayTTFT        bool
	attemptCount          int
	hasAttemptCount       bool
	fallbackCount         int
	hasFallbackCount      bool
	capacityWaitMs        int64
	hasCapacityWait       bool
	stickyAction          string
	deliveryStatus        string
	settlementStatus      string
	inputTokens           int64
	cacheReadInputTokens  int64
	cacheWriteInputTokens int64
	outputTokens          int64
	reasoningTokens       int64
	totalTokens           int64
	hasUsage              bool
	chargedAmount         string
	currency              string
	errorCode             string
	completionLevel       string
	jsonDecode            JSONDecodeSummary
	hasJSONDecode         bool
}

// NewContext 在 ctx 中安装一个携带 traceID 的 Fields，并返回该 Fields 指针。
func NewContext(ctx context.Context, traceID string) (context.Context, *Fields) {
	f := &Fields{traceID: traceID}
	return context.WithValue(ctx, ctxKey{}, f), f
}

// FromContext 返回 ctx 中的 Fields；不存在时返回 (nil, false)。
func FromContext(ctx context.Context) (*Fields, bool) {
	f, ok := ctx.Value(ctxKey{}).(*Fields)
	return f, ok
}

// ContextZapFields 返回当前请求已传播的结构化字段；不存在时返回 nil。
func ContextZapFields(ctx context.Context) []zap.Field {
	if fields, ok := FromContext(ctx); ok {
		return fields.ZapFields()
	}
	return nil
}

// SetIdentity 记录认证身份字段。
func (f *Fields) SetIdentity(userID int64, apiKeyID int64) {
	if f == nil {
		return
	}

	f.mu.Lock()
	defer f.mu.Unlock()
	f.userID = userID
	f.apiKeyID = apiKeyID
}

// SetRequestID 记录业务 request_records.request_id。
func (f *Fields) SetRequestID(requestID string) {
	if f == nil {
		return
	}

	f.mu.Lock()
	defer f.mu.Unlock()
	f.requestID = requestID
}

func (f *Fields) SetAttemptID(attemptID int64) {
	if f == nil {
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.attemptID = attemptID
	f.hasAttemptID = attemptID != 0
}

func (f *Fields) SetUpstreamTTFT(value int64) {
	if f == nil {
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.upstreamTTFTMs = value
	f.hasUpstreamTTFT = true
}

func (f *Fields) SetGatewayTTFT(value int64) {
	if f == nil {
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.gatewayTTFTMs = value
	f.hasGatewayTTFT = true
}

func (f *Fields) SetAttemptCount(value int) {
	if f == nil {
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.attemptCount = value
	f.hasAttemptCount = true
}

func (f *Fields) IncrementFallbackCount() {
	if f == nil {
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.fallbackCount++
	f.hasFallbackCount = true
}

func (f *Fields) SetFallbackCount(value int) {
	if f == nil {
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.fallbackCount = value
	f.hasFallbackCount = true
}

func (f *Fields) SetCapacityWait(value int64) {
	if f == nil {
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.capacityWaitMs = value
	f.hasCapacityWait = true
}

func (f *Fields) SetStickyAction(value string) {
	if f == nil || value == "" {
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.stickyAction = value
}

func (f *Fields) SetDeliveryStatus(value string) {
	if f == nil || value == "" {
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.deliveryStatus = value
}

func (f *Fields) SetSettlementStatus(value string) {
	if f == nil || value == "" {
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.settlementStatus = value
}

type UsageSummary struct {
	InputTokens           int64
	CacheReadInputTokens  int64
	CacheWriteInputTokens int64
	OutputTokens          int64
	ReasoningTokens       int64
	TotalTokens           int64
	ChargedAmount         string
	Currency              string
}

// JSONDecodeSummary 是公开 Gateway JSON 解码拒绝写入请求完成日志的脱敏诊断。
type JSONDecodeSummary struct {
	Reason           string
	Kind             string
	Field            string
	Offset           int64
	BytesRead        int64
	ContentLength    int64
	BodyLimit        int64
	CompletionStatus string
	ContentEncoding  string
	TransferEncoding string
	HTTPVersion      string
	UserAgent        string
}

// SetJSONDecodeSummary 记录一次 invalid JSON 拒绝的脱敏诊断。
func (f *Fields) SetJSONDecodeSummary(summary JSONDecodeSummary) {
	if f == nil {
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.jsonDecode = summary
	f.hasJSONDecode = true
}

func (f *Fields) SetUsageSummary(summary UsageSummary) {
	if f == nil {
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.inputTokens = summary.InputTokens
	f.cacheReadInputTokens = summary.CacheReadInputTokens
	f.cacheWriteInputTokens = summary.CacheWriteInputTokens
	f.outputTokens = summary.OutputTokens
	f.reasoningTokens = summary.ReasoningTokens
	f.totalTokens = summary.TotalTokens
	f.chargedAmount = summary.ChargedAmount
	f.currency = summary.Currency
	f.hasUsage = true
}

func (f *Fields) SetCompletion(level string, errorCode string) {
	if f == nil {
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if level != "" {
		f.completionLevel = level
	}
	if errorCode != "" {
		f.errorCode = errorCode
	}
}

func (f *Fields) CompletionLevel() string {
	if f == nil {
		return ""
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.completionLevel
}

// SetModel 记录请求目标模型（API 模型字符串）。
func (f *Fields) SetModel(model string) {
	if f == nil {
		return
	}

	f.mu.Lock()
	defer f.mu.Unlock()
	f.model = model
}

// SetModelID 记录模型表数字主键。
func (f *Fields) SetModelID(modelID int64) {
	if f == nil {
		return
	}

	f.mu.Lock()
	defer f.mu.Unlock()
	f.modelID = modelID
	f.hasModelID = true
}

// SetRouteID 记录线路数字 ID（产品语义上的「线路」，非渠道）。
func (f *Fields) SetRouteID(routeID int64) {
	if f == nil {
		return
	}

	f.mu.Lock()
	defer f.mu.Unlock()
	f.routeID = routeID
	f.hasRouteID = true
}

// SetRouter 记录线路名 routes.name。
func (f *Fields) SetRouter(router string) {
	if f == nil {
		return
	}

	f.mu.Lock()
	defer f.mu.Unlock()
	f.router = router
}

// UpstreamAttempt 是一次上游尝试写入 access log 的路由维度。
type UpstreamAttempt struct {
	ModelID    int64
	Router     string
	ProviderID int64
	Provider   string // providers.slug
	ChannelID  int64
	Channel    string // channels.name
}

// SetUpstreamAttempt 记录当前（或最后一次）上游尝试的模型/线路名/服务商/渠道维度。
func (f *Fields) SetUpstreamAttempt(a UpstreamAttempt) {
	if f == nil {
		return
	}

	f.mu.Lock()
	defer f.mu.Unlock()
	if a.ModelID != 0 {
		f.modelID = a.ModelID
		f.hasModelID = true
	}
	if a.Router != "" {
		f.router = a.Router
	}
	f.providerID = a.ProviderID
	f.hasProviderID = a.ProviderID != 0
	f.provider = a.Provider
	f.channelID = a.ChannelID
	f.hasChannelID = a.ChannelID != 0
	f.channel = a.Channel
}

// ZapFields 返回已设置字段的 zap.Field 列表，未设置的字段不输出。
func (f *Fields) ZapFields() []zap.Field {
	if f == nil {
		return nil
	}

	f.mu.Lock()
	defer f.mu.Unlock()

	fields := make([]zap.Field, 0, 16)
	if f.traceID != "" {
		fields = append(fields, zap.String("trace_id", f.traceID))
	}
	if f.requestID != "" {
		fields = append(fields, zap.String("request_id", f.requestID))
	}
	if f.hasAttemptID {
		fields = append(fields, zap.Int64("attempt_id", f.attemptID))
	}
	if f.userID != 0 {
		fields = append(fields, zap.Int64("user_id", f.userID))
	}
	if f.apiKeyID != 0 {
		fields = append(fields, zap.Int64("api_key_id", f.apiKeyID))
	}
	if f.model != "" {
		fields = append(fields, zap.String("model", f.model))
	}
	if f.hasModelID {
		fields = append(fields, zap.Int64("model_id", f.modelID))
	}
	if f.hasRouteID {
		fields = append(fields, zap.Int64("route_id", f.routeID))
	}
	if f.router != "" {
		fields = append(fields, zap.String("route_name", f.router))
	}
	if f.hasProviderID {
		fields = append(fields, zap.Int64("provider_id", f.providerID))
	}
	if f.provider != "" {
		fields = append(fields, zap.String("provider_slug", f.provider))
	}
	if f.hasChannelID {
		fields = append(fields, zap.Int64("channel_id", f.channelID))
	}
	if f.channel != "" {
		fields = append(fields, zap.String("channel_name", f.channel))
	}
	if f.hasUpstreamTTFT {
		fields = append(fields, zap.Int64("upstream_ttft_ms", f.upstreamTTFTMs))
	}
	if f.hasGatewayTTFT {
		fields = append(fields, zap.Int64("gateway_ttft_ms", f.gatewayTTFTMs))
	}
	if f.hasAttemptCount {
		fields = append(fields, zap.Int("attempt_count", f.attemptCount))
	}
	if f.hasFallbackCount {
		fields = append(fields, zap.Int("fallback_count", f.fallbackCount))
	}
	if f.hasCapacityWait {
		fields = append(fields, zap.Int64("capacity_wait_ms", f.capacityWaitMs))
	}
	if f.stickyAction != "" {
		fields = append(fields, zap.String("sticky_action", f.stickyAction))
	}
	if f.deliveryStatus != "" {
		fields = append(fields, zap.String("delivery_status", f.deliveryStatus))
	}
	if f.settlementStatus != "" {
		fields = append(fields, zap.String("settlement_status", f.settlementStatus))
	}
	if f.hasUsage {
		fields = append(fields,
			zap.Int64("input_tokens", f.inputTokens),
			zap.Int64("cache_read_input_tokens", f.cacheReadInputTokens),
			zap.Int64("cache_write_input_tokens", f.cacheWriteInputTokens),
			zap.Int64("output_tokens", f.outputTokens),
			zap.Int64("reasoning_tokens", f.reasoningTokens),
			zap.Int64("total_tokens", f.totalTokens),
		)
		if f.chargedAmount != "" {
			fields = append(fields, zap.String("charged_amount", f.chargedAmount))
		}
		if f.currency != "" {
			fields = append(fields, zap.String("currency", f.currency))
		}
	}
	if f.errorCode != "" {
		fields = append(fields, zap.String("error_code", f.errorCode))
	}
	if f.hasJSONDecode {
		reason := f.jsonDecode.Reason
		if reason == "" {
			reason = "invalid_json"
		}
		fields = append(fields,
			zap.String("rejection_reason", reason),
			zap.String("request_body_error_kind", f.jsonDecode.Kind),
			zap.Int64("body_bytes_read", f.jsonDecode.BytesRead),
		)
		if reason == "invalid_json" {
			fields = append(fields, zap.String("decode_error_kind", f.jsonDecode.Kind))
		}
		if f.jsonDecode.Field != "" {
			fields = append(fields, zap.String("json_field", f.jsonDecode.Field))
		}
		if f.jsonDecode.Offset > 0 {
			fields = append(fields, zap.Int64("json_offset", f.jsonDecode.Offset))
		}
		if f.jsonDecode.ContentLength >= 0 {
			fields = append(fields, zap.Int64("content_length", f.jsonDecode.ContentLength))
		}
		if f.jsonDecode.BodyLimit > 0 {
			fields = append(fields, zap.Int64("request_body_limit", f.jsonDecode.BodyLimit))
		}
		if f.jsonDecode.CompletionStatus != "" {
			fields = append(fields, zap.String("body_completion_status", f.jsonDecode.CompletionStatus))
		}
		if f.jsonDecode.ContentEncoding != "" {
			fields = append(fields, zap.String("content_encoding", f.jsonDecode.ContentEncoding))
		}
		if f.jsonDecode.TransferEncoding != "" {
			fields = append(fields, zap.String("transfer_encoding", f.jsonDecode.TransferEncoding))
		}
		if f.jsonDecode.HTTPVersion != "" {
			fields = append(fields, zap.String("http_version", f.jsonDecode.HTTPVersion))
		}
		if f.jsonDecode.UserAgent != "" {
			fields = append(fields, zap.String("user_agent", f.jsonDecode.UserAgent))
		}
	}

	return fields
}

// SetIdentity 在 ctx 存在 Fields 时记录认证身份；否则静默忽略。
func SetIdentity(ctx context.Context, userID int64, apiKeyID int64) {
	if f, ok := FromContext(ctx); ok {
		f.SetIdentity(userID, apiKeyID)
	}
}

// SetRequestID 在 ctx 存在 Fields 时记录业务 request_id；否则静默忽略。
func SetRequestID(ctx context.Context, requestID string) {
	if f, ok := FromContext(ctx); ok {
		f.SetRequestID(requestID)
	}
}

func SetAttemptID(ctx context.Context, attemptID int64) {
	if f, ok := FromContext(ctx); ok {
		f.SetAttemptID(attemptID)
	}
}

func SetUpstreamTTFT(ctx context.Context, value int64) {
	if f, ok := FromContext(ctx); ok {
		f.SetUpstreamTTFT(value)
	}
}

func SetGatewayTTFT(ctx context.Context, value int64) {
	if f, ok := FromContext(ctx); ok {
		f.SetGatewayTTFT(value)
	}
}

func SetAttemptCount(ctx context.Context, value int) {
	if f, ok := FromContext(ctx); ok {
		f.SetAttemptCount(value)
	}
}

func IncrementFallbackCount(ctx context.Context) {
	if f, ok := FromContext(ctx); ok {
		f.IncrementFallbackCount()
	}
}

func SetFallbackCount(ctx context.Context, value int) {
	if f, ok := FromContext(ctx); ok {
		f.SetFallbackCount(value)
	}
}

func SetCapacityWait(ctx context.Context, value int64) {
	if f, ok := FromContext(ctx); ok {
		f.SetCapacityWait(value)
	}
}

func SetStickyAction(ctx context.Context, value string) {
	if f, ok := FromContext(ctx); ok {
		f.SetStickyAction(value)
	}
}

func SetDeliveryStatus(ctx context.Context, value string) {
	if f, ok := FromContext(ctx); ok {
		f.SetDeliveryStatus(value)
	}
}

func SetSettlementStatus(ctx context.Context, value string) {
	if f, ok := FromContext(ctx); ok {
		f.SetSettlementStatus(value)
	}
}

func SetUsageSummary(ctx context.Context, summary UsageSummary) {
	if f, ok := FromContext(ctx); ok {
		f.SetUsageSummary(summary)
	}
}

// SetJSONDecodeSummary 在 ctx 存在 Fields 时记录 invalid JSON 脱敏诊断。
func SetJSONDecodeSummary(ctx context.Context, summary JSONDecodeSummary) {
	if f, ok := FromContext(ctx); ok {
		f.SetJSONDecodeSummary(summary)
	}
}

func SetCompletion(ctx context.Context, level string, errorCode string) {
	if f, ok := FromContext(ctx); ok {
		f.SetCompletion(level, errorCode)
	}
}

// SetModel 在 ctx 存在 Fields 时记录目标模型；否则静默忽略。
func SetModel(ctx context.Context, model string) {
	if f, ok := FromContext(ctx); ok {
		f.SetModel(model)
	}
}

// SetRouteID 在 ctx 存在 Fields 时记录线路 ID；否则静默忽略。
func SetRouteID(ctx context.Context, routeID int64) {
	if f, ok := FromContext(ctx); ok {
		f.SetRouteID(routeID)
	}
}

// SetUpstreamAttempt 在 ctx 存在 Fields 时记录上游尝试维度；否则静默忽略。
func SetUpstreamAttempt(ctx context.Context, a UpstreamAttempt) {
	if f, ok := FromContext(ctx); ok {
		f.SetUpstreamAttempt(a)
	}
}
