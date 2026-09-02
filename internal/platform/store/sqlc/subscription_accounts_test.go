package sqlc_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/ThankCat/unio-gateway/internal/platform/store/sqlc"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

// insertPoolChannel 插入一个池型渠道（不持凭据），返回主键。
func insertPoolChannel(t *testing.T, ctx context.Context, tx pgx.Tx, providerID int64, name string, defaultConcurrency *int32) int64 {
	t.Helper()

	var id int64
	err := tx.QueryRow(ctx, `
		INSERT INTO channels (provider_id, name, adapter_key, credential, protocols, status, priority,
		                      supply_form, account_default_concurrency)
		VALUES ($1, $2, 'codex', '', ARRAY['openai'], 'disabled', 50, 'pool', $3)
		RETURNING id
	`, providerID, name, defaultConcurrency).Scan(&id)
	if err != nil {
		t.Fatalf("insert pool channel: %v", err)
	}
	return id
}

func createAccount(t *testing.T, ctx context.Context, q *sqlc.Queries, channelID int64, upstreamID string, priority int32) sqlc.SubscriptionAccount {
	t.Helper()

	account, err := q.AdminCreateSubscriptionAccount(ctx, sqlc.AdminCreateSubscriptionAccountParams{
		ChannelID:         channelID,
		Platform:          "openai",
		CredentialType:    "oauth",
		UpstreamAccountID: upstreamID,
		DisplayName:       upstreamID + "@example.com",
		PlanType:          pgtype.Text{String: "plus", Valid: true},
		Credentials:       []byte(`{"access_token":"at","refresh_token":"rt"}`),
		Priority:          priority,
	})
	if err != nil {
		t.Fatalf("create account %s: %v", upstreamID, err)
	}
	return account
}

// 导入一律落 disabled，须由管理员显式启用——这条不变量若被破坏，导入即上线，风险不可控。
func TestAdminCreateSubscriptionAccountLandsDisabled(t *testing.T) {
	ctx, tx, queries, cleanup := newModelChannelTestTx(t)
	defer cleanup()

	providerID := insertProvider(t, ctx, tx, "codex-provider", "enabled")
	channelID := insertPoolChannel(t, ctx, tx, providerID, "codex-pool", nil)

	account := createAccount(t, ctx, queries, channelID, "acct-new", 50)

	if account.Status != "disabled" {
		t.Fatalf("imported account must land disabled, got %q", account.Status)
	}
	if account.ConfigRevision != 1 {
		t.Fatalf("initial config_revision = %d, want 1", account.ConfigRevision)
	}
}

// 候选快照只取本渠道下 enabled 的账号：停用与归档的号不得进入调度，否则会被选中并打到已失效凭据。
func TestListSchedulableAccountsByChannelExcludesNonEnabled(t *testing.T) {
	ctx, tx, queries, cleanup := newModelChannelTestTx(t)
	defer cleanup()

	providerID := insertProvider(t, ctx, tx, "codex-provider", "enabled")
	defaultConcurrency := int32(4)
	channelID := insertPoolChannel(t, ctx, tx, providerID, "codex-pool", &defaultConcurrency)
	otherChannelID := insertPoolChannel(t, ctx, tx, providerID, "codex-pool-other", nil)

	enabled := createAccount(t, ctx, queries, channelID, "acct-enabled", 10)
	disabled := createAccount(t, ctx, queries, channelID, "acct-disabled", 20)
	archived := createAccount(t, ctx, queries, channelID, "acct-archived", 30)
	otherChannel := createAccount(t, ctx, queries, otherChannelID, "acct-other-channel", 10)

	enableAccount(t, ctx, queries, enabled.ID)
	enableAccount(t, ctx, queries, otherChannel.ID)
	setStatus(t, ctx, queries, archived.ID, "archived")

	rows, err := queries.ListSchedulableAccountsByChannel(ctx, channelID)
	if err != nil {
		t.Fatalf("list schedulable: %v", err)
	}

	if len(rows) != 1 {
		t.Fatalf("schedulable accounts = %d, want 1 (enabled only)", len(rows))
	}
	if rows[0].ID != enabled.ID {
		t.Fatalf("schedulable account = %d, want %d", rows[0].ID, enabled.ID)
	}
	if rows[0].AccountDefaultConcurrency.Int32 != defaultConcurrency {
		t.Fatalf("channel default concurrency = %v, want %d", rows[0].AccountDefaultConcurrency, defaultConcurrency)
	}
	// 账号自身未设并发时保持 NULL，由调用方回落到渠道默认——两者必须可区分。
	if rows[0].ConcurrencyLimit.Valid {
		t.Fatalf("account concurrency_limit should stay NULL, got %v", rows[0].ConcurrencyLimit)
	}

	_ = disabled
}

