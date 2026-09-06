package subscriptionaccount

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/ThankCat/unio-gateway/internal/core/runtimecontrol"
	"github.com/ThankCat/unio-gateway/internal/platform/breakerstore"
	"github.com/ThankCat/unio-gateway/internal/platform/failure"
	"github.com/ThankCat/unio-gateway/internal/platform/store/sqlc"
	subscriptionhealth "github.com/ThankCat/unio-gateway/internal/service/subscription/health"
	subscriptionquota "github.com/ThankCat/unio-gateway/internal/service/subscription/quota"
)

// fakeQueries 是 Queries 的可配置替身：只实现用例路径需要的查询，其余返回零值。
type fakeQueries struct {
	accounts     map[int64]sqlc.SubscriptionAccount
	channel      sqlc.Channel
	channelErr   error
	schedulable  int64
	listRows     []sqlc.AdminListSubscriptionAccountsRow
	counts       sqlc.AdminCountAccountsByChannelRow
	deleteErr    error
	deleted      []int64
	ledgerParams sqlc.AdminCreateSubscriptionLedgerEntryParams
	proxyErr     error
	// thresholdWrites 记录账号阈值列的写入次数。
	thresholdWrites int
}

func (q *fakeQueries) AdminListSubscriptionAccounts(context.Context, sqlc.AdminListSubscriptionAccountsParams) ([]sqlc.AdminListSubscriptionAccountsRow, error) {
	return q.listRows, nil
}
func (q *fakeQueries) AdminGetSubscriptionAccount(_ context.Context, id int64) (sqlc.SubscriptionAccount, error) {
	row, ok := q.accounts[id]
	if !ok {
		return sqlc.SubscriptionAccount{}, pgx.ErrNoRows
	}
	return row, nil
}
func (q *fakeQueries) AdminCountAccountsByChannel(context.Context, int64) (sqlc.AdminCountAccountsByChannelRow, error) {
	return q.counts, nil
}
func (q *fakeQueries) AdminListSubscriptionLedger(context.Context, int64) ([]sqlc.SubscriptionLedgerEntry, error) {
	return nil, nil
}
func (q *fakeQueries) AdminCreateSubscriptionLedgerEntry(_ context.Context, arg sqlc.AdminCreateSubscriptionLedgerEntryParams) (sqlc.SubscriptionLedgerEntry, error) {
	q.ledgerParams = arg
	return sqlc.SubscriptionLedgerEntry{ID: 1, AccountID: arg.AccountID, Amount: arg.Amount, Currency: arg.Currency, PeriodStart: arg.PeriodStart, PeriodEnd: arg.PeriodEnd}, nil
}
func (q *fakeQueries) AdminCreateSubscriptionAccount(context.Context, sqlc.AdminCreateSubscriptionAccountParams) (sqlc.SubscriptionAccount, error) {
	return sqlc.SubscriptionAccount{}, errors.New("not used")
}
func (q *fakeQueries) AdminReauthorizeSubscriptionAccount(context.Context, sqlc.AdminReauthorizeSubscriptionAccountParams) (sqlc.SubscriptionAccount, error) {
	return sqlc.SubscriptionAccount{}, errors.New("not used")
}
func (q *fakeQueries) AdminDeleteSubscriptionAccountCascade(_ context.Context, id int64) (int64, error) {
	if q.deleteErr != nil {
		return 0, q.deleteErr
	}
	if _, ok := q.accounts[id]; !ok {
		return 0, nil
	}
	delete(q.accounts, id)
	q.deleted = append(q.deleted, id)
	return 1, nil
}
func (q *fakeQueries) GetAccountByPlatformUpstreamID(context.Context, sqlc.GetAccountByPlatformUpstreamIDParams) (sqlc.GetAccountByPlatformUpstreamIDRow, error) {
	return sqlc.GetAccountByPlatformUpstreamIDRow{}, pgx.ErrNoRows
}
func (q *fakeQueries) GetChannel(context.Context, int64) (sqlc.Channel, error) {
	if q.channelErr != nil {
		return sqlc.Channel{}, q.channelErr
	}
	return q.channel, nil
}
func (q *fakeQueries) CountSchedulableAccountsByChannel(context.Context, int64) (int64, error) {
	return q.schedulable, nil
}
func (q *fakeQueries) AdminListPoolChannels(context.Context) ([]sqlc.AdminListPoolChannelsRow, error) {
	return nil, nil
}
func (q *fakeQueries) GetEnabledProxyURL(context.Context, int64) (string, error) {
	if q.proxyErr != nil {
		return "", q.proxyErr
	}
	return "http://proxy.internal:8080", nil
}
func (q *fakeQueries) AdminChannelAccountsUsage24h(context.Context, int64) ([]sqlc.AdminChannelAccountsUsage24hRow, error) {
	return nil, nil
}
func (q *fakeQueries) AdminChannelAccountsAttempts24h(context.Context, int64) ([]sqlc.AdminChannelAccountsAttempts24hRow, error) {
	return nil, nil
}
func (q *fakeQueries) AdminChannelAccountsSale24h(context.Context, int64) ([]sqlc.AdminChannelAccountsSale24hRow, error) {
	return nil, nil
}
func (q *fakeQueries) AdminChannelAccountsLastFailure24h(context.Context, int64) ([]sqlc.AdminChannelAccountsLastFailure24hRow, error) {
	return nil, nil
}
func (q *fakeQueries) AdminChannelAccountsLifetimeStats(context.Context, int64) ([]sqlc.AdminChannelAccountsLifetimeStatsRow, error) {
	return nil, nil
}
func (q *fakeQueries) AdminUpdateSubscriptionAccountUsagePauseThreshold(_ context.Context, arg sqlc.AdminUpdateSubscriptionAccountUsagePauseThresholdParams) (sqlc.SubscriptionAccount, error) {
	row, ok := q.accounts[arg.ID]
	if !ok {
		return sqlc.SubscriptionAccount{}, pgx.ErrNoRows
	}
	row.UsagePauseThresholdPercent = arg.UsagePauseThresholdPercent
	row.ConfigRevision++
	q.accounts[arg.ID] = row
	q.thresholdWrites++
	return row, nil
}

