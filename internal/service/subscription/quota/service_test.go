package quota

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/ThankCat/unio-gateway/internal/core/adapter"
	"github.com/ThankCat/unio-gateway/internal/platform/failure"
	"github.com/ThankCat/unio-gateway/internal/platform/store/sqlc"
	"github.com/ThankCat/unio-gateway/internal/service/subscription"
)

func asUpstreamError(err error, target **UpstreamError) bool { return errors.As(err, target) }

var testNow = time.Date(2026, 9, 6, 10, 0, 0, 0, time.UTC)

type fakeQueries struct {
	accounts        map[int64]sqlc.SubscriptionAccount
	creditSnapshots map[int64][]byte
	states          map[int64][]byte
	profiles        map[int64][]byte
	facts           []sqlc.UpdateAccountSubscriptionFactsParams
	autoRows        []sqlc.ListAutoResetCreditAccountsRow
}

func newFakeQueries(accounts ...sqlc.SubscriptionAccount) *fakeQueries {
	q := &fakeQueries{accounts: map[int64]sqlc.SubscriptionAccount{}, creditSnapshots: map[int64][]byte{}, states: map[int64][]byte{}, profiles: map[int64][]byte{}}
	for _, account := range accounts {
		q.accounts[account.ID] = account
	}
	return q
}

func (q *fakeQueries) AdminGetSubscriptionAccount(_ context.Context, id int64) (sqlc.SubscriptionAccount, error) {
	account, ok := q.accounts[id]
	if !ok {
		return sqlc.SubscriptionAccount{}, pgx.ErrNoRows
	}
	return account, nil
}

func (q *fakeQueries) UpdateAccountResetCreditsSnapshot(_ context.Context, arg sqlc.UpdateAccountResetCreditsSnapshotParams) error {
	q.creditSnapshots[arg.ID] = arg.ResetCreditsSnapshot
	return nil
}

func (q *fakeQueries) UpdateAccountProfileSnapshot(_ context.Context, arg sqlc.UpdateAccountProfileSnapshotParams) error {
	q.profiles[arg.ID] = arg.AccountProfile
	return nil
}

func (q *fakeQueries) UpdateAccountSubscriptionFacts(_ context.Context, arg sqlc.UpdateAccountSubscriptionFactsParams) error {
	q.facts = append(q.facts, arg)
	return nil
}

func (q *fakeQueries) UpdateAccountAutoResetCreditState(_ context.Context, arg sqlc.UpdateAccountAutoResetCreditStateParams) error {
	q.states[arg.ID] = arg.AutoResetCreditState
	return nil
}

func (q *fakeQueries) ListAutoResetCreditAccounts(context.Context, int32) ([]sqlc.ListAutoResetCreditAccountsRow, error) {
	return q.autoRows, nil
}

type fakeIdentity struct{ err error }

func (f fakeIdentity) ResolveAccountIdentity(_ context.Context, accountID int64) (subscription.ProbeIdentity, error) {
	if f.err != nil {
		return subscription.ProbeIdentity{}, f.err
	}
	return subscription.ProbeIdentity{AccountID: accountID, AccessToken: "token", UpstreamAccountID: "acct"}, nil
}

type consumeCall struct {
	creditID string
	redeemID string
}

type fakeUpstream struct {
	usage      Usage
	usageErr   error
	credits    ResetCredits
	creditsErr error
	consume    ConsumeResult
	consumeErr error
	consumed   []consumeCall
	// afterConsume 让消费成功后的回读看到新水位。
	afterConsume *Usage
	check        AccountCheck
	checkErr     error
	me           Me
	meErr        error
}

func (f *fakeUpstream) FetchAccountCheck(context.Context, Identity) (AccountCheck, error) {
	if f.checkErr != nil {
		return AccountCheck{}, f.checkErr
	}
	return f.check, nil
}

func (f *fakeUpstream) FetchMe(context.Context, Identity) (Me, error) {
	if f.meErr != nil {
		return Me{}, f.meErr
	}
	return f.me, nil
}

