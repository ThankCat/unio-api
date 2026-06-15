package adminapi

import (
	"net/http"
	"strconv"

	"github.com/ThankCat/unio-api/internal/platform/httpx"
)

const (
	defaultPageSize = 20
	maxPageSize     = 100
)

// pageParams 是解析后的分页请求（page 从 1 起）。
type pageParams struct {
	Page     int
	PageSize int
}

// Limit / Offset 换算成 SQL LIMIT/OFFSET（int32 与 sqlc 生成参数对齐）。
func (p pageParams) Limit() int32  { return int32(p.PageSize) }
func (p pageParams) Offset() int32 { return int32((p.Page - 1) * p.PageSize) }

// parsePage 从 query 解析 page/page_size；非法或缺省回退默认值，并夹紧上限。
func parsePage(r *http.Request) pageParams {
	page := 1
	pageSize := defaultPageSize
	if n, err := strconv.Atoi(r.URL.Query().Get("page")); err == nil && n > 0 {
		page = n
	}
	if n, err := strconv.Atoi(r.URL.Query().Get("page_size")); err == nil && n > 0 {
		pageSize = n
		if pageSize > maxPageSize {
			pageSize = maxPageSize
		}
	}
	return pageParams{Page: page, PageSize: pageSize}
}

// parseBoolQuery 解析布尔型 query 参数；仅 "true"/"1" 视为真，其余（含缺省/非法）为假。
func parseBoolQuery(r *http.Request, key string) bool {
	switch r.URL.Query().Get(key) {
	case "true", "1":
		return true
	default:
		return false
	}
}

// listStatus 只接受 enabled/disabled 作为状态过滤值，其它一律视为不过滤。
func listStatus(r *http.Request) string {
	switch r.URL.Query().Get("status") {
	case "enabled":
		return "enabled"
	case "disabled":
		return "disabled"
	default:
		return ""
	}
}

// writeList 写出带分页 meta 的成功信封 { "data": [...], "meta": {...} }。
func writeList(w http.ResponseWriter, status int, data any, p pageParams, total int64) {
	_ = httpx.WriteJSON(w, status, map[string]any{
		"data": data,
		"meta": map[string]any{
			"page":      p.Page,
			"page_size": p.PageSize,
			"total":     total,
		},
	})
}
