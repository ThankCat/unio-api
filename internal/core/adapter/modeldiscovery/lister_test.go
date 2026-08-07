package modeldiscovery

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ThankCat/unio-gateway/internal/core/channel"
)

func TestOpenAICompatibleListModelsAcceptsVersionedRootAndBearerAuth(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			t.Fatalf("path = %q", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer secret" {
			t.Fatalf("authorization = %q", got)
		}
		_, _ = w.Write([]byte(`{"data":[{"id":"gpt-b","owned_by":"openai","created":1700000000},{"id":"gpt-a"}]}`))
	}))
	defer server.Close()

	result, err := NewOpenAICompatible(server.Client()).ListModels(context.Background(), channel.Runtime{
		Origin: server.URL + "/v1", APIKey: "secret",
	})
	if err != nil {
		t.Fatalf("ListModels: %v", err)
	}
	if len(result.Items) != 2 || result.Items[0].ID != "gpt-a" || result.Items[1].ID != "gpt-b" {
		t.Fatalf("unexpected items: %+v", result.Items)
	}
	if result.Items[1].CreatedAt == nil {
		t.Fatal("expected created timestamp")
	}
}

func TestAnthropicListModelsPaginatesAndDeduplicates(t *testing.T) {
	t.Parallel()
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Header.Get("x-api-key") != "secret" || r.Header.Get("anthropic-version") == "" {
			t.Fatal("missing anthropic authentication headers")
		}
		if r.URL.Query().Get("limit") != "100" {
			t.Fatalf("limit = %q", r.URL.Query().Get("limit"))
		}
		if r.URL.Query().Get("after_id") == "" {
			_, _ = w.Write([]byte(`{"data":[{"id":"claude-a"},{"id":"claude-b"}],"has_more":true,"last_id":"claude-b"}`))
			return
		}
		if r.URL.Query().Get("after_id") != "claude-b" {
			t.Fatalf("after_id = %q", r.URL.Query().Get("after_id"))
		}
		_, _ = w.Write([]byte(`{"data":[{"id":"claude-b"},{"id":"claude-c"}],"has_more":false}`))
	}))
	defer server.Close()

	result, err := NewAnthropic(server.Client()).ListModels(context.Background(), channel.Runtime{Origin: server.URL, APIKey: "secret"})
	if err != nil {
		t.Fatalf("ListModels: %v", err)
	}
	if calls.Load() != 2 || len(result.Items) != 3 {
		t.Fatalf("calls=%d items=%+v", calls.Load(), result.Items)
	}
}

func TestListModelsAllowsEmptyData(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"data":[]}`))
	}))
	defer server.Close()
	result, err := NewOpenAICompatible(server.Client()).ListModels(context.Background(), channel.Runtime{Origin: server.URL, APIKey: "secret"})
	if err != nil || len(result.Items) != 0 {
		t.Fatalf("result=%+v err=%v", result, err)
	}
}

func TestListModelsClassifiesHTTPFailures(t *testing.T) {
	t.Parallel()
	tests := []struct {
		status int
		code   string
	}{
		{401, CodeCredentialInvalid}, {403, CodePermissionDenied}, {404, CodeUnsupportedEndpoint},
		{405, CodeUnsupportedEndpoint}, {429, CodeRateLimited}, {500, CodeUpstreamError},
	}
	for _, tt := range tests {
		t.Run(fmt.Sprintf("status_%d", tt.status), func(t *testing.T) {
			t.Parallel()
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tt.status)
				_, _ = w.Write([]byte(`sensitive upstream body`))
			}))
			defer server.Close()
			_, err := NewOpenAICompatible(server.Client()).ListModels(context.Background(), channel.Runtime{Origin: server.URL, APIKey: "secret"})
			discoveryErr, ok := ErrorOf(err)
			if !ok || discoveryErr.Code != tt.code || strings.Contains(err.Error(), "sensitive") {
				t.Fatalf("error=%v discovery=%+v", err, discoveryErr)
			}
		})
	}
}

func TestListModelsRejectsMalformedAndOversizedBodies(t *testing.T) {
	t.Parallel()
	for _, body := range []string{`not-json`, `{"object":"list"}`} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(body)) }))
		lister := NewOpenAICompatible(server.Client())
		_, err := lister.ListModels(context.Background(), channel.Runtime{Origin: server.URL, APIKey: "secret"})
		server.Close()
		if discoveryErr, ok := ErrorOf(err); !ok || discoveryErr.Code != CodeProtocolError {
			t.Fatalf("body=%q err=%v", body, err)
		}
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"data":[]}` + strings.Repeat(" ", 128)))
	}))
	defer server.Close()
	lister := NewOpenAICompatible(server.Client())
	lister.maxResponseBytes = 16
	_, err := lister.ListModels(context.Background(), channel.Runtime{Origin: server.URL, APIKey: "secret"})
	if discoveryErr, ok := ErrorOf(err); !ok || discoveryErr.Code != CodeProtocolError {
		t.Fatalf("oversized error=%v", err)
	}
}

func TestListModelsTimeoutAndRedirectRefusal(t *testing.T) {
	t.Parallel()
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(100 * time.Millisecond)
		_, _ = w.Write([]byte(`{"data":[]}`))
	}))
	defer slow.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	_, err := NewOpenAICompatible(slow.Client()).ListModels(ctx, channel.Runtime{Origin: slow.URL, APIKey: "secret"})
	if discoveryErr, ok := ErrorOf(err); !ok || discoveryErr.Code != CodeTimeout {
		t.Fatalf("timeout error=%v", err)
	}

	var redirected atomic.Bool
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { redirected.Store(true) }))
	defer target.Close()
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
	}))
	defer redirect.Close()
	_, err = NewOpenAICompatible(redirect.Client()).ListModels(context.Background(), channel.Runtime{Origin: redirect.URL, APIKey: "secret"})
	if err == nil || redirected.Load() {
		t.Fatalf("redirect must be refused: err=%v redirected=%v", err, redirected.Load())
	}
}

func TestListModelsClassifiesUnreachable(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	origin := server.URL
	server.Close()
	_, err := NewOpenAICompatible(http.DefaultClient).ListModels(context.Background(), channel.Runtime{Origin: origin, APIKey: "secret"})
	discoveryErr, ok := ErrorOf(err)
	if !ok || discoveryErr.Code != CodeUnreachable || errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("unreachable error=%v", err)
	}
}
