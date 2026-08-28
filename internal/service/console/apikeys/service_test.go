package apikeys

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/ThankCat/unio-gateway/internal/core/apikey"
	"github.com/ThankCat/unio-gateway/internal/platform/store/sqlc"
)

const testUserID int64 = 42

var testNow = time.Date(2026, 8, 26, 6, 0, 0, 0, time.UTC)

func testWindow() Window {
	return Window{
		From: testNow.AddDate(0, 0, -30),
		To:   testNow.AddDate(0, 0, 1),
		TZ:   "Asia/Shanghai",
	}
}

// fakeStore 记录每次调用的参数，测试据此断言 user_id 是否被带到了 SQL 层。
type fakeStore struct {
	listArg      sqlc.ListConsoleAPIKeysParams
	countArg     sqlc.CountConsoleAPIKeysParams
	getArg       sqlc.GetConsoleAPIKeyParams
	dailyArg     sqlc.ListConsoleAPIKeyDailyChargeParams
	modelsArg    sqlc.ListConsoleAPIKeyTopModelsParams
	createArg    sqlc.CreateConsoleAPIKeyParams
	updateArg    sqlc.UpdateConsoleAPIKeyParams
	revokeArg    sqlc.RevokeConsoleAPIKeyParams
	deleteArg    sqlc.DeleteConsoleAPIKeyParams
	lifecycleArg sqlc.GetConsoleAPIKeyLifecycleParams
	summaryUID   int64
	windowArg    sqlc.SummarizeConsoleAPIKeyWindowParams

	getErr       error
	updateErr    error
	revokeErr    error
	deleteErr    error
	deleteRows   int64
	lifecycleRow sqlc.GetConsoleAPIKeyLifecycleRow
	lifecycleErr error
	dailyRows    []sqlc.ListConsoleAPIKeyDailyChargeRow
	listRows     []sqlc.ListConsoleAPIKeysRow
	createdRow   sqlc.CreateConsoleAPIKeyRow
	createdSet   bool
	summaryCount sqlc.SummarizeConsoleAPIKeysRow
}

func (s *fakeStore) ListConsoleAPIKeys(_ context.Context, arg sqlc.ListConsoleAPIKeysParams) ([]sqlc.ListConsoleAPIKeysRow, error) {
	s.listArg = arg
	return s.listRows, nil
}

func (s *fakeStore) CountConsoleAPIKeys(_ context.Context, arg sqlc.CountConsoleAPIKeysParams) (int64, error) {
	s.countArg = arg
	return int64(len(s.listRows)), nil
}

func (s *fakeStore) SummarizeConsoleAPIKeys(_ context.Context, userID int64) (sqlc.SummarizeConsoleAPIKeysRow, error) {
	s.summaryUID = userID
	return s.summaryCount, nil
}

func (s *fakeStore) SummarizeConsoleAPIKeyWindow(_ context.Context, arg sqlc.SummarizeConsoleAPIKeyWindowParams) (sqlc.SummarizeConsoleAPIKeyWindowRow, error) {
	s.windowArg = arg
	return sqlc.SummarizeConsoleAPIKeyWindowRow{RequestCount: 9}, nil
}

func (s *fakeStore) GetConsoleAPIKey(_ context.Context, arg sqlc.GetConsoleAPIKeyParams) (sqlc.GetConsoleAPIKeyRow, error) {
	s.getArg = arg
	if s.getErr != nil {
		return sqlc.GetConsoleAPIKeyRow{}, s.getErr
	}
	return sqlc.GetConsoleAPIKeyRow{
		ID:        arg.ID,
		Name:      "prod",
		KeyPrefix: "sk-unio-a3f9k2m1",
		CreatedAt: pgtype.Timestamptz{Time: testNow, Valid: true},
		UpdatedAt: pgtype.Timestamptz{Time: testNow, Valid: true},
	}, nil
}

func (s *fakeStore) ListConsoleAPIKeyDailyCharge(_ context.Context, arg sqlc.ListConsoleAPIKeyDailyChargeParams) ([]sqlc.ListConsoleAPIKeyDailyChargeRow, error) {
	s.dailyArg = arg
	return s.dailyRows, nil
}