// 调度参数变更必须提升 config_revision：运行态围栏靠它感知配置漂移，不涨就永远读到旧容量。
func TestUpdateAccountConfigBumpsRevision(t *testing.T) {
	ctx, tx, queries, cleanup := newModelChannelTestTx(t)
	defer cleanup()

	providerID := insertProvider(t, ctx, tx, "codex-provider", "enabled")
	channelID := insertPoolChannel(t, ctx, tx, providerID, "codex-pool", nil)
	account := createAccount(t, ctx, queries, channelID, "acct-cfg", 50)

	updated, err := queries.AdminUpdateSubscriptionAccountConfig(ctx, sqlc.AdminUpdateSubscriptionAccountConfigParams{
		ID:               account.ID,
		DisplayName:      "renamed",
		ProxyUrl:         pgtype.Text{String: "http://proxy.example:8080", Valid: true},
		ConcurrencyLimit: pgtype.Int4{Int32: 3, Valid: true},
		Priority:         10,
	})
	if err != nil {
		t.Fatalf("update config: %v", err)
	}

	if updated.ConfigRevision != account.ConfigRevision+1 {
		t.Fatalf("config_revision = %d, want %d", updated.ConfigRevision, account.ConfigRevision+1)
	}
	if updated.ProxyUrl.String != "http://proxy.example:8080" {
		t.Fatalf("proxy_url = %q", updated.ProxyUrl.String)
	}
}

// 账号配置变更要传播到渠道运行态围栏：改的是账号的并发，渠道自己的并发一个字都没动，
// 但候选快照按 capacity_revision 判新旧，不推进这一版，新配置就要等到下次渠道自身变更才生效。
func TestBumpChannelCapacityRevisionPropagatesAccountConfigChange(t *testing.T) {
	ctx, tx, queries, cleanup := newModelChannelTestTx(t)
	defer cleanup()

	providerID := insertProvider(t, ctx, tx, "codex-provider", "enabled")
	channelID := insertPoolChannel(t, ctx, tx, providerID, "codex-pool", nil)
	account := createAccount(t, ctx, queries, channelID, "acct-propagate", 50)

	before, err := queries.GetChannel(ctx, channelID)
	if err != nil {
		t.Fatalf("get channel: %v", err)
	}

	if _, err := queries.AdminUpdateSubscriptionAccountConfig(ctx, sqlc.AdminUpdateSubscriptionAccountConfigParams{
		ID:               account.ID,
		DisplayName:      "propagated",
		ConcurrencyLimit: pgtype.Int4{Int32: 2, Valid: true},
		Priority:         10,
	}); err != nil {
		t.Fatalf("update account config: %v", err)
	}

	bumped, err := queries.BumpChannelCapacityRevision(ctx, sqlc.BumpChannelCapacityRevisionParams{
		ID:              channelID,
		CurrentRevision: before.CapacityRevision,
		NextRevision:    before.CapacityRevision + 1,
	})
	if err != nil {
		t.Fatalf("bump capacity revision: %v", err)
	}
	if bumped.CapacityRevision != before.CapacityRevision+1 {
		t.Fatalf("capacity_revision = %d, want %d", bumped.CapacityRevision, before.CapacityRevision+1)
	}
	// 渠道自己的并发容量必须原样保留：这次变的是账号，不是渠道。
	if bumped.ConcurrencyLimit != before.ConcurrencyLimit {
		t.Fatalf("channel concurrency changed: %v → %v", before.ConcurrencyLimit, bumped.ConcurrencyLimit)
	}

	// CAS 语义与 CommitChannelCapacityAtRevision 一致：拿旧 revision 的并发写入必须落空，
	// 否则两个管理员同时改同一个池就会各自推进一版，Redis control 与库里对不上。
	if _, err := queries.BumpChannelCapacityRevision(ctx, sqlc.BumpChannelCapacityRevisionParams{
		ID:              channelID,
		CurrentRevision: before.CapacityRevision,
		NextRevision:    before.CapacityRevision + 1,
	}); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("stale revision bump = %v, want pgx.ErrNoRows", err)
	}
	// 版本必须逐级推进，不允许跳号：Redis control 的 revision 与库里一一对应。
	if _, err := queries.BumpChannelCapacityRevision(ctx, sqlc.BumpChannelCapacityRevisionParams{
		ID:              channelID,
		CurrentRevision: bumped.CapacityRevision,
		NextRevision:    bumped.CapacityRevision + 2,
	}); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("revision gap bump = %v, want pgx.ErrNoRows", err)
	}
}

