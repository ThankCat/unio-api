// Package accountpool 表达池型渠道的第二阶段选号：从一池订阅账号里挑出本次出站用哪一个。
//
// 第一阶段（渠道层五项评分与确定性排序）不在这里，也不受本包影响。两阶段的分工是：
// 渠道层回答「打哪条线路」，池内回答「用哪个号」。渠道层同分时取更小 ID 以保证可复现，
// 池内恰恰相反——同档必须随机打散，否则一池同质账号会被稳定地按 ID 顺序捶打第一个。
//
// 本包是纯函数：账号事实来自数据库，运行态来自 Redis，都由调用方读好后传入。
// 这样选号规则可以脱离 Redis 与 Postgres 单测，也不会在热路径上藏一次隐式 IO。
package accountpool

import (
	"math/rand/v2"
	"sort"
	"time"
)

// Account 是池内一个账号的调度事实（数据库侧，随配置变更而变）。
type Account struct {
	ID int64
	// Priority 越小越靠前，同值由随机打散决胜。
	Priority int32
	// ConcurrencyLimit 为 nil 表示继承渠道默认；0 表示不限；正数为硬上限。
	ConcurrencyLimit *int64
	// LastSuccessAt 是最近一次完整成功时间，零值表示从未成功过（LRU 排最前）。
	LastSuccessAt time.Time
	// ConfigRevision 固化进 permit，供收口审计定位「这次是按哪版配置放行的」。
	ConfigRevision int64
}

// Runtime 是账号的短时运行态（Redis 侧）。三个剩余毫秒均以 Redis 服务端时间为准，
// 0 表示该状态此刻不生效。并发满不在其中——满员的账号仍然可调度，只是此刻没有空槽。
type Runtime struct {
	CooldownRemainingMs      int64
	UnschedulableRemainingMs int64
	UsagePauseRemainingMs    int64
	InFlight                 int64
}

// Member 是账号事实与运行态的合并视图，并带上已完成回落的并发上限。
type Member struct {
	Account
	Runtime

	// Limit 是回落后的账号并发上限：账号自身 → 渠道 account_default_concurrency → 不限。
	// 0 表示不限，与渠道并发同语义。
	Limit int64
}

// Blocked 表示该账号此刻被运行态挡在调度之外。三种状态都是有到期时刻的短事实，
// 到点自愈，因此它不代表账号失效，也不应触发任何持久化状态变更。
func (m Member) Blocked() bool {
	return m.CooldownRemainingMs > 0 || m.UnschedulableRemainingMs > 0 || m.UsagePauseRemainingMs > 0
}

// RecoveryMs 是该账号回到可调度所需的毫秒数：三个状态取最晚到期（都解除才算恢复）。
// 未被挡住时为 0。
func (m Member) RecoveryMs() int64 {
	recovery := m.CooldownRemainingMs
	if m.UnschedulableRemainingMs > recovery {
		recovery = m.UnschedulableRemainingMs
	}
	if m.UsagePauseRemainingMs > recovery {
		recovery = m.UsagePauseRemainingMs
	}
	return recovery
}

// LoadRatio 是账号当前负载率（0~1）。不限并发的账号恒为 0：它没有会被填满的槽位，
// 排序上应当与「完全空闲」等价。
func (m Member) LoadRatio() float64 {
	if m.Limit <= 0 {
		return 0
	}
	inFlight := m.InFlight
	if inFlight < 0 {
		inFlight = 0
	}
	ratio := float64(inFlight) / float64(m.Limit)
	if ratio > 1 {
		return 1
	}
	return ratio
}

// Pool 是一个池型渠道在某一时刻的账号快照。成员顺序不承载语义，排序由 Order 负责。
type Pool struct {
	Members []Member
}

// NewPool 把数据库账号列表与同序的运行态列表合并成快照，并完成并发上限回落。
// runtimes 短于 accounts 时缺失项按「运行态未知」处理（不冷却、无在途），
// 这只会让该账号被当作可调度并在 acquire 时被原子门禁拦下，不会放过真正的硬门槛。
func NewPool(accounts []Account, runtimes []Runtime, channelDefault *int64) Pool {
	members := make([]Member, 0, len(accounts))
	for index, account := range accounts {
		member := Member{Account: account, Limit: resolveLimit(account.ConcurrencyLimit, channelDefault)}
		if index < len(runtimes) {
			member.Runtime = runtimes[index]
		}
		members = append(members, member)
	}
	return Pool{Members: members}
}

