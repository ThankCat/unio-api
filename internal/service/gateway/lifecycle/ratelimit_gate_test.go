package lifecycle

import (
	"context"
	"testing"

	"github.com/ThankCat/unio-api/internal/core/auth"
	"github.com/ThankCat/unio-api/internal/core/channel"
	"github.com/ThankCat/unio-api/internal/core/routing"
	coreusage "github.com/ThankCat/unio-api/internal/core/usage"
	"github.com/ThankCat/unio-api/internal/platform/ratelimit"
)

func tokenPtr(v int64) *int64 { return &v }

// TestBillableTPMTokensExcludesCacheRead 锁定 DEC-028：cache_read（缓存命中读取）不计入 TPM，
// 而 uncached / cache_write(5m+1h) / output 全额计入。
func TestBillableTPMTokensExcludesCacheRead(t *testing.T) {
	facts := coreusage.Facts{
		UncachedInputTokens:     coreusage.KnownTokens(1_000),
		CacheReadInputTokens:    coreusage.KnownTokens(80_000), // 应被排除
		CacheWrite5mInputTokens: coreusage.KnownTokens(200),
		CacheWrite1hInputTokens: coreusage.KnownTokens(300),
		OutputTokensTotal:       coreusage.KnownTokens(500),
		ReasoningOutputTokens:   coreusage.KnownTokens(100), // 已含于 OutputTokensTotal，不另加
	}

	got := billableTPMTokens(facts)
	want := int64(1_000 + 200 + 300 + 500) // 明确不含 80_000 的 cache_read

	if got != want {
		t.Fatalf("billableTPMTokens = %d, want %d (cache_read must be excluded)", got, want)
	}
}

// TestBillableTPMTokensAllCacheReadIsZero 验证「一轮全是缓存命中」时 TPM 计数只剩 output——
// 正是 Codex 多轮重发缓存上下文却不再撑爆窗口的关键。
func TestBillableTPMTokensAllCacheReadIsZero(t *testing.T) {
	facts := coreusage.Facts{
		UncachedInputTokens:     coreusage.KnownTokens(0),
		CacheReadInputTokens:    coreusage.KnownTokens(90_000),
		CacheWrite5mInputTokens: coreusage.NotApplicableTokens(),
		CacheWrite1hInputTokens: coreusage.NotApplicableTokens(),
		OutputTokensTotal:       coreusage.KnownTokens(42),
	}
	if got := billableTPMTokens(facts); got != 42 {
		t.Fatalf("billableTPMTokens = %d, want 42 (only output should count)", got)
	}
}

// fakeReservationGuard 是 TPM 预占释放单测用的假 Guard：只累加 backfill（正/负）增量，
// 记录 route+user 与 channel 维度的净变化，Allow* 恒放行、TPM 生效条件为 override >0。
type fakeReservationGuard struct {
	routeUser map[[2]int64]int64
	channel   map[int64]int64
}

func newFakeReservationGuard() *fakeReservationGuard {
	return &fakeReservationGuard{routeUser: map[[2]int64]int64{}, channel: map[int64]int64{}}
}

func (f *fakeReservationGuard) AllowRouteUserTokens(context.Context, int64, int64, ratelimit.Limits, int64) (ratelimit.Decision, error) {
	return ratelimit.Decision{Allowed: true}, nil
}

func (f *fakeReservationGuard) AllowChannel(context.Context, int64, ratelimit.Limits, int64) (ratelimit.Decision, error) {
	return ratelimit.Decision{Allowed: true}, nil
}

func (f *fakeReservationGuard) TokensEnforced(limits ratelimit.Limits) bool {
	return limits.TPM != nil && *limits.TPM > 0
}

func (f *fakeReservationGuard) BackfillRouteUserTokens(_ context.Context, routeID, userID, delta int64) {
	f.routeUser[[2]int64{routeID, userID}] += delta
}

func (f *fakeReservationGuard) BackfillChannelTokens(_ context.Context, channelID, delta int64) {
	f.channel[channelID] += delta
}

