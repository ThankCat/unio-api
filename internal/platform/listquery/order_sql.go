package listquery

import (
	"fmt"
	"strings"
)

// SortField 将 API sort 字段映射到 SQL 排序表达式（仅内部白名单，禁止拼接用户输入）。
type SortField struct {
	Key  string
	Expr string
}

// OrderByCase 生成 sqlc 兼容的动态 ORDER BY（使用 sort_field / sort_desc 命名参数）。
func OrderByCase(fields []SortField, defaultKey string, defaultDesc bool, tieBreaker string) string {
	if defaultKey == "" {
		panic("listquery: defaultKey required")
	}
	if tieBreaker == "" {
		tieBreaker = "id DESC"
	}

	var defaultExpr string
	for _, f := range fields {
		if f.Key == defaultKey {
			defaultExpr = f.Expr
			break
		}
	}
	if defaultExpr == "" {
		panic("listquery: defaultKey not in fields")
	}

	var parts []string
	parts = append(parts,
		fmt.Sprintf(
			"CASE WHEN COALESCE(sqlc.narg('sort_field')::text, '%s') IN ('', '%s') AND COALESCE(sqlc.narg('sort_desc')::bool, %t) THEN %s END DESC NULLS LAST",
			defaultKey, defaultKey, defaultDesc, defaultExpr,
		),
		fmt.Sprintf(
			"CASE WHEN COALESCE(sqlc.narg('sort_field')::text, '%s') IN ('', '%s') AND NOT COALESCE(sqlc.narg('sort_desc')::bool, %t) THEN %s END ASC NULLS LAST",
			defaultKey, defaultKey, defaultDesc, defaultExpr,
		),
	)

	for _, f := range fields {
		if f.Key == defaultKey {
			continue
		}
		parts = append(parts,
			fmt.Sprintf(
				"CASE WHEN sqlc.narg('sort_field')::text = '%s' AND COALESCE(sqlc.narg('sort_desc')::bool, false) THEN %s END DESC NULLS LAST",
				f.Key, f.Expr,
			),
			fmt.Sprintf(
				"CASE WHEN sqlc.narg('sort_field')::text = '%s' AND NOT COALESCE(sqlc.narg('sort_desc')::bool, false) THEN %s END ASC NULLS LAST",
				f.Key, f.Expr,
			),
		)
	}

	return "ORDER BY\n  " + strings.Join(parts, ",\n  ") + ",\n  " + tieBreaker
}
