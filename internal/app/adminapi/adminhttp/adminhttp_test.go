package adminhttp

import (
	"net/http"
	"net/http/httptest"
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
		Code: "23514", ConstraintName: "ck_non_negative_route_margin", Message: "negative route margin",
	})
	if recorder.Code != http.StatusUnprocessableEntity {
		t.Fatalf("expected 422, got %d", recorder.Code)
	}
	if len(metrics.results) != 1 || metrics.results[0] != "configuration_rejected" {
		t.Fatalf("unexpected margin metrics: %+v", metrics.results)
	}
}
