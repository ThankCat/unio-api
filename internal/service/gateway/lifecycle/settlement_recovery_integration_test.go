package lifecycle

import (
	"fmt"
	"testing"
	"time"

	"github.com/ThankCat/unio-gateway/internal/core/billing"
	"github.com/ThankCat/unio-gateway/internal/core/ledger"
	"github.com/ThankCat/unio-gateway/internal/core/requestlog"
	coreusage "github.com/ThankCat/unio-gateway/internal/core/usage"
)

func TestSettlementRecoveryPreservesPartialFinalState(t *testing.T) {
	tests := []struct {
		name          string
		reason        string
		requestStatus requestlog.RequestStatus
		attemptStatus requestlog.AttemptStatus
		errorCode     string
		errorMessage  string
		errorDetail   string
	}{
		{
			name:          "client canceled",
			reason:        PartialReasonClientCanceled,
			requestStatus: requestlog.RequestStatusCanceled,
			attemptStatus: requestlog.AttemptStatusCanceled,
			errorCode:     "client_canceled",
			errorMessage:  "Request canceled.",
			errorDetail:   "context canceled",
		},
		{
			name:          "upstream interrupted",
			reason:        PartialReasonInterrupted,
			requestStatus: requestlog.RequestStatusFailed,
			attemptStatus: requestlog.AttemptStatusFailed,
			errorCode:     "stream_adapter_error",
			errorMessage:  "Upstream stream failed.",
			errorDetail:   "upstream stream interrupted",
		},
		{
			name:          "completed without final usage",
			reason:        PartialReasonFinalUsageMissing,
			requestStatus: requestlog.RequestStatusSucceeded,
			attemptStatus: requestlog.AttemptStatusSucceeded,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			d := newChatSettlementDBDeps(t)
			params := d.params()
			params.RequestFinalStatus = tc.requestStatus
			params.AttemptFinalStatus = tc.attemptStatus
			params.ErrorCode = tc.errorCode
			params.ErrorMessage = tc.errorMessage
			params.InternalErrorDetail = tc.errorDetail
			params.ResponseID = fmt.Sprintf("partial-recovery-%d", d.requestRecord.ID)
			params.Facts = chatSettlementFacts(coreusage.SourcePartialStreamEstimate)
			params.Facts.UpstreamResponseID = params.ResponseID
			params.Facts.Finish.RawReason = tc.reason
			params.Facts.UsageMappingVersion = "partial.v1"

			store := NewChatSettlementRecoveryStore(d.queries, time.Minute, 20)
			job, err := store.CreatePendingChatSettlementRecoveryJob(d.ctx, params)
			if err != nil {
				t.Fatalf("create partial recovery job: %v", err)
			}
			if job.RequestFinalStatus != string(tc.requestStatus) || job.AttemptFinalStatus != string(tc.attemptStatus) ||
				job.SettlementErrorCode != tc.errorCode || job.SettlementErrorMessage != tc.errorMessage ||
				job.SettlementInternalErrorDetail != tc.errorDetail {
				t.Fatalf("partial recovery facts mismatch: %+v", job)
			}

			settlement := NewChatSettlementService(d.pool, d.queries, chatSettlementBilling(testNumeric(61_000000, -10)), ledger.NewService(d.pool, d.queries))
			recovery := NewChatSettlementRecoveryService(d.queries, settlement)
			if err := recovery.RecoverChatSettlement(d.ctx, job); err != nil {
				t.Fatalf("recover partial settlement: %v", err)
			}
			// 模拟 settlement 已提交但 recovery job 完成标记失败：同一 job 再次重放必须按原终态幂等成功。
			if err := recovery.RecoverChatSettlement(d.ctx, job); err != nil {
				t.Fatalf("replay committed partial settlement: %v", err)
			}

			assertRecoveredPartialFacts(t, d, tc.requestStatus, tc.attemptStatus, tc.errorCode, tc.errorMessage, tc.errorDetail)
		})
	}
}

