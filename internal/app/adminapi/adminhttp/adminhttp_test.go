package adminhttp

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
)

type fakeMarginMetrics struct {
	results []string
}

func (m *fakeMarginMetrics) IncRoutingMarginGuard(result string) {
	m.results = append(m.results, result)
}

func TestWriteServiceErrorRecordsMarginConfigurationRejection(t *testing.T) {
	metrics := &fakeMarginMetrics{}
	SetRoutingMarginMetrics(metrics)
	defer SetRoutingMarginMetrics(nil)
	recorder := httptest.NewRecorder()
	WriteServiceError(recorder, &pgconn.PgError{
		Code: "23514", ConstraintName: marginGuardConstraint, Message: "negative margin",
	})
	if recorder.Code != http.StatusUnprocessableEntity {
		t.Fatalf("expected 422, got %d", recorder.Code)
	}
	if len(metrics.results) != 1 || metrics.results[0] != "configuration_rejected" {
		t.Fatalf("unexpected margin metrics: %+v", metrics.results)
	}
}

// 守卫的 DETAIL 必须落到响应文案里：亏本的常常是某个缓存分项而非主价，
// 只说「售价低于成本」会让人对着两个看起来正常的主价发懵。
func TestWriteServiceErrorSurfacesMarginComponent(t *testing.T) {
	recorder := httptest.NewRecorder()
	WriteServiceError(recorder, &pgconn.PgError{
		Code:           "23514",
		ConstraintName: marginGuardConstraint,
		Message:        "negative margin: channel=7 model=3 component=standard/cache_creation_1h_input sale=1 cost=2",
		// 与 json_build_object 的实际输出对齐：sale / cost 是数字，不是字符串。
		Detail: `{"channel_id" : 7, "model_id" : 3, ` +
			`"component" : "standard/cache_creation_1h_input", "sale" : 1.0000000000, "cost" : 2.0000000000}`,
	})

	body := recorder.Body.String()
	for _, want := range []string{"standard/cache_creation_1h_input", "模型 3", "渠道 7"} {
		if !strings.Contains(body, want) {
			t.Fatalf("margin rejection body must mention %q, got %s", want, body)
		}
	}
}

// DETAIL 缺失或不可解析时退回守卫原始 MESSAGE，不能把拒绝理由整个吞掉。
func TestWriteServiceErrorFallsBackToMarginMessage(t *testing.T) {
	recorder := httptest.NewRecorder()
	WriteServiceError(recorder, &pgconn.PgError{
		Code:           "23514",
		ConstraintName: marginGuardConstraint,
		Message:        "negative margin: channel=7 model=3",
		Detail:         "not-json",
	})

	if !strings.Contains(recorder.Body.String(), "negative margin: channel=7 model=3") {
		t.Fatalf("expected raw guard message, got %s", recorder.Body.String())
	}
}
