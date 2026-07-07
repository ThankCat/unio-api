package messages

import (
	"context"
	"testing"
)

func eq(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// TestDefaultBetaPolicy 锁定默认策略:filter 模式 + 仅拦截 context-1m(code-execution 走透传吸收)。
func TestDefaultBetaPolicy(t *testing.T) {
	p := DefaultBetaPolicy()
	if p.Mode != BetaModeFilter {
		t.Fatalf("mode = %q, want filter", p.Mode)
	}
	if !eq(p.List, []string{"context-1m-2025-08-07"}) {
		t.Fatalf("list = %v, want [context-1m-2025-08-07]", p.List)
	}
}

func TestForwardableBetasFilterMode(t *testing.T) {
	policy := BetaPolicy{Mode: BetaModeFilter, List: []string{"context-1m-2025-08-07"}}
	got := forwardableBetas([]string{
		"context-1m-2025-08-07",
		"extended-cache-ttl-2025-04-11",
		"code-execution-2025-05-22",
		"extended-cache-ttl-2025-04-11", // dup
	}, policy)
	// 黑名单里的 context-1m 被拦;其余(含未来/未知)透传;去重保序。
	want := []string{"extended-cache-ttl-2025-04-11", "code-execution-2025-05-22"}
	if !eq(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestForwardableBetasPassthroughMode(t *testing.T) {
	policy := BetaPolicy{Mode: BetaModePassthrough}
	got := forwardableBetas([]string{"anything-goes", "context-1m-2025-08-07"}, policy)
	want := []string{"anything-goes", "context-1m-2025-08-07"}
	if !eq(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestForwardableBetasWhitelistMode(t *testing.T) {
	policy := BetaPolicy{Mode: BetaModeWhitelist, List: []string{"prompt-caching-2024-07-31"}}
	got := forwardableBetas([]string{"prompt-caching-2024-07-31", "extended-cache-ttl-2025-04-11"}, policy)
	want := []string{"prompt-caching-2024-07-31"}
	if !eq(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

// TestForwardableBetasDefaultKeepsExtendedCacheTTL 锁定 P0-A:默认策略下 1h 缓存 beta 必须转发。
func TestForwardableBetasDefaultKeepsExtendedCacheTTL(t *testing.T) {
	got := forwardableBetas([]string{"extended-cache-ttl-2025-04-11"}, DefaultBetaPolicy())
	if !eq(got, []string{"extended-cache-ttl-2025-04-11"}) {
		t.Fatalf("got %v, want [extended-cache-ttl-2025-04-11]", got)
	}
}

func TestBlockedBetasFilterMode(t *testing.T) {
	policy := BetaPolicy{Mode: BetaModeFilter, List: []string{"context-1m-2025-08-07"}}
	got := blockedBetas([]string{
		"prompt-caching-2024-07-31",
		"context-1m-2025-08-07",
	}, policy)
	if !eq(got, []string{"context-1m-2025-08-07"}) {
		t.Fatalf("got %v, want [context-1m-2025-08-07]", got)
	}
}

// TestActiveBetaPolicyUsesProviderThenDefault 验证 provider 注入优先、清空后回退默认。
func TestActiveBetaPolicyUsesProviderThenDefault(t *testing.T) {
	t.Cleanup(func() { SetBetaPolicyProvider(nil) })

	if got := activeBetaPolicy(context.Background()); got.Mode != BetaModeFilter {
		t.Fatalf("default mode = %q, want filter", got.Mode)
	}

	SetBetaPolicyProvider(staticBetaPolicy{BetaPolicy{Mode: BetaModePassthrough}})
	if got := activeBetaPolicy(context.Background()); got.Mode != BetaModePassthrough {
		t.Fatalf("provider mode = %q, want passthrough", got.Mode)
	}

	SetBetaPolicyProvider(nil)
	if got := activeBetaPolicy(context.Background()); got.Mode != BetaModeFilter {
		t.Fatalf("after clear mode = %q, want filter", got.Mode)
	}
}

type staticBetaPolicy struct{ p BetaPolicy }

func (s staticBetaPolicy) BetaPolicy(context.Context) BetaPolicy { return s.p }
