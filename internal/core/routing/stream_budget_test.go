package routing

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/ThankCat/unio-gateway/internal/platform/store/sqlc"
)

func timeoutRow(responseTimeout, firstTokenTimeout pgtype.Int4) sqlc.FindModelCandidatesRow {
	return sqlc.FindModelCandidatesRow{
		AdapterKey:          "openai",
		Protocol:            ProtocolOpenAI,
		ChannelID:           123,
		Origin:              "https://api.openai.example/v1",
		Credential:          "secret://openai/main",
		ResponseTimeoutMs:   responseTimeout,
		FirstTokenTimeoutMs: firstTokenTimeout,
		UpstreamModel:       "gpt-4.1",
		SaleDiscount:        testSaleDiscount(),
	}
}

func planSingle(t *testing.T, router *Router) ChatRouteCandidate {
	t.Helper()
	got, err := router.PlanChat(context.Background(), ChatRouteRequest{
		UserID: 42, ModelID: "openai/gpt-4.1", IngressProtocol: ProtocolOpenAI,
	})
	if err != nil {
		t.Fatalf("PlanChat returned error: %v", err)
	}
	if len(got.Candidates) != 1 {
		t.Fatalf("candidate count = %d, want 1", len(got.Candidates))
	}
	return got.Candidates[0]
}

// TestTimeoutsDefaultToUnlimited 冻结 2026-09-05 的默认值语义：两项上游超时未设置时都是 0（不限制），
// 没有按协议族分叉的默认，也没有按推理强度分档的默认。
func TestTimeoutsDefaultToUnlimited(t *testing.T) {
	router := NewRouter(&fakeStore{rows: []sqlc.FindModelCandidatesRow{timeoutRow(pgtype.Int4{}, pgtype.Int4{})}}, 0)
	c := planSingle(t, router)
	if c.Channel.ResponseTimeout != 0 || c.Channel.FirstTokenTimeout != 0 {
		t.Fatalf("both timeouts must default to unlimited, got %v / %v", c.Channel.ResponseTimeout, c.Channel.FirstTokenTimeout)
	}
}

// TestTimeoutsFollowHotReloadedDefaults 验证两项全局默认热改后的候选取值。
func TestTimeoutsFollowHotReloadedDefaults(t *testing.T) {
	router := NewRouter(&fakeStore{rows: []sqlc.FindModelCandidatesRow{timeoutRow(pgtype.Int4{}, pgtype.Int4{})}}, 0)
	router.SetDefaultResponseTimeout(90 * time.Second)
	router.SetDefaultFirstTokenTimeout(60 * time.Second)

	c := planSingle(t, router)
	if c.Channel.ResponseTimeout != 90*time.Second || c.Channel.FirstTokenTimeout != 60*time.Second {
		t.Fatalf("timeouts = %v / %v, want 90s / 60s", c.Channel.ResponseTimeout, c.Channel.FirstTokenTimeout)
	}

	router.SetDefaultFirstTokenTimeout(-time.Second)
	if c = planSingle(t, router); c.Channel.FirstTokenTimeout != 0 {
		t.Fatalf("negative default must be treated as unlimited, got %v", c.Channel.FirstTokenTimeout)
	}
}

// TestChannelRowExplicitZeroMeansUnlimited 冻结渠道行语义：显式 0 = 不限制并覆盖全局默认。
func TestChannelRowExplicitZeroMeansUnlimited(t *testing.T) {
	router := NewRouter(&fakeStore{rows: []sqlc.FindModelCandidatesRow{timeoutRow(
		pgtype.Int4{Int32: 0, Valid: true}, pgtype.Int4{Int32: 0, Valid: true})}}, 200*time.Second)
	router.SetDefaultFirstTokenTimeout(60 * time.Second)

	c := planSingle(t, router)
	if c.Channel.ResponseTimeout != 0 || c.Channel.FirstTokenTimeout != 0 {
		t.Fatalf("explicit 0 must be unlimited: %v / %v", c.Channel.ResponseTimeout, c.Channel.FirstTokenTimeout)
	}
}

// TestChannelRowPositiveOverridesDefault 验证渠道显式正数覆写全局默认；负数视为未配置按继承处理。
func TestChannelRowPositiveOverridesDefault(t *testing.T) {
	router := NewRouter(&fakeStore{rows: []sqlc.FindModelCandidatesRow{timeoutRow(
		pgtype.Int4{Int32: 600000, Valid: true}, pgtype.Int4{Int32: -1, Valid: true})}}, 0)
	router.SetDefaultFirstTokenTimeout(60 * time.Second)

	c := planSingle(t, router)
	if c.Channel.ResponseTimeout != 600*time.Second {
		t.Fatalf("explicit response timeout must win, got %v", c.Channel.ResponseTimeout)
	}
	if c.Channel.FirstTokenTimeout != 60*time.Second {
		t.Fatalf("negative channel value must inherit the default, got %v", c.Channel.FirstTokenTimeout)
	}
}
