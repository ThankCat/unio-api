package breakerstore

import (
	"context"
	"strings"
	"testing"

	"github.com/redis/go-redis/v9"
)

// accountAcquireInput 构造一个池型渠道的 attempt 入参；accountID=0 表示 credential 型渠道，
// 账号维度整体缺席，脚本必须走与引入号池之前完全相同的路径。
func accountAcquireInput(permitID string, channelID, accountID, accountLimit int64) AcquireAttemptInput {
	in := AcquireAttemptInput{
		PermitID:               permitID,
		AdmissionFingerprint:   permitID + "-fp",
		RequestAdmissionID:     "req-" + permitID,
		ProviderID:             900,
		ChannelID:              channelID,
		OriginRevision:         1,
		ProviderStatusRevision: 1,
		ChannelConfigRevision:  1,
		ModelID:                100,
		UpstreamEndpoint:       EndpointResponses,
		RequestMode:            ModeNonStream,
		InputEstimate:          10,
	}
	if accountID > 0 {
		in.AccountID = accountID
		in.AccountConcurrencyLimit = accountLimit
		in.AccountConfigRevision = 1
	}
	return withAttemptControlRevisions(in)
}

func mustAcquireAccount(t *testing.T, s *Store, in AcquireAttemptInput) AttemptAdmission {
	t.Helper()
	adm, err := acquireAttempt(t, s, in)
	if err != nil {
		t.Fatalf("acquire %s: %v", in.PermitID, err)
	}
	return adm
}

// TestAccountConcurrencyIsPerAccountNotPerChannel 冻结号池的核心容量语义：账号槽独立于渠道槽。
// 同一渠道里 A 号满不影响 B 号，这是「同渠道换号重试」成立的前提；拒绝原因必须与渠道级
// concurrency_full 区分，否则路由无法判断该换号还是该换渠道。
func TestAccountConcurrencyIsPerAccountNotPerChannel(t *testing.T) {
	s, _, _ := newTestStore(t)
	cfg := testConfig()
	const channelID = 90
	// 渠道并发放到 5，确保下面的拒绝只可能来自账号维度。
	seedAttemptControls(t, s, cfg, channelID, `{"concurrency":5}`)

	first := mustAcquireAccount(t, s, accountAcquireInput("acc-a1", channelID, 9001, 1))
	if first.Mode != AdmissionPermit {
		t.Fatalf("first acquire on account 9001 want permit, got %s/%s", first.Mode, first.Reason)
	}

	second := mustAcquireAccount(t, s, accountAcquireInput("acc-a2", channelID, 9001, 1))
	if second.Mode != AdmissionDenied || second.Reason != ReasonAccountConcurrencyFull {
		t.Fatalf("second acquire on account 9001 want denied/account_concurrency_full, got %s/%s", second.Mode, second.Reason)
	}

	// 换号即可继续：渠道还有 4 个空位，账号 9002 自己的槽是空的。
	other := mustAcquireAccount(t, s, accountAcquireInput("acc-b1", channelID, 9002, 1))
	if other.Mode != AdmissionPermit {
		t.Fatalf("acquire on account 9002 want permit, got %s/%s", other.Mode, other.Reason)
	}
}

// TestAccountConcurrencyUnlimitedWhenZero 冻结「0 = 不限」与渠道并发同语义。
func TestAccountConcurrencyUnlimitedWhenZero(t *testing.T) {
	s, _, _ := newTestStore(t)
	cfg := testConfig()
	const channelID = 91
	seedAttemptControls(t, s, cfg, channelID, `{"concurrency":5}`)

	for _, permitID := range []string{"acc-u1", "acc-u2", "acc-u3"} {
		adm := mustAcquireAccount(t, s, accountAcquireInput(permitID, channelID, 9101, 0))
		if adm.Mode != AdmissionPermit {
			t.Fatalf("acquire %s with unlimited account concurrency want permit, got %s/%s", permitID, adm.Mode, adm.Reason)
		}
	}
}