func (f *fakeUpstream) FetchUsage(context.Context, Identity) (Usage, error) {
	if f.usageErr != nil {
		return Usage{}, f.usageErr
	}
	return f.usage, nil
}

func (f *fakeUpstream) FetchResetCredits(context.Context, Identity) (ResetCredits, error) {
	if f.creditsErr != nil {
		return ResetCredits{}, f.creditsErr
	}
	return f.credits, nil
}

func (f *fakeUpstream) ConsumeResetCredit(_ context.Context, _ Identity, creditID, redeemID string) (ConsumeResult, error) {
	f.consumed = append(f.consumed, consumeCall{creditID: creditID, redeemID: redeemID})
	if f.consumeErr != nil {
		return ConsumeResult{}, f.consumeErr
	}
	if f.afterConsume != nil {
		f.usage = *f.afterConsume
		if len(f.credits.Credits) > 0 {
			f.credits.Credits = f.credits.Credits[1:]
			f.credits.AvailableCount--
		}
		if f.usage.ResetCreditCounts != nil {
			f.usage.ResetCreditCounts.AvailableCount = f.credits.AvailableCount
		}
	}
	return f.consume, nil
}

type observation struct {
	accountID int64
	facts     *adapter.AccountUsageFacts
}

type fakeObserver struct{ observed []observation }

func (o *fakeObserver) RecordAccountUsageObservation(_ context.Context, accountID int64, facts *adapter.AccountUsageFacts) {
	o.observed = append(o.observed, observation{accountID: accountID, facts: facts})
}

func openAIAccount(id int64) sqlc.SubscriptionAccount {
	return sqlc.SubscriptionAccount{ID: id, ChannelID: 7, Platform: "openai", Status: "enabled", DisplayName: "acct"}
}

func fullUsage(primaryPercent, secondaryPercent float64, available int) Usage {
	return Usage{
		PlanType: "plus",
		RateLimit: &RateLimit{
			PrimaryWindow:   &Window{UsedPercent: primaryPercent, LimitWindowSeconds: 18000, ResetAfterSeconds: 3600, ResetAt: testNow.Unix() + 3600},
			SecondaryWindow: &Window{UsedPercent: secondaryPercent, LimitWindowSeconds: 604800, ResetAfterSeconds: 86400, ResetAt: testNow.Unix() + 86400},
		},
		ResetCreditCounts: &ResetCreditCounts{AvailableCount: available},
	}
}

func twoCredits() ResetCredits {
	return ResetCredits{AvailableCount: 2, Credits: []ResetCredit{
		{ID: "credit-late", ResetType: "codex_rate_limits", Status: "available", ExpiresAt: testNow.Add(48 * time.Hour)},
		{ID: "credit-early", ResetType: "codex_rate_limits", Status: "available", ExpiresAt: testNow.Add(24 * time.Hour)},
	}}
}

func newTestService(q *fakeQueries, upstream *fakeUpstream, observer *fakeObserver) *Service {
	svc := NewService(q, fakeIdentity{}, upstream, observer, nil)
	svc.now = func() time.Time { return testNow }
	return svc
}

