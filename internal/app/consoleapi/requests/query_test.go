package requests

import (
	"net/http"
	"net/url"
	"testing"
	"time"
)

func TestParseListQueryDefaultsAndFilters(t *testing.T) {
	t.Parallel()
	from := time.Date(2026, 8, 20, 0, 0, 0, 0, time.UTC)
	values := url.Values{}
	values.Set("page", "2")
	values.Set("page_size", "10")
	values.Set("q", " claude ")
	values.Add("route_id", "3")
	values.Add("route_id", "5")
	values.Set("api_key_id", "9")
	values.Set("endpoint", "/v1/chat/completions")
	values.Set("stream", "stream")
	values.Set("sort", "-model")
	values.Set("from", from.Format(time.RFC3339))
	req := &http.Request{URL: &url.URL{RawQuery: values.Encode()}}

	parsed, err := parseListQuery(req)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.page != 2 || parsed.pageSize != 10 || parsed.params.Offset != 10 || parsed.params.Limit != 10 {
		t.Fatalf("pagination = %+v", parsed)
	}
	if parsed.params.Q != "claude" || parsed.params.SortField != "model" || !parsed.params.SortDesc {
		t.Fatalf("search/sort = %+v", parsed.params)
	}
	if len(parsed.params.RouteIDs) != 2 || parsed.params.RouteIDs[0] != 3 || parsed.params.RouteIDs[1] != 5 {
		t.Fatalf("route ids = %#v", parsed.params.RouteIDs)
	}
	if len(parsed.params.Endpoints) != 1 || parsed.params.Endpoints[0] != "/v1/chat/completions" {
		t.Fatalf("endpoints = %#v", parsed.params.Endpoints)
	}
	if parsed.params.From == nil || !parsed.params.From.Equal(from) {
		t.Fatalf("from = %v", parsed.params.From)
	}
}

func TestParseListQueryClampsPageSizeAndDefaultsSort(t *testing.T) {
	t.Parallel()
	req := &http.Request{URL: &url.URL{RawQuery: "page_size=1000"}}
	parsed, err := parseListQuery(req)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.page != 1 || parsed.pageSize != 100 || parsed.params.SortField != "created_at" || !parsed.params.SortDesc {
		t.Fatalf("parsed = %+v params=%+v", parsed, parsed.params)
	}
}

func TestParseListQueryRejectsInvalidValues(t *testing.T) {
	t.Parallel()
	cases := []string{
		"page=0",
		"page_size=abc",
		"from=yesterday",
		"route_id=abc",
		"endpoint=/v1/models",
		"stream=true",
		"sort=unknown",
	}
	for _, raw := range cases {
		req := &http.Request{URL: &url.URL{RawQuery: raw}}
		_, err := parseListQuery(req)
		if err == nil || err.Code != "invalid_argument" || err.Status != 400 {
			t.Fatalf("%s: got %#v", raw, err)
		}
	}
}
