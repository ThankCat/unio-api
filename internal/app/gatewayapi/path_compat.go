package gatewayapi

import (
	"net/http"
	"strings"
)

// v1CompatExemptPaths 是不做版本前缀规范化的基础设施上游源站（非 /v1 API 面）。
var v1CompatExemptPaths = map[string]struct{}{
	"/":        {},
	"/healthz": {},
	"/readyz":  {},
	"/metrics": {},
}

// v1PathCompat 让网关对客户端 base_url 的版本前缀更宽容。所有 API 上游源站都挂在 /v1 下，
// 但用户常把 base_url 配错：配成带 /v1（客户端 SDK 再拼 /v1/messages → /v1/v1/messages，404），
// 或完全不带（拼出 /messages，404）。本中间件把路径规范化为「恰好一个 /v1 前缀」：
//   - 折叠任意多余的前导 /v1：/v1/v1/messages、/v1/v1/v1/responses → /v1/messages、/v1/responses
//   - 为缺前缀的「已知上游源站」补齐：/messages → /v1/messages、/chat/completions → /v1/chat/completions
//
// 折叠对所有已在 /v1 面内的路径无条件生效（安全）；补前缀仅对已知上游源站生效，故根级未知路径
// （如 /not-found）保持原样，仍返回干净的 404，不会被误吞进 /v1 面而变成 401。
// 基础设施上游源站（/healthz、/metrics、根 /）豁免。
//
// 为便于运维定位「客户端配错了」，本中间件不就地改动原始请求：改写后的路径放在请求副本上传给
// 下游路由，原始 *http.Request 保持不变，故访问日志/指标仍记录客户端真实发来的路径。
func v1PathCompat(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		canonical := canonicalizeV1Path(r.URL.Path)
		if canonical == r.URL.Path {
			next.ServeHTTP(w, r)
			return
		}

		proxied := *r
		rewritten := *r.URL
		rewritten.Path = canonical
		// 清空 RawPath：Path 已是规范化后的解码值，chi 会据 Path 路由并重解析路径参数。
		rewritten.RawPath = ""
		proxied.URL = &rewritten
		next.ServeHTTP(w, &proxied)
	})
}

// canonicalizeV1Path 把请求路径规范化为「恰好一个 /v1 前缀」；豁免路径原样返回。
//   - 已带 /v1 前缀：剥掉所有前导 /v1 段再补回单个 /v1（折叠多余前缀），无论后段是否已知。
//   - 未带 /v1 前缀：仅当命中已知上游源站时补 /v1，否则原样返回（保留未知路径的干净 404）。
func canonicalizeV1Path(p string) string {
	if _, exempt := v1CompatExemptPaths[p]; exempt {
		return p
	}

	rest := p
	strippedV1 := false
	for {
		if rest == "/v1" {
			rest = ""
			strippedV1 = true
			break
		}
		if strings.HasPrefix(rest, "/v1/") {
			rest = rest[len("/v1"):]
			strippedV1 = true
			continue
		}
		break
	}

	if strippedV1 {
		if rest == "" {
			return "/v1"
		}
		return "/v1" + rest
	}

	if isKnownV1Origin(rest) {
		return "/v1" + rest
	}
	return p
}

// isKnownV1Origin 判断裸路径（无 /v1 前缀）是否对应一个已注册的 /v1 API 上游源站。
// 与 router.go 的 /v1 路由表保持一致：新增/移除上游源站时同步更新此处。
func isKnownV1Origin(bare string) bool {
	switch bare {
	case "/models", "/messages", "/chat/completions", "/responses":
		return true
	}
	// 有状态 responses 子路径：/responses/compact、/responses/input_tokens、
	// /responses/{id}、/responses/{id}/cancel、/responses/{id}/input_items。
	return strings.HasPrefix(bare, "/responses/")
}