func (s *fakeStore) ListConsoleAPIKeyTopModels(_ context.Context, arg sqlc.ListConsoleAPIKeyTopModelsParams) ([]sqlc.ListConsoleAPIKeyTopModelsRow, error) {
	s.modelsArg = arg
	return nil, nil
}

func (s *fakeStore) CreateConsoleAPIKey(_ context.Context, arg sqlc.CreateConsoleAPIKeyParams) (sqlc.CreateConsoleAPIKeyRow, error) {
	s.createArg = arg
	if s.createdSet {
		return s.createdRow, nil
	}
	return sqlc.CreateConsoleAPIKeyRow{
		ID:         11,
		Name:       arg.Name,
		KeyPrefix:  arg.KeyPrefix,
		ExpiresAt:  arg.ExpiresAt,
		SpendLimit: arg.SpendLimit,
		CreatedAt:  pgtype.Timestamptz{Time: testNow, Valid: true},
		UpdatedAt:  pgtype.Timestamptz{Time: testNow, Valid: true},
	}, nil
}

func (s *fakeStore) UpdateConsoleAPIKey(_ context.Context, arg sqlc.UpdateConsoleAPIKeyParams) (sqlc.UpdateConsoleAPIKeyRow, error) {
	s.updateArg = arg
	if s.updateErr != nil {
		return sqlc.UpdateConsoleAPIKeyRow{}, s.updateErr
	}
	return sqlc.UpdateConsoleAPIKeyRow{
		ID:         arg.ID,
		Name:       arg.Name,
		KeyPrefix:  "sk-unio-a3f9k2m1",
		SpendLimit: arg.SpendLimit,
		ExpiresAt:  arg.ExpiresAt,
		DisabledAt: arg.DisabledAt,
		CreatedAt:  pgtype.Timestamptz{Time: testNow, Valid: true},
		UpdatedAt:  pgtype.Timestamptz{Time: testNow, Valid: true},
	}, nil
}

func (s *fakeStore) RevokeConsoleAPIKey(_ context.Context, arg sqlc.RevokeConsoleAPIKeyParams) (sqlc.RevokeConsoleAPIKeyRow, error) {
	s.revokeArg = arg
	if s.revokeErr != nil {
		return sqlc.RevokeConsoleAPIKeyRow{}, s.revokeErr
	}
	return sqlc.RevokeConsoleAPIKeyRow{
		ID:        arg.ID,
		Name:      "prod",
		KeyPrefix: "sk-unio-a3f9k2m1",
		RevokedAt: pgtype.Timestamptz{Time: testNow, Valid: true},
		CreatedAt: pgtype.Timestamptz{Time: testNow, Valid: true},
		UpdatedAt: pgtype.Timestamptz{Time: testNow, Valid: true},
	}, nil
}

func (s *fakeStore) DeleteConsoleAPIKey(_ context.Context, arg sqlc.DeleteConsoleAPIKeyParams) (int64, error) {
	s.deleteArg = arg
	return s.deleteRows, s.deleteErr
}

func (s *fakeStore) GetConsoleAPIKeyLifecycle(_ context.Context, arg sqlc.GetConsoleAPIKeyLifecycleParams) (sqlc.GetConsoleAPIKeyLifecycleRow, error) {
	s.lifecycleArg = arg
	return s.lifecycleRow, s.lifecycleErr
}

func newTestService(store *fakeStore) *Service {
	svc := NewService(store)
	svc.now = func() time.Time { return testNow }
	return svc
}

