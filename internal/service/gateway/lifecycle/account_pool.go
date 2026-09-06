package lifecycle

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/ThankCat/unio-gateway/internal/core/accountpool"
	"github.com/ThankCat/unio-gateway/internal/core/accountusage"
	"github.com/ThankCat/unio-gateway/internal/core/routing"
	"github.com/ThankCat/unio-gateway/internal/platform/breakerstore"
	"github.com/ThankCat/unio-gateway/internal/platform/store/sqlc"
)

// 池型渠道的第二阶段选号需要两份事实：账号配置（Postgres）与账号运行态（Redis）。
// 两者都按渠道成批读取——单请求可能有多个池型候选，逐个账号往返会把选路延迟做成 N+1。
// 用量暂停不读 Redis：按账号行里的 usage_snapshot 与三层继承的阈值（账号 → 渠道 → 全局）实时判定，
// 任一层阈值改动对下一次请求即生效。

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

	now := e.now()
	globalThreshold := e.globalUsagePauseThreshold(ctx)
	accounts := make([]accountpool.Account, 0, len(rows))
	usagePauseMs := make([]int64, 0, len(rows))
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
		snapshot, hasSnapshot := accountusage.ParseSnapshot(row.UsageSnapshot)
		if hasSnapshot && snapshot.Primary != nil {
			account.UsageResetAtUnix = snapshot.Primary.ResetAt
		}
		accounts = append(accounts, account)
		usagePauseMs = append(usagePauseMs, derivedUsagePauseMs(snapshot, hasSnapshot, row, globalThreshold, now))
		ids = append(ids, row.ID)
	}

	runtimes, err := e.accountRuntime.AccountRuntimeMany(ctx, ids)
	if err != nil {
		// 账号运行态与渠道运行态同为 fail-closed：读不到就不敢调度，避免把请求打到
		// 一个正在 429 冷却的号上，再由上游把整池的封禁风险放大。
		return accountPoolSnapshot{Reason: accountPoolReasonUnavailable}
	}

	pool := accountpool.NewPool(accounts, accountRuntimeFacts(runtimes, usagePauseMs), candidate.AccountDefaultConcurrency)
	if len(pool.Schedulable()) == 0 {
		return accountPoolSnapshot{Pool: pool, Reason: accountPoolReasonCooldown}
	}
	return accountPoolSnapshot{Pool: pool}
}

// globalUsagePauseThreshold 取全局账号用量暂停阈值；未注入热读取时用代码默认。
func (e *Executor) globalUsagePauseThreshold(ctx context.Context) int32 {
	if e.usagePauseThreshold != nil {
		if v := e.usagePauseThreshold(ctx); accountusage.ValidThreshold(v) {
			return v
		}
	}
	return accountusage.DefaultThresholdPercent
}

// derivedUsagePauseMs 按「账号快照水位 vs 三层继承的生效阈值」实时判定用量暂停剩余毫秒；0 表示不暂停。
// 这是用量暂停的唯一拦截依据：Redis 里的 usage_pause 标记只是展示缓存，不参与调度——
// 否则阈值放宽后，旧阈值写下的标记会继续把账号锁到窗口重置。无快照的账号无从判定，视为不暂停。
func derivedUsagePauseMs(
	snapshot accountusage.Snapshot,
	hasSnapshot bool,
	row sqlc.ListSchedulableAccountsByChannelRow,
	globalThreshold int32,
	now time.Time,
) int64 {
	if !hasSnapshot {
		return 0
	}
	threshold := accountusage.ResolveThreshold(
		thresholdOverride(row.UsagePauseThresholdPercent),
		thresholdOverride(row.AccountUsagePauseThresholdPercent),
		globalThreshold,
	)
	return accountusage.Evaluate(snapshot, threshold.Percent, now).RemainingMs(now)
}

// thresholdOverride 把可空的阈值列还原为 *int32（NULL → nil，表示继承上一层）。
func thresholdOverride(v pgtype.Int4) *int32 {
	if !v.Valid {
		return nil
	}
	percent := v.Int32
	return &percent
}

// accountRuntimeFacts 合并 Redis 运行态与按快照派生的用量暂停。冷却、临时不可调度与在途仍以 Redis 为准；
// 用量暂停一律取派生值（usagePauseMs 与 runtimes 同序，短于 runtimes 的部分按不暂停处理）。
func accountRuntimeFacts(runtimes []breakerstore.AccountRuntime, usagePauseMs []int64) []accountpool.Runtime {
	out := make([]accountpool.Runtime, 0, len(runtimes))
	for index, runtime := range runtimes {
		var pauseMs int64
		if index < len(usagePauseMs) {
			pauseMs = usagePauseMs[index]
		}
		out = append(out, accountpool.Runtime{
			CooldownRemainingMs:      runtime.CooldownRemainingMs,
			UnschedulableRemainingMs: runtime.UnschedulableRemainingMs,
			UsagePauseRemainingMs:    pauseMs,
			InFlight:                 runtime.InFlight,
		})
	}
	return out
}
