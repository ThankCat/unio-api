package breakerstore

import (
	"context"
	"errors"
	"strconv"
)

// 账号运行态只活在 Redis：冷却、临时不可调度、用量暂停都是分钟到小时级的短事实，写库既放大写入
// 又会让多实例看到不同版本。账号自身的启停（status）仍以数据库为准，两者互不覆盖。

// accountRuntimeGraceMs 是账号运行态 hash 在最后一个状态到期之后的额外存活时间。
// 留一段宽限期而不是掐点过期：读侧据此能把「刚刚恢复」与「从来没有过」区分开，也避免时钟毛刺
// 造成键在到期瞬间反复重建。
const accountRuntimeGraceMs = 5_000

// AccountRuntimeState 是账号被移出调度的三类事实，各自到期时刻独立。
type AccountRuntimeState string

const (
	// AccountStateCooldown 来自上游 429：到期时刻取自上游给出的窗口重置时刻。
	AccountStateCooldown AccountRuntimeState = "cooldown"
	// AccountStateUnschedulable 是本地判定的临时隔离（令牌刷新窗口、疑似代理故障）。
	AccountStateUnschedulable AccountRuntimeState = "unschedulable"
	// AccountStateUsagePaused 是用量水位达阈值后的提前退出，避免踩到上游 429。
	AccountStateUsagePaused AccountRuntimeState = "usage_pause"
)

// AccountUnschedulableReason 是临时隔离的受控原因，运维界面据此区分「刷新中」与「疑似代理故障」。
type AccountUnschedulableReason string

const (
	// AccountUnschedulableTokenRefresh 给令牌刷新留出窗口：401 只隔离，不禁用，确认吊销才禁用。
	AccountUnschedulableTokenRefresh AccountUnschedulableReason = "token_refresh"
	// AccountUnschedulableProxySuspect 对应持久传输错误（代理认证失败、连接拒绝、DNS 失败、无路由）。
	AccountUnschedulableProxySuspect AccountUnschedulableReason = "proxy_suspect"
	// AccountUnschedulableManual 是运维手工隔离。
	AccountUnschedulableManual AccountUnschedulableReason = "manual"
)

func (r AccountUnschedulableReason) valid() bool {
	switch r {
	case AccountUnschedulableTokenRefresh, AccountUnschedulableProxySuspect, AccountUnschedulableManual:
		return true
	default:
		return false
	}
}

// AccountUsageWindow 标出是哪个用量窗口触顶。primary 是 5 小时窗口、secondary 是 7 天窗口——
// 这个方向与上游 x-codex-primary/secondary 头一致，反过来会让 7 天水位被当成 5 小时水位处置。
type AccountUsageWindow string

const (
	AccountUsageWindowPrimary   AccountUsageWindow = "primary"
	AccountUsageWindowSecondary AccountUsageWindow = "secondary"
)

func (w AccountUsageWindow) valid() bool {
	return w == AccountUsageWindowPrimary || w == AccountUsageWindowSecondary
}

// AccountRuntime 是单个账号的运行态只读视图。三个剩余毫秒都以 Redis 服务端时间为准，
// 0 表示该状态此刻不生效。
type AccountRuntime struct {
	AccountID int64

	CooldownRemainingMs int64
	// CooldownWindow 是触发冷却的用量窗口（primary/secondary），上游未指明时为空。
	CooldownWindow AccountUsageWindow

	UnschedulableRemainingMs int64
	UnschedulableReason      AccountUnschedulableReason

	UsagePauseRemainingMs int64
	UsagePauseWindow      AccountUsageWindow

	// InFlight 是账号并发租约中尚未到期的 permit 数，供池内负载率排序与空闲槽位聚合。
	InFlight int64
}

// Schedulable 表示三类运行态都不生效。它不包含账号、渠道、Provider 的 enabled 状态：
// 那三层来自数据库，由调用方与本视图取交集（可调度性 = 账号 ∧ Channel ∧ Provider）。
func (r AccountRuntime) Schedulable() bool {
	return r.CooldownRemainingMs == 0 && r.UnschedulableRemainingMs == 0 && r.UsagePauseRemainingMs == 0
}

// SetAccountCooldown 登记账号 429 冷却，durationMs 由调用方从上游 reset_at 换算而来。
//
// 与 SetChannel429Cooldown 的「只延长」不同，账号冷却按最近一次观测覆盖：官方支持付费即时重置与
// 可储存重置，reset_at 会提前，只增不减会让配额已恢复的账号继续被停用数小时。durationMs <= 0
// 表示确认已恢复，直接清除。
func (s *Store) SetAccountCooldown(
	ctx context.Context,
	accountID, durationMs int64,
	window AccountUsageWindow,
) (untilMs int64, err error) {
	if window != "" && !window.valid() {
		return 0, configInvalid("unknown account cooldown usage window")
	}
	return s.markAccountRuntime(ctx, accountID, AccountStateCooldown, durationMs, string(window))
}

// ClearAccountCooldown 在确认账号配额已恢复时立即解除冷却。
func (s *Store) ClearAccountCooldown(ctx context.Context, accountID int64) error {
	_, err := s.markAccountRuntime(ctx, accountID, AccountStateCooldown, 0, "")
	return err
}

// MarkAccountUnschedulable 把账号临时移出调度。多个故障源叠加时取最晚到期：一次 401 的刷新窗口
// 不该把更长的代理故障隔离提前解除。
func (s *Store) MarkAccountUnschedulable(
	ctx context.Context,
	accountID, durationMs int64,
	reason AccountUnschedulableReason,
) (untilMs int64, err error) {
	if durationMs <= 0 {
		return 0, configInvalid("account unschedulable duration must be positive")
	}
	if !reason.valid() {
		return 0, configInvalid("unknown account unschedulable reason")
	}
	return s.markAccountRuntime(ctx, accountID, AccountStateUnschedulable, durationMs, string(reason))
}