// 归属是这个包的安全底线：每一条读写都必须把会话的 user_id 带进 SQL。
// 漏掉任何一处，用户就能操作别人的密钥。
func TestEveryQueryScopesToUser(t *testing.T) {
	store := &fakeStore{deleteRows: 1}
	svc := newTestService(store)
	ctx := context.Background()

	if _, _, err := svc.List(ctx, ListParams{UserID: testUserID, Window: testWindow()}); err != nil {
		t.Fatalf("list: %v", err)
	}
	if store.listArg.UserID != testUserID || store.countArg.UserID != testUserID {
		t.Fatalf("list scoping: list=%d count=%d", store.listArg.UserID, store.countArg.UserID)
	}
	if store.dailyArg.UserID != testUserID {
		t.Fatalf("daily charge scoping: %d", store.dailyArg.UserID)
	}

	if _, err := svc.Get(ctx, GetParams{UserID: testUserID, KeyID: 7, Window: testWindow()}); err != nil {
		t.Fatalf("get: %v", err)
	}
	if store.getArg.UserID != testUserID || store.getArg.ID != 7 {
		t.Fatalf("get scoping: %+v", store.getArg)
	}
	if store.modelsArg.UserID != testUserID || store.modelsArg.ApiKeyID != 7 {
		t.Fatalf("top models scoping: %+v", store.modelsArg)
	}

	if _, err := svc.Create(ctx, CreateParams{UserID: testUserID, Name: "ci"}); err != nil {
		t.Fatalf("create: %v", err)
	}
	if store.createArg.UserID != testUserID {
		t.Fatalf("create scoping: %d", store.createArg.UserID)
	}

	name := "renamed"
	if _, err := svc.Update(ctx, UpdateParams{
		UserID: testUserID, KeyID: 7, Name: &name, NameProvided: true,
	}); err != nil {
		t.Fatalf("update: %v", err)
	}
	if store.updateArg.UserID != testUserID || store.updateArg.ID != 7 {
		t.Fatalf("update scoping: %+v", store.updateArg)
	}

	if _, err := svc.Revoke(ctx, testUserID, 7); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	if store.revokeArg.UserID != testUserID || store.revokeArg.ID != 7 {
		t.Fatalf("revoke scoping: %+v", store.revokeArg)
	}

	if err := svc.Delete(ctx, testUserID, 7); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if store.deleteArg.UserID != testUserID || store.deleteArg.ID != 7 {
		t.Fatalf("delete scoping: %+v", store.deleteArg)
	}

	if _, err := svc.Summary(ctx, testUserID, testWindow()); err != nil {
		t.Fatalf("summary: %v", err)
	}
	if store.summaryUID != testUserID || store.windowArg.UserID != testUserID {
		t.Fatalf("summary scoping: counts=%d window=%d", store.summaryUID, store.windowArg.UserID)
	}
}

