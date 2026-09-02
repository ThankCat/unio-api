package breakerstore

import (
	"context"
	"testing"

	"github.com/redis/go-redis/v9"
)

// redisNowMs 读 Redis 服务端时间。账号运行态的所有到期判断都以它为准，测试里造过期数据时
// 必须用同一个时钟，否则本机时钟哪怕偏几毫秒都会让断言随机翻车。
func redisNowMs(t *testing.T, client *redis.Client) int64 {
	t.Helper()
	now, err := client.Time(context.Background()).Result()
	if err != nil {
		t.Fatalf("read redis server time: %v", err)
	}
	return now.UnixMilli()
}

func accountRuntime(t *testing.T, s *Store, accountID int64) AccountRuntime {
	t.Helper()
	runtimes, err := s.AccountRuntimeMany(context.Background(), []int64{accountID})
	if err != nil {
		t.Fatalf("read account runtime: %v", err)
	}
	if len(runtimes) != 1 {
		t.Fatalf("account runtime rows = %d, want 1", len(runtimes))
	}
	return runtimes[0]
}

func accountInFlight(t *testing.T, s *Store, accountID int64) int64 {
	t.Helper()
	return accountRuntime(t, s, accountID).InFlight
}

// TestAccountCooldownDeniesAcquireWithRemaining 验证账号 429 冷却是准入硬门槛，并且回带剩余毫秒——
// 全池账号都在冷却时，Retry-After 要取其中最早恢复的那个，没有剩余毫秒就只能瞎猜。
func TestAccountCooldownDeniesAcquireWithRemaining(t *testing.T) {
	s, _, _ := newTestStore(t)
	cfg := testConfig()
	const channelID = 80
	const accountID = int64(8001)
	seedAttemptControls(t, s, cfg, channelID, `{"concurrency":5}`)

	ctx := context.Background()
	until, err := s.SetAccountCooldown(ctx, accountID, 60_000, AccountUsageWindowPrimary)
	if err != nil || until <= 0 {
		t.Fatalf("set account cooldown: until=%d err=%v", until, err)
	}

	denied := mustAcquireAccount(t, s, accountAcquireInput("cool-1", channelID, accountID, 1))
	if denied.Mode != AdmissionDenied || denied.Reason != ReasonAccountCooldown {
		t.Fatalf("acquire during account cooldown want denied/account_cooldown, got %s/%s", denied.Mode, denied.Reason)
	}
	if denied.CooldownRemainingMs <= 0 || denied.CooldownRemainingMs > 60_000 {
		t.Fatalf("account cooldown remaining = %d, want within (0, 60000]", denied.CooldownRemainingMs)
	}

	runtime := accountRuntime(t, s, accountID)
	if runtime.CooldownWindow != AccountUsageWindowPrimary {
		t.Fatalf("cooldown window = %q, want primary", runtime.CooldownWindow)
	}
	if runtime.Schedulable() {
		t.Fatal("account in cooldown reported as schedulable")
	}

	if err := s.ClearAccountCooldown(ctx, accountID); err != nil {
		t.Fatalf("clear account cooldown: %v", err)
	}
	allowed := mustAcquireAccount(t, s, accountAcquireInput("cool-2", channelID, accountID, 1))
	if allowed.Mode != AdmissionPermit {
		t.Fatalf("acquire after cooldown cleared want permit, got %s/%s", allowed.Mode, allowed.Reason)
	}
}

// TestAccountCooldownFollowsLatestObservation 冻结与渠道 429 冷却不同的那一条：账号冷却按最近一次
// 观测覆盖，不是只增不减。官方支持付费即时重置，reset_at 会提前，只延长会把配额已恢复的账号
// 继续锁住数小时。
func TestAccountCooldownFollowsLatestObservation(t *testing.T) {
	s, _, _ := newTestStore(t)
	ctx := context.Background()
	const accountID = int64(8101)

	if _, err := s.SetAccountCooldown(ctx, accountID, 3_600_000, AccountUsageWindowSecondary); err != nil {
		t.Fatalf("set long cooldown: %v", err)
	}
	if _, err := s.SetAccountCooldown(ctx, accountID, 5_000, AccountUsageWindowPrimary); err != nil {
		t.Fatalf("set shortened cooldown: %v", err)
	}

	runtime := accountRuntime(t, s, accountID)
	if runtime.CooldownRemainingMs <= 0 || runtime.CooldownRemainingMs > 5_000 {
		t.Fatalf("cooldown remaining = %d, want the shortened window within (0, 5000]", runtime.CooldownRemainingMs)
	}
	if runtime.CooldownWindow != AccountUsageWindowPrimary {
		t.Fatalf("cooldown window = %q, want the window of the latest observation", runtime.CooldownWindow)
	}
}

