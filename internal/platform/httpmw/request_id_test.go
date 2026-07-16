package httpmw

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ThankCat/unio-gateway/internal/platform/httpx"
)

func TestRequestIDGeneratesWhenMissing(t *testing.T) {
	var contextRequestID string
	handler := RequestID(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		contextRequestID = httpx.RequestID(r.Context())
		w.WriteHeader(http.StatusNoContent)
	}))

	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	headerRequestID := rec.Header().Get(httpx.HeaderRequestID)
	if headerRequestID == "" {
		t.Fatalf("expected generated response request id")
	}
	if contextRequestID != headerRequestID {
		t.Fatalf("expected context request id %q, got %q", headerRequestID, contextRequestID)
	}
}

func TestRequestIDPreservesSafeClientHeader(t *testing.T) {
	tests := []string{
		"req_ABC-123.test:edge",
		"550e8400-e29b-41d4-a716-446655440000",
		strings.Repeat("a", maxRequestIDLength),
	}

	for _, requestID := range tests {
		t.Run(requestID, func(t *testing.T) {
			var contextRequestID string
			handler := RequestID(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				contextRequestID = httpx.RequestID(r.Context())
				w.WriteHeader(http.StatusNoContent)
			}))

			req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
			req.Header.Set(httpx.HeaderRequestID, requestID)
			rec := httptest.NewRecorder()

			handler.ServeHTTP(rec, req)

			if got := rec.Header().Get(httpx.HeaderRequestID); got != requestID {
				t.Fatalf("expected safe client request id %q to be preserved, got %q", requestID, got)
			}
			if contextRequestID != requestID {
				t.Fatalf("expected context request id %q, got %q", requestID, contextRequestID)
			}
		})
	}
}

func TestRequestIDReplacesUnsafeClientHeader(t *testing.T) {
	tests := []struct {
		name      string
		requestID string
	}{
		{
			name:      "too long",
			requestID: strings.Repeat("a", maxRequestIDLength+1),
		},
		{
			name:      "control character",
			requestID: "bad\nid",
		},
		{
			name:      "space",
			requestID: "bad id",
		},
		{
			name:      "unicode",
			requestID: "bad-\u2603-id",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var contextRequestID string
			handler := RequestID(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				contextRequestID = httpx.RequestID(r.Context())
				w.WriteHeader(http.StatusNoContent)
			}))

			req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
			req.Header.Set(httpx.HeaderRequestID, tt.requestID)
			rec := httptest.NewRecorder()

			handler.ServeHTTP(rec, req)

			headerRequestID := rec.Header().Get(httpx.HeaderRequestID)
			if headerRequestID == "" {
				t.Fatalf("expected replacement response request id")
			}
			if headerRequestID == tt.requestID {
				t.Fatalf("expected unsafe client request id %q to be replaced", tt.requestID)
			}
			if contextRequestID != headerRequestID {
				t.Fatalf("expected context request id %q, got %q", headerRequestID, contextRequestID)
			}
			if !isSafeRequestID(headerRequestID) {
				t.Fatalf("expected replacement request id to be safe, got %q", headerRequestID)
			}
		})
	}
}
