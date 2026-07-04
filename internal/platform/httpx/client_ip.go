package httpx

import (
	"context"
	"net"
	"net/http"
	"strings"
)

// clientIPKey 用作 context 的私有 key。
type clientIPKey struct{}

// ContextWithClientIP 返回携带客户端 IP 的新 context。
func ContextWithClientIP(ctx context.Context, ip string) context.Context {
	return context.WithValue(ctx, clientIPKey{}, ip)
}

// ClientIP 从 ctx 读取客户端 IP；不存在返回空字符串。
func ClientIP(ctx context.Context) string {
	ip, ok := ctx.Value(clientIPKey{}).(string)
	if !ok {
		return ""
	}
	return ip
}

// ExtractClientIP 从请求提取客户端来源 IP：优先 X-Forwarded-For 首个、其次 X-Real-IP，
// 最后回退 RemoteAddr（去端口）。仅取 IP，不做地理解析。
//
// 注意：X-Forwarded-For 可被客户端伪造，仅作展示/审计用，不用于任何安全决策。
func ExtractClientIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		// 取第一个（最靠近客户端的）地址。
		first := strings.TrimSpace(strings.Split(xff, ",")[0])
		if first != "" {
			return first
		}
	}
	if xr := strings.TrimSpace(r.Header.Get("X-Real-IP")); xr != "" {
		return xr
	}
	if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		return host
	}
	return r.RemoteAddr
}