// ClearAccountUnschedulable 在令牌刷新成功或代理恢复后立即放回调度，不必等隔离窗口自然到期。
func (s *Store) ClearAccountUnschedulable(ctx context.Context, accountID int64) error {
	_, err := s.markAccountRuntime(ctx, accountID, AccountStateUnschedulable, 0, "")
	return err
}

// PauseAccountUsage 在用量水位达阈值时提前把账号移出调度，到期时刻取窗口重置时刻。
// 与冷却同为覆盖语义：更新的快照可能把重置时刻提前（付费重置），也可能推后。
func (s *Store) PauseAccountUsage(
	ctx context.Context,
	accountID, durationMs int64,
	window AccountUsageWindow,
) (untilMs int64, err error) {
	if durationMs <= 0 {
		return 0, configInvalid("account usage pause duration must be positive")
	}
	if !window.valid() {
		return 0, configInvalid("unknown account usage pause window")
	}
	return s.markAccountRuntime(ctx, accountID, AccountStateUsagePaused, durationMs, string(window))
}

// ResumeAccountUsage 在窗口重置或快照回落到阈值之下时恢复调度。
func (s *Store) ResumeAccountUsage(ctx context.Context, accountID int64) error {
	_, err := s.markAccountRuntime(ctx, accountID, AccountStateUsagePaused, 0, "")
	return err
}

func (s *Store) markAccountRuntime(
	ctx context.Context,
	accountID int64,
	state AccountRuntimeState,
	durationMs int64,
	reason string,
) (untilMs int64, err error) {
	done := s.beginOperation(ctx, operationMarkAccountRuntime)
	defer func() {
		result := operationResultIdle
		if untilMs > 0 {
			result = operationResultActive
		}
		done(result, err)
	}()

	if accountID <= 0 {
		return 0, configInvalid("account id must be positive")
	}
	if durationMs < 0 {
		durationMs = 0
	}
	if durationMs > maxLuaExactInteger {
		return 0, configInvalid("account runtime duration must be a Lua-exact integer")
	}
	res, err := s.accountRuntimeMark.Run(ctx, s.client,
		[]string{s.keys.account(accountID)},
		string(state),
		strconv.FormatInt(durationMs, 10),
		reason,
		strconv.FormatInt(accountRuntimeGraceMs, 10),
	).Result()
	if err != nil {
		return 0, storeUnavailable(err, "breakerstore mark account runtime")
	}
	arr, ok := res.([]interface{})
	if !ok || len(arr) == 0 {
		return 0, storeUnavailable(errors.New("unexpected account runtime reply"), "breakerstore mark account runtime")
	}
	until, _ := redisInt64(arr[0])
	return until, nil
}

// AccountRuntimeMany 一次读出一池账号的运行态与在途并发，供候选快照聚合空闲槽位与池内选号过滤。
// 返回顺序与入参一致；重复的账号 id 视为调用方错误。
func (s *Store) AccountRuntimeMany(ctx context.Context, accountIDs []int64) (runtimes []AccountRuntime, err error) {
	done := s.beginOperation(ctx, operationReadAccountRuntime)
	defer func() { done(operationResultSuccess, err) }()

	if len(accountIDs) == 0 {
		return nil, nil
	}
	seen := make(map[int64]struct{}, len(accountIDs))
	keys := make([]string, 0, len(accountIDs)*2)
	for _, id := range accountIDs {
		if id <= 0 {
			return nil, configInvalid("account id must be positive")
		}
		if _, duplicate := seen[id]; duplicate {
			return nil, configInvalid("account runtime batch must not repeat an account id")
		}
		seen[id] = struct{}{}
		keys = append(keys, s.keys.account(id), s.keys.accountConcurrency(id))
	}

	res, err := s.accountRuntimeRead.Run(ctx, s.client, keys, strconv.Itoa(len(accountIDs))).Result()
	if err != nil {
		return nil, storeUnavailable(err, "breakerstore read account runtime")
	}
	reply, ok := res.([]interface{})
	if !ok || len(reply) != 2 {
		return nil, storeUnavailable(errors.New("unexpected account runtime batch reply"), "breakerstore read account runtime")
	}
	rows, ok := reply[1].([]interface{})
	if !ok || len(rows) != len(accountIDs) {
		return nil, storeUnavailable(errors.New("account runtime batch arity mismatch"), "breakerstore read account runtime")
	}

	runtimes = make([]AccountRuntime, 0, len(rows))
	for index, raw := range rows {
		row, ok := raw.([]interface{})
		if !ok || len(row) != 7 {
			return nil, storeUnavailable(errors.New("unexpected account runtime row"), "breakerstore read account runtime")
		}
		cooldown, _ := redisInt64(row[0])
		cooldownWindow, _ := redisString(row[1])
		unschedulable, _ := redisInt64(row[2])
		unschedulableReason, _ := redisString(row[3])
		usagePause, _ := redisInt64(row[4])
		usagePauseWindow, _ := redisString(row[5])
		inFlight, _ := redisInt64(row[6])
		runtimes = append(runtimes, AccountRuntime{
			AccountID:                accountIDs[index],
			CooldownRemainingMs:      cooldown,
			CooldownWindow:           AccountUsageWindow(cooldownWindow),
			UnschedulableRemainingMs: unschedulable,
			UnschedulableReason:      AccountUnschedulableReason(unschedulableReason),
			UsagePauseRemainingMs:    usagePause,
			UsagePauseWindow:         AccountUsageWindow(usagePauseWindow),
			InFlight:                 inFlight,
		})
	}
	return runtimes, nil
}
