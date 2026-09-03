package accountpool

import (
	"math/rand/v2"
	"testing"
	"time"
)

func limitPtr(v int64) *int64 { return &v }

// 并发上限回落链：账号 → 渠道默认 → 不限。0 是「显式不限」，不能与「未设置」混为一谈。
func TestNewPoolResolvesConcurrencyLimitFallback(t *testing.T) {
	channelDefault := int64(4)
	accounts := []Account{
		{ID: 1, ConcurrencyLimit: limitPtr(2)},
		{ID: 2},
		{ID: 3, ConcurrencyLimit: limitPtr(0)},
	}
	pool := NewPool(accounts, nil, &channelDefault)

	want := []int64{2, 4, 0}
	for index, expected := range want {
		if pool.Members[index].Limit != expected {
			t.Fatalf("member %d limit = %d, want %d", index, pool.Members[index].Limit, expected)
		}
	}

	noDefault := NewPool([]Account{{ID: 9}}, nil, nil)
	if noDefault.Members[0].Limit != 0 {
		t.Fatalf("missing channel default must fall back to unlimited, got %d", noDefault.Members[0].Limit)
	}
}

// 过滤链只看三种「有到期时刻的短事实」；并发满不属于过滤条件——满员的账号依然可调度，
// 只是此刻没有空槽，由原子准入去判定并换号。
func TestSchedulableExcludesOnlyRuntimeStates(t *testing.T) {
	pool := NewPool(
		[]Account{{ID: 1}, {ID: 2}, {ID: 3}, {ID: 4}, {ID: 5}},
		[]Runtime{
			{},
			{CooldownRemainingMs: 1000},
			{UnschedulableRemainingMs: 2000},
			{UsagePauseRemainingMs: 3000},
			{InFlight: 99},
		},
		limitPtr(1),
	)

	schedulable := pool.Schedulable()
	if len(schedulable) != 2 {
		t.Fatalf("schedulable = %d, want 2 (healthy + saturated)", len(schedulable))
	}
	if schedulable[0].ID != 1 || schedulable[1].ID != 5 {
		t.Fatalf("schedulable ids = %d,%d want 1,5", schedulable[0].ID, schedulable[1].ID)
	}
}

// 容量分输入是全池可调度账号的在途/上限聚合；被运行态挡住的账号不贡献任何容量。
func TestCapacityAggregatesSchedulableMembersOnly(t *testing.T) {
	pool := NewPool(
		[]Account{{ID: 1}, {ID: 2}, {ID: 3}},
		[]Runtime{{InFlight: 1}, {InFlight: 3}, {InFlight: 5, CooldownRemainingMs: 500}},
		limitPtr(4),
	)

	used, limit, known := pool.Capacity()
	if !known || used != 4 || limit != 8 {
		t.Fatalf("capacity = (used=%d limit=%d known=%v), want (4, 8, true)", used, limit, known)
	}
}

// 任一可调度账号不限并发即整池不限：此时池不存在会被填满的容量上界，
// limit 必须回到 0（评分侧的「不限」表达），不能退化成其他账号上限之和。
func TestCapacityUnlimitedWhenAnyMemberUnlimited(t *testing.T) {
	pool := NewPool(
		[]Account{{ID: 1, ConcurrencyLimit: limitPtr(2)}, {ID: 2, ConcurrencyLimit: limitPtr(0)}},
		[]Runtime{{InFlight: 2}, {InFlight: 7}},
		nil,
	)

	used, limit, known := pool.Capacity()
	if !known || used != 9 || limit != 0 {
		t.Fatalf("capacity = (used=%d limit=%d known=%v), want (9, 0, true)", used, limit, known)
	}
}

// 没有可调度账号时容量未知：调用方必须把候选整体排除，而不是当成「容量为 0 的可用渠道」。
func TestCapacityUnknownWhenPoolFullyBlocked(t *testing.T) {
	pool := NewPool(
		[]Account{{ID: 1}, {ID: 2}},
		[]Runtime{{CooldownRemainingMs: 4000}, {UsagePauseRemainingMs: 1500}},
		limitPtr(2),
	)

	if _, _, known := pool.Capacity(); known {
		t.Fatal("fully blocked pool must report unknown capacity")
	}
	if got := pool.EarliestRecoveryMs(); got != 1500 {
		t.Fatalf("earliest recovery = %d, want 1500 (soonest of the two)", got)
	}
}

// 单个账号叠加多种状态时，恢复时刻取最晚：任何一种仍然生效，账号就还不能用。
func TestRecoveryMsTakesLatestStateOnOneAccount(t *testing.T) {
	pool := NewPool(
		[]Account{{ID: 1}},
		[]Runtime{{CooldownRemainingMs: 1000, UnschedulableRemainingMs: 9000}},
		nil,
	)
	if got := pool.Members[0].RecoveryMs(); got != 9000 {
		t.Fatalf("recovery = %d, want 9000", got)
	}
}