func (q *fakeQueries) UpdateAccountAutoResetCreditConfig(_ context.Context, arg sqlc.UpdateAccountAutoResetCreditConfigParams) (sqlc.SubscriptionAccount, error) {
	row, ok := q.accounts[arg.ID]
	if !ok {
		return sqlc.SubscriptionAccount{}, pgx.ErrNoRows
	}
	row.AutoResetCreditEnabled = arg.AutoResetCreditEnabled
	row.AutoResetCreditMode = arg.AutoResetCreditMode
	row.AutoResetCredit5hThresholdPercent = arg.Threshold5hPercent
	row.AutoResetCredit7dThresholdPercent = arg.Threshold7dPercent
	row.ConfigRevision++
	q.accounts[arg.ID] = row
	return row, nil
}

// fakeQuota 是 QuotaService 的替身：记录调用并返回预设结果。
type fakeQuota struct {
	report     subscriptionquota.RefreshReport
	reportErr  error
	outcome    subscriptionquota.ResetOutcome
	outcomeErr error
	queried    []int64
	reset      []int64
	// batches 由后台 goroutine 写入，测试端用 channel 同步读取。
	batches chan []int64
}

func (f *fakeQuota) Refresh(_ context.Context, accountID int64) (subscriptionquota.RefreshReport, error) {
	f.queried = append(f.queried, accountID)
	if f.reportErr != nil {
		return subscriptionquota.RefreshReport{}, f.reportErr
	}
	return f.report, nil
}

func (f *fakeQuota) RefreshMany(_ context.Context, accountIDs []int64, _ int) {
	if f.batches != nil {
		f.batches <- accountIDs
	}
}

func (f *fakeQuota) ResetCredit(_ context.Context, accountID int64) (subscriptionquota.ResetOutcome, error) {
	f.reset = append(f.reset, accountID)
	if f.outcomeErr != nil {
		return subscriptionquota.ResetOutcome{}, f.outcomeErr
	}
	return f.outcome, nil
}

// fakeReconciler 记录账号阈值变更后触发的重算。
type fakeReconciler struct {
	accountIDs []int64
	err        error
}

func (r *fakeReconciler) ReconcileAccount(_ context.Context, accountID int64) (subscriptionhealth.ReconcileResult, error) {
	r.accountIDs = append(r.accountIDs, accountID)
	if r.err != nil {
		return subscriptionhealth.ReconcileResult{}, r.err
	}
	return subscriptionhealth.ReconcileResult{Scanned: 1, Resumed: 1}, nil
}

// fakePublisher 记录发布请求；不执行 BusinessCommit（那需要真实 pgx.Tx），只验证两阶段发布的请求形状。
type fakePublisher struct {
	requests []runtimecontrol.PublishRequest
	err      error
}

func (p *fakePublisher) Publish(_ context.Context, req runtimecontrol.PublishRequest) (runtimecontrol.PublishResult, error) {
	p.requests = append(p.requests, req)
	if p.err != nil {
		return runtimecontrol.PublishResult{}, p.err
	}
	return runtimecontrol.PublishResult{ActiveRevision: req.NextRevision}, nil
}

type fakeControls struct{}

func (fakeControls) ChannelCapacityControl(int64) breakerstore.ControlTarget {
	return breakerstore.ControlTarget{}
}

type fakeRuntime struct {
	runtimes []breakerstore.AccountRuntime
	err      error
}

func (r *fakeRuntime) AccountRuntimeMany(context.Context, []int64) ([]breakerstore.AccountRuntime, error) {
	return r.runtimes, r.err
}

func poolChannel(id int64) sqlc.Channel {
	return sqlc.Channel{ID: id, SupplyForm: "pool", Status: "enabled", CapacityRevision: 3, ConcurrencyLimit: pgtype.Int4{Int32: 8, Valid: true}}
}

func account(id, channelID int64, status string) sqlc.SubscriptionAccount {
	return sqlc.SubscriptionAccount{
		ID: id, ChannelID: channelID, Platform: "openai", CredentialType: "oauth", UpstreamAccountID: "acct",
		DisplayName: "acct", Status: status, Credentials: []byte(`{}`), FingerprintMode: "off",
	}
}

