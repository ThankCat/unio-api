// Package meta 暴露 Console 的接入元信息：网关对外地址与各协议可用端点。
//
// 这些内容全部来自进程配置，不查库、不依赖会话，因此挂在 RequireAuth 之外：
// 用户在登录页就该能看到「往哪儿发请求」，不必先有账号。
package meta

import (
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/ThankCat/unio-gateway/internal/app/consoleapi/transport"
)

// Deps 包含接入元信息所需的配置。
type Deps struct {
	// GatewayPublicBaseURL 是网关对外的推理入口，如 https://api.example.com/v1。
	// 留空表示部署方没配，此时接入区返回 configured=false，前端隐藏整块。
	GatewayPublicBaseURL string
	// DocsBaseURL 是文档站根地址，用于拼各协议的文档锚点。留空则不返回文档链接。
	DocsBaseURL string
}

// protocolDTO 描述一个可接入协议：用哪个 base_url、能打哪些端点。
type protocolDTO struct {
	Key       string   `json:"key"`
	Label     string   `json:"label"`
	BaseURL   string   `json:"base_url"`
	AuthStyle string   `json:"auth_style"`
	Endpoints []string `json:"endpoints"`
	DocURL    string   `json:"doc_url,omitempty"`
}

type endpointsDTO struct {
	// Configured=false 时 Protocols 为空：部署方没配公开地址，前端不该猜。
	Configured bool          `json:"configured"`
	Protocols  []protocolDTO `json:"protocols"`
}

// Register 挂载 /meta 下的接入元信息路由。
func Register(r chi.Router, deps Deps) {
	h := &handler{deps: deps}
	r.Route("/meta", func(r chi.Router) {
		r.Get("/endpoints", h.endpoints)
	})
}

type handler struct {
	deps Deps
}

func (h *handler) endpoints(w http.ResponseWriter, _ *http.Request) {
	base := h.deps.GatewayPublicBaseURL
	if base == "" {
		_ = transport.WriteData(w, http.StatusOK, endpointsDTO{Configured: false, Protocols: []protocolDTO{}})
		return
	}

	docs := h.deps.DocsBaseURL
	protocolDoc := func(anchor string) string {
		if docs == "" {
			return ""
		}
		return docs + anchor
	}

	// 端点清单与 gatewayapi 路由表一一对应：这里改了，那边也得改，反之亦然。
	// 之所以硬编码而不从 chi 反射，是因为要给用户看的是「协议 + 语义分组」，
	// 而不是路由树的全部叶子（含 healthz、内部探针等无关项）。
	_ = transport.WriteData(w, http.StatusOK, endpointsDTO{
		Configured: true,
		Protocols: []protocolDTO{
			{
				Key:       "openai",
				Label:     "OpenAI 兼容",
				BaseURL:   base,
				AuthStyle: "bearer",
				Endpoints: []string{"/chat/completions", "/responses", "/models"},
				DocURL:    protocolDoc("/quickstart/openai"),
			},
			{
				Key:       "anthropic",
				Label:     "Anthropic 兼容",
				BaseURL:   base,
				AuthStyle: "x-api-key",
				Endpoints: []string{"/messages"},
				DocURL:    protocolDoc("/quickstart/anthropic"),
			},
		},
	})
}
