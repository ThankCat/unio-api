package websiteapi

import (
	"context"
	"errors"
	"net/http"
	"regexp"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
)

// LabLogoStore 读取模型出品方图标 SVG；*sqlc.Queries 直接满足。
type LabLogoStore interface {
	GetModelLabLogo(ctx context.Context, slug string) (string, error)
}

// labSlugPattern 约束 slug 形态（models.dev 的 lab id 均为小写字母数字加连字符）。
var labSlugPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)

// handleLabLogo 输出出品方图标 SVG，供营销页 <img> 直接引用。
// 与 console/admin 的同名端点一致：公开 + 长缓存，CSP 掐死 SVG 里可能的脚本。
func handleLabLogo(store LabLogoStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		slug := chi.URLParam(r, "slug")
		if !labSlugPattern.MatchString(slug) {
			w.WriteHeader(http.StatusNotFound)
			return
		}

		svg, err := store.GetModelLabLogo(r.Context(), slug)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
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
}
