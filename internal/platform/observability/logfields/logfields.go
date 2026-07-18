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

	correlationID string
	requestID     string
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
}

// NewContext 在 ctx 中安装一个携带 correlationID 的 Fields，并返回该 Fields 指针。
func NewContext(ctx context.Context, correlationID string) (context.Context, *Fields) {
	f := &Fields{correlationID: correlationID}
	return context.WithValue(ctx, ctxKey{}, f), f
}

// FromContext 返回 ctx 中的 Fields；不存在时返回 (nil, false)。
func FromContext(ctx context.Context) (*Fields, bool) {
	f, ok := ctx.Value(ctxKey{}).(*Fields)
	return f, ok
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
	if f.correlationID != "" {
		fields = append(fields, zap.String("correlation_id", f.correlationID))
	}
	if f.requestID != "" {
		fields = append(fields, zap.String("request_id", f.requestID))
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
		fields = append(fields, zap.String("router", f.router))
	}
	if f.hasProviderID {
		fields = append(fields, zap.Int64("provider_id", f.providerID))
	}
	if f.provider != "" {
		fields = append(fields, zap.String("provider", f.provider))
	}
	if f.hasChannelID {
		fields = append(fields, zap.Int64("channel_id", f.channelID))
	}
	if f.channel != "" {
		fields = append(fields, zap.String("channel", f.channel))
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
