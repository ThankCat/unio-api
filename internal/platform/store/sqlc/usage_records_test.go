package sqlc_test

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/ThankCat/unio-api/internal/platform/store/sqlc"
	"github.com/jackc/pgx/v5/pgconn"
)

// isCheckViolation 判断数据库错误是否是 CHECK 约束冲突。
func isCheckViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23514"
}

func usageRecordParams(requestRecordID int64) sqlc.CreateUsageRecordParams {
	return sqlc.CreateUsageRecordParams{
		RequestRecordID:              requestRecordID,
		UncachedInputTokens:          88,
		UncachedInputTokensState:     "known",
		CacheReadInputTokens:         12,
		CacheReadInputTokensState:    "known",
		CacheWrite5mInputTokens:      0,
		CacheWrite5mInputTokensState: "not_applicable",
		CacheWrite1hInputTokens:      0,
		CacheWrite1hInputTokensState: "not_applicable",
		OutputTokensTotal:            40,
		OutputTokensTotalState:       "known",
		ReasoningOutputTokens:        8,
		ReasoningOutputTokensState:   "known",
		UsageSource:                  "upstream_response",
		UsageMappingVersion:          "openai_chat_usage_v1",
	}
}

func TestUsageRecordCreateGetAndLineItems(t *testing.T) {
	ctx, _, queries, cleanup := newModelChannelTestTx(t)
	defer cleanup()

	identity := createRequestRecordIdentity(t, ctx, queries)
	requestID := fmt.Sprintf("usage-record-%d", time.Now().UnixNano())
	requestRecord := createRequestRecordForTest(t, ctx, queries, identity, requestID)

	created, err := queries.CreateUsageRecord(ctx, usageRecordParams(requestRecord.ID))
	if err != nil {
		t.Fatalf("create usage record: %v", err)
	}
	if created.ID == 0 || created.RequestRecordID != requestRecord.ID {
		t.Fatalf("unexpected usage record: %#v", created)
	}
	if created.UncachedInputTokens != 88 || created.CacheReadInputTokens != 12 || created.OutputTokensTotal != 40 {
		t.Fatalf("unexpected neutral token usage: %#v", created)
	}

	lineItem, err := queries.CreateUsageLineItem(ctx, sqlc.CreateUsageLineItemParams{
		UsageRecordID: created.ID,
		Kind:          "server_web_search_request",
		Quantity:      2,
	})
	if err != nil {
		t.Fatalf("create usage line item: %v", err)
	}
	items, err := queries.ListUsageLineItemsByUsageRecord(ctx, created.ID)
	if err != nil {
		t.Fatalf("list usage line items: %v", err)
	}
	if len(items) != 1 || items[0].ID != lineItem.ID || items[0].Quantity != 2 {
		t.Fatalf("unexpected usage line items: %#v", items)
	}

	got, err := queries.GetUsageRecordByRequest(ctx, requestRecord.ID)
	if err != nil {
		t.Fatalf("get usage by request: %v", err)
	}
	if got.ID != created.ID || got.UsageMappingVersion != "openai_chat_usage_v1" {
		t.Fatalf("unexpected usage record readback: %#v", got)
	}
}

func TestUsageRecordRejectsDuplicateRequest(t *testing.T) {
	ctx, _, queries, cleanup := newModelChannelTestTx(t)
	defer cleanup()

	identity := createRequestRecordIdentity(t, ctx, queries)
	requestRecord := createRequestRecordForTest(t, ctx, queries, identity, fmt.Sprintf("usage-duplicate-%d", time.Now().UnixNano()))
	params := usageRecordParams(requestRecord.ID)

	if _, err := queries.CreateUsageRecord(ctx, params); err != nil {
		t.Fatalf("create first usage record: %v", err)
	}
	if _, err := queries.CreateUsageRecord(ctx, params); !isUniqueViolation(err) {
		t.Fatalf("expected unique violation, got %v", err)
	}
}

func TestUsageRecordRejectsInvalidFacts(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*sqlc.CreateUsageRecordParams)
	}{
		{
			name: "negative uncached input",
			mutate: func(params *sqlc.CreateUsageRecordParams) {
				params.UncachedInputTokens = -1
			},
		},
		{
			name: "non known value is nonzero",
			mutate: func(params *sqlc.CreateUsageRecordParams) {
				params.CacheWrite5mInputTokensState = "unknown"
				params.CacheWrite5mInputTokens = 1
			},
		},
		{
			name: "reasoning exceeds output",
			mutate: func(params *sqlc.CreateUsageRecordParams) {
				params.ReasoningOutputTokens = params.OutputTokensTotal + 1
			},
		},
		{
			name: "invalid source",
			mutate: func(params *sqlc.CreateUsageRecordParams) {
				params.UsageSource = "estimated"
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx, _, queries, cleanup := newModelChannelTestTx(t)
			defer cleanup()

			identity := createRequestRecordIdentity(t, ctx, queries)
			requestRecord := createRequestRecordForTest(t, ctx, queries, identity, fmt.Sprintf("usage-invalid-%s-%d", tc.name, time.Now().UnixNano()))
			params := usageRecordParams(requestRecord.ID)
			tc.mutate(&params)

			if _, err := queries.CreateUsageRecord(ctx, params); !isCheckViolation(err) {
				t.Fatalf("expected check violation, got %v", err)
			}
		})
	}
}

func TestUsageLineItemRejectsUnregisteredKind(t *testing.T) {
	ctx, _, queries, cleanup := newModelChannelTestTx(t)
	defer cleanup()

	identity := createRequestRecordIdentity(t, ctx, queries)
	requestRecord := createRequestRecordForTest(t, ctx, queries, identity, fmt.Sprintf("usage-line-item-kind-%d", time.Now().UnixNano()))
	record, err := queries.CreateUsageRecord(ctx, usageRecordParams(requestRecord.ID))
	if err != nil {
		t.Fatalf("create usage record: %v", err)
	}

	// 未登记 kind 必须 CHECK 违反；该违反会中止当前事务，因此独立用例验证，
	// 不与后续 insert 共享同一事务（否则 25P02 aborted）。
	if _, err := queries.CreateUsageLineItem(ctx, sqlc.CreateUsageLineItemParams{
		UsageRecordID: record.ID,
		Kind:          "provider_arbitrary_key",
		Quantity:      1,
	}); !isCheckViolation(err) {
		t.Fatalf("expected unregistered kind check violation, got %v", err)
	}
}

func TestUsageLineItemRejectsDuplicate(t *testing.T) {
	ctx, _, queries, cleanup := newModelChannelTestTx(t)
	defer cleanup()

	identity := createRequestRecordIdentity(t, ctx, queries)
	requestRecord := createRequestRecordForTest(t, ctx, queries, identity, fmt.Sprintf("usage-line-item-dup-%d", time.Now().UnixNano()))
	record, err := queries.CreateUsageRecord(ctx, usageRecordParams(requestRecord.ID))
	if err != nil {
		t.Fatalf("create usage record: %v", err)
	}

	params := sqlc.CreateUsageLineItemParams{
		UsageRecordID: record.ID,
		Kind:          "server_web_fetch_request",
		Quantity:      1,
	}
	if _, err := queries.CreateUsageLineItem(ctx, params); err != nil {
		t.Fatalf("create line item: %v", err)
	}
	if _, err := queries.CreateUsageLineItem(ctx, params); !isUniqueViolation(err) {
		t.Fatalf("expected duplicate kind unique violation, got %v", err)
	}
}
