package lifecycle

import (
	"context"
	"testing"

	"github.com/ThankCat/unio-gateway/internal/core/accountpool"
	"github.com/ThankCat/unio-gateway/internal/core/requestlog"
	"github.com/ThankCat/unio-gateway/internal/platform/breakerstore"
	"github.com/ThankCat/unio-gateway/internal/service/gateway/requestadmission"
	"github.com/ThankCat/unio-gateway/internal/service/gateway/runtimefacts"
)

// newAccountSelectionRunner 组装一个只用于考察选号的 runner：真实 permit manager + 桩 store。
func newAccountSelectionRunner(results []breakerstore.AttemptAdmission) (*AttemptRunner, *attemptPermitStoreStub, context.Context) {
	integrity := runtimefacts.Integrity{Epoch: "epoch-pool", Revision: 1}
	store := &attemptPermitStoreStub{acquireResults: results}
	manager := NewAttemptPermitManager(store, attemptRuntimeFactsStub{
		integrity: integrity,
		admission: runtimefacts.AdmissionRevisions{Integrity: integrity, RequestRateLimits: 1, Concurrency: 1},
		routing:   runtimefacts.RoutingRevisions{Integrity: integrity, CircuitBreaker: 1, RoutingBalance: 1},
	}, AttemptPermitManagerOptions{})
	runner := &AttemptRunner{retryClassifier: NeverRetryClassifier{}, permitManager: manager}
	ctx := requestadmission.ContextWithRequestSession(
		context.Background(),
		&attemptRequestSessionStub{requestID: "request-account-selection"},
	)
	return runner, store, ctx
}

func poolCandidate(accountIDs ...int64) Candidate {
	accounts := make([]accountpool.Account, 0, len(accountIDs))
	for _, id := range accountIDs {
		accounts = append(accounts, accountpool.Account{ID: id, Priority: 50, ConfigRevision: 1})
	}
	limit := int64(2)
	pool := accountpool.NewPool(accounts, nil, &limit)
	return Candidate{Route: poolRoute(7), AccountPool: &pool}
}

func deniedAccountConcurrency() breakerstore.AttemptAdmission {
	return breakerstore.AttemptAdmission{
		Mode: breakerstore.AdmissionDenied, Reason: breakerstore.ReasonAccountConcurrencyFull,
	}
}

func grantedPermit() breakerstore.AttemptAdmission {
	return breakerstore.AttemptAdmission{
		Mode: breakerstore.AdmissionPermit,
		Permit: &breakerstore.AttemptPermit{
			PermitID: "permit-pool", IntegrityEpoch: "epoch-pool", IntegrityRevision: 1,
			PermitTTLMs: 30_000, RenewMs: 10_000, TerminalTTLMs: 300_000,
		},
	}
}

// 账号槽满就换下一个号：池内逐号 AttemptPermit 直到取得 permit，账号身份随之带回。
func TestAcquireCandidatePermitFallsThroughSaturatedAccounts(t *testing.T) {
	runner, store, ctx := newAccountSelectionRunner([]breakerstore.AttemptAdmission{
		deniedAccountConcurrency(),
		deniedAccountConcurrency(),
		grantedPermit(),
	})
	attempted := map[int64]bool{}

	acquire, err := runner.acquireCandidatePermit(
		ctx, poolCandidate(1, 2, 3), requestlog.UpstreamEndpointResponses,
		breakerstore.ModeNonStream, 10, 0, attempted,
	)
	admission, owner, account := acquire.Admission, acquire.Owner, acquire.Account
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	if admission.Mode != breakerstore.AdmissionPermit || owner == nil {
		t.Fatalf("expected a permit after skipping saturated accounts, got %+v", admission)
	}
	_ = owner.Abort(context.Background())
	if account.ID == 0 {
		t.Fatal("granted permit must report which account it froze")
	}
	if store.acquireCalls != 3 {
		t.Fatalf("acquire calls = %d, want 3 (one per account until admitted)", store.acquireCalls)
	}
	if got := store.acquireInput; got.AccountID != account.ID || got.AccountConfigRevision != 1 || got.AccountConcurrencyLimit != 2 {
		t.Fatalf("account dimension must reach the store: %+v", got)
	}
	if len(attempted) != 3 {
		t.Fatalf("every tried account must be recorded, got %v", attempted)
	}
}

// 同一账号在单请求内绝不重复：跨候选共享的 attemptedAccounts 已登记的号必须被跳过。
func TestAcquireCandidatePermitNeverRetriesSameAccount(t *testing.T) {
	runner, store, ctx := newAccountSelectionRunner([]breakerstore.AttemptAdmission{grantedPermit()})
	attempted := map[int64]bool{1: true, 2: true}

	acquire, err := runner.acquireCandidatePermit(
		ctx, poolCandidate(1, 2, 3), requestlog.UpstreamEndpointResponses,
		breakerstore.ModeNonStream, 10, 0, attempted,
	)
	admission, owner, account := acquire.Admission, acquire.Owner, acquire.Account
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	if admission.Mode != breakerstore.AdmissionPermit || account.ID != 3 {
		t.Fatalf("only the untried account may be used, got account=%d admission=%+v", account.ID, admission)
	}
	_ = owner.Abort(context.Background())
	if store.acquireCalls != 1 {
		t.Fatalf("acquire calls = %d, want 1 (tried accounts must not be re-attempted)", store.acquireCalls)
	}
}