// 凭据轮换不改变调度行为，不应提升 config_revision——否则每次后台刷新令牌都会无谓地失效运行态快照。
func TestUpdateAccountTokensDoesNotBumpRevision(t *testing.T) {
	ctx, tx, queries, cleanup := newModelChannelTestTx(t)
	defer cleanup()

	providerID := insertProvider(t, ctx, tx, "codex-provider", "enabled")
	channelID := insertPoolChannel(t, ctx, tx, providerID, "codex-pool", nil)
	account := createAccount(t, ctx, queries, channelID, "acct-token", 50)

	if err := queries.UpdateAccountTokens(ctx, sqlc.UpdateAccountTokensParams{
		ID:          account.ID,
		Credentials: []byte(`{"access_token":"new","refresh_token":"rt"}`),
	}); err != nil {
		t.Fatalf("update tokens: %v", err)
	}

	after, err := queries.AdminGetSubscriptionAccount(ctx, account.ID)
	if err != nil {
		t.Fatalf("get account: %v", err)
	}
	if after.ConfigRevision != account.ConfigRevision {
		t.Fatalf("config_revision changed on token refresh: %d → %d", account.ConfigRevision, after.ConfigRevision)
	}
}

// 重新授权按 (platform, upstream_account_id) 覆盖凭据，保留调度参数——续命是高频操作，不能走删除重建。
func TestReauthorizePreservesSchedulingConfig(t *testing.T) {
	ctx, tx, queries, cleanup := newModelChannelTestTx(t)
	defer cleanup()

	providerID := insertProvider(t, ctx, tx, "codex-provider", "enabled")
	channelID := insertPoolChannel(t, ctx, tx, providerID, "codex-pool", nil)
	account := createAccount(t, ctx, queries, channelID, "acct-reauth", 50)

	if _, err := queries.AdminUpdateSubscriptionAccountConfig(ctx, sqlc.AdminUpdateSubscriptionAccountConfigParams{
		ID:               account.ID,
		DisplayName:      "kept",
		ConcurrencyLimit: pgtype.Int4{Int32: 7, Valid: true},
		Priority:         20,
	}); err != nil {
		t.Fatalf("update config: %v", err)
	}

	reauthorized, err := queries.AdminReauthorizeSubscriptionAccount(ctx, sqlc.AdminReauthorizeSubscriptionAccountParams{
		Platform:          "openai",
		UpstreamAccountID: "acct-reauth",
		Credentials:       []byte(`{"access_token":"rotated"}`),
	})
	if err != nil {
		t.Fatalf("reauthorize: %v", err)
	}

	if reauthorized.ID != account.ID {
		t.Fatalf("reauthorize created a new row: %d vs %d", reauthorized.ID, account.ID)
	}
	if reauthorized.ConcurrencyLimit.Int32 != 7 || reauthorized.Priority != 20 {
		t.Fatalf("scheduling config lost on reauthorize: conc=%v priority=%d",
			reauthorized.ConcurrencyLimit, reauthorized.Priority)
	}
	// plan_type 传 NULL 时保留原值（COALESCE），不得被清空。
	if reauthorized.PlanType.String != "plus" {
		t.Fatalf("plan_type overwritten with NULL: %v", reauthorized.PlanType)
	}
}