func newTestService(q *fakeQueries, publisher *fakePublisher, runtime AccountRuntimeReader) *Service {
	return NewService(q, runtime, publisher, fakeControls{}, nil, nil, nil)
}

func TestSetStatusEnforcesLifecycleTransitions(t *testing.T) {
	cases := []struct {
		name     string
		current  string
		action   string
		wantCode failure.Code
		wantNext string
	}{
		{name: "enable archived is rejected", current: "archived", action: "enable", wantCode: failure.CodeAdminConflict},
		{name: "archive enabled is rejected", current: "enabled", action: "archive", wantCode: failure.CodeAdminConflict},
		{name: "restore non-archived is rejected", current: "disabled", action: "restore", wantCode: failure.CodeAdminConflict},
		{name: "unknown action is invalid", current: "disabled", action: "pause", wantCode: failure.CodeAdminInvalidArgument},
		{name: "restore lands on disabled", current: "archived", action: "restore", wantNext: "disabled"},
		{name: "archive disabled", current: "disabled", action: "archive", wantNext: "archived"},
		{name: "enable disabled", current: "disabled", action: "enable", wantNext: "enabled"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			q := &fakeQueries{accounts: map[int64]sqlc.SubscriptionAccount{7: account(7, 3, tc.current)}, channel: poolChannel(3), schedulable: 5}
			publisher := &fakePublisher{}
			svc := newTestService(q, publisher, nil)

			_, err := svc.SetStatus(context.Background(), SetStatusInput{AccountID: 7, Action: tc.action})
			if tc.wantCode != "" {
				if failure.CodeOf(err) != tc.wantCode {
					t.Fatalf("expected %s, got %v", tc.wantCode, err)
				}
				if len(publisher.requests) != 0 {
					t.Fatal("rejected transition must not publish")
				}
				return
			}
			if err != nil {
				t.Fatalf("set status: %v", err)
			}
			if len(publisher.requests) != 1 {
				t.Fatalf("expected one publish, got %d", len(publisher.requests))
			}
			req := publisher.requests[0]
			// 账号变更必须借渠道容量 control 推进 capacity_revision，让运行态围栏立即失效旧候选快照。
			if req.Kind != runtimecontrol.KindChannelCapacity || req.CurrentRevision != 3 || req.NextRevision != 4 ||
				req.ChannelID == nil || *req.ChannelID != 3 || req.BusinessCommit == nil || req.Token == "" {
				t.Fatalf("publish request shape = %+v", req)
			}
		})
	}
}

func TestSetStatusRequiresConfirmationWhenRemovingLastSchedulableAccount(t *testing.T) {
	q := &fakeQueries{accounts: map[int64]sqlc.SubscriptionAccount{7: account(7, 3, "enabled")}, channel: poolChannel(3), schedulable: 1}
	publisher := &fakePublisher{}
	svc := newTestService(q, publisher, nil)

	_, err := svc.SetStatus(context.Background(), SetStatusInput{AccountID: 7, Action: "disable"})
	if failure.CodeOf(err) != failure.CodeAdminConflict {
		t.Fatalf("disabling the last schedulable account must require confirmation, got %v", err)
	}
	if len(publisher.requests) != 0 {
		t.Fatal("unconfirmed change must not publish")
	}

	if _, err := svc.SetStatus(context.Background(), SetStatusInput{AccountID: 7, Action: "disable", ConfirmSupplyImpact: true}); err != nil {
		t.Fatalf("confirmed change must proceed: %v", err)
	}
	if len(publisher.requests) != 1 {
		t.Fatalf("confirmed change must publish once, got %d", len(publisher.requests))
	}

	// 渠道本身已停用时没有供给可失去：不需要确认。
	q.channel.Status = "disabled"
	publisher.requests = nil
	if _, err := svc.SetStatus(context.Background(), SetStatusInput{AccountID: 7, Action: "disable"}); err != nil {
		t.Fatalf("disabled channel must not gate the change: %v", err)
	}
	if len(publisher.requests) != 1 {
		t.Fatal("change on a disabled channel must still publish")
	}
}

func TestSetStatusPropagatesPublishFailureAndMissingPublisher(t *testing.T) {
	q := &fakeQueries{accounts: map[int64]sqlc.SubscriptionAccount{7: account(7, 3, "disabled")}, channel: poolChannel(3), schedulable: 5}
	publisher := &fakePublisher{err: errors.New("redis down")}
	svc := newTestService(q, publisher, nil)
	if _, err := svc.SetStatus(context.Background(), SetStatusInput{AccountID: 7, Action: "enable"}); err == nil {
		t.Fatal("publish failure must surface")
	}

	noPublisher := NewService(q, nil, nil, nil, nil, nil, nil)
	_, err := noPublisher.SetStatus(context.Background(), SetStatusInput{AccountID: 7, Action: "enable"})
	if failure.CodeOf(err) != failure.CodeGatewayBreakerStoreUnavailable {
		t.Fatalf("missing publisher must fail closed, got %v", err)
	}

	if _, err := svc.SetStatus(context.Background(), SetStatusInput{AccountID: 404, Action: "enable"}); failure.CodeOf(err) != failure.CodeAdminNotFound {
		t.Fatalf("missing account must be not found, got %v", err)
	}
	if _, err := svc.SetStatus(context.Background(), SetStatusInput{AccountID: 0, Action: "enable"}); failure.CodeOf(err) != failure.CodeAdminInvalidArgument {
		t.Fatalf("non-positive id must be invalid, got %v", err)
	}
}

