// Package listquery 提供 admin 列表 REST 查询参数的解析与白名单校验。
package listquery

import (
	"fmt"
	"net/http"
	"strings"
)

// Sort 表示 ?sort=field 或 ?sort=-field（前缀 - 为降序）。
type Sort struct {
	Field string
	Desc  bool
}

// ParseSort 从 query 解析 sort；field 必须在 allowed 内，否则返回错误。
// defaultField 在 sort 缺省时回退；空串表示不排序（须在 allowed 内或为空）。
func ParseSort(r *http.Request, allowed map[string]struct{}, defaultField string, defaultDesc bool) (Sort, error) {
	if defaultField != "" {
		if _, ok := allowed[defaultField]; !ok {
			return Sort{}, fmt.Errorf("listquery: invalid defaultField %q", defaultField)
		}
	}
	raw := strings.TrimSpace(r.URL.Query().Get("sort"))
	if raw == "" {
		return Sort{Field: defaultField, Desc: defaultDesc}, nil
	}
	desc := false
	field := raw
	if strings.HasPrefix(field, "-") {
		desc = true
		field = strings.TrimPrefix(field, "-")
	} else if strings.HasPrefix(field, "+") {
		field = strings.TrimPrefix(field, "+")
	}
	field = strings.TrimSpace(field)
	if field == "" {
		return Sort{}, fmt.Errorf("sort: empty field")
	}
	if _, ok := allowed[field]; !ok {
		return Sort{}, fmt.Errorf("sort: unsupported field %q", field)
	}
	return Sort{Field: field, Desc: desc}, nil
}

// SQLParams 转为 sqlc 可绑定的 sort_field / sort_desc。
func (s Sort) SQLParams() (field string, desc bool) {
	return s.Field, s.Desc
}