func TestSettlementRecoveryPreservesLongContextPolicyForAbsoluteCostOverride(t *testing.T) {
	d := newChatSettlementDBDeps(t)
	params := d.params()
	params.ChannelPriceID = d.channelPriceID
	params.LongContextPolicy = billing.LongContextPolicy{
		Enabled:          true,
		Threshold:        9,
		InputMultiplier:  testNumeric(2, 0),
		OutputMultiplier: testNumeric(3, 0),
	}

	store := NewChatSettlementRecoveryStore(d.queries, time.Minute, 20)
	job, err := store.CreatePendingChatSettlementRecoveryJob(d.ctx, params)
	if err != nil {
		t.Fatalf("create long-context recovery job: %v", err)
	}
	if !job.PriceID.Valid || job.PriceID.Int64 != d.channelPriceID || job.CostBaseModelPriceID.Valid {
		t.Fatalf("expected absolute cost override without model price pin: price=%#v model_price=%#v", job.PriceID, job.CostBaseModelPriceID)
	}
	if !job.LongContextEnabled || !job.LongContextThreshold.Valid || job.LongContextThreshold.Int64 != 9 {
		t.Fatalf("long-context policy was not persisted: %+v", job)
	}

	settlement := NewChatSettlementService(d.pool, d.queries, chatSettlementBilling(testNumeric(61_000000, -10)), ledger.NewService(d.pool, d.queries))
	recovery := NewChatSettlementRecoveryService(d.queries, settlement)
	if err := recovery.RecoverChatSettlement(d.ctx, job); err != nil {
		t.Fatalf("recover long-context settlement: %v", err)
	}

	priceSnapshot, err := d.queries.GetPriceSnapshotByRequest(d.ctx, d.requestRecord.ID)
	if err != nil {
		t.Fatalf("get recovered price snapshot: %v", err)
	}
	if !priceSnapshot.LongContextApplied {
		t.Fatal("expected recovered customer price to apply long-context policy")
	}
	assertNumericEqual(t, priceSnapshot.UncachedInputPrice, testNumeric(6, 0))
	assertNumericEqual(t, priceSnapshot.OutputPrice, testNumeric(36, 0))

	costSnapshot, err := d.queries.GetCostSnapshotByRequest(d.ctx, d.requestRecord.ID)
	if err != nil {
		t.Fatalf("get recovered cost snapshot: %v", err)
	}
	if !costSnapshot.LongContextApplied {
		t.Fatal("expected recovered provider cost to apply long-context policy")
	}
	assertNumericEqual(t, costSnapshot.UncachedInputCost, testNumeric(2, 0))
	assertNumericEqual(t, costSnapshot.OutputCost, testNumeric(12, 0))
}

func assertRecoveredPartialFacts(
	t *testing.T,
	d *chatSettlementDBDeps,
	wantRequest requestlog.RequestStatus,
	wantAttempt requestlog.AttemptStatus,
	wantCode string,
	wantMessage string,
	wantDetail string,
) {
	t.Helper()
	var requestStatus, requestCode, requestMessage, requestDetail string
	var attemptStatus, attemptCode, attemptMessage, attemptDetail string
	var finalUsageReceived bool
	err := d.pool.QueryRow(d.ctx, `
		SELECT
		    r.status,
		    COALESCE(r.error_code, ''),
		    COALESCE(r.error_message, ''),
		    COALESCE(r.internal_error_detail, ''),
		    a.status,
		    COALESCE(a.error_code, ''),
		    COALESCE(a.error_message, ''),
		    COALESCE(a.internal_error_detail, ''),
		    a.final_usage_received
		FROM request_records r
		JOIN request_attempts a ON a.request_record_id = r.id
		WHERE r.id = $1 AND a.id = $2
	`, d.requestRecord.ID, d.attemptRecord.ID).Scan(
		&requestStatus,
		&requestCode,
		&requestMessage,
		&requestDetail,
		&attemptStatus,
		&attemptCode,
		&attemptMessage,
		&attemptDetail,
		&finalUsageReceived,
	)
	if err != nil {
		t.Fatalf("read recovered partial facts: %v", err)
	}
	if requestStatus != string(wantRequest) || attemptStatus != string(wantAttempt) {
		t.Fatalf("recovered status mismatch: request=%q attempt=%q", requestStatus, attemptStatus)
	}
	if requestCode != wantCode || requestMessage != wantMessage || requestDetail != wantDetail ||
		attemptCode != wantCode || attemptMessage != wantMessage || attemptDetail != wantDetail {
		t.Fatalf("recovered error facts mismatch: request=%q/%q/%q attempt=%q/%q/%q", requestCode, requestMessage, requestDetail, attemptCode, attemptMessage, attemptDetail)
	}
	if finalUsageReceived {
		t.Fatal("partial recovery must preserve final_usage_received=false")
	}
}