func TestQueryUsageObservesWindowsAndPersistsCreditsSnapshot(t *testing.T) {
	q := newFakeQueries(openAIAccount(1))
	upstream := &fakeUpstream{usage: fullUsage(95, 71, 2), credits: twoCredits()}
	observer := &fakeObserver{}

	report, err := newTestService(q, upstream, observer).QueryUsage(context.Background(), 1)
	if err != nil {
		t.Fatalf("QueryUsage: %v", err)
	}
	if len(observer.observed) != 1 || observer.observed[0].accountID != 1 {
		t.Fatalf("usage must be observed exactly once, got %+v", observer.observed)
	}
	facts := observer.observed[0].facts
	if !facts.Primary.Present || facts.Primary.UsedPercent != 95 || facts.Primary.WindowMinutes != 300 || facts.Primary.ResetAtUnix != testNow.Unix()+3600 {
		t.Fatalf("primary facts = %+v", facts.Primary)
	}
	if !facts.Secondary.Present || facts.Secondary.WindowMinutes != 10080 || facts.PlanType != "plus" {
		t.Fatalf("secondary facts = %+v plan=%q", facts.Secondary, facts.PlanType)
	}
	if report.Credits.AvailableCount != 2 || len(report.Credits.Credits) != 2 {
		t.Fatalf("credits snapshot = %+v", report.Credits)
	}
	if report.Credits.Credits[0].ExpiresAt != testNow.Add(24*time.Hour) {
		t.Fatalf("snapshot must list the earliest expiring credit first, got %+v", report.Credits.Credits)
	}
	if usable := report.UsableCredits(); len(usable) != 2 || usable[0].ID != "credit-early" {
		t.Fatalf("usable credits = %+v", usable)
	}
	stored, ok := ParseCreditsSnapshot(q.creditSnapshots[1])
	if !ok || stored.AvailableCount != 2 || len(stored.Credits) != 2 || !stored.FetchedAt.Equal(testNow) {
		t.Fatalf("persisted snapshot = %+v (ok=%v)", stored, ok)
	}
	if string(q.creditSnapshots[1]) == "" || containsCreditID(q.creditSnapshots[1]) {
		t.Fatalf("persisted snapshot must not contain credit ids: %s", q.creditSnapshots[1])
	}
}

func containsCreditID(raw []byte) bool {
	s := string(raw)
	return strings.Contains(s, "credit-early") || strings.Contains(s, "credit-late")
}

func contains(s, sub string) bool { return strings.Contains(s, sub) }

func TestQueryUsageKeepsUsageWhenCreditDetailsFail(t *testing.T) {
	q := newFakeQueries(openAIAccount(1))
	upstream := &fakeUpstream{usage: fullUsage(10, 20, 2), creditsErr: errors.New("credits endpoint 500")}
	observer := &fakeObserver{}

	report, err := newTestService(q, upstream, observer).QueryUsage(context.Background(), 1)
	if err != nil {
		t.Fatalf("QueryUsage must succeed without credit details: %v", err)
	}
	if report.CreditsError == "" || report.Credits.AvailableCount != 2 || len(report.Credits.Credits) != 0 {
		t.Fatalf("report = %+v", report)
	}
	if len(observer.observed) != 1 {
		t.Fatalf("usage must still be observed, got %d observations", len(observer.observed))
	}
}

func TestQueryUsageRejectsNonOpenAIAccountsAndMapsUpstreamErrors(t *testing.T) {
	anthropic := openAIAccount(2)
	anthropic.Platform = "anthropic"
	q := newFakeQueries(openAIAccount(1), anthropic)
	upstream := &fakeUpstream{usageErr: &UpstreamError{Operation: "usage", StatusCode: http.StatusUnauthorized, Body: `{"detail":"token expired"}`}}
	svc := newTestService(q, upstream, &fakeObserver{})

	if _, err := svc.QueryUsage(context.Background(), 2); failure.CodeOf(err) != failure.CodeAdminInvalidArgument {
		t.Fatalf("non-openai account must be invalid argument, got %v", err)
	}
	if _, err := svc.QueryUsage(context.Background(), 404); failure.CodeOf(err) != failure.CodeAdminNotFound {
		t.Fatalf("missing account must be not found, got %v", err)
	}
	_, err := svc.QueryUsage(context.Background(), 1)
	if failure.CodeOf(err) != failure.CodeAdminUpstreamUnavailable {
		t.Fatalf("upstream 401 must map to admin upstream unavailable, got %v", err)
	}
	if !contains(err.Error(), "401") || !contains(err.Error(), "token expired") {
		t.Fatalf("error must carry status and body, got %v", err)
	}
	upstream.usageErr = failure.New(failure.CodeAdapterSendRequestFailed, failure.WithMessage("dial tcp: timeout"))
	if _, err := svc.QueryUsage(context.Background(), 1); failure.CodeOf(err) != failure.CodeAdminUpstreamUnavailable {
		t.Fatalf("network failure must map to admin upstream unavailable, got %v", err)
	}
}