// TestAccountSlotReleasedByFinishAndAbort 验证账号槽与渠道槽同时收口：只归还渠道侧会让账号容量
// 缓慢泄漏，最终整池假性满载而没有任何错误日志。
func TestAccountSlotReleasedByFinishAndAbort(t *testing.T) {
	s, _, _ := newTestStore(t)
	cfg := testConfig()
	const channelID = 92
	seedAttemptControls(t, s, cfg, channelID, `{"concurrency":5}`)

	held := mustAcquireAccount(t, s, accountAcquireInput("acc-f1", channelID, 9201, 1))
	if held.Mode != AdmissionPermit {
		t.Fatalf("acquire acc-f1 want permit, got %s/%s", held.Mode, held.Reason)
	}
	if _, err := s.Finish(context.Background(), *held.Permit, FinishOutcome{
		ProviderOutcome: OutcomeIgnored, ChannelOutcome: OutcomeEligibleSuccess,
		RequestWriteState: RequestWriteCompleted,
	}); err != nil {
		t.Fatalf("finish acc-f1: %v", err)
	}
	afterFinish := mustAcquireAccount(t, s, accountAcquireInput("acc-f2", channelID, 9201, 1))
	if afterFinish.Mode != AdmissionPermit {
		t.Fatalf("acquire after finish released the account slot want permit, got %s/%s", afterFinish.Mode, afterFinish.Reason)
	}

	if err := s.Abort(context.Background(), *afterFinish.Permit); err != nil {
		t.Fatalf("abort acc-f2: %v", err)
	}
	afterAbort := mustAcquireAccount(t, s, accountAcquireInput("acc-f3", channelID, 9201, 1))
	if afterAbort.Mode != AdmissionPermit {
		t.Fatalf("acquire after abort released the account slot want permit, got %s/%s", afterAbort.Mode, afterAbort.Reason)
	}
}

// TestRenewExtendsAccountLease 守住一个不会报错、只会算错的缺陷：长流请求持续续期时，如果只续渠道槽，
// 账号租约会先到期，账号在途数被系统性少算，账号并发上限随之形同虚设。
func TestRenewExtendsAccountLease(t *testing.T) {
	s, client, _ := newTestStore(t)
	cfg := testConfig()
	const channelID = 93
	const accountID = int64(9301)
	seedAttemptControls(t, s, cfg, channelID, `{"concurrency":5}`)

	adm := mustAcquireAccount(t, s, accountAcquireInput("acc-r1", channelID, accountID, 1))
	if adm.Mode != AdmissionPermit {
		t.Fatalf("acquire acc-r1 want permit, got %s/%s", adm.Mode, adm.Reason)
	}
	if inFlight := accountInFlight(t, s, accountID); inFlight != 1 {
		t.Fatalf("in-flight after acquire = %d, want 1", inFlight)
	}

	// 把账号租约改成已过期，模拟长流请求跑过了一个 permit TTL 而没有续期。
	ctx := context.Background()
	expired := redis.Z{Score: float64(redisNowMs(t, client) - 1), Member: adm.Permit.PermitID}
	if err := client.ZAdd(ctx, s.keys.accountConcurrency(accountID), expired).Err(); err != nil {
		t.Fatalf("expire account lease: %v", err)
	}
	if inFlight := accountInFlight(t, s, accountID); inFlight != 0 {
		t.Fatalf("in-flight with an expired lease = %d, want 0", inFlight)
	}

	if err := s.Renew(ctx, *adm.Permit); err != nil {
		t.Fatalf("renew acc-r1: %v", err)
	}
	if inFlight := accountInFlight(t, s, accountID); inFlight != 1 {
		t.Fatalf("in-flight after renew = %d, want 1 (account lease was not extended)", inFlight)
	}
}

// TestRenewDoesNotResurrectReleasedAccountSlot 守住续期的另一侧：已经归还的槽不能因为一次迟到的
// 续期重新出现在在途计数里。
func TestRenewDoesNotResurrectReleasedAccountSlot(t *testing.T) {
	s, client, _ := newTestStore(t)
	cfg := testConfig()
	const channelID = 94
	const accountID = int64(9401)
	seedAttemptControls(t, s, cfg, channelID, `{"concurrency":5}`)

	adm := mustAcquireAccount(t, s, accountAcquireInput("acc-r2", channelID, accountID, 1))
	if adm.Mode != AdmissionPermit {
		t.Fatalf("acquire acc-r2 want permit, got %s/%s", adm.Mode, adm.Reason)
	}
	ctx := context.Background()
	if err := client.ZRem(ctx, s.keys.accountConcurrency(accountID), adm.Permit.PermitID).Err(); err != nil {
		t.Fatalf("release account slot: %v", err)
	}
	if err := s.Renew(ctx, *adm.Permit); err != nil {
		t.Fatalf("renew acc-r2: %v", err)
	}
	if inFlight := accountInFlight(t, s, accountID); inFlight != 0 {
		t.Fatalf("in-flight after renewing a released slot = %d, want 0", inFlight)
	}
}

