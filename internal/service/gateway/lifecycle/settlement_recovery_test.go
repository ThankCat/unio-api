package lifecycle

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/ThankCat/unio-gateway/internal/core/requestlog"
	"github.com/ThankCat/unio-gateway/internal/platform/store/sqlc"
)

type fakeChatSettlementRecoveryRecorder struct {
	createParams []ChatSettlementParams
	markIDs      []int64
	job          sqlc.SettlementRecoveryJob
	createErr    error
	markErr      error
}

func (r *fakeChatSettlementRecoveryRecorder) CreatePendingChatSettlementRecoveryJob(ctx context.Context, params ChatSettlementParams) (sqlc.SettlementRecoveryJob, error) {
	r.createParams = append(r.createParams, params)
	if r.createErr != nil {
		return sqlc.SettlementRecoveryJob{}, r.createErr
	}

	job := r.job
	if job.ID == 0 {
		job.ID = 55
	}
	return job, nil
}

func (r *fakeChatSettlementRecoveryRecorder) MarkChatSettlementRecoveryJobSucceeded(ctx context.Context, jobID int64) error {
	r.markIDs = append(r.markIDs, jobID)
	return r.markErr
}

type recordingChatSettlementExecutor struct {
	params []ChatSettlementParams
	err    error
	before func(context.Context, ChatSettlementParams)
}

func (e *recordingChatSettlementExecutor) SettleSuccessfulChat(ctx context.Context, params ChatSettlementParams) error {
	if e.before != nil {
		e.before(ctx, params)
	}
	e.params = append(e.params, params)
	return e.err
}

func TestRecoverableChatSettlementExecutorCreatesJobBeforeSettlementAndMarksSucceeded(t *testing.T) {
	recorder := &fakeChatSettlementRecoveryRecorder{job: sqlc.SettlementRecoveryJob{ID: 77}}
	settlement := &recordingChatSettlementExecutor{
		before: func(ctx context.Context, params ChatSettlementParams) {
			if len(recorder.createParams) != 1 {
				t.Fatalf("expected recovery job before settlement, got %d creates", len(recorder.createParams))
			}
		},
	}
	executor := NewRecoverableChatSettlementExecutor(settlement, recorder, time.Second)

	params := ChatSettlementParams{RequestRecord: requestlog.RequestRecord{ID: 10}}
	if err := executor.SettleSuccessfulChat(context.Background(), params); err != nil {
		t.Fatalf("SettleSuccessfulChat returned err: %v", err)
	}

	if len(recorder.createParams) != 1 {
		t.Fatalf("expected one recovery job create, got %d", len(recorder.createParams))
	}
	if len(settlement.params) != 1 {
		t.Fatalf("expected one settlement call, got %d", len(settlement.params))
	}
	if len(recorder.markIDs) != 1 || recorder.markIDs[0] != 77 {
		t.Fatalf("expected job 77 to be marked succeeded, got %#v", recorder.markIDs)
	}
}

func TestRecoverableChatSettlementExecutorSchedulesRecoveryOnSettlementFailure(t *testing.T) {
	settlementErr := errors.New("settlement commit failed")
	recorder := &fakeChatSettlementRecoveryRecorder{job: sqlc.SettlementRecoveryJob{ID: 88}}
	settlement := &recordingChatSettlementExecutor{err: settlementErr}
	executor := NewRecoverableChatSettlementExecutor(settlement, recorder, time.Second)

	err := executor.SettleSuccessfulChat(context.Background(), ChatSettlementParams{RequestRecord: requestlog.RequestRecord{ID: 20}})
	if !IsChatSettlementRecoveryScheduled(err) {
		t.Fatalf("expected recovery scheduled error, got %v", err)
	}
	if !errors.Is(err, settlementErr) {
		t.Fatalf("expected original settlement error to be wrapped, got %v", err)
	}
	if len(recorder.markIDs) != 0 {
		t.Fatalf("expected failed settlement not to mark job succeeded, got %#v", recorder.markIDs)
	}
}

func TestRecoverableChatSettlementExecutorDoesNotSettleWhenRecoveryJobCreateFails(t *testing.T) {
	createErr := errors.New("insert recovery job failed")
	recorder := &fakeChatSettlementRecoveryRecorder{createErr: createErr}
	settlement := &recordingChatSettlementExecutor{}
	executor := NewRecoverableChatSettlementExecutor(settlement, recorder, time.Second)

	err := executor.SettleSuccessfulChat(context.Background(), ChatSettlementParams{RequestRecord: requestlog.RequestRecord{ID: 30}})
	if !errors.Is(err, createErr) {
		t.Fatalf("expected create error, got %v", err)
	}
	if len(settlement.params) != 0 {
		t.Fatalf("expected settlement not to run without recovery job, got %d calls", len(settlement.params))
	}
}