// TestAccountUnschedulableKeepsLongestIsolation 与冷却相反：本地判定的隔离按最晚到期叠加。
// 一次 401 的短刷新窗口不该把更长的代理故障隔离提前解除，展示出来的原因也要跟着实际生效的那个。
func TestAccountUnschedulableKeepsLongestIsolation(t *testing.T) {
	s, _, _ := newTestStore(t)
	ctx := context.Background()
	const accountID = int64(8201)

	if _, err := s.MarkAccountUnschedulable(ctx, accountID, 600_000, AccountUnschedulableProxySuspect); err != nil {
		t.Fatalf("mark proxy suspect: %v", err)
	}
	if _, err := s.MarkAccountUnschedulable(ctx, accountID, 5_000, AccountUnschedulableTokenRefresh); err != nil {
		t.Fatalf("mark token refresh: %v", err)
	}

	runtime := accountRuntime(t, s, accountID)
	if runtime.UnschedulableRemainingMs <= 5_000 {
		t.Fatalf("unschedulable remaining = %d, want the longer isolation to survive", runtime.UnschedulableRemainingMs)
	}
	if runtime.UnschedulableReason != AccountUnschedulableProxySuspect {
		t.Fatalf("unschedulable reason = %q, want the reason of the surviving deadline", runtime.UnschedulableReason)
	}

	if err := s.ClearAccountUnschedulable(ctx, accountID); err != nil {
		t.Fatalf("clear unschedulable: %v", err)
	}
	if remaining := accountRuntime(t, s, accountID).UnschedulableRemainingMs; remaining != 0 {
		t.Fatalf("unschedulable remaining after clear = %d, want 0", remaining)
	}
}

// TestAccountUnschedulableDeniesAcquire 验证临时隔离同样是准入硬门槛，并与冷却给出不同的拒绝原因。
func TestAccountUnschedulableDeniesAcquire(t *testing.T) {
	s, _, _ := newTestStore(t)
	cfg := testConfig()
	const channelID = 83
	const accountID = int64(8301)
	seedAttemptControls(t, s, cfg, channelID, `{"concurrency":5}`)

	if _, err := s.MarkAccountUnschedulable(
		context.Background(), accountID, 60_000, AccountUnschedulableTokenRefresh,
	); err != nil {
		t.Fatalf("mark unschedulable: %v", err)
	}
	denied := mustAcquireAccount(t, s, accountAcquireInput("unsched-1", channelID, accountID, 1))
	if denied.Mode != AdmissionDenied || denied.Reason != ReasonAccountUnschedulable {
		t.Fatalf("acquire want denied/account_unschedulable, got %s/%s", denied.Mode, denied.Reason)
	}
	if denied.CooldownRemainingMs <= 0 {
		t.Fatalf("unschedulable denial carries no remaining ms (%d)", denied.CooldownRemainingMs)
	}
}

// TestAccountUsagePauseIsRoutingFilterNotGate 固定用量暂停的作用边界：它是路由过滤链上的一条，
// 不是准入硬门槛。已经选中的请求继续放行，只是下一轮选号不再选它——这样阈值判断的时效性问题
// 最多多发一个请求，而不会把请求打回给客户。
func TestAccountUsagePauseIsRoutingFilterNotGate(t *testing.T) {
	s, _, _ := newTestStore(t)
	cfg := testConfig()
	const channelID = 84
	const accountID = int64(8401)
	seedAttemptControls(t, s, cfg, channelID, `{"concurrency":5}`)

	ctx := context.Background()
	if _, err := s.PauseAccountUsage(ctx, accountID, 60_000, AccountUsageWindowPrimary); err != nil {
		t.Fatalf("pause account usage: %v", err)
	}
	runtime := accountRuntime(t, s, accountID)
	if runtime.UsagePauseRemainingMs <= 0 || runtime.UsagePauseWindow != AccountUsageWindowPrimary {
		t.Fatalf("usage pause not visible to routing: %+v", runtime)
	}
	if runtime.Schedulable() {
		t.Fatal("usage-paused account reported as schedulable")
	}

	adm := mustAcquireAccount(t, s, accountAcquireInput("usage-1", channelID, accountID, 1))
	if adm.Mode != AdmissionPermit {
		t.Fatalf("usage pause must not block admission, got %s/%s", adm.Mode, adm.Reason)
	}

	if err := s.ResumeAccountUsage(ctx, accountID); err != nil {
		t.Fatalf("resume account usage: %v", err)
	}
	if remaining := accountRuntime(t, s, accountID).UsagePauseRemainingMs; remaining != 0 {
		t.Fatalf("usage pause remaining after resume = %d, want 0", remaining)
	}
}