func TestResetCreditConsumesThenRefreshesUsage(t *testing.T) {
	q := newFakeQueries(openAIAccount(1))
	after := fullUsage(0, 0, 1)
	upstream := &fakeUpstream{
		usage: fullUsage(100, 80, 2), credits: twoCredits(),
		consume: ConsumeResult{Code: "success", WindowsReset: 2}, afterConsume: &after,
	}
	observer := &fakeObserver{}

	outcome, err := newTestService(q, upstream, observer).ResetCredit(context.Background(), 1)
	if err != nil {
		t.Fatalf("ResetCredit: %v", err)
	}
	if len(upstream.consumed) != 1 || upstream.consumed[0].creditID != "" || upstream.consumed[0].redeemID == "" {
		t.Fatalf("manual reset must consume untargeted with a redeem id, got %+v", upstream.consumed)
	}
	if outcome.Result.WindowsReset != 2 || outcome.Report == nil || outcome.RefreshError != "" {
		t.Fatalf("outcome = %+v", outcome)
	}
	// 回读后的观测把新水位（0%）送进观测链路，让暂停按新水位解除。
	if len(observer.observed) != 1 || observer.observed[0].facts.Primary.UsedPercent != 0 {
		t.Fatalf("post-consume observation = %+v", observer.observed)
	}
	if outcome.Report.Credits.AvailableCount != 1 {
		t.Fatalf("credits after consume = %+v", outcome.Report.Credits)
	}
}

func TestResetCreditSurfacesUpstreamRejectionAndRefreshFailure(t *testing.T) {
	q := newFakeQueries(openAIAccount(1))
	upstream := &fakeUpstream{consumeErr: &UpstreamError{Operation: "consume reset credit", StatusCode: http.StatusConflict, Body: "no applicable credit"}}
	svc := newTestService(q, upstream, &fakeObserver{})

	_, err := svc.ResetCredit(context.Background(), 1)
	if failure.CodeOf(err) != failure.CodeAdminUpstreamUnavailable || !contains(err.Error(), "no applicable credit") {
		t.Fatalf("rejection must surface upstream body, got %v", err)
	}

	// 消费成功但回读失败：不报错，只在 RefreshError 说明。
	upstream.consumeErr = nil
	upstream.consume = ConsumeResult{Code: "success", WindowsReset: 2}
	upstream.usageErr = errors.New("usage endpoint down")
	outcome, err := svc.ResetCredit(context.Background(), 1)
	if err != nil {
		t.Fatalf("consume success must not fail on refresh error: %v", err)
	}
	if outcome.Report != nil || outcome.RefreshError == "" || outcome.Result.WindowsReset != 2 {
		t.Fatalf("outcome = %+v", outcome)
	}
}

func sampleCheck() AccountCheck {
	return AccountCheck{
		AccountID: "acct", PlanType: "plus", PlanDisplayName: "Plus", Structure: "personal", HasPreviouslyPaidSubscription: true,
		CreatedTime: testNow.Add(-72 * time.Hour), FeatureCount: 2,
		Entitlement: &Entitlement{
			HasActiveSubscription: true, SubscriptionPlan: "chatgptplusplan", BillingPeriod: "monthly",
			ExpiresAt: testNow.Add(20 * 24 * time.Hour), RenewsAt: testNow.Add(20*24*time.Hour - 6*time.Hour),
			PromoCampaignID: "plus-1-month-free", DiscountPercent: 100,
		},
	}
}

func sampleMeValue() Me {
	return Me{Email: "acct@example.com", Name: "Nancy", MFAEnabled: true, Country: "JP", Region: "Tokyo", Created: testNow.Add(-100 * time.Hour),
		Orgs: []MeOrg{{Title: "Personal", Personal: true, IsDefault: true, Role: "owner"}}}
}

