// Package logfields 提供按请求传播的结构化日志字段。
//
// 设计动机：HTTP 访问日志在中间件最外层写出，但 user/project/api_key（认证中间件）
// 和 request_id/model/provider/channel（gateway）是在更内层才确定的。
// 通过在请求最外层安装一个可变 *Fields 指针并由下游填充，外层日志即可拿到全量字段。
//
// 脱敏原则：这里只承载稳定、非敏感的标识与路由维度。
// 绝不承载 API key 明文、credential、上游 Authorization、用户 prompt 等敏感内容。
package logfields

import (
	"context"
	"sync"
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
	projectID     int64
	apiKeyID      int64
	model         string
	provider      string
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
func (f *Fields) SetIdentity(userID int64, projectID int64, apiKeyID int64) {
	if f == nil {
		return
	}

	f.mu.Lock()
	defer f.mu.Unlock()
	f.userID = userID
	f.projectID = projectID
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

// SetRoute 记录本次请求最终命中的 model/provider/channel。
func (f *Fields) SetRoute(model string, provider string, channel string) {
	if f == nil {
		return
	}

	f.mu.Lock()
	defer f.mu.Unlock()
	f.model = model
	f.provider = provider
	f.channel = channel
}

// Attrs 返回已设置字段的 slog key/value 列表，未设置的字段不输出。
func (f *Fields) Attrs() []any {
	if f == nil {
		return nil
	}

	f.mu.Lock()
	defer f.mu.Unlock()

	attrs := make([]any, 0, 16)
	if f.correlationID != "" {
		attrs = append(attrs, "correlation_id", f.correlationID)
	}
	if f.requestID != "" {
		attrs = append(attrs, "request_id", f.requestID)
	}
	if f.userID != 0 {
		attrs = append(attrs, "user_id", f.userID)
	}
	if f.projectID != 0 {
		attrs = append(attrs, "project_id", f.projectID)
	}
	if f.apiKeyID != 0 {
		attrs = append(attrs, "api_key_id", f.apiKeyID)
	}
	if f.model != "" {
		attrs = append(attrs, "model", f.model)
	}
	if f.provider != "" {
		attrs = append(attrs, "provider", f.provider)
	}
	if f.channel != "" {
		attrs = append(attrs, "channel", f.channel)
	}

	return attrs
}

// SetIdentity 在 ctx 存在 Fields 时记录认证身份；否则静默忽略。
func SetIdentity(ctx context.Context, userID int64, projectID int64, apiKeyID int64) {
	if f, ok := FromContext(ctx); ok {
		f.SetIdentity(userID, projectID, apiKeyID)
	}
}

// SetRequestID 在 ctx 存在 Fields 时记录业务 request_id；否则静默忽略。
func SetRequestID(ctx context.Context, requestID string) {
	if f, ok := FromContext(ctx); ok {
		f.SetRequestID(requestID)
	}
}

// SetRoute 在 ctx 存在 Fields 时记录命中的 model/provider/channel；否则静默忽略。
func SetRoute(ctx context.Context, model string, provider string, channel string) {
	if f, ok := FromContext(ctx); ok {
		f.SetRoute(model, provider, channel)
	}
}