func TestUpdateConfigValidatesInputBeforePublishing(t *testing.T) {
	negative := int64(-1)
	negativeTimeout := int32(-5)
	cases := []struct {
		name  string
		input UpdateConfigInput
	}{
		{name: "blank display name", input: UpdateConfigInput{AccountID: 7, DisplayName: "  "}},
		{name: "negative priority", input: UpdateConfigInput{AccountID: 7, DisplayName: "a", Priority: -1}},
		{name: "negative concurrency", input: UpdateConfigInput{AccountID: 7, DisplayName: "a", ConcurrencyLimit: &negative}},
		{name: "unknown fingerprint mode", input: UpdateConfigInput{AccountID: 7, DisplayName: "a", FingerprintMode: "browser"}},
		{name: "negative response timeout", input: UpdateConfigInput{AccountID: 7, DisplayName: "a", ResponseTimeoutMs: &negativeTimeout}},
		{name: "negative first token timeout", input: UpdateConfigInput{AccountID: 7, DisplayName: "a", FirstTokenTimeoutMs: &negativeTimeout}},
		{name: "non-positive id", input: UpdateConfigInput{AccountID: 0, DisplayName: "a"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			q := &fakeQueries{accounts: map[int64]sqlc.SubscriptionAccount{7: account(7, 3, "enabled")}, channel: poolChannel(3)}
			publisher := &fakePublisher{}
			svc := newTestService(q, publisher, nil)
			_, err := svc.UpdateConfig(context.Background(), tc.input)
			if failure.CodeOf(err) != failure.CodeAdminInvalidArgument {
				t.Fatalf("expected invalid argument, got %v", err)
			}
			if len(publisher.requests) != 0 {
				t.Fatal("validation failure must not publish")
			}
		})
	}
}

func TestUpdateConfigRejectsUnknownProxyEntityAndPublishesOtherwise(t *testing.T) {
	q := &fakeQueries{accounts: map[int64]sqlc.SubscriptionAccount{7: account(7, 3, "enabled")}, channel: poolChannel(3), proxyErr: pgx.ErrNoRows}
	publisher := &fakePublisher{}
	svc := newTestService(q, publisher, nil)

	_, err := svc.UpdateConfig(context.Background(), UpdateConfigInput{AccountID: 7, DisplayName: "acct", ProxyID: 9})
	if failure.CodeOf(err) != failure.CodeAdminInvalidArgument {
		t.Fatalf("unknown proxy entity must be invalid, got %v", err)
	}

	q.proxyErr = nil
	if _, err := svc.UpdateConfig(context.Background(), UpdateConfigInput{AccountID: 7, DisplayName: "acct", ProxyID: 9, FingerprintMode: "device"}); err != nil {
		t.Fatalf("update config: %v", err)
	}
	if len(publisher.requests) != 1 || publisher.requests[0].Kind != runtimecontrol.KindChannelCapacity || publisher.requests[0].NextRevision != 4 {
		t.Fatalf("config change must publish via channel capacity control: %+v", publisher.requests)
	}
}

func TestDeleteOnlyRemovesArchivedAccountsAndKeepsReferencedOnes(t *testing.T) {
	q := &fakeQueries{accounts: map[int64]sqlc.SubscriptionAccount{
		1: account(1, 3, "enabled"),
		2: account(2, 3, "archived"),
		3: account(3, 3, "archived"),
	}}
	svc := newTestService(q, &fakePublisher{}, nil)

	if err := svc.Delete(context.Background(), 1); failure.CodeOf(err) != failure.CodeAdminConflict {
		t.Fatalf("non-archived account must be a conflict, got %v", err)
	}
	if err := svc.Delete(context.Background(), 2); err != nil {
		t.Fatalf("delete archived: %v", err)
	}
	if _, exists := q.accounts[2]; exists || len(q.deleted) != 1 {
		t.Fatalf("archived account must be deleted: deleted=%v", q.deleted)
	}

	// 已被请求历史引用（FK 23503）：降级为 conflict，保住账务归因链路。
	q.deleteErr = &pgconn.PgError{Code: "23503"}
	if err := svc.Delete(context.Background(), 3); failure.CodeOf(err) != failure.CodeAdminConflict {
		t.Fatalf("referenced account must be a conflict, got %v", err)
	}
	if _, exists := q.accounts[3]; !exists {
		t.Fatal("referenced account must survive")
	}
	q.deleteErr = errors.New("connection reset")
	if err := svc.Delete(context.Background(), 3); failure.CodeOf(err) != failure.CodeAdminStoreFailed {
		t.Fatalf("other store errors must be store failures, got %v", err)
	}
	if err := svc.Delete(context.Background(), 404); failure.CodeOf(err) != failure.CodeAdminNotFound {
		t.Fatalf("missing account must be not found, got %v", err)
	}
}

