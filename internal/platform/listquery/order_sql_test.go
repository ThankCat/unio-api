package listquery_test

import (
	"testing"

	"github.com/ThankCat/unio-gateway/internal/platform/listquery"
)

func TestOrderByCaseUsage(t *testing.T) {
	sql := listquery.OrderByCase([]listquery.SortField{
		{Key: "created_at", Expr: "u.created_at"},
		{Key: "model", Expr: "r.requested_model_id"},
		{Key: "user_id", Expr: "r.user_id"},
	}, "created_at", true, "u.id DESC")
	t.Log(sql)
}

func TestOrderByCaseProvidersOps(t *testing.T) {
	sql := listquery.OrderByCase([]listquery.SortField{
		{Key: "name", Expr: "a.name"},
		{Key: "requests", Expr: "a.attempt_total"},
		{Key: "success_rate", Expr: "(a.attempt_succeeded::float8 / NULLIF(a.attempt_total, 0))"},
		{Key: "tokens", Expr: "COALESCE(m.tokens_total, 0)"},
		{Key: "margin", Expr: "(COALESCE(m.revenue_usd, 0) - COALESCE(m.cost_usd, 0))"},
	}, "success_rate", false, "a.id")
	t.Log(sql)
}

func TestOrderByCaseRequests(t *testing.T) {
	sql := listquery.OrderByCase([]listquery.SortField{
		{Key: "created_at", Expr: "created_at"},
		{Key: "status", Expr: "status"},
		{Key: "user_id", Expr: "user_id"},
		{Key: "model", Expr: "requested_model_id"},
		{Key: "stream", Expr: "stream"},
	}, "created_at", true, "id DESC")
	if sql == "" {
		t.Fatal("empty sql")
	}
	t.Log(sql)
}