// 账号聚合是「池空 ≠ 熔断」在管理面的数据来源，必须分别数清各状态与临期账号。
func TestAdminCountAccountsByChannel(t *testing.T) {
	ctx, tx, queries, cleanup := newModelChannelTestTx(t)
	defer cleanup()

	providerID := insertProvider(t, ctx, tx, "codex-provider", "enabled")
	channelID := insertPoolChannel(t, ctx, tx, providerID, "codex-pool", nil)

	enabled := createAccount(t, ctx, queries, channelID, "acct-c1", 10)
	expiring := createAccount(t, ctx, queries, channelID, "acct-c2", 20)
	archived := createAccount(t, ctx, queries, channelID, "acct-c3", 30)
	enableAccount(t, ctx, queries, enabled.ID)
	setStatus(t, ctx, queries, archived.ID, "archived")

	if _, err := tx.Exec(ctx, `UPDATE subscription_accounts SET subscription_expires_at = now() + interval '2 days' WHERE id = $1`, expiring.ID); err != nil {
		t.Fatalf("set expiry: %v", err)
	}

	counts, err := queries.AdminCountAccountsByChannel(ctx, channelID)
	if err != nil {
		t.Fatalf("count accounts: %v", err)
	}
	if counts.Total != 3 || counts.Enabled != 1 || counts.Disabled != 1 || counts.Archived != 1 {
		t.Fatalf("counts = %+v, want total=3 enabled=1 disabled=1 archived=1", counts)
	}
	if counts.ExpiringSoon != 1 {
		t.Fatalf("expiring_soon = %d, want 1", counts.ExpiringSoon)
	}
}

// 出站凭据单独取用，不随候选快照下发；归档账号仍可读到（用于审计与错误说明）。
func TestGetAccountOutboundCredential(t *testing.T) {
	ctx, tx, queries, cleanup := newModelChannelTestTx(t)
	defer cleanup()

	providerID := insertProvider(t, ctx, tx, "codex-provider", "enabled")
	channelID := insertPoolChannel(t, ctx, tx, providerID, "codex-pool", nil)
	account := createAccount(t, ctx, queries, channelID, "acct-cred", 50)

	got, err := queries.GetAccountOutboundCredential(ctx, account.ID)
	if err != nil {
		t.Fatalf("get outbound credential: %v", err)
	}
	if got.UpstreamAccountID != "acct-cred" || len(got.Credentials) == 0 {
		t.Fatalf("outbound credential incomplete: %+v", got)
	}
}

// 订阅台账是离线摊销的唯一事实来源；账号被引用时不得删除，否则历史支出凭空消失。
func TestSubscriptionLedgerRetainedAgainstAccountDeletion(t *testing.T) {
	ctx, tx, queries, cleanup := newModelChannelTestTx(t)
	defer cleanup()

	providerID := insertProvider(t, ctx, tx, "codex-provider", "enabled")
	channelID := insertPoolChannel(t, ctx, tx, providerID, "codex-pool", nil)
	account := createAccount(t, ctx, queries, channelID, "acct-ledger", 50)

	now := time.Now().UTC()
	if _, err := queries.AdminCreateSubscriptionLedgerEntry(ctx, sqlc.AdminCreateSubscriptionLedgerEntryParams{
		AccountID:   account.ID,
		Amount:      numeric(20),
		Currency:    "USD",
		PeriodStart: timestamptz(now),
		PeriodEnd:   timestamptz(now.Add(30 * 24 * time.Hour)),
		Note:        pgtype.Text{String: "2026-09", Valid: true},
	}); err != nil {
		t.Fatalf("create ledger entry: %v", err)
	}

	entries, err := queries.AdminListSubscriptionLedger(ctx, account.ID)
	if err != nil {
		t.Fatalf("list ledger: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("ledger entries = %d, want 1", len(entries))
	}

	if _, err := tx.Exec(ctx, `DELETE FROM subscription_accounts WHERE id = $1`, account.ID); err == nil {
		t.Fatal("deleting an account referenced by the ledger must be rejected")
	}
}

func enableAccount(t *testing.T, ctx context.Context, q *sqlc.Queries, id int64) {
	t.Helper()
	setStatus(t, ctx, q, id, "enabled")
}

func setStatus(t *testing.T, ctx context.Context, q *sqlc.Queries, id int64, status string) {
	t.Helper()
	if _, err := q.AdminSetSubscriptionAccountStatus(ctx, sqlc.AdminSetSubscriptionAccountStatusParams{
		ID:     id,
		Status: status,
	}); err != nil {
		t.Fatalf("set status %s: %v", status, err)
	}
}
