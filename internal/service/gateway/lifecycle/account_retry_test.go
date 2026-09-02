package lifecycle

import (
	"context"
	"errors"
	"testing"

	"github.com/ThankCat/unio-gateway/internal/core/accountpool"
	"github.com/ThankCat/unio-gateway/internal/core/adapter"
	"github.com/ThankCat/unio-gateway/internal/core/routing"
	"github.com/ThankCat/unio-gateway/internal/platform/breakerstore"
)

type alwaysRetryClassifier struct{}

func (alwaysRetryClassifier) IsRetryable(error) bool { return true }

// stubAccountOutbound 返回固定凭据：这些测试只考察换号次序，不考察凭据解析。
type stubAccountOutbound struct{}

func (stubAccountOutbound) ResolveAccountOutbound(_ context.Context, accountID int64) (AccountOutbound, error) {
	return AccountOutbound{AccessToken: "at", UpstreamAccountID: "up"}, nil
}

// permitFor 为逐次 acquire 构造可用 permit（permit id 无关紧要，harness 不校验）。
func permitFor(id string) breakerstore.AttemptAdmission {
	return breakerstore.AttemptAdmission{
		Mode: breakerstore.AdmissionPermit,
		Permit: &breakerstore.AttemptPermit{
			PermitID: id, IntegrityEpoch: "permit-guard-epoch", IntegrityRevision: 1,
			PermitTTLMs: 30_000, RenewMs: 10_000, TerminalTTLMs: 300_000,
		},
	}
}

// TestRunNonStreamRetriesSameChannelWithDifferentAccount 冻结边界 6 的验收语义：
// 池型渠道 A 的第一次传输失败（可重试）后，先以另一个账号重试 A，再落到渠道 B；
// 同一账号绝不重复，A 的传输次数不超过预算（1 原始 + 1 换号重试）。
func TestRunNonStreamRetriesSameChannelWithDifferentAccount(t *testing.T) {
	log := &permitGuardPanicLog{attemptAuditLog: &attemptAuditLog{}}
	runner, store, _, ctx := newPermitGuardRunner(log)
	runner.retryClassifier = alwaysRetryClassifier{}
	runner.lifecycle.SetAccountOutboundResolver(stubAccountOutbound{})
	store.acquireResults = []breakerstore.AttemptAdmission{
		permitFor("p1"), permitFor("p2"), permitFor("p3"),
	}

	limit := int64(4)
	pool := accountpool.NewPool(
		[]accountpool.Account{
			{ID: 11, Priority: 10, ConfigRevision: 1},
			{ID: 12, Priority: 20, ConfigRevision: 1},
		},
		nil, &limit,
	)
	poolChannel := permitGuardCandidate()
	poolChannel.Channel.ID = 7
	credential := permitGuardCandidate()
	credential.Channel.ID = 8

	retryableErr := adapter.NewUpstreamError(
		adapter.UpstreamErrorServer,
		adapter.UpstreamMetadata{StatusCode: 502},
		errors.New("upstream 502"),
	)
	var invokedChannels []int64
	result, err := runner.RunNonStream(ctx, RunNonStreamParams{
		Candidates: []Candidate{
			{Route: poolChannel, AccountPool: &pool},
			{Route: credential},
		},
		Invoke: func(ctx context.Context, candidate routing.ChatRouteCandidate) (AttemptSuccess, error) {
			adapter.MarkTransportStarted(ctx)
			adapter.MarkRequestWritten(ctx, nil)
			invokedChannels = append(invokedChannels, candidate.Channel.ID)
			return AttemptSuccess{}, retryableErr
		},
	})
	if !errors.Is(err, retryableErr) {
		t.Fatalf("final error = %v, want the last retryable transport error", err)
	}

	// 传输顺序必须是 A(a?) → A(a?) → B：同渠道换号优先于跨渠道切换。
	wantChannels := []int64{7, 7, 8}
	if len(invokedChannels) != len(wantChannels) {
		t.Fatalf("transports = %v, want channels %v", invokedChannels, wantChannels)
	}
	for i, want := range wantChannels {
		if invokedChannels[i] != want {
			t.Fatalf("transport %d hit channel %d, want %d (%v)", i, invokedChannels[i], want, invokedChannels)
		}
	}
	// 两次 A 传输用的必须是两个不同账号，且都被记进 trace 事实。
	if len(result.AttemptedAccountIDs) != 2 || result.AttemptedAccountIDs[0] == result.AttemptedAccountIDs[1] {
		t.Fatalf("attempted accounts = %v, want two distinct accounts", result.AttemptedAccountIDs)
	}
	if result.SelectedAccountID != 0 {
		t.Fatalf("selected account = %d, want 0 on failure", result.SelectedAccountID)
	}
}

// TestRunNonStreamPoolRetryStopsAtBudget 冻结「设次数上限」：池内即便还有第三个号，
// 传输预算（2）耗尽后也不再打同一渠道。
func TestRunNonStreamPoolRetryStopsAtBudget(t *testing.T) {
	log := &permitGuardPanicLog{attemptAuditLog: &attemptAuditLog{}}
	runner, store, _, ctx := newPermitGuardRunner(log)
	runner.retryClassifier = alwaysRetryClassifier{}
	runner.lifecycle.SetAccountOutboundResolver(stubAccountOutbound{})
	store.acquireResults = []breakerstore.AttemptAdmission{permitFor("p1"), permitFor("p2")}

	limit := int64(4)
	pool := accountpool.NewPool(
		[]accountpool.Account{
			{ID: 11, Priority: 10, ConfigRevision: 1},
			{ID: 12, Priority: 20, ConfigRevision: 1},
			{ID: 13, Priority: 30, ConfigRevision: 1},
		},
		nil, &limit,
	)
	poolChannel := permitGuardCandidate()
	poolChannel.Channel.ID = 7

	retryableErr := adapter.NewUpstreamError(
		adapter.UpstreamErrorServer,
		adapter.UpstreamMetadata{StatusCode: 502},
		errors.New("upstream 502"),
	)
	transports := 0
	_, err := runner.RunNonStream(ctx, RunNonStreamParams{
		Candidates: []Candidate{{Route: poolChannel, AccountPool: &pool}},
		Invoke: func(ctx context.Context, _ routing.ChatRouteCandidate) (AttemptSuccess, error) {
			adapter.MarkTransportStarted(ctx)
			adapter.MarkRequestWritten(ctx, nil)
			transports++
			return AttemptSuccess{}, retryableErr
		},
	})
	if !errors.Is(err, retryableErr) {
		t.Fatalf("final error = %v, want retryable transport error", err)
	}
	if transports != poolChannelTransportBudget {
		t.Fatalf("transports = %d, want budget %d (third account must not be used)", transports, poolChannelTransportBudget)
	}
}
