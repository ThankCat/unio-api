// Package meta 暴露 Console 的接入元信息：网关对外地址、各协议可用端点、模型出品方图标。
//
// 这些内容要么来自进程配置，要么是公开的展示资产（models.dev 图标），不依赖会话，
// 因此挂在 RequireAuth 之外：用户在登录页就该能看到「往哪儿发请求」，不必先有账号。
package meta

import (
	"context"
	"errors"
	"net/http"
	"regexp"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"

	"github.com/ThankCat/unio-gateway/internal/app/consoleapi/transport"
)

// LabLogoStore 读取出品方图标 SVG；*sqlc.Queries 直接满足。
type LabLogoStore interface {
	GetModelLabLogo(ctx context.Context, slug string) (string, error)
}

// Deps 包含接入元信息所需的配置。
type Deps struct {
	// GatewayPublicBaseURL 是网关对外的推理入口，如 https://api.example.com/v1。
	// 留空表示部署方没配，此时接入区返回 configured=false，前端隐藏整块。
	GatewayPublicBaseURL string
	// DocsBaseURL 是文档站根地址，用于拼各协议的文档锚点。留空则不返回文档链接。
	DocsBaseURL string
	// LabLogos 提供出品方图标；nil 时不挂图标路由。
	LabLogos LabLogoStore
}

// labSlugPattern 约束 slug 形态（models.dev 的 lab id 均为小写字母数字加连字符）。
// 不匹配直接 404，把奇形怪状的路径挡在查库之前。
var labSlugPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)

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
		if deps.LabLogos != nil {
			r.Get("/labs/{slug}/logo.svg", h.labLogo)
		}
	})
}

// labLogo 输出出品方图标 SVG。
//
// 公开 + 长缓存：图标由 <img> 直接引用，来源是 models.dev 公开数据，无鉴权价值；
// CSP 把 SVG 里可能的脚本掐死（导航直开也不会执行），img 渲染不受影响。
func (h *handler) labLogo(w http.ResponseWriter, r *http.Request) {
	slug := chi.URLParam(r, "slug")
	if !labSlugPattern.MatchString(slug) {
		w.WriteHeader(http.StatusNotFound)
		return
	}

	svg, err := h.deps.LabLogos.GetModelLabLogo(r.Context(), slug)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	// 空串表示登记过但上游没有图标：与未登记同样按 404，前端走字母兜底。
	if svg == "" {
		w.WriteHeader(http.StatusNotFound)
		return
	}

	w.Header().Set("Content-Type", "image/svg+xml; charset=utf-8")
	w.Header().Set("Cache-Control", "public, max-age=86400")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'")
	_, _ = w.Write([]byte(svg))
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
