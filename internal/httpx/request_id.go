package httpx

import "context"

// HeaderRequestID 是用于传递请求 ID 的 HTTP header 名称。
const HeaderRequestID = "X-Request-ID"

// requestIDKey 用作 context 的私有 key，避免和其他 package 的 key 冲突。
type requestIDKey struct{}

// ContextWithRequestID 返回一个携带 requestID 的新 context。
func ContextWithRequestID(ctx context.Context, requestID string) context.Context {
	return context.WithValue(ctx, requestIDKey{}, requestID)
}

// RequestID 从 ctx 中读取请求 ID；如果不存在，则返回空字符串。
func RequestID(ctx context.Context) string {
	requestID := ctx.Value(requestIDKey{}).(string)
	return requestID
}