// 创建时明文只出现在返回值里，落库参数只有 prefix 和 hash。
func TestCreateDoesNotPersistPlaintext(t *testing.T) {
	store := &fakeStore{}
	svc := newTestService(store)

	created, err := svc.Create(context.Background(), CreateParams{UserID: testUserID, Name: "ci"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if created.Plaintext == "" {
		t.Fatal("expected the one-time plaintext to be returned")
	}
	if !strings.HasPrefix(created.Plaintext, "sk-unio-") || len(created.Plaintext) != apikey.MaxPlaintextLen {
		t.Fatalf("plaintext = %q", created.Plaintext)
	}
	// 落库参数里不能有任何等于明文的字段。
	if store.createArg.KeyPrefix == created.Plaintext || store.createArg.KeyHash == created.Plaintext {
		t.Fatalf("plaintext leaked into store params: %+v", store.createArg)
	}
	if !strings.HasPrefix(created.Plaintext, store.createArg.KeyPrefix) {
		t.Fatalf("prefix %q is not a prefix of %q", store.createArg.KeyPrefix, created.Plaintext)
	}
	if len(store.createArg.KeyHash) != 64 {
		t.Fatalf("key hash should be a sha-256 hex digest, got %q", store.createArg.KeyHash)
	}
}

// 别人的密钥、不存在的密钥、已吊销的密钥都返回同一个 not_found：
// 区分开就等于确认了那把密钥的存在。
func TestMissingKeyLooksTheSameAsSomeoneElsesKey(t *testing.T) {
	svc := newTestService(&fakeStore{getErr: pgx.ErrNoRows})
	_, err := svc.Get(context.Background(), GetParams{UserID: testUserID, KeyID: 7, Window: testWindow()})
	if err == nil || err.Status != 404 || err.Code != "api_key_not_found" {
		t.Fatalf("get = %+v", err)
	}

	updateSvc := newTestService(&fakeStore{updateErr: pgx.ErrNoRows})
	name := "x"
	_, err = updateSvc.Update(context.Background(), UpdateParams{
		UserID: testUserID, KeyID: 7, Name: &name, NameProvided: true,
	})
	if err == nil || err.Status != 404 {
		t.Fatalf("update = %+v", err)
	}

	revokeSvc := newTestService(&fakeStore{revokeErr: pgx.ErrNoRows})
	_, err = revokeSvc.Revoke(context.Background(), testUserID, 7)
	if err == nil || err.Status != 404 {
		t.Fatalf("revoke = %+v", err)
	}
}

func TestUpdateRequiresAtLeastOneField(t *testing.T) {
	svc := newTestService(&fakeStore{})
	_, err := svc.Update(context.Background(), UpdateParams{UserID: testUserID, KeyID: 7})
	if err == nil || err.Status != 400 || err.Param != "body" {
		t.Fatalf("err = %+v", err)
	}
}

// Provided 标志决定字段是否参与更新，narg 为空表示清空。
// 这两件事在 SQL 里由 *_provided 布尔控制，服务层必须如实转达。
func TestUpdateForwardsProvidedFlags(t *testing.T) {
	store := &fakeStore{}
	svc := newTestService(store)

	if _, err := svc.Update(context.Background(), UpdateParams{
		UserID: testUserID, KeyID: 7,
		SpendLimitProvided: true, SpendLimit: nil,
	}); err != nil {
		t.Fatalf("clear limit: %v", err)
	}
	if !store.updateArg.SpendLimitProvided || store.updateArg.SpendLimit.Valid {
		t.Fatalf("clearing the limit should pass SQL NULL: %+v", store.updateArg)
	}
	if store.updateArg.NameProvided || store.updateArg.ExpiresProvided || store.updateArg.DisabledProvided {
		t.Fatalf("untouched fields must stay unprovided: %+v", store.updateArg)
	}

	disabled := true
	if _, err := svc.Update(context.Background(), UpdateParams{
		UserID: testUserID, KeyID: 7,
		DisabledProvided: true, Disabled: &disabled,
	}); err != nil {
		t.Fatalf("disable: %v", err)
	}
	if !store.updateArg.DisabledProvided || !store.updateArg.DisabledAt.Valid {
		t.Fatalf("disabling should stamp disabled_at: %+v", store.updateArg)
	}

	enabled := false
	if _, err := svc.Update(context.Background(), UpdateParams{
		UserID: testUserID, KeyID: 7,
		DisabledProvided: true, Disabled: &enabled,
	}); err != nil {
		t.Fatalf("enable: %v", err)
	}
	if !store.updateArg.DisabledProvided || store.updateArg.DisabledAt.Valid {
		t.Fatalf("enabling should clear disabled_at: %+v", store.updateArg)
	}
}

func TestStatusDerivation(t *testing.T) {
	svc := newTestService(&fakeStore{})
	stamp := pgtype.Timestamptz{Time: testNow.Add(-time.Hour), Valid: true}
	future := pgtype.Timestamptz{Time: testNow.Add(time.Hour), Valid: true}
	none := pgtype.Timestamptz{}

	if got := svc.deriveStatus(stamp, stamp, none); got != StatusRevoked {
		t.Fatalf("revoked wins over disabled, got %q", got)
	}
	if got := svc.deriveStatus(stamp, none, none); got != StatusDisabled {
		t.Fatalf("disabled = %q", got)
	}
	if got := svc.deriveStatus(none, none, stamp); got != StatusExpired {
		t.Fatalf("expired = %q", got)
	}
	if got := svc.deriveStatus(none, none, future); got != StatusActive {
		t.Fatalf("future expiry is still active, got %q", got)
	}
	if got := svc.deriveStatus(none, none, none); got != StatusActive {
		t.Fatalf("active = %q", got)
	}
}

func TestCreateRejectsBadInput(t *testing.T) {
	svc := newTestService(&fakeStore{})
	ctx := context.Background()
	past := testNow.Add(-time.Hour)
	negative := "-1"
	zero := "0"
	garbage := "1e5"

	for _, tc := range []struct {
		name   string
		params CreateParams
		param  string
	}{
		{"empty name", CreateParams{UserID: testUserID, Name: "   "}, "name"},
		{"long name", CreateParams{UserID: testUserID, Name: strings.Repeat("x", maxNameLen+1)}, "name"},
		{"past expiry", CreateParams{UserID: testUserID, Name: "ci", ExpiresAt: &past}, "expires_at"},
		{"negative limit", CreateParams{UserID: testUserID, Name: "ci", SpendLimit: &negative}, "spend_limit"},
		{"zero limit", CreateParams{UserID: testUserID, Name: "ci", SpendLimit: &zero}, "spend_limit"},
		{"scientific notation", CreateParams{UserID: testUserID, Name: "ci", SpendLimit: &garbage}, "spend_limit"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := svc.Create(ctx, tc.params)
			if err == nil || err.Status != 400 || err.Param != tc.param {
				t.Fatalf("err = %+v", err)
			}
		})
	}
}

// 空额度上限表示不限额，走 SQL NULL 而不是 0。
func TestCreateTreatsBlankLimitAsUnlimited(t *testing.T) {
	store := &fakeStore{}
	svc := newTestService(store)
	blank := "   "

	if _, err := svc.Create(context.Background(), CreateParams{
		UserID: testUserID, Name: "ci", SpendLimit: &blank,
	}); err != nil {
		t.Fatalf("create: %v", err)
	}
	if store.createArg.SpendLimit.Valid {
		t.Fatalf("blank limit should become SQL NULL: %+v", store.createArg.SpendLimit)
	}
}

func TestWindowValidation(t *testing.T) {
	svc := newTestService(&fakeStore{})
	ctx := context.Background()

	for _, tc := range []struct {
		name   string
		window Window
		param  string
	}{
		{"missing from", Window{To: testNow}, "from"},
		{"missing to", Window{From: testNow}, "from"},
		{"inverted", Window{From: testNow, To: testNow.Add(-time.Hour)}, "to"},
		{"bad tz", Window{From: testNow.Add(-time.Hour), To: testNow, TZ: "Mars/Olympus"}, "tz"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := svc.List(ctx, ListParams{UserID: testUserID, Window: tc.window})
			if err == nil || err.Status != 400 || err.Param != tc.param {
				t.Fatalf("err = %+v", err)
			}
		})
	}
}