// resolveLimit 执行并发上限回落链：账号 → 渠道默认 → 不限。
//
// 链末是「不限」而不是全局 channel_limit：那个全局默认限的是整条渠道，按它去限每个账号
// 会把「渠道最多 10 个在途」悄悄变成「每个账号各 10 个在途」，语义完全不同。
// 渠道级上限在 Lua 门禁里仍然先于账号槽生效，池整体不会因此失去保护。
func resolveLimit(accountLimit, channelDefault *int64) int64 {
	if accountLimit != nil {
		return normalizeLimit(*accountLimit)
	}
	if channelDefault != nil {
		return normalizeLimit(*channelDefault)
	}
	return 0
}

func normalizeLimit(value int64) int64 {
	if value < 0 {
		return 0
	}
	return value
}

// Schedulable 返回当前未被运行态挡住的账号，保持输入顺序。
func (p Pool) Schedulable() []Member {
	out := make([]Member, 0, len(p.Members))
	for _, member := range p.Members {
		if !member.Blocked() {
			out = append(out, member)
		}
	}
	return out
}

// Capacity 聚合全池空闲槽位，作为渠道层并发容量分的输入（五项评分公式与权重不变，
// 只是把「渠道的在途/上限」换成「池内可调度账号的在途/上限之和」）。
//
// 任一可调度账号不限并发即整池不限（limit=0）：此时池确实不存在会被填满的容量上界。
// 没有可调度账号时 known=false，调用方应当把该候选整体排除，而不是当成容量为 0。
func (p Pool) Capacity() (used, limit int64, known bool) {
	for _, member := range p.Members {
		if member.Blocked() {
			continue
		}
		known = true
		inFlight := member.InFlight
		if inFlight < 0 {
			inFlight = 0
		}
		used += inFlight
		if limit >= 0 {
			if member.Limit <= 0 {
				limit = -1
				continue
			}
			limit += member.Limit
		}
	}
	if limit < 0 {
		limit = 0
	}
	return used, limit, known
}

// EarliestRecoveryMs 是整池最早恢复可调度的毫秒数，供「全员冷却」时的 Retry-After。
// 返回 0 表示没有任何账号能给出恢复时刻（例如池里根本没有账号）。
func (p Pool) EarliestRecoveryMs() int64 {
	earliest := int64(0)
	for _, member := range p.Members {
		recovery := member.RecoveryMs()
		if recovery <= 0 {
			continue
		}
		if earliest == 0 || recovery < earliest {
			earliest = recovery
		}
	}
	return earliest
}

// Shuffle 打散同档账号。签名与 math/rand 的 Shuffle 一致，测试注入确定性实现。
type Shuffle func(n int, swap func(i, j int))

// Order 产出本次请求的池内尝试顺序（账号 ID）：
//
//	Sticky 绑定账号优先 → 过滤（已在 Schedulable 完成）→ 优先级 → 负载率 → 最久未使用（LRU）
//
// 同档随机打散的实现方式是「先整体打散，再做稳定排序」：稳定排序保留等价键之间的相对顺序，
// 于是等价的账号之间保留的就是随机顺序。这比排序后再逐段打散少一层易错的分段逻辑。
//
// stickyAccountID 不在可调度集合里时置顶自然落空，调用方据此区分「临时绕行」与「绑定失效」。
func (p Pool) Order(stickyAccountID int64, shuffle Shuffle) []int64 {
	members := p.Schedulable()
	if len(members) == 0 {
		return nil
	}
	if shuffle == nil {
		shuffle = rand.Shuffle
	}
	shuffle(len(members), func(i, j int) { members[i], members[j] = members[j], members[i] })
	sort.SliceStable(members, func(i, j int) bool {
		left, right := members[i], members[j]
		if left.Priority != right.Priority {
			return left.Priority < right.Priority
		}
		leftLoad, rightLoad := left.LoadRatio(), right.LoadRatio()
		if leftLoad != rightLoad {
			return leftLoad < rightLoad
		}
		if !left.LastSuccessAt.Equal(right.LastSuccessAt) {
			return left.LastSuccessAt.Before(right.LastSuccessAt)
		}
		return false
	})

	ids := make([]int64, 0, len(members))
	stickyFound := false
	for _, member := range members {
		if stickyAccountID > 0 && member.ID == stickyAccountID {
			stickyFound = true
			continue
		}
		ids = append(ids, member.ID)
	}
	if stickyFound {
		return append([]int64{stickyAccountID}, ids...)
	}
	return ids
}

// Lookup 按账号 ID 取回成员事实，供准入阶段填充 permit 的账号维度。
func (p Pool) Lookup(accountID int64) (Member, bool) {
	for _, member := range p.Members {
		if member.ID == accountID {
			return member, true
		}
	}
	return Member{}, false
}