func TestListMergesAggregatesAndRuntimeViews(t *testing.T) {
	now := time.Now().UTC()
	q := &fakeQueries{
		listRows: []sqlc.AdminListSubscriptionAccountsRow{
			{ID: 1, ChannelID: 3, Status: "enabled", DisplayName: "a", ConcurrencyLimit: pgtype.Int4{Int32: 4, Valid: true}, CreatedAt: pgtype.Timestamptz{Time: now, Valid: true}, HasRefreshToken: true},
			{ID: 2, ChannelID: 3, Status: "disabled", DisplayName: "b", DisabledReason: pgtype.Text{String: "manual", Valid: true}},
			{ID: 3, ChannelID: 3, Status: "archived", DisplayName: "c"},
		},
		counts: sqlc.AdminCountAccountsByChannelRow{Total: 3, Enabled: 1, Disabled: 1, Archived: 1, ExpiringSoon: 0},
	}
	runtime := &fakeRuntime{runtimes: []breakerstore.AccountRuntime{
		{AccountID: 1, InFlight: 2, CooldownRemainingMs: 500},
		{AccountID: 2, UsagePauseRemainingMs: 9000},
	}}
	svc := newTestService(q, &fakePublisher{}, runtime)

	result, err := svc.List(context.Background(), 3, "")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(result.Accounts) != 3 || result.Aggregates.Total != 3 || result.Aggregates.Enabled != 1 {
		t.Fatalf("list result = %+v", result.Aggregates)
	}
	if result.Aggregates.InFlight != 2 || result.Aggregates.CoolingDown != 1 || result.Aggregates.UsagePaused != 1 {
		t.Fatalf("runtime aggregates = %+v", result.Aggregates)
	}
	first := result.Accounts[0]
	if first.Runtime == nil || first.Runtime.InFlight != 2 || first.ConcurrencyLimit == nil || *first.ConcurrencyLimit != 4 || !first.HasRefreshToken {
		t.Fatalf("first account view = %+v runtime=%+v", first, first.Runtime)
	}
	if result.Accounts[1].DisabledReason != "manual" || result.Accounts[2].Runtime != nil {
		t.Fatalf("disabled reason / archived runtime mapping wrong: %+v %+v", result.Accounts[1], result.Accounts[2].Runtime)
	}

	if _, err := svc.List(context.Background(), 0, ""); failure.CodeOf(err) != failure.CodeAdminInvalidArgument {
		t.Fatalf("non-positive channel id must be invalid, got %v", err)
	}

	// 运行态读失败只影响运行态列，不阻断列表主体。
	runtime.err = errors.New("redis down")
	degraded, err := svc.List(context.Background(), 3, "")
	if err != nil || len(degraded.Accounts) != 3 || degraded.Accounts[0].Runtime != nil {
		t.Fatalf("runtime failure must degrade gracefully: err=%v accounts=%d", err, len(degraded.Accounts))
	}
}

func TestCreateLedgerValidatesPeriodAndNormalizesCurrency(t *testing.T) {
	q := &fakeQueries{}
	svc := newTestService(q, &fakePublisher{}, nil)
	start := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)

	_, err := svc.CreateLedger(context.Background(), CreateLedgerInput{AccountID: 7, Currency: "usd", PeriodStart: start, PeriodEnd: start})
	if failure.CodeOf(err) != failure.CodeAdminInvalidArgument {
		t.Fatalf("empty period must be invalid, got %v", err)
	}
	if _, err := svc.CreateLedger(context.Background(), CreateLedgerInput{AccountID: 7, Currency: " ", PeriodStart: start, PeriodEnd: start.AddDate(0, 1, 0)}); failure.CodeOf(err) != failure.CodeAdminInvalidArgument {
		t.Fatalf("blank currency must be invalid, got %v", err)
	}
	entry, err := svc.CreateLedger(context.Background(), CreateLedgerInput{AccountID: 7, Currency: " usd ", PeriodStart: start, PeriodEnd: start.AddDate(0, 1, 0), Note: "sept"})
	if err != nil {
		t.Fatalf("create ledger: %v", err)
	}
	if q.ledgerParams.Currency != "USD" || !q.ledgerParams.Note.Valid || entry.AccountID != 7 {
		t.Fatalf("ledger params = %+v entry=%+v", q.ledgerParams, entry)
	}
}