// 时区要原样传给 SQL：分桶边界错一天，走势图就会整体偏移。
func TestListForwardsTimeZoneAndDefaultsToUTC(t *testing.T) {
	store := &fakeStore{}
	svc := newTestService(store)
	ctx := context.Background()

	if _, _, err := svc.List(ctx, ListParams{UserID: testUserID, Window: testWindow()}); err != nil {
		t.Fatalf("list: %v", err)
	}
	if store.dailyArg.Tz != "Asia/Shanghai" {
		t.Fatalf("tz = %q", store.dailyArg.Tz)
	}

	naked := testWindow()
	naked.TZ = ""
	if _, _, err := svc.List(ctx, ListParams{UserID: testUserID, Window: naked}); err != nil {
		t.Fatalf("list: %v", err)
	}
	if store.dailyArg.Tz != "UTC" {
		t.Fatalf("default tz = %q, want UTC", store.dailyArg.Tz)
	}
}

// 走势一次查完再按 key 分派：列表里每把密钥单独查一次会变成 N+1。
func TestListFetchesTrendsInOneQuery(t *testing.T) {
	store := &fakeStore{
		listRows: []sqlc.ListConsoleAPIKeysRow{
			{ID: 1, Name: "a", KeyPrefix: "sk-unio-aaaaaaaa"},
			{ID: 2, Name: "b", KeyPrefix: "sk-unio-bbbbbbbb"},
		},
		dailyRows: []sqlc.ListConsoleAPIKeyDailyChargeRow{
			{ApiKeyID: 1, BucketStart: pgtype.Timestamptz{Time: testNow, Valid: true}, RequestCount: 3},
			{ApiKeyID: 2, BucketStart: pgtype.Timestamptz{Time: testNow, Valid: true}, RequestCount: 5},
			{ApiKeyID: 2, BucketStart: pgtype.Timestamptz{Time: testNow.AddDate(0, 0, -1), Valid: true}, RequestCount: 7},
		},
	}
	svc := newTestService(store)

	keys, total, err := svc.List(context.Background(), ListParams{UserID: testUserID, Window: testWindow()})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if total != 2 || len(keys) != 2 {
		t.Fatalf("keys = %d total = %d", len(keys), total)
	}
	// 不带 api_key_id 才能一次拿全所有密钥的分桶。
	if store.dailyArg.ApiKeyID.Valid {
		t.Fatalf("list should not scope the trend query to one key: %+v", store.dailyArg)
	}
	if len(keys[0].Trend) != 1 || len(keys[1].Trend) != 2 {
		t.Fatalf("trend distribution: %d / %d", len(keys[0].Trend), len(keys[1].Trend))
	}
}

