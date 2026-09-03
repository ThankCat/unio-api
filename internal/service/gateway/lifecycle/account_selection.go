package lifecycle

import (
	"context"
	"time"

	"github.com/ThankCat/unio-gateway/internal/core/accountpool"
	"github.com/ThankCat/unio-gateway/internal/core/requestlog"
	"github.com/ThankCat/unio-gateway/internal/platform/breakerstore"
)

// accountAttemptBudget 限制单个请求在同一个池内一次 acquire 扫描最多尝试多少个账号。
// 池可能挂着几十个号，逐个试到底会把一次请求的准入耗时放大成 N 次 Redis 往返；
// 试过几个都拿不到槽，基本可以断定整池饱和，换渠道比继续换号更快。
const accountAttemptBudget = 5

// accountAcquireOutcome 是一次池内 acquire 扫描的完整结果，供 trace 记录实际试过哪些号。
type accountAcquireOutcome struct {
	Admission breakerstore.AttemptAdmission
	Owner     *AttemptPermitOwner
	Account   accountpool.Member
	// TriedAccountIDs 是本次扫描真实向 Redis 发起过 acquire 的账号（按尝试顺序）。
	TriedAccountIDs []int64
}

// acquireCandidatePermit 为一个候选取得 permit。
//
// credential 型候选与改造前完全一致：一次 Acquire，结果原样返回。
// 池型候选在这里完成第二阶段选号——Sticky 绑定账号置顶后按池内顺序逐号 AttemptPermit，
// 账号槽满或账号冷却就换下一个号；账号级拒绝才换号，渠道级拒绝（熔断、渠道冷却、权限暂停、
// 配置不同步）立即返回，因为换哪个号都会得到同一个答案。
//
// attemptedAccounts 是请求级的，跨候选与跨扫描共享：同一账号在单请求内绝不重复尝试（边界 6）。
func (r *AttemptRunner) acquireCandidatePermit(
	ctx context.Context,
	prepared Candidate,
	endpoint requestlog.UpstreamEndpoint,
	mode breakerstore.RequestMode,
	inputEstimate int64,
	stickyAccountID int64,
	attemptedAccounts map[int64]bool,
) (accountAcquireOutcome, error) {
	if prepared.AccountPool == nil {
		admission, owner, err := r.permitManager.Acquire(ctx, AttemptPermitAcquireParams{
			Candidate:        prepared.Route,
			UpstreamEndpoint: endpoint,
			RequestMode:      mode,
			InputEstimate:    inputEstimate,
		})
		return accountAcquireOutcome{Admission: admission, Owner: owner}, err
	}

	pool := *prepared.AccountPool
	var denials accountDenialSummary
	var tried []int64
	orderOpts := accountpool.OrderOptions{}
	if r.permitManager != nil && r.permitManager.AccountPoolPreferSoonestReset() {
		orderOpts = accountpool.OrderOptions{PreferSoonestReset: true, NowUnix: time.Now().Unix()}
	}
	for _, accountID := range pool.Order(stickyAccountID, nil, orderOpts) {
		if attemptedAccounts[accountID] {
			continue
		}
		if len(tried) >= accountAttemptBudget {
			break
		}
		member, ok := pool.Lookup(accountID)
		if !ok {
			continue
		}
		tried = append(tried, accountID)
		attemptedAccounts[accountID] = true

		admission, owner, err := r.permitManager.Acquire(ctx, AttemptPermitAcquireParams{
			Candidate:        prepared.Route,
			UpstreamEndpoint: endpoint,
			RequestMode:      mode,
			InputEstimate:    inputEstimate,
			Account:          member,
		})
		if err != nil {
			return accountAcquireOutcome{Admission: admission, Owner: owner, Account: member, TriedAccountIDs: tried}, err
		}
		if admission.Mode == breakerstore.AdmissionPermit {
			return accountAcquireOutcome{Admission: admission, Owner: owner, Account: member, TriedAccountIDs: tried}, nil
		}
		if !accountScopedDenial(admission.Reason) {
			// 渠道级拒绝与账号无关，换号是白跑一趟 Redis。
			return accountAcquireOutcome{Admission: admission, TriedAccountIDs: tried}, nil
		}
		denials.record(admission)
	}

	return accountAcquireOutcome{Admission: denials.admission(), TriedAccountIDs: tried}, nil
}