// 混合拒绝映射（边界 5）：只要还有账号是「仅并发满」，整个渠道就报 concurrency_full——
// 它是可等待的，全池短等的进入条件依赖这个映射的确定性。
func TestAcquireCandidatePermitMapsConcurrencyFullPoolToWaitableDenial(t *testing.T) {
	runner, _, ctx := newAccountSelectionRunner([]breakerstore.AttemptAdmission{
		{Mode: breakerstore.AdmissionDenied, Reason: breakerstore.ReasonAccountCooldown, CooldownRemainingMs: 5_000},
		deniedAccountConcurrency(),
	})

	acquire, err := runner.acquireCandidatePermit(
		ctx, poolCandidate(1, 2), requestlog.UpstreamEndpointResponses,
		breakerstore.ModeNonStream, 10, 0, map[int64]bool{},
	)
	admission, owner := acquire.Admission, acquire.Owner
	if err != nil || owner != nil {
		t.Fatalf("expected a plain denial, owner=%v err=%v", owner, err)
	}
	if admission.Reason != breakerstore.ReasonConcurrencyFull {
		t.Fatalf("reason = %q, want concurrency_full", admission.Reason)
	}
}

// 全部账号因冷却/临时不可调度被拒时报 cooldown，并带最早恢复时刻——冷却不可等待，
// 伪装成并发满会让请求白等一轮短等预算。
func TestAcquireCandidatePermitMapsFullyCooledPoolToCooldown(t *testing.T) {
	runner, _, ctx := newAccountSelectionRunner([]breakerstore.AttemptAdmission{
		{Mode: breakerstore.AdmissionDenied, Reason: breakerstore.ReasonAccountCooldown, CooldownRemainingMs: 9_000},
		{Mode: breakerstore.AdmissionDenied, Reason: breakerstore.ReasonAccountUnschedulable, CooldownRemainingMs: 3_000},
	})

	acquire, err := runner.acquireCandidatePermit(
		ctx, poolCandidate(1, 2), requestlog.UpstreamEndpointResponses,
		breakerstore.ModeNonStream, 10, 0, map[int64]bool{},
	)
	admission := acquire.Admission
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	if admission.Reason != breakerstore.ReasonCooldown || admission.CooldownRemainingMs != 3_000 {
		t.Fatalf("cooled pool denial = %+v, want cooldown with 3000ms", admission)
	}
}

// 渠道级拒绝与账号无关：熔断/权限/配置不同步一律立即返回，换号只是白跑 Redis。
func TestAcquireCandidatePermitStopsOnChannelScopedDenial(t *testing.T) {
	runner, store, ctx := newAccountSelectionRunner([]breakerstore.AttemptAdmission{
		{Mode: breakerstore.AdmissionDenied, Reason: breakerstore.ReasonOpen},
		grantedPermit(),
	})

	acquire, err := runner.acquireCandidatePermit(
		ctx, poolCandidate(1, 2, 3), requestlog.UpstreamEndpointResponses,
		breakerstore.ModeNonStream, 10, 0, map[int64]bool{},
	)
	admission := acquire.Admission
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	if admission.Reason != breakerstore.ReasonOpen {
		t.Fatalf("channel denial must pass through unchanged, got %+v", admission)
	}
	if store.acquireCalls != 1 {
		t.Fatalf("acquire calls = %d, want 1 (no account rotation on channel denials)", store.acquireCalls)
	}
}

// credential 型候选一次 Acquire、不带任何账号维度：存量渠道必须逐字节走原路径。
func TestAcquireCandidatePermitKeepsCredentialChannelUnchanged(t *testing.T) {
	runner, store, ctx := newAccountSelectionRunner([]breakerstore.AttemptAdmission{grantedPermit()})
	attempted := map[int64]bool{}

	acquire, err := runner.acquireCandidatePermit(
		ctx, Candidate{Route: candidateRoute(8, "openai")}, requestlog.UpstreamEndpointResponses,
		breakerstore.ModeNonStream, 10, 0, attempted,
	)
	owner, account := acquire.Owner, acquire.Account
	if err != nil || owner == nil {
		t.Fatalf("acquire owner=%v err=%v", owner, err)
	}
	_ = owner.Abort(context.Background())
	if account.ID != 0 || len(attempted) != 0 {
		t.Fatalf("credential channel must not touch the account dimension: account=%+v attempted=%v", account, attempted)
	}
	if got := store.acquireInput; got.AccountID != 0 || got.AccountConcurrencyLimit != 0 || got.AccountConfigRevision != 0 {
		t.Fatalf("credential channel acquire must carry a zeroed account dimension: %+v", got)
	}
}