// 排序主键顺序：优先级 → 负载率 → LRU。
func TestOrderSortsByPriorityThenLoadThenLRU(t *testing.T) {
	base := time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC)
	pool := NewPool(
		[]Account{
			{ID: 1, Priority: 50, LastSuccessAt: base},
			{ID: 2, Priority: 10, LastSuccessAt: base},
			{ID: 3, Priority: 50, LastSuccessAt: base.Add(-time.Hour)},
			{ID: 4, Priority: 10, LastSuccessAt: base.Add(-time.Minute)},
		},
		[]Runtime{{InFlight: 0}, {InFlight: 3}, {InFlight: 0}, {InFlight: 3}},
		limitPtr(4),
	)

	// 打散固定为恒等置换，只考察排序键本身。
	got := pool.Order(0, func(int, func(int, int)) {}, OrderOptions{})
	want := []int64{4, 2, 3, 1}
	for index := range want {
		if got[index] != want[index] {
			t.Fatalf("order = %v, want %v", got, want)
		}
	}
}

// 同档必须随机打散：一池同质账号若按 ID 稳定排序，第一个号会被持续捶打。
func TestOrderShufflesEqualTier(t *testing.T) {
	accounts := make([]Account, 0, 8)
	for id := int64(1); id <= 8; id++ {
		accounts = append(accounts, Account{ID: id, Priority: 50})
	}
	pool := NewPool(accounts, nil, limitPtr(4))

	source := rand.New(rand.NewPCG(1, 2))
	firstSeen := make(map[int64]int)
	for i := 0; i < 400; i++ {
		order := pool.Order(0, source.Shuffle, OrderOptions{})
		firstSeen[order[0]]++
	}
	if len(firstSeen) < 6 {
		t.Fatalf("equal-tier accounts must spread across first position, got %v", firstSeen)
	}
}

// Sticky 命中即置顶，其余顺序不变；未命中时置顶落空且不改变其他账号的相对顺序。
func TestOrderPinsStickyAccount(t *testing.T) {
	pool := NewPool(
		[]Account{{ID: 1, Priority: 10}, {ID: 2, Priority: 20}, {ID: 3, Priority: 30}},
		nil,
		limitPtr(2),
	)
	identity := func(int, func(int, int)) {}

	pinned := pool.Order(3, identity, OrderOptions{})
	if len(pinned) != 3 || pinned[0] != 3 || pinned[1] != 1 || pinned[2] != 2 {
		t.Fatalf("sticky order = %v, want [3 1 2]", pinned)
	}

	missing := pool.Order(99, identity, OrderOptions{})
	if len(missing) != 3 || missing[0] != 1 {
		t.Fatalf("unknown sticky account must not change order, got %v", missing)
	}
}

// 被运行态挡住的 Sticky 账号不得出现在尝试序列里：绑定保留与否是调用方的决定，
// 但本次绝不能再打到一个正在冷却的号上。
func TestOrderDropsBlockedStickyAccount(t *testing.T) {
	pool := NewPool(
		[]Account{{ID: 1}, {ID: 2}},
		[]Runtime{{CooldownRemainingMs: 5000}, {}},
		limitPtr(2),
	)
	got := pool.Order(1, func(int, func(int, int)) {}, OrderOptions{})
	if len(got) != 1 || got[0] != 2 {
		t.Fatalf("blocked sticky account must be skipped, got %v", got)
	}
}

// TestOrderPreferSoonestReset 冻结 use-it-or-lose-it 排序（与 sub2api prefer_soonest_reset 同语义）：
// 同优先级内，5h 窗口最早重置者先用；无活跃窗口观测（0 或已过期）排在有观测者之后；
// 开关关闭时该维度完全不参与。
func TestOrderPreferSoonestReset(t *testing.T) {
	now := int64(1_000_000)
	identity := func(int, func(int, int)) {}
	pool := NewPool([]Account{
		{ID: 1, Priority: 10, UsageResetAtUnix: now + 3600}, // 1 小时后重置
		{ID: 2, Priority: 10, UsageResetAtUnix: now + 60},   // 1 分钟后重置（最早，应最先用）
		{ID: 3, Priority: 10},                               // 无观测，排最后
		{ID: 4, Priority: 10, UsageResetAtUnix: now - 10},   // 已过期观测 = 无活跃窗口
	}, nil, nil)

	got := pool.Order(0, identity, OrderOptions{PreferSoonestReset: true, NowUnix: now})
	if len(got) != 4 || got[0] != 2 || got[1] != 1 {
		t.Fatalf("prefer soonest reset order = %v, want [2 1 ...]", got)
	}
	// 3 与 4 都是无活跃窗口，顺序由后续键（LRU 均零值）保持稳定，但必须在 1、2 之后。
	if got[2] == 1 || got[2] == 2 || got[3] == 1 || got[3] == 2 {
		t.Fatalf("accounts without active window must sort last, got %v", got)
	}

	// 关闭开关：reset 维度不参与，全员同档（priority 相同、负载与 LRU 零值），保持输入序。
	off := pool.Order(0, identity, OrderOptions{})
	if len(off) != 4 || off[0] != 1 || off[1] != 2 || off[2] != 3 || off[3] != 4 {
		t.Fatalf("disabled option must not reorder, got %v", off)
	}
}
