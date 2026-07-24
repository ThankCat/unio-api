//go:build blackbox

package sdkfixture

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestBlackboxConfigRedisNamespace(t *testing.T) {
	t.Run("default", func(t *testing.T) {
		t.Setenv("REDIS_KEY_NAMESPACE", "")

		got := blackboxConfig().Redis.KeyNamespace
		if got != defaultRedisNamespace {
			t.Fatalf("Redis.KeyNamespace = %q, want %q", got, defaultRedisNamespace)
		}
	})

	t.Run("environment override", func(t *testing.T) {
		const namespace = "unio:p4:blackbox:test-run"
		t.Setenv("REDIS_KEY_NAMESPACE", namespace)

		got := blackboxConfig().Redis.KeyNamespace
		if got != namespace {
			t.Fatalf("Redis.KeyNamespace = %q, want %q", got, namespace)
		}
	})
}

func TestFixtureCleanupAllowsReusingOneOriginRoot(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	t.Cleanup(upstream.Close)

	for _, name := range []string{"first", "second"} {
		t.Run(name, func(t *testing.T) {
			Setup(t, SetupOptions{
				Mode:            UpstreamMock,
				UpstreamBaseURL: upstream.URL,
				Protocol:        "openai",
				AdapterKey:      "openai",
			})
		})
	}
}
