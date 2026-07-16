package lifecycle

import (
	"context"
	"math/big"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/ThankCat/unio-gateway/internal/core/adapter"
	"github.com/ThankCat/unio-gateway/internal/core/billing"
	"github.com/ThankCat/unio-gateway/internal/core/requestlog"
	"github.com/ThankCat/unio-gateway/internal/core/routing"
	"github.com/ThankCat/unio-gateway/internal/platform/failure"
)

type fakeCostExposureRecorder struct {
	calls  []CostExposureParams
	failed bool
}

func (f *fakeCostExposureRecorder) RecordChannelCostExposure(_ context.Context, params CostExposureParams) error {
	f.calls = append(f.calls, params)
	return nil
}

// billsOnDisconnectCandidate 构造带成本价快照的 bill-on-disconnect 候选。
func billsOnDisconnectCandidate(channelID int64, maxOutputTokens int64) routing.ChatRouteCandidate {
	c := candidateRoute(channelID, "anthropic")
	c.ProviderID = 7
	c.BillsOnDisconnect = true
	c.MaxOutputTokens = maxOutputTokens
	c.ChannelCost = billing.ProviderCostSnapshot{
		Currency:    "USD",
		PricingUnit: billing.PricingUnitPer1MTokens,
		// 2 USD / 1M uncached input；4 USD / 1M output；其余 0。
		UncachedInputCost:     gatewayTestNumeric(2, 0),
		CacheReadInputCost:    gatewayTestNumeric(0, 0),
		CacheWrite5mInputCost: gatewayTestNumeric(0, 0),
		CacheWrite1hInputCost: gatewayTestNumeric(0, 0),
		OutputCost:            gatewayTestNumeric(4, 0),
		ReasoningOutputCost:   gatewayTestNumeric(0, 0),
	}
	return c
}

func upstreamErrOf(category adapter.UpstreamErrorCategory) error {
	return adapter.NewUpstreamError(category, adapter.UpstreamMetadata{}, failure.New(failure.CodeAdapterSendRequestFailed))
}

// TestCostExposureReasonClassification 验证敞口成因映射：仅取消/超时/5xx 触发。
func TestCostExposureReasonClassification(t *testing.T) {
	if reason, ok := costExposureReason(context.Canceled); !ok || reason != CostExposureReasonClientCanceled {
		t.Fatalf("context.Canceled: reason=%q ok=%v", reason, ok)
	}
	if reason, ok := costExposureReason(upstreamErrOf(adapter.UpstreamErrorCanceled)); !ok || reason != CostExposureReasonClientCanceled {
		t.Fatalf("canceled category: reason=%q ok=%v", reason, ok)
	}
	if reason, ok := costExposureReason(upstreamErrOf(adapter.UpstreamErrorTimeout)); !ok || reason != CostExposureReasonUpstreamTimeout {
		t.Fatalf("timeout: reason=%q ok=%v", reason, ok)
	}
	if reason, ok := costExposureReason(upstreamErrOf(adapter.UpstreamErrorServer)); !ok || reason != CostExposureReasonUpstreamError {
		t.Fatalf("server: reason=%q ok=%v", reason, ok)
	}
	for _, category := range []adapter.UpstreamErrorCategory{
		adapter.UpstreamErrorRateLimit,
		adapter.UpstreamErrorAuth,
		adapter.UpstreamErrorPermission,
		adapter.UpstreamErrorBadRequest,
	} {
		if _, ok := costExposureReason(upstreamErrOf(category)); ok {
			t.Fatalf("category %v should not create exposure", category)
		}
	}
	if _, ok := costExposureReason(failure.New(failure.CodeAdapterSendRequestFailed)); ok {
		t.Fatal("error without upstream category should not create exposure")
	}
}

