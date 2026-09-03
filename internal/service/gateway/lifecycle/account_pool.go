package lifecycle

import (
	"context"
	"encoding/json"

	"github.com/ThankCat/unio-gateway/internal/core/accountpool"
	"github.com/ThankCat/unio-gateway/internal/core/routing"
	"github.com/ThankCat/unio-gateway/internal/platform/breakerstore"
	"github.com/ThankCat/unio-gateway/internal/platform/store/sqlc"
)

// 池型渠道的第二阶段选号需要两份事实：账号配置（Postgres）与账号运行态（Redis）。
// 两者都按渠道成批读取——单请求可能有多个池型候选，逐个账号往返会把选路延迟做成 N+1。

// AccountPoolStore 是候选准备读取池内账号所需的最小数据库能力。
type AccountPoolStore interface {
	ListSchedulableAccountsByChannel(ctx context.Context, channelID int64) ([]sqlc.ListSchedulableAccountsByChannelRow, error)
}

// AccountRuntimeStore 是候选准备读取账号运行态所需的最小 Redis 能力。
type AccountRuntimeStore interface {
	AccountRuntimeMany(ctx context.Context, accountIDs []int64) ([]breakerstore.AccountRuntime, error)
}

// accountPoolExclusionReason 标出池型候选为何拿不到账号。两种原因必须分开：
// 冷却是有恢复时刻的短事实（可给 Retry-After），池空是配置事实（要运维介入）。
const (
	accountPoolReasonEmpty    = "account_pool_empty"
	accountPoolReasonCooldown = "account_pool_cooldown"
	// accountPoolReasonUnavailable 表示账号事实读不出来（DB/Redis 故障或未注入读取器）。
	// 与 Redis 运行态一致地 fail-closed：宁可少一个候选，也不拿一个不知道能不能用的号出站。
	accountPoolReasonUnavailable = "account_pool_unavailable"
)

// accountPoolSnapshot 是一个池型候选在本次请求内冻结的池快照。
// 冻结而不是每次 acquire 重读：同一请求内换号重试必须基于同一份事实，
// 否则「同账号不重复」的判断会踩在一份中途变化的名单上。
type accountPoolSnapshot struct {
	Pool   accountpool.Pool
	Reason string
}

// usable 表示该池此刻至少有一个账号可以尝试。
func (s accountPoolSnapshot) usable() bool {
	return s.Reason == "" && len(s.Pool.Schedulable()) > 0
}

// loadAccountPools 为全部池型候选读取账号快照。credential 型候选不出现在结果里。
// 单个渠道读取失败不影响其他候选：失败的那个池被标成 unavailable 并在资格判定里排除。
func (e *Executor) loadAccountPools(
	ctx context.Context,
	candidates []routing.ChatRouteCandidate,
) map[int64]accountPoolSnapshot {
	var pools map[int64]accountPoolSnapshot
	for _, candidate := range candidates {
		if !candidate.SupplyForm.IsPool() {
			continue
		}
		if pools == nil {
			pools = make(map[int64]accountPoolSnapshot, len(candidates))
		}
		if _, done := pools[candidate.Channel.ID]; done {
			continue
		}
		pools[candidate.Channel.ID] = e.loadAccountPool(ctx, candidate)
	}
	return pools
}

func (e *Executor) loadAccountPool(ctx context.Context, candidate routing.ChatRouteCandidate) accountPoolSnapshot {
	if e.accountPool == nil || e.accountRuntime == nil {
		return accountPoolSnapshot{Reason: accountPoolReasonUnavailable}
	}
	rows, err := e.accountPool.ListSchedulableAccountsByChannel(ctx, candidate.Channel.ID)
	if err != nil {
		return accountPoolSnapshot{Reason: accountPoolReasonUnavailable}
	}
	if len(rows) == 0 {
		// 候选 SQL 已要求池内至少一个 enabled 账号，走到这里说明账号在两次查询之间被停用了。
		return accountPoolSnapshot{Reason: accountPoolReasonEmpty}
	}

	accounts := make([]accountpool.Account, 0, len(rows))
	ids := make([]int64, 0, len(rows))
	for _, row := range rows {
		account := accountpool.Account{
			ID:             row.ID,
			Priority:       row.Priority,
			ConfigRevision: row.ConfigRevision,
		}
		if row.ConcurrencyLimit.Valid {
			limit := int64(row.ConcurrencyLimit.Int32)
			account.ConcurrencyLimit = &limit
		}
		if row.LastSuccessAt.Valid {
			account.LastSuccessAt = row.LastSuccessAt.Time
		}
		account.UsageResetAtUnix = usagePrimaryResetAt(row.UsageSnapshot)
		accounts = append(accounts, account)
		ids = append(ids, row.ID)
	}

	runtimes, err := e.accountRuntime.AccountRuntimeMany(ctx, ids)
	if err != nil {
		// 账号运行态与渠道运行态同为 fail-closed：读不到就不敢调度，避免把请求打到
		// 一个正在 429 冷却的号上，再由上游把整池的封禁风险放大。
		return accountPoolSnapshot{Reason: accountPoolReasonUnavailable}
	}

	pool := accountpool.NewPool(accounts, accountRuntimeFacts(runtimes), candidate.AccountDefaultConcurrency)
	if len(pool.Schedulable()) == 0 {
		return accountPoolSnapshot{Pool: pool, Reason: accountPoolReasonCooldown}
	}
	return accountPoolSnapshot{Pool: pool}
}

// usagePrimaryResetAt 从 usage_snapshot 文档取 5h 窗口重置时刻（unix 秒）；缺失/解析失败返回 0。
// 只解一个字段，损坏的快照静默忽略——排序退化为现状，不能因观测数据坏掉阻断选号。
func usagePrimaryResetAt(snapshot []byte) int64 {
	if len(snapshot) == 0 {
		return 0
	}
	var doc struct {
		Primary struct {
			ResetAt int64 `json:"reset_at"`
		} `json:"primary"`
	}
	if err := json.Unmarshal(snapshot, &doc); err != nil {
		return 0
	}
	return doc.Primary.ResetAt
}

func accountRuntimeFacts(runtimes []breakerstore.AccountRuntime) []accountpool.Runtime {
	out := make([]accountpool.Runtime, 0, len(runtimes))
	for _, runtime := range runtimes {
		out = append(out, accountpool.Runtime{
			CooldownRemainingMs:      runtime.CooldownRemainingMs,
			UnschedulableRemainingMs: runtime.UnschedulableRemainingMs,
			UsagePauseRemainingMs:    runtime.UsagePauseRemainingMs,
			InFlight:                 runtime.InFlight,
		})
	}
	return out
}
