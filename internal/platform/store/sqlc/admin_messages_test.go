package sqlc_test

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/ThankCat/unio-gateway/internal/platform/store/sqlc"
)

func createAdminMessageForTest(t *testing.T, ctx context.Context, queries *sqlc.Queries, dedupeKey string) int64 {
	t.Helper()
	key := pgtype.Text{}
	if dedupeKey != "" {
		key = pgtype.Text{String: dedupeKey, Valid: true}
	}
	rows, err := queries.CreateAdminMessage(ctx, sqlc.CreateAdminMessageParams{
		Severity:  "warning",
		Topic:     "system",
		Title:     "测试消息",
		Body:      "测试正文",
		Source:    "admin-messages-test",
		DedupeKey: key,
	})
	if err != nil {
		t.Fatalf("create admin message: %v", err)
	}
	return rows
}

// 未读去重：同 dedupe_key 存在未读消息时第二次写入被静默跳过；标记已读后同 key 可再次写入。
func TestAdminMessageDedupeSkipsUnreadDuplicate(t *testing.T) {
	ctx, _, queries, cleanup := newModelChannelTestTx(t)
	defer cleanup()

	if rows := createAdminMessageForTest(t, ctx, queries, "dedupe-a"); rows != 1 {
		t.Fatalf("first insert rows = %d, want 1", rows)
	}
	if rows := createAdminMessageForTest(t, ctx, queries, "dedupe-a"); rows != 0 {
		t.Fatalf("duplicate unread insert rows = %d, want 0 (skipped)", rows)
	}
	unread, err := queries.CountUnreadAdminMessages(ctx)
	if err != nil {
		t.Fatalf("count unread: %v", err)
	}
	if unread != 1 {
		t.Fatalf("unread count = %d, want 1", unread)
	}

	if _, err := queries.MarkAllAdminMessagesRead(ctx); err != nil {
		t.Fatalf("mark all read: %v", err)
	}
	if rows := createAdminMessageForTest(t, ctx, queries, "dedupe-a"); rows != 1 {
		t.Fatalf("insert after read rows = %d, want 1 (dedupe released)", rows)
	}
}

// 无 dedupe_key 的消息不去重，可重复写入。
func TestAdminMessageWithoutDedupeKeyAlwaysInserts(t *testing.T) {
	ctx, _, queries, cleanup := newModelChannelTestTx(t)
	defer cleanup()

	for i := 0; i < 2; i++ {
		if rows := createAdminMessageForTest(t, ctx, queries, ""); rows != 1 {
			t.Fatalf("insert #%d rows = %d, want 1", i+1, rows)
		}
	}
	unread, err := queries.CountUnreadAdminMessages(ctx)
	if err != nil {
		t.Fatalf("count unread: %v", err)
	}
	if unread != 2 {
		t.Fatalf("unread count = %d, want 2", unread)
	}
}

// 标记已读幂等：重复标记保留首次 read_at；不存在的 id 返回 ErrNoRows。
func TestAdminMessageMarkReadIdempotent(t *testing.T) {
	ctx, _, queries, cleanup := newModelChannelTestTx(t)
	defer cleanup()

	createAdminMessageForTest(t, ctx, queries, "")
	items, err := queries.ListAdminMessagesPage(ctx, sqlc.ListAdminMessagesPageParams{
		UnreadOnly: true, PageLimit: 10,
	})
	if err != nil || len(items) == 0 {
		t.Fatalf("list unread: err=%v len=%d", err, len(items))
	}
	id := items[0].ID

	first, err := queries.MarkAdminMessageRead(ctx, id)
	if err != nil {
		t.Fatalf("mark read: %v", err)
	}
	if !first.ReadAt.Valid {
		t.Fatal("read_at not set after mark read")
	}
	second, err := queries.MarkAdminMessageRead(ctx, id)
	if err != nil {
		t.Fatalf("mark read again: %v", err)
	}
	if !second.ReadAt.Time.Equal(first.ReadAt.Time) {
		t.Fatalf("repeat mark read changed read_at: %v -> %v", first.ReadAt.Time, second.ReadAt.Time)
	}

	if _, err := queries.MarkAdminMessageRead(ctx, id+999999); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("mark read missing id err = %v, want pgx.ErrNoRows", err)
	}
}

// 列表过滤与计数同口径：unread_only 与 topic 过滤生效，Count 与 List 一致。
func TestAdminMessagesListFilterMatchesCount(t *testing.T) {
	ctx, _, queries, cleanup := newModelChannelTestTx(t)
	defer cleanup()

	createAdminMessageForTest(t, ctx, queries, "")
	createAdminMessageForTest(t, ctx, queries, "")
	items, err := queries.ListAdminMessagesPage(ctx, sqlc.ListAdminMessagesPageParams{
		UnreadOnly: true, PageLimit: 10,
	})
	if err != nil {
		t.Fatalf("list unread: %v", err)
	}
	if _, err := queries.MarkAdminMessageRead(ctx, items[0].ID); err != nil {
		t.Fatalf("mark one read: %v", err)
	}

	unreadItems, err := queries.ListAdminMessagesPage(ctx, sqlc.ListAdminMessagesPageParams{
		UnreadOnly: true, PageLimit: 10,
	})
	if err != nil {
		t.Fatalf("list unread after read: %v", err)
	}
	unreadTotal, err := queries.CountAdminMessages(ctx, sqlc.CountAdminMessagesParams{UnreadOnly: true})
	if err != nil {
		t.Fatalf("count unread: %v", err)
	}
	if int64(len(unreadItems)) != unreadTotal || unreadTotal != 1 {
		t.Fatalf("unread list=%d count=%d, want both 1", len(unreadItems), unreadTotal)
	}

	topicTotal, err := queries.CountAdminMessages(ctx, sqlc.CountAdminMessagesParams{
		Topic: pgtype.Text{String: "no-such-topic", Valid: true},
	})
	if err != nil {
		t.Fatalf("count by topic: %v", err)
	}
	if topicTotal != 0 {
		t.Fatalf("count by missing topic = %d, want 0", topicTotal)
	}
}