// 列表视图按三层继承给出生效阈值与来源：账号覆写 > 渠道覆写 > 全局。
func TestListResolvesEffectiveUsagePauseThreshold(t *testing.T) {
	q := &fakeQueries{
		listRows: []sqlc.AdminListSubscriptionAccountsRow{
			{ID: 1, ChannelID: 3, Status: "enabled", UsagePauseThresholdPercent: pgtype.Int4{Int32: 70, Valid: true}, ChannelUsagePauseThresholdPercent: pgtype.Int4{Int32: 80, Valid: true}},
			{ID: 2, ChannelID: 3, Status: "enabled", ChannelUsagePauseThresholdPercent: pgtype.Int4{Int32: 80, Valid: true}},
			{ID: 3, ChannelID: 3, Status: "enabled"},
		},
		counts: sqlc.AdminCountAccountsByChannelRow{Total: 3, Enabled: 3},
	}
	svc := newTestService(q, &fakePublisher{}, nil).
		WithUsagePausePolicy(func(context.Context) int32 { return 95 }, nil)

	result, err := svc.List(context.Background(), 3, "")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	want := []struct {
		percent int32
		source  string
	}{{70, "account"}, {80, "channel"}, {95, "global"}}
	for index, expect := range want {
		got := result.Accounts[index]
		if got.EffectiveUsagePauseThresholdPercent != expect.percent || got.UsagePauseThresholdSource != expect.source {
			t.Fatalf("account %d effective = %d/%s, want %d/%s", got.ID, got.EffectiveUsagePauseThresholdPercent, got.UsagePauseThresholdSource, expect.percent, expect.source)
		}
	}
	if result.Accounts[0].UsagePauseThresholdPercent == nil || *result.Accounts[0].UsagePauseThresholdPercent != 70 {
		t.Fatalf("account override must be surfaced, got %v", result.Accounts[0].UsagePauseThresholdPercent)
	}
	if result.Accounts[1].UsagePauseThresholdPercent != nil {
		t.Fatalf("inherited account must report nil override, got %d", *result.Accounts[1].UsagePauseThresholdPercent)
	}
}

// 账号阈值独立接口：校验值域、普通列更新（不经容量发布）、随后只重算该账号；显式 nil 回到继承。
func TestUpdateUsagePauseThresholdWritesAndReconcilesAccountOnly(t *testing.T) {
	q := &fakeQueries{accounts: map[int64]sqlc.SubscriptionAccount{1: account(1, 3, "enabled")}, channel: poolChannel(3)}
	q.channel.AccountUsagePauseThresholdPercent = pgtype.Int4{Int32: 80, Valid: true}
	publisher := &fakePublisher{}
	reconciler := &fakeReconciler{}
	svc := newTestService(q, publisher, nil).
		WithUsagePausePolicy(func(context.Context) int32 { return 90 }, reconciler)

	for _, bad := range []int32{0, -1, 101} {
		if _, err := svc.UpdateUsagePauseThreshold(context.Background(), 1, &bad); failure.CodeOf(err) != failure.CodeAdminInvalidArgument {
			t.Fatalf("threshold %d must be invalid, got %v", bad, err)
		}
	}
	if _, err := svc.UpdateUsagePauseThreshold(context.Background(), 404, nil); failure.CodeOf(err) != failure.CodeAdminNotFound {
		t.Fatalf("missing account must be not found, got %v", err)
	}
	if q.thresholdWrites != 0 || len(reconciler.accountIDs) != 0 {
		t.Fatalf("rejected inputs must not write or reconcile: writes=%d reconciles=%v", q.thresholdWrites, reconciler.accountIDs)
	}

	percent := int32(60)
	result, err := svc.UpdateUsagePauseThreshold(context.Background(), 1, &percent)
	if err != nil {
		t.Fatalf("update threshold: %v", err)
	}
	if q.thresholdWrites != 1 || len(publisher.requests) != 0 {
		t.Fatalf("threshold must be a plain column update without capacity publish: writes=%d publishes=%d", q.thresholdWrites, len(publisher.requests))
	}
	if len(reconciler.accountIDs) != 1 || reconciler.accountIDs[0] != 1 {
		t.Fatalf("reconcile must target account 1 only, got %v", reconciler.accountIDs)
	}
	if result.RuntimeRefresh == nil || result.RuntimeRefresh.Scanned != 1 || result.RuntimeRefreshError != "" {
		t.Fatalf("runtime refresh must be reported, got %+v / %q", result.RuntimeRefresh, result.RuntimeRefreshError)
	}
	if result.Account.UsagePauseThresholdPercent == nil || *result.Account.UsagePauseThresholdPercent != 60 ||
		result.Account.EffectiveUsagePauseThresholdPercent != 60 || result.Account.UsagePauseThresholdSource != "account" {
		t.Fatalf("account view must reflect the override: %+v", result.Account)
	}

	cleared, err := svc.UpdateUsagePauseThreshold(context.Background(), 1, nil)
	if err != nil {
		t.Fatalf("clear threshold: %v", err)
	}
	if cleared.Account.UsagePauseThresholdPercent != nil || cleared.Account.EffectiveUsagePauseThresholdPercent != 80 || cleared.Account.UsagePauseThresholdSource != "channel" {
		t.Fatalf("cleared account must inherit the channel threshold: %+v", cleared.Account)
	}

	// 重算失败不回滚阈值：结果里报错，账号视图照常返回。
	reconciler.err = errors.New("redis down")
	failed, err := svc.UpdateUsagePauseThreshold(context.Background(), 1, &percent)
	if err != nil {
		t.Fatalf("update with failing reconcile must still succeed: %v", err)
	}
	if failed.RuntimeRefresh != nil || failed.RuntimeRefreshError == "" {
		t.Fatalf("reconcile failure must be surfaced, got %+v / %q", failed.RuntimeRefresh, failed.RuntimeRefreshError)
	}
}