// TestRecordCostExposureWritesUpperBoundEstimate 验证敞口写入：金额=输入×成本价+假定输出×成本价（上界口径）。
func TestRecordCostExposureWritesUpperBoundEstimate(t *testing.T) {
	recorder := &fakeCostExposureRecorder{}
	lc := &RequestLifecycle{}
	lc.SetCostExposureRecorder(recorder, 4096)

	// 500_000 uncached input @2/1M = 1；100_000 assumed output @4/1M = 0.4 → 1.4 USD。
	candidate := billsOnDisconnectCandidate(42, 100_000)
	lc.RecordCostExposure(
		context.Background(),
		requestlog.RequestRecord{ID: 11},
		requestlog.AttemptRecord{ID: 22},
		candidate,
		500_000,
		upstreamErrOf(adapter.UpstreamErrorTimeout),
	)

	if len(recorder.calls) != 1 {
		t.Fatalf("expected 1 exposure, got %d", len(recorder.calls))
	}
	got := recorder.calls[0]
	if got.RequestRecordID != 11 || got.AttemptID != 22 || got.ChannelID != 42 || got.ProviderID != 7 {
		t.Fatalf("unexpected ids: %+v", got)
	}
	if got.Reason != CostExposureReasonUpstreamTimeout {
		t.Fatalf("reason = %q, want upstream_timeout", got.Reason)
	}
	if got.EstimatedInputTokens != 500_000 || got.AssumedOutputTokens != 100_000 {
		t.Fatalf("tokens = %d/%d, want 500000/100000", got.EstimatedInputTokens, got.AssumedOutputTokens)
	}
	if got.Currency != "USD" {
		t.Fatalf("currency = %q, want USD", got.Currency)
	}
	assertNumericEquals(t, got.EstimatedCostAmount, "1.4")
}

// TestRecordCostExposureRespectsGates 验证三道闸：未注入 recorder / 非 bill-on-disconnect 渠道 /
// 非敞口错误分类，均不写入；模型无 max_output_tokens 时回退进程级兜底。
func TestRecordCostExposureRespectsGates(t *testing.T) {
	recorder := &fakeCostExposureRecorder{}
	lc := &RequestLifecycle{}
	lc.SetCostExposureRecorder(recorder, 4096)

	// 非 bill-on-disconnect 渠道：不写。
	normal := billsOnDisconnectCandidate(1, 0)
	normal.BillsOnDisconnect = false
	lc.RecordCostExposure(context.Background(), requestlog.RequestRecord{ID: 1}, requestlog.AttemptRecord{ID: 1}, normal, 100, upstreamErrOf(adapter.UpstreamErrorTimeout))
	if len(recorder.calls) != 0 {
		t.Fatal("non bill-on-disconnect channel must not record exposure")
	}

	// 非敞口分类（429）：不写。
	flagged := billsOnDisconnectCandidate(2, 0)
	lc.RecordCostExposure(context.Background(), requestlog.RequestRecord{ID: 1}, requestlog.AttemptRecord{ID: 1}, flagged, 100, upstreamErrOf(adapter.UpstreamErrorRateLimit))
	if len(recorder.calls) != 0 {
		t.Fatal("rate limit error must not record exposure")
	}

	// max_output_tokens 未配置：回退进程级兜底 4096。
	lc.RecordCostExposure(context.Background(), requestlog.RequestRecord{ID: 1}, requestlog.AttemptRecord{ID: 1}, flagged, 100, upstreamErrOf(adapter.UpstreamErrorServer))
	if len(recorder.calls) != 1 {
		t.Fatalf("expected 1 exposure, got %d", len(recorder.calls))
	}
	if got := recorder.calls[0].AssumedOutputTokens; got != 4096 {
		t.Fatalf("assumed output = %d, want fallback 4096", got)
	}

	// 未注入 recorder：安全 no-op。
	bare := &RequestLifecycle{}
	bare.RecordCostExposure(context.Background(), requestlog.RequestRecord{}, requestlog.AttemptRecord{}, flagged, 100, upstreamErrOf(adapter.UpstreamErrorServer))
}

// assertNumericEquals 断言 pgtype.Numeric 与十进制字符串数值相等（用 big.Rat 比较，避免标度差异误报）。
func assertNumericEquals(t *testing.T, got pgtype.Numeric, want string) {
	t.Helper()
	if !got.Valid {
		t.Fatalf("numeric is invalid, want %s", want)
	}
	gotRat := new(big.Rat).SetInt(got.Int)
	if got.Exp > 0 {
		scale := new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(got.Exp)), nil)
		gotRat.Mul(gotRat, new(big.Rat).SetInt(scale))
	} else if got.Exp < 0 {
		scale := new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(-got.Exp)), nil)
		gotRat.Quo(gotRat, new(big.Rat).SetInt(scale))
	}
	wantRat, ok := new(big.Rat).SetString(want)
	if !ok {
		t.Fatalf("bad want %q", want)
	}
	if gotRat.Cmp(wantRat) != 0 {
		t.Fatalf("numeric = %s, want %s", gotRat.RatString(), wantRat.RatString())
	}
}