// 详情只要这一把的走势，必须把 api_key_id 传下去。
func TestGetScopesTrendToOneKey(t *testing.T) {
	store := &fakeStore{}
	svc := newTestService(store)

	if _, err := svc.Get(context.Background(), GetParams{
		UserID: testUserID, KeyID: 7, Window: testWindow(),
	}); err != nil {
		t.Fatalf("get: %v", err)
	}
	if !store.dailyArg.ApiKeyID.Valid || store.dailyArg.ApiKeyID.Int64 != 7 {
		t.Fatalf("detail trend should be scoped to key 7: %+v", store.dailyArg)
	}
}

// 删除是软删除，且只对已吊销的密钥开放：未吊销时要指引用户先吊销，而不是报不存在。
func TestDeleteRequiresRevokedKey(t *testing.T) {
	store := &fakeStore{
		deleteRows:   0,
		lifecycleRow: sqlc.GetConsoleAPIKeyLifecycleRow{},
	}
	svc := newTestService(store)
	err := svc.Delete(context.Background(), testUserID, 7)
	if err == nil || err.Status != 409 || err.Code != "api_key_not_revoked" {
		t.Fatalf("err = %+v", err)
	}
	if store.lifecycleArg.UserID != testUserID || store.lifecycleArg.ID != 7 {
		t.Fatalf("lifecycle scoping: %+v", store.lifecycleArg)
	}
}

func TestDeleteMissingKeyIsNotFound(t *testing.T) {
	svc := newTestService(&fakeStore{deleteRows: 0, lifecycleErr: pgx.ErrNoRows})
	err := svc.Delete(context.Background(), testUserID, 7)
	if err == nil || err.Status != 404 {
		t.Fatalf("err = %+v", err)
	}
}

// 已经软删除的密钥再删一次按不存在处理，不能泄露它曾经存在。
func TestDeleteAlreadyDeletedIsNotFound(t *testing.T) {
	svc := newTestService(&fakeStore{
		deleteRows: 0,
		lifecycleRow: sqlc.GetConsoleAPIKeyLifecycleRow{
			RevokedAt: pgtype.Timestamptz{Time: testNow, Valid: true},
			DeletedAt: pgtype.Timestamptz{Time: testNow, Valid: true},
		},
	})
	err := svc.Delete(context.Background(), testUserID, 7)
	if err == nil || err.Status != 404 {
		t.Fatalf("err = %+v", err)
	}
}

func TestListRejectsUnknownStatusFilter(t *testing.T) {
	svc := newTestService(&fakeStore{})
	_, _, err := svc.List(context.Background(), ListParams{
		UserID: testUserID, Window: testWindow(), Status: "nonsense",
	})
	if err == nil || err.Param != "status" {
		t.Fatalf("err = %+v", err)
	}
}

func TestListClampsPageSize(t *testing.T) {
	store := &fakeStore{}
	svc := newTestService(store)
	ctx := context.Background()

	if _, _, err := svc.List(ctx, ListParams{UserID: testUserID, Window: testWindow(), Limit: 0}); err != nil {
		t.Fatalf("list: %v", err)
	}
	if store.listArg.PageLimit != defaultPageSize {
		t.Fatalf("limit = %d, want default %d", store.listArg.PageLimit, defaultPageSize)
	}

	if _, _, err := svc.List(ctx, ListParams{
		UserID: testUserID, Window: testWindow(), Limit: maxPageSize + 500,
	}); err != nil {
		t.Fatalf("list: %v", err)
	}
	if store.listArg.PageLimit != maxPageSize {
		t.Fatalf("limit = %d, want cap %d", store.listArg.PageLimit, maxPageSize)
	}
}