// TestAccountRuntimeStatesAreIndependent 验证三种状态共用一个 hash 但互不干扰，并且键 TTL 覆盖
// 最晚的那个到期时刻——按单个状态各自 PEXPIRE 会把另一个状态提前抹掉。
func TestAccountRuntimeStatesAreIndependent(t *testing.T) {
	s, client, _ := newTestStore(t)
	ctx := context.Background()
	const accountID = int64(8501)

	if _, err := s.SetAccountCooldown(ctx, accountID, 5_000, AccountUsageWindowPrimary); err != nil {
		t.Fatalf("set cooldown: %v", err)
	}
	if _, err := s.MarkAccountUnschedulable(ctx, accountID, 600_000, AccountUnschedulableManual); err != nil {
		t.Fatalf("mark unschedulable: %v", err)
	}
	if err := s.ClearAccountCooldown(ctx, accountID); err != nil {
		t.Fatalf("clear cooldown: %v", err)
	}

	runtime := accountRuntime(t, s, accountID)
	if runtime.CooldownRemainingMs != 0 {
		t.Fatalf("cooldown remaining = %d, want 0 after clear", runtime.CooldownRemainingMs)
	}
	if runtime.UnschedulableRemainingMs <= 0 {
		t.Fatal("clearing the cooldown also dropped the unrelated isolation")
	}

	ttl, err := client.PTTL(ctx, s.keys.account(accountID)).Result()
	if err != nil {
		t.Fatalf("read account runtime ttl: %v", err)
	}
	if ttl.Milliseconds() <= 600_000 {
		t.Fatalf("account runtime ttl = %v, want it to outlive the longest deadline", ttl)
	}

	if err := s.ClearAccountUnschedulable(ctx, accountID); err != nil {
		t.Fatalf("clear unschedulable: %v", err)
	}
	exists, err := client.Exists(ctx, s.keys.account(accountID)).Result()
	if err != nil {
		t.Fatalf("check account runtime key: %v", err)
	}
	if exists != 0 {
		t.Fatal("account runtime key survived with every state cleared")
	}
}

// TestAccountRuntimeManyPreservesOrderAndCountsInFlight 验证批量读的形状契约：顺序与入参一致、
// 未知账号是全零而不是缺行——候选快照按下标对齐账号，错一位就会把 A 号的水位算到 B 号头上。
func TestAccountRuntimeManyPreservesOrderAndCountsInFlight(t *testing.T) {
	s, _, _ := newTestStore(t)
	cfg := testConfig()
	const channelID = 86
	const busyAccount = int64(8601)
	const idleAccount = int64(8602)
	const unknownAccount = int64(8603)
	seedAttemptControls(t, s, cfg, channelID, `{"concurrency":5}`)

	for _, permitID := range []string{"many-1", "many-2"} {
		adm := mustAcquireAccount(t, s, accountAcquireInput(permitID, channelID, busyAccount, 0))
		if adm.Mode != AdmissionPermit {
			t.Fatalf("acquire %s want permit, got %s/%s", permitID, adm.Mode, adm.Reason)
		}
	}
	if _, err := s.SetAccountCooldown(context.Background(), idleAccount, 30_000, AccountUsageWindowSecondary); err != nil {
		t.Fatalf("set cooldown on idle account: %v", err)
	}

	ids := []int64{unknownAccount, busyAccount, idleAccount}
	runtimes, err := s.AccountRuntimeMany(context.Background(), ids)
	if err != nil {
		t.Fatalf("read account runtimes: %v", err)
	}
	if len(runtimes) != len(ids) {
		t.Fatalf("account runtime rows = %d, want %d", len(runtimes), len(ids))
	}
	for index, id := range ids {
		if runtimes[index].AccountID != id {
			t.Fatalf("row %d account id = %d, want %d", index, runtimes[index].AccountID, id)
		}
	}
	if unknown := runtimes[0]; !unknown.Schedulable() || unknown.InFlight != 0 {
		t.Fatalf("unknown account row = %+v, want an all-zero schedulable row", unknown)
	}
	if busy := runtimes[1]; busy.InFlight != 2 {
		t.Fatalf("busy account in-flight = %d, want 2", busy.InFlight)
	}
	if idle := runtimes[2]; idle.InFlight != 0 || idle.CooldownRemainingMs <= 0 {
		t.Fatalf("idle account row = %+v, want no in-flight and an active cooldown", idle)
	}
}

// TestAccountRuntimeRejectsInvalidInput 守住调用方错误：重复账号会让候选快照按下标对齐时错位，
// 非法 id 与原因则说明调用点拿错了值。
func TestAccountRuntimeRejectsInvalidInput(t *testing.T) {
	s, _, _ := newTestStore(t)
	ctx := context.Background()

	if _, err := s.AccountRuntimeMany(ctx, []int64{8701, 8701}); err == nil {
		t.Fatal("duplicate account id was accepted")
	}
	if _, err := s.AccountRuntimeMany(ctx, []int64{0}); err == nil {
		t.Fatal("zero account id was accepted")
	}
	if _, err := s.SetAccountCooldown(ctx, 0, 1_000, AccountUsageWindowPrimary); err == nil {
		t.Fatal("cooldown on a zero account id was accepted")
	}
	if _, err := s.MarkAccountUnschedulable(ctx, 8702, 1_000, AccountUnschedulableReason("bogus")); err == nil {
		t.Fatal("unknown unschedulable reason was accepted")
	}
	if _, err := s.PauseAccountUsage(ctx, 8702, 1_000, AccountUsageWindow("bogus")); err == nil {
		t.Fatal("unknown usage window was accepted")
	}
	if runtimes, err := s.AccountRuntimeMany(ctx, nil); err != nil || runtimes != nil {
		t.Fatalf("empty batch = %v, %v; want nil, nil", runtimes, err)
	}
}