func principalWithTPM(routeID, userID, tpm int64) *auth.APIKeyPrincipal {
	return &auth.APIKeyPrincipal{UserID: userID, RouteID: tokenPtr(routeID), TPMLimit: tokenPtr(tpm)}
}

func channelWithTPM(channelID, tpm int64) routing.ChatRouteCandidate {
	return routing.ChatRouteCandidate{Channel: channel.Runtime{ID: channelID}, TPMLimit: tokenPtr(tpm)}
}

// TestReleaseUnreconciledTPMFallbackReleasesLosers 覆盖 fallback 场景：胜出候选被结算回填对账，
// 落选候选与 route+user 的处理——只释放未对账的落选渠道，route+user 与胜出渠道不回退（DEC-028 Fix 3）。
func TestReleaseUnreconciledTPMFallbackReleasesLosers(t *testing.T) {
	guard := newFakeReservationGuard()
	r := &AttemptRunner{guard: guard}

	res := &tpmReservations{}
	r.recordKeyTPMReservation(res, principalWithTPM(3, 1, 300_000), 1_000)
	r.recordChannelTPMReservation(res, channelWithTPM(10, 100_000), 1_000) // 落选
	r.recordChannelTPMReservation(res, channelWithTPM(20, 100_000), 1_000) // 胜出
	res.markReconciled(20)

	r.releaseUnreconciledTPM(context.Background(), res)

	if got := guard.routeUser[[2]int64{3, 1}]; got != 0 {
		t.Fatalf("route+user should not be released after reconcile, got delta %d", got)
	}
	if got := guard.channel[10]; got != -1_000 {
		t.Fatalf("losing channel 10 should be released -1000, got %d", got)
	}
	if got := guard.channel[20]; got != 0 {
		t.Fatalf("winning channel 20 should not be released, got %d", got)
	}
}

// TestReleaseUnreconciledTPMFailureReleasesAll 覆盖失败/取消/无结算：没有任何回填对账，
// route+user 与已预占渠道的预占应全部释放（DEC-028 Fix 2）。
func TestReleaseUnreconciledTPMFailureReleasesAll(t *testing.T) {
	guard := newFakeReservationGuard()
	r := &AttemptRunner{guard: guard}

	res := &tpmReservations{}
	r.recordKeyTPMReservation(res, principalWithTPM(3, 1, 300_000), 1_500)
	r.recordChannelTPMReservation(res, channelWithTPM(10, 100_000), 1_500)

	r.releaseUnreconciledTPM(context.Background(), res)

	if got := guard.routeUser[[2]int64{3, 1}]; got != -1_500 {
		t.Fatalf("route+user should be released -1500 on failure, got %d", got)
	}
	if got := guard.channel[10]; got != -1_500 {
		t.Fatalf("channel 10 should be released -1500 on failure, got %d", got)
	}
}

// TestReleaseUnreconciledTPMSkipsUnenforced 验证 TPM 未生效（无 override）时不登记预占、收尾不释放，
// 保证释放量恒等于预占量，绝不把桶推成无中生有的负数。
func TestReleaseUnreconciledTPMSkipsUnenforced(t *testing.T) {
	guard := newFakeReservationGuard()
	r := &AttemptRunner{guard: guard}

	res := &tpmReservations{}
	r.recordKeyTPMReservation(res, &auth.APIKeyPrincipal{UserID: 1, RouteID: tokenPtr(3)}, 1_000) // TPMLimit nil → 不生效
	r.recordChannelTPMReservation(res, routing.ChatRouteCandidate{Channel: channel.Runtime{ID: 10}}, 1_000)

	r.releaseUnreconciledTPM(context.Background(), res)

	if len(guard.routeUser) != 0 || len(guard.channel) != 0 {
		t.Fatalf("no reservation should be recorded/released when TPM is not enforced, got route=%v chan=%v", guard.routeUser, guard.channel)
	}
}