// 刷新状态 / 手动用卡：未注入用量面时 409；注入后透传结果并返回刷新后的账号视图（含卡快照与画像）。
func TestRefreshStatusAndResetCreditGoThroughQuotaService(t *testing.T) {
	acct := account(1, 3, "enabled")
	acct.Platform = "openai"
	acct.ResetCreditsSnapshot = []byte(`{"available_count":2,"applicable_available_count":0,"credits":[{"expires_at":"2026-10-04T02:31:51Z","title":"Full reset"}],"fetched_at":"2026-09-06T10:00:00Z"}`)
	acct.AccountProfile = []byte(`{"fetched_at":"2026-09-06T10:00:00Z","account":{"plan_type":"plus","plan_display_name":"Plus","structure":"personal"},"subscription":{"has_active_subscription":true,"plan":"chatgptplusplan","expires_at":"2026-10-02T17:53:17Z","renews_at":"2026-10-02T11:53:17Z","billing_period":"monthly"},"user":{"country":"JP","region":"Tokyo","mfa_enabled":true}}`)
	acct.SubscriptionExpiresAt = pgtype.Timestamptz{Time: time.Date(2026, 10, 2, 17, 53, 17, 0, time.UTC), Valid: true}
	q := &fakeQueries{accounts: map[int64]sqlc.SubscriptionAccount{1: acct}, channel: poolChannel(3)}
	svc := newTestService(q, &fakePublisher{}, nil)

	if _, err := svc.RefreshStatus(context.Background(), 1); failure.CodeOf(err) != failure.CodeAdminConflict {
		t.Fatalf("missing quota service must be conflict, got %v", err)
	}
	if _, err := svc.ResetCredit(context.Background(), 1); failure.CodeOf(err) != failure.CodeAdminConflict {
		t.Fatalf("missing quota service must be conflict, got %v", err)
	}

	quota := &fakeQuota{
		report:  subscriptionquota.RefreshReport{Report: subscriptionquota.Report{Credits: subscriptionquota.CreditsSnapshot{AvailableCount: 2}}},
		outcome: subscriptionquota.ResetOutcome{Result: subscriptionquota.ConsumeResult{Code: "success", WindowsReset: 2}},
	}
	svc.WithQuota(quota)

	refreshed, err := svc.RefreshStatus(context.Background(), 1)
	if err != nil {
		t.Fatalf("RefreshStatus: %v", err)
	}
	if len(quota.queried) != 1 || refreshed.Report.Credits.AvailableCount != 2 {
		t.Fatalf("refresh result = %+v (queried=%v)", refreshed.Report, quota.queried)
	}
	if refreshed.Account.ResetCredits == nil || refreshed.Account.ResetCredits.AvailableCount != 2 || len(refreshed.Account.ResetCredits.Credits) != 1 {
		t.Fatalf("account view must expose the credits snapshot, got %+v", refreshed.Account.ResetCredits)
	}
	if refreshed.Account.AutoResetCredit.Enabled || refreshed.Account.AutoResetCredit.Mode != "any" {
		t.Fatalf("auto reset view defaults = %+v", refreshed.Account.AutoResetCredit)
	}
	if refreshed.Account.Profile == nil || refreshed.Account.Profile.User == nil || refreshed.Account.Profile.User.Country != "JP" {
		t.Fatalf("account view must expose the profile, got %+v", refreshed.Account.Profile)
	}
	if refreshed.Account.SubscriptionSource != "upstream" {
		t.Fatalf("subscription source must be upstream when the profile carries entitlement expiry, got %q", refreshed.Account.SubscriptionSource)
	}

	result, err := svc.ResetCredit(context.Background(), 1)
	if err != nil {
		t.Fatalf("ResetCredit: %v", err)
	}
	if len(quota.reset) != 1 || result.Outcome.Result.WindowsReset != 2 {
		t.Fatalf("reset result = %+v (reset=%v)", result.Outcome, quota.reset)
	}

	archived := account(2, 3, "archived")
	q.accounts[2] = archived
	if _, err := svc.ResetCredit(context.Background(), 2); failure.CodeOf(err) != failure.CodeAdminConflict {
		t.Fatalf("archived account must not consume credits, got %v", err)
	}
	quota.outcomeErr = failure.New(failure.CodeAdminUpstreamUnavailable, failure.WithMessage("upstream 409"))
	if _, err := svc.ResetCredit(context.Background(), 1); failure.CodeOf(err) != failure.CodeAdminUpstreamUnavailable {
		t.Fatalf("upstream failure must pass through, got %v", err)
	}
}

