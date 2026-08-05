package breakerstore

import (
	"context"
	"errors"
	"time"

	"github.com/ThankCat/unio-gateway/internal/platform/failure"
)

// OperationObserver is the narrow observability contract used by BreakerStore.
// The platform package deliberately does not depend on the Prometheus implementation.
type OperationObserver interface {
	ObserveBreakerStoreOperation(operation, result string, duration time.Duration)
}

const (
	operationVerifyDeployment       = "verify_deployment"
	operationPing                   = "ping"
	operationAcquireRequest         = "acquire_request"
	operationReserveRequest         = "reserve_request"
	operationRenewRequest           = "renew_request"
	operationFinishRequest          = "finish_request"
	operationAcquireAttempt         = "acquire_attempt"
	operationRenewAttempt           = "renew_attempt"
	operationFinishAttempt          = "finish_attempt"
	operationAbortAttempt           = "abort_attempt"
	operationSet429Cooldown         = "set_429_cooldown"
	operationRead429Cooldown        = "read_429_cooldown"
	operationPausePermission        = "pause_permission"
	operationClearPermission        = "clear_permission"
	operationClaimPermissionRecheck = "claim_permission_recheck"
	operationFinishPermissionCheck  = "finish_permission_recheck"
	operationReset                  = "reset"
	operationSnapshot               = "snapshot"
	operationSnapshotMany           = "snapshot_many"
	operationStateIntegrity         = "state_integrity"
	operationRuntimeReadiness       = "runtime_readiness"
	operationBeginRuntimeReconcile  = "begin_runtime_reconcile"
	operationClearRuntimeFault      = "clear_runtime_fault"
	operationRecordTPMObservation   = "record_tpm_observation"
	operationCorrectTPMObservation  = "correct_tpm_observation"
	operationReadTPMObservation     = "read_tpm_observation"
)

const (
	operationResultSuccess     = "success"
	operationResultAllowed     = "allowed"
	operationResultDenied      = "denied"
	operationResultApplied     = "applied"
	operationResultIgnored     = "ignored"
	operationResultActive      = "active"
	operationResultIdle        = "idle"
	operationResultPaused      = "paused"
	operationResultPresent     = "present"
	operationResultPending     = "pending"
	operationResultReady       = "ready"
	operationResultNotReady    = "not_ready"
	operationResultInvalid     = "invalid"
	operationResultUnsupported = "unsupported"
	operationResultUnavailable = "unavailable"
	operationResultError       = "error"
)

func (s *Store) beginOperation(ctx context.Context, operation string) func(string, error) {
	startedAt := time.Now()
	return func(result string, err error) {
		boundedResult := boundedOperationResult(result, err)
		// Only a confirmed Store/Redis failure creates a new latch generation. Requests denied by an
		// already-present shared latch remain observable as unavailable but must not continuously
		// replace its CAS token and starve reconciliation.
		if errors.Is(err, ErrStoreUnavailable) || failure.CodeOf(err) == failure.CodeDependencyRedisUnavailable {
			s.latchRuntimeInfrastructureFault(ctx)
		}
		if s.observer == nil {
			return
		}
		s.observer.ObserveBreakerStoreOperation(
			operation,
			boundedResult,
			time.Since(startedAt),
		)
	}
}

// beginObservationOperation 是 best-effort 观测写入的指标钩子（§8/§10）。
// 与 beginOperation 的唯一区别：Redis 故障只上报指标，绝不置位共享基础设施故障 latch。
// 观测桶不参与准入，一次观测写失败不允许 fence 掉整个 namespace 的请求。
func (s *Store) beginObservationOperation(operation string) func(string, error) {
	startedAt := time.Now()
	return func(result string, err error) {
		if s.observer == nil {
			return
		}
		s.observer.ObserveBreakerStoreOperation(
			operation,
			boundedOperationResult(result, err),
			time.Since(startedAt),
		)
	}
}

func runtimeReadinessOperationResult(result RuntimeReadinessResult) string {
	if result.Ready {
		return operationResultReady
	}
	return operationResultNotReady
}

// boundedOperationResult is the final cardinality boundary for the result label.
// Never forward Redis replies, IDs, or error text without an explicit allow-list entry.
func boundedOperationResult(result string, err error) string {
	if err != nil {
		switch {
		case errors.Is(err, ErrStoreUnavailable),
			failure.CodeOf(err) == failure.CodeDependencyRedisUnavailable,
			failure.CodeOf(err) == failure.CodeGatewayBreakerStoreUnavailable:
			return operationResultUnavailable
		case errors.Is(err, ErrRuntimeStateLost), failure.CodeOf(err) == failure.CodeGatewayRuntimeStateLost:
			return string(RequestRuntimeStateLost)
		case errors.Is(err, ErrStaleIntegrityEpoch):
			return string(RequestStaleEpoch)
		case errors.Is(err, ErrRuntimeSyncRequired), failure.CodeOf(err) == failure.CodeGatewayRuntimeSyncRequired:
			return string(RequestRuntimeSyncReq)
		case failure.CodeOf(err) == failure.CodeConfigInvalid:
			return operationResultInvalid
		case failure.CodeOf(err) == failure.CodeConfigUnsupported:
			return operationResultUnsupported
		case failure.CodeOf(err) == failure.CodeGatewayBreakerPermitConflict:
			return string(RequestConflict)
		default:
			return operationResultError
		}
	}

	switch result {
	case "success", "allowed", "denied", "applied", "ignored", "active", "idle", "paused",
		"present", "pending", "ready", "not_ready", "invalid", "unsupported", "unavailable", "error",
		"limited", "store_unavailable", "runtime_state_lost", "stale_integrity_epoch", "runtime_sync_required",
		"runtime_sync_pending", "stale_setting_revision", "conflict", "reserved",
		"unknown_request_admission", "renewed", "finished", "terminal", "expired", "result_unknown",
		"cleared", "rescheduled", "stale", "absent", "superseded", "claimed":
		return result
	default:
		return operationResultError
	}
}

func requestAdmissionOperationResult(result RequestAdmissionResult) string {
	return string(result.Outcome)
}

func attemptAdmissionOperationResult(admission AttemptAdmission) string {
	switch admission.Mode {
	case AdmissionPermit:
		return operationResultAllowed
	case AdmissionDenied:
		return operationResultDenied
	default:
		return operationResultError
	}
}

func finishAttemptOperationResult(result FinishResult) string {
	if result.ProviderDisposition == DispositionResultUnknown || result.ChannelDisposition == DispositionResultUnknown {
		return string(DispositionResultUnknown)
	}
	if result.ProviderDisposition == DispositionApplied || result.ChannelDisposition == DispositionApplied {
		return operationResultApplied
	}
	return operationResultIgnored
}

func stateIntegrityOperationResult(snapshot StateIntegritySnapshot) string {
	if !snapshot.Exists {
		return string(PermissionRecheckAbsent)
	}
	switch snapshot.State {
	case operationResultReady:
		return operationResultReady
	case operationResultPending:
		return operationResultPending
	default:
		return operationResultError
	}
}
