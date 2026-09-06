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