// 刷新状态 = 用量 + 卡 + 画像：画像落库、套餐与到期回写为上游权威值。
func TestRefreshPersistsProfileAndSubscriptionFacts(t *testing.T) {
	q := newFakeQueries(openAIAccount(1))
	usage := fullUsage(10, 71, 2)
	usage.Credits = &UsageCredits{HasCredits: false, Balance: "0"}
	upstream := &fakeUpstream{usage: usage, credits: twoCredits(), check: sampleCheck(), me: sampleMeValue()}
	observer := &fakeObserver{}

	report, err := newTestService(q, upstream, observer).Refresh(context.Background(), 1)
	if err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	if len(observer.observed) != 1 || report.Credits.AvailableCount != 2 {
		t.Fatalf("usage part must behave like QueryUsage: observed=%d credits=%+v", len(observer.observed), report.Credits)
	}
	p := report.Profile
	if p.Account == nil || p.Account.PlanDisplayName != "Plus" || p.Subscription == nil || !p.Subscription.HasActiveSubscription {
		t.Fatalf("profile = %+v", p)
	}
	if p.User == nil || p.User.Country != "JP" || !p.User.MFAEnabled || len(p.User.Orgs) != 1 {
		t.Fatalf("profile user = %+v", p.User)
	}
	if p.Credits == nil || p.Credits.Balance != "0" || len(p.Errors) != 0 || p.Abnormal() != "" {
		t.Fatalf("profile credits/errors = %+v", p)
	}
	stored, ok := ParseProfile(q.profiles[1])
	if !ok || stored.Subscription.PromoCampaignID != "plus-1-month-free" || stored.Subscription.DiscountPercent != 100 {
		t.Fatalf("persisted profile = %+v ok=%v", stored, ok)
	}
	if len(q.facts) != 1 || q.facts[0].PlanType.String != "plus" || !q.facts[0].SubscriptionExpiresAt.Valid ||
		!q.facts[0].SubscriptionExpiresAt.Time.Equal(sampleCheck().Entitlement.ExpiresAt) {
		t.Fatalf("subscription facts = %+v", q.facts)
	}
}

// 画像端点部分失败：用量照常，错误记进 Profile.Errors，能拿到的分项照常落库；不回写订阅事实。
func TestRefreshKeepsUsageWhenProfileEndpointsFail(t *testing.T) {
	q := newFakeQueries(openAIAccount(1))
	upstream := &fakeUpstream{
		usage: fullUsage(10, 71, 2), credits: twoCredits(),
		checkErr: &UpstreamError{Operation: "accounts check", StatusCode: 403, Body: "forbidden"},
		me:       sampleMeValue(),
	}

	report, err := newTestService(q, upstream, &fakeObserver{}).Refresh(context.Background(), 1)
	if err != nil {
		t.Fatalf("Refresh must not fail on profile errors: %v", err)
	}
	if report.Profile.Account != nil || report.Profile.Subscription != nil || report.Profile.User == nil {
		t.Fatalf("profile = %+v", report.Profile)
	}
	if !strings.Contains(report.Profile.Errors["accounts_check"], "403") {
		t.Fatalf("errors = %+v", report.Profile.Errors)
	}
	if len(q.facts) != 0 {
		t.Fatalf("subscription facts must not be written without accounts/check, got %+v", q.facts)
	}
}

func TestProfileAbnormalFlags(t *testing.T) {
	cases := []struct {
		name    string
		profile Profile
		want    string
	}{
		{name: "healthy", profile: Profile{Subscription: &ProfileSubscription{HasActiveSubscription: true}}, want: ""},
		{name: "deactivated", profile: Profile{Account: &ProfileAccount{IsDeactivated: true}}, want: "上游已停用账号"},
		{name: "banned org", profile: Profile{User: &ProfileUser{Orgs: []ProfileOrg{{Banned: true}}}}, want: "所属组织已被封禁"},
		{name: "delinquent", profile: Profile{Subscription: &ProfileSubscription{HasActiveSubscription: true, IsDelinquent: true}}, want: "订阅欠费"},
		{name: "no subscription", profile: Profile{Subscription: &ProfileSubscription{}}, want: "无有效订阅"},
	}
	for _, tc := range cases {
		if got := tc.profile.Abnormal(); got != tc.want {
			t.Fatalf("%s: Abnormal() = %q, want %q", tc.name, got, tc.want)
		}
	}
}
