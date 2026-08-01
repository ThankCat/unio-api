package gatewaylogging

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ThankCat/unio-gateway/internal/platform/failure"
)

func TestListLogsBuildsBoundedQueryAndParsesEntries(t *testing.T) {
	now := time.Date(2026, time.August, 1, 9, 0, 0, 0, time.UTC)
	line := `{"level":"warning","timestamp":"2026-08-01T08:59:59Z","message":"request completed","server":"gateway","environment":"production","instance":"gw-envelope","type":"http","event":"request","data":{"trace_id":"trace_1","request_id":"req_1","status_code":503}}`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/loki/api/v1/query_range" {
			t.Errorf("path = %q", r.URL.Path)
		}
		query := r.URL.Query()
		wantLogQL := `{server="gateway",level="warning",type="http",event="request"} |= "req_1" |= "timeout"`
		if query.Get("query") != wantLogQL || query.Get("limit") != "51" || query.Get("direction") != "backward" {
			t.Errorf("query = %v", query)
		}
		if query.Get("start") != "1785553200000000000" || query.Get("end") != "1785574800000000000" {
			t.Errorf("range = %s..%s", query.Get("start"), query.Get("end"))
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"status": "success",
			"data": map[string]any{"result": []any{map[string]any{
				"stream": map[string]string{"environment": "production", "instance": "gw-1"},
				"values": [][]string{{"1785596399000000000", line}},
			}}},
		})
	}))
	defer server.Close()

	service := NewService(&controlStub{}, http.DefaultClient, nil, "", server.URL)
	service.now = func() time.Time { return now }
	result, err := service.ListLogs(context.Background(), LogQuery{
		Window: "6h", Level: "warning", Type: "http", Event: "request",
		RelatedID: "req_1", Search: "timeout", Limit: 50,
	})
	if err != nil {
		t.Fatalf("list logs: %v", err)
	}
	if len(result.Items) != 1 || result.Truncated || result.Limit != 50 {
		t.Fatalf("result = %+v", result)
	}
	entry := result.Items[0]
	if entry.ID == "" || entry.Level != "warning" || entry.Instance != "gw-envelope" || entry.Environment != "production" ||
		entry.Data["request_id"] != "req_1" || entry.Data["status_code"] != float64(503) {
		t.Fatalf("entry = %+v", entry)
	}
}

func TestListLogsOnlyReportsTruncationWhenAnotherEntryExists(t *testing.T) {
	now := time.Date(2026, time.August, 1, 9, 0, 0, 0, time.UTC)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("limit") != "2" {
			t.Errorf("limit = %q", r.URL.Query().Get("limit"))
		}
		line := func(message string) string {
			return `{"level":"info","timestamp":"2026-08-01T08:59:59Z","message":"` + message + `","server":"gateway","type":"http","event":"request","data":{}}`
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"status": "success",
			"data": map[string]any{"result": []any{map[string]any{
				"stream": map[string]string{"environment": "production", "instance": "gw-1"},
				"values": [][]string{
					{"1785596399000000001", line("newer")},
					{"1785596399000000000", line("older")},
				},
			}}},
		})
	}))
	defer server.Close()

	service := NewService(&controlStub{}, http.DefaultClient, nil, "", server.URL)
	service.now = func() time.Time { return now }
	result, err := service.ListLogs(context.Background(), LogQuery{Limit: 1})
	if err != nil {
		t.Fatalf("list logs: %v", err)
	}
	if !result.Truncated || len(result.Items) != 1 || result.Items[0].Message != "newer" {
		t.Fatalf("result = %+v", result)
	}
}

func TestListLogsRejectsUnsafeOrUnboundedQueries(t *testing.T) {
	service := NewService(&controlStub{}, http.DefaultClient, nil, "", "http://loki.local")
	for _, query := range []LogQuery{
		{Window: "30d"},
		{Level: "fatal"},
		{Type: `http"}`},
		{Search: "bad\nquery"},
		{RelatedID: strings.Repeat("x", maxLogRelatedIDRunes+1)},
		{Limit: maxLogLimit + 1},
	} {
		if _, err := service.ListLogs(context.Background(), query); !strings.Contains(errorString(err), ErrLogQueryInvalid.Error()) {
			t.Fatalf("query=%+v error=%v", query, err)
		}
	}
}

func TestListLogsMapsLokiFailureToDependencyUnavailable(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer server.Close()
	service := NewService(&controlStub{}, http.DefaultClient, nil, "", server.URL)
	_, err := service.ListLogs(context.Background(), LogQuery{})
	if failure.CodeOf(err) != failure.CodeDependencyLokiUnavailable {
		t.Fatalf("error = %v code=%s", err, failure.CodeOf(err))
	}
}

func errorString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