// accountScopedDenial 区分「换个号也许能过」与「换号没有意义」。
func accountScopedDenial(reason breakerstore.DeniedReason) bool {
	switch reason {
	case breakerstore.ReasonAccountConcurrencyFull,
		breakerstore.ReasonAccountCooldown,
		breakerstore.ReasonAccountUnschedulable:
		return true
	default:
		return false
	}
}

// accountDenialSummary 把一轮池内逐号扫描的结果折叠成一个渠道级拒绝原因（边界 5）。
//
// 映射必须确定：池内只要还有账号是「仅并发满」，整个渠道就报 concurrency_full——它是可等待的，
// 全池短等的进入条件依赖这一点；全部因冷却/临时不可调度才报 cooldown，并带上最早恢复时刻。
// 把冷却伪装成并发满会让请求白等一轮，把并发满伪装成冷却则会让本可短等的请求直接失败。
type accountDenialSummary struct {
	seen                 bool
	concurrencyFull      int
	recoverable          int
	minRecoveryMs        int64
	lastNonRecoverReason breakerstore.DeniedReason
}

func (s *accountDenialSummary) record(admission breakerstore.AttemptAdmission) {
	s.seen = true
	switch admission.Reason {
	case breakerstore.ReasonAccountConcurrencyFull:
		s.concurrencyFull++
	case breakerstore.ReasonAccountCooldown, breakerstore.ReasonAccountUnschedulable:
		s.recoverable++
		if remaining := admission.CooldownRemainingMs; remaining > 0 &&
			(s.minRecoveryMs == 0 || remaining < s.minRecoveryMs) {
			s.minRecoveryMs = remaining
		}
	default:
		s.lastNonRecoverReason = admission.Reason
	}
}

func (s accountDenialSummary) admission() breakerstore.AttemptAdmission {
	if !s.seen {
		// 池内没有任何号可试：本请求已把可调度账号试尽（跨候选共享的 attemptedAccounts 挡掉了全部）。
		// 这不是池的状态事实——每个号的拒绝原因在第一次尝试时已被记录过——所以用专属原因让
		// runner 跳过且不再计入拒绝汇总，避免把「试尽」二次折叠成并发满或冷却而污染最终错误码。
		return breakerstore.AttemptAdmission{
			Mode:   breakerstore.AdmissionDenied,
			Reason: breakerstore.ReasonAccountPoolExhausted,
		}
	}
	if s.concurrencyFull > 0 {
		return breakerstore.AttemptAdmission{
			Mode:   breakerstore.AdmissionDenied,
			Reason: breakerstore.ReasonConcurrencyFull,
		}
	}
	if s.recoverable > 0 {
		return breakerstore.AttemptAdmission{
			Mode:                breakerstore.AdmissionDenied,
			Reason:              breakerstore.ReasonCooldown,
			CooldownRemainingMs: s.minRecoveryMs,
		}
	}
	return breakerstore.AttemptAdmission{Mode: breakerstore.AdmissionDenied, Reason: s.lastNonRecoverReason}
}

// poolChannelTransportBudget 是单请求内一个池型渠道最多发起的真实上游调用次数：
// 1 次原始 + 1 次换号重试（边界 6 的次数上限）。credential 型渠道恒为 1（§3.5 禁止 A→B→A 不变）。
const poolChannelTransportBudget = 2

// transportBudget 返回该候选在单请求内的真实上游调用预算。
func transportBudget(prepared Candidate) int {
	if prepared.AccountPool != nil {
		return poolChannelTransportBudget
	}
	return 1
}

// expandPoolRetryCandidates 把池型候选原位连续复制到传输预算次数，实现「同渠道换号重试」：
// 候选列表 [A(pool), B] 展开为 [A, A, B]，第一次 A 传输失败且可重试时，紧随其后的 A 会以
// 不同账号（attemptedAccounts 保证）再试一次，然后才轮到 B——A(a1)→A(a2)→B，而 A→B→A 仍被禁止。
// credential 型候选不复制，列表与行为逐字节不变。
func expandPoolRetryCandidates(candidates []Candidate) []Candidate {
	expanded := make([]Candidate, 0, len(candidates)+2)
	for _, prepared := range candidates {
		expanded = append(expanded, prepared)
		for extra := 1; extra < transportBudget(prepared); extra++ {
			expanded = append(expanded, prepared)
		}
	}
	return expanded
}