// TestLifecycleRejectsMismatchedAccountIdentity 验证账号身份与渠道身份同权固化：调用方声称要收口的
// 账号必须与 permit 当初占用的一致，否则一次 finish 就能归还别人的槽。
// 收口路径沿用既有约定：Finish 把身份冲突表达为 terminal_conflict 处置而不是 Go 错误，Abort 则报错；
// 两者共同的硬要求是零资源变化。
func TestLifecycleRejectsMismatchedAccountIdentity(t *testing.T) {
	s, _, _ := newTestStore(t)
	cfg := testConfig()
	const channelID = 95
	const accountID = int64(9501)
	seedAttemptControls(t, s, cfg, channelID, `{"concurrency":5}`)

	adm := mustAcquireAccount(t, s, accountAcquireInput("acc-i1", channelID, accountID, 1))
	if adm.Mode != AdmissionPermit {
		t.Fatalf("acquire acc-i1 want permit, got %s/%s", adm.Mode, adm.Reason)
	}

	tampered := *adm.Permit
	tampered.AccountID = accountID + 1
	result, err := s.Finish(context.Background(), tampered, FinishOutcome{
		ProviderOutcome: OutcomeIgnored, ChannelOutcome: OutcomeEligibleSuccess,
		RequestWriteState: RequestWriteCompleted,
	})
	if err != nil {
		t.Fatalf("finish with a mismatched account id: %v", err)
	}
	if result.ProviderDisposition != DispositionTerminalConflict || result.ChannelDisposition != DispositionTerminalConflict {
		t.Fatalf("finish dispositions = %s/%s, want terminal_conflict on both",
			result.ProviderDisposition, result.ChannelDisposition)
	}
	if err := s.Abort(context.Background(), tampered); err == nil {
		t.Fatal("abort with a mismatched account id was accepted")
	}

	// 原账号的槽必须原样保留：认错账号的收口请求不能造成任何资源变化。
	if inFlight := accountInFlight(t, s, accountID); inFlight != 1 {
		t.Fatalf("in-flight after a rejected close-out = %d, want 1", inFlight)
	}
	// 冒名账号的槽也不该被凭空创建。
	if inFlight := accountInFlight(t, s, accountID+1); inFlight != 0 {
		t.Fatalf("impersonated account in-flight = %d, want 0", inFlight)
	}
	// permit 仍是 active：真正的持有方之后还能正常收口。
	if _, err := s.Finish(context.Background(), *adm.Permit, FinishOutcome{
		ProviderOutcome: OutcomeIgnored, ChannelOutcome: OutcomeEligibleSuccess,
		RequestWriteState: RequestWriteCompleted,
	}); err != nil {
		t.Fatalf("finish with the real account id: %v", err)
	}
	if inFlight := accountInFlight(t, s, accountID); inFlight != 0 {
		t.Fatalf("in-flight after the real close-out = %d, want 0", inFlight)
	}
}

// TestCredentialChannelLeavesNoAccountState 是存量零回归的护栏：不带账号维度的渠道跑完整条
// acquire/finish 之后，Redis 里不应出现任何账号相关的键。
func TestCredentialChannelLeavesNoAccountState(t *testing.T) {
	s, client, ns := newTestStore(t)
	cfg := testConfig()
	const channelID = 96
	seedAttemptControls(t, s, cfg, channelID, `{"concurrency":2}`)

	adm := mustAcquireAccount(t, s, accountAcquireInput("cred-1", channelID, 0, 0))
	if adm.Mode != AdmissionPermit {
		t.Fatalf("credential acquire want permit, got %s/%s", adm.Mode, adm.Reason)
	}
	if adm.Permit.AccountID != 0 {
		t.Fatalf("credential permit carries account id %d", adm.Permit.AccountID)
	}
	if _, err := s.Finish(context.Background(), *adm.Permit, FinishOutcome{
		ProviderOutcome: OutcomeIgnored, ChannelOutcome: OutcomeEligibleSuccess,
		RequestWriteState: RequestWriteCompleted,
	}); err != nil {
		t.Fatalf("credential finish: %v", err)
	}

	for key := range dumpTestNamespace(t, client, ns) {
		if strings.Contains(key, ":account:") {
			t.Fatalf("credential channel created an account key: %s", key)
		}
	}
}

// TestAcquireRejectsPartialAccountInput 守住最危险的一类传参错误：只给上限不给账号 id 时，
// 脚本会静默跳过全部账号门禁，表现为账号并发形同虚设而不是任何一处报错。
func TestAcquireRejectsPartialAccountInput(t *testing.T) {
	s, _, _ := newTestStore(t)
	cfg := testConfig()
	const channelID = 97
	seedAttemptControls(t, s, cfg, channelID, `{"concurrency":2}`)

	cases := map[string]func(*AcquireAttemptInput){
		"limit without account id":    func(in *AcquireAttemptInput) { in.AccountConcurrencyLimit = 3 },
		"revision without account id": func(in *AcquireAttemptInput) { in.AccountConfigRevision = 1 },
		"account id without revision": func(in *AcquireAttemptInput) { in.AccountID = 9701 },
		"negative account id":         func(in *AcquireAttemptInput) { in.AccountID = -1 },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			in := accountAcquireInput("acc-v1", channelID, 0, 0)
			mutate(&in)
			if _, err := acquireAttempt(t, s, in); err == nil {
				t.Fatal("invalid account input was accepted")
			}
		})
	}
}