// 自动用卡配置：阈值 1~100 校验、mode 校验、开启时至少一个窗口参与、非 OpenAI 账号不可开启；视图回显配置。
func TestUpdateAutoResetCreditValidatesAndPersists(t *testing.T) {
	acct := account(1, 3, "enabled")
	acct.Platform = "openai"
	anthropic := account(2, 3, "enabled")
	anthropic.Platform = "anthropic"
	q := &fakeQueries{accounts: map[int64]sqlc.SubscriptionAccount{1: acct, 2: anthropic}, channel: poolChannel(3)}
	svc := newTestService(q, &fakePublisher{}, nil)

	bad := int32(0)
	ninety := int32(90)
	if _, err := svc.UpdateAutoResetCredit(context.Background(), 1, AutoResetCreditInput{Enabled: true, Threshold5hPercent: &bad}); failure.CodeOf(err) != failure.CodeAdminInvalidArgument {
		t.Fatalf("threshold 0 must be invalid, got %v", err)
	}
	if _, err := svc.UpdateAutoResetCredit(context.Background(), 1, AutoResetCreditInput{Enabled: true, Mode: "sometimes", Threshold7dPercent: &ninety}); failure.CodeOf(err) != failure.CodeAdminInvalidArgument {
		t.Fatalf("unknown mode must be invalid, got %v", err)
	}
	if _, err := svc.UpdateAutoResetCredit(context.Background(), 1, AutoResetCreditInput{Enabled: true}); failure.CodeOf(err) != failure.CodeAdminInvalidArgument {
		t.Fatalf("enabling without any participating window must be invalid, got %v", err)
	}
	if _, err := svc.UpdateAutoResetCredit(context.Background(), 2, AutoResetCreditInput{Enabled: true, Threshold7dPercent: &ninety}); failure.CodeOf(err) != failure.CodeAdminInvalidArgument {
		t.Fatalf("non-openai account must not enable auto reset, got %v", err)
	}
	if _, err := svc.UpdateAutoResetCredit(context.Background(), 404, AutoResetCreditInput{}); failure.CodeOf(err) != failure.CodeAdminNotFound {
		t.Fatalf("missing account must be not found, got %v", err)
	}

	// 只填 7d 90%：5h 留空 = 不参与；mode 缺省按 any。
	view, err := svc.UpdateAutoResetCredit(context.Background(), 1, AutoResetCreditInput{Enabled: true, Threshold7dPercent: &ninety})
	if err != nil {
		t.Fatalf("UpdateAutoResetCredit: %v", err)
	}
	auto := view.AutoResetCredit
	if !auto.Enabled || auto.Mode != "any" || auto.Threshold5hPercent != nil || auto.Threshold7dPercent == nil || *auto.Threshold7dPercent != 90 {
		t.Fatalf("auto reset config = %+v", auto)
	}
	if q.accounts[1].ConfigRevision != 1 {
		t.Fatalf("config revision must bump for audit, got %d", q.accounts[1].ConfigRevision)
	}

	// all 模式 + 两个窗口；关闭时允许两个都留空（保留配置）。
	hundred := int32(100)
	view, err = svc.UpdateAutoResetCredit(context.Background(), 1, AutoResetCreditInput{Enabled: true, Mode: "all", Threshold5hPercent: &hundred, Threshold7dPercent: &ninety})
	if err != nil || view.AutoResetCredit.Mode != "all" || view.AutoResetCredit.Threshold5hPercent == nil {
		t.Fatalf("all mode config = %+v err=%v", view.AutoResetCredit, err)
	}
	view, err = svc.UpdateAutoResetCredit(context.Background(), 1, AutoResetCreditInput{Enabled: false})
	if err != nil || view.AutoResetCredit.Enabled {
		t.Fatalf("disable must succeed without thresholds: %+v err=%v", view.AutoResetCredit, err)
	}
}

// 订阅到期来源：无画像但有手工值 → manual；无画像无值 → 空。
func TestProfileViewDerivesSubscriptionSource(t *testing.T) {
	manualAt := pgtype.Timestamptz{Time: time.Now(), Valid: true}
	if profile, source := profileView(nil, manualAt); profile != nil || source != "manual" {
		t.Fatalf("manual expiry without profile = %v/%q", profile, source)
	}
	if profile, source := profileView(nil, pgtype.Timestamptz{}); profile != nil || source != "" {
		t.Fatalf("no expiry without profile = %v/%q", profile, source)
	}
	raw := []byte(`{"fetched_at":"2026-09-06T10:00:00Z","subscription":{"has_active_subscription":true}}`)
	if profile, source := profileView(raw, manualAt); profile == nil || source != "manual" {
		t.Fatalf("profile without entitlement expiry must keep manual source, got %v/%q", profile, source)
	}
}

// 导入后首刷：文件导入的账号后台批量刷新；OAuth 完成同步刷新。
func TestImportTriggersInitialRefresh(t *testing.T) {
	q := &fakeQueries{accounts: map[int64]sqlc.SubscriptionAccount{}, channel: poolChannel(3)}
	quota := &fakeQuota{batches: make(chan []int64, 1)}
	svc := newTestService(q, &fakePublisher{}, nil).WithQuota(quota)

	svc.refreshAfterImport(context.Background(), []int64{5, 6}, false)
	// 后台批量刷新在 goroutine 里执行：等它把批次送出来。
	select {
	case batch := <-quota.batches:
		if len(batch) != 2 || len(quota.queried) != 0 {
			t.Fatalf("file import must refresh in one background batch, got batch=%v queried=%v", batch, quota.queried)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("background batch refresh was not triggered")
	}
	svc.refreshAfterImport(context.Background(), []int64{7}, true)
	if len(quota.queried) != 1 || quota.queried[0] != 7 {
		t.Fatalf("oauth import must refresh synchronously, got %v", quota.queried)
	}
	svc.refreshAfterImport(context.Background(), nil, true)
	if len(quota.queried) != 1 {
		t.Fatal("empty id list must be a no-op")
	}
}
