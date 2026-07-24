package breakerstore

import (
	"context"
	"errors"
	"strconv"
	"sync"
	"time"

	"github.com/google/uuid"
)

const (
	// RuntimeReadinessReasonStoreFaultLatched is intentionally stable and contains no Redis detail.
	RuntimeReadinessReasonStoreFaultLatched = "breaker_store_unavailable"
	runtimeFaultPublishTimeout              = 250 * time.Millisecond
	runtimeRedisInstanceChanged             = "redis_instance_changed"
)

type runtimeFaultLatchState struct {
	mu         sync.Mutex
	instanceID string
	generation uint64
	latched    bool
	token      string
}

func newRuntimeFaultLatchState() runtimeFaultLatchState {
	return runtimeFaultLatchState{instanceID: uuid.NewString()}
}

func (s *runtimeFaultLatchState) latch() (uint64, string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.generation++
	s.latched = true
	s.token = s.instanceID + ":" + strconv.FormatUint(s.generation, 10)
	return s.generation, s.token
}

func (s *runtimeFaultLatchState) latchIfClear() (uint64, string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.latched {
		return s.generation, s.token, false
	}
	s.generation++
	s.latched = true
	s.token = s.instanceID + ":" + strconv.FormatUint(s.generation, 10)
	return s.generation, s.token, true
}

func (s *runtimeFaultLatchState) snapshot() (uint64, string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.generation, s.token, s.latched
}

func (s *Store) latchRuntimeInfrastructureFault(ctx context.Context) {
	_, token := s.fault.latch()
	s.publishRuntimeInfrastructureFault(ctx, token, true)
}

func (s *Store) ensureRuntimeInfrastructureFault(ctx context.Context) {
	_, token, created := s.fault.latchIfClear()
	s.publishRuntimeInfrastructureFault(ctx, token, created)
}

func (s *Store) publishRuntimeInfrastructureFault(ctx context.Context, token string, replace bool) {
	if token == "" {
		return
	}
	publishCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), runtimeFaultPublishTimeout)
	defer cancel()
	if replace {
		// A newly confirmed fault replaces the previous token so an older reconciliation proof can
		// never clear it. SET also repairs a malformed key owned solely by this latch.
		_ = s.client.Set(publishCtx, s.keys.runtimeInfrastructureFault(), token, 0).Err()
		return
	}
	// Republishing a historical local latch only fills an absent key. It must not overwrite a newer
	// fault generation reported by another Gateway.
	_ = s.client.SetNX(publishCtx, s.keys.runtimeInfrastructureFault(), token, 0).Err()
}

// localRuntimeInfrastructureFault keeps the discovering process fail-closed even when Redis was
// unavailable and the shared latch could not initially be published.
func (s *Store) localRuntimeInfrastructureFault(ctx context.Context) bool {
	_, token, latched := s.fault.snapshot()
	if latched {
		s.publishRuntimeInfrastructureFault(ctx, token, false)
	}
	return latched
}

// deleteRuntimeInfrastructureFaultIfLocalGeneration serializes the final Redis latch delete with
// local fault discovery. A concurrent request-time fault either wins before this lock (and rejects
// the stale generation) or waits and becomes a strictly newer fault after the successful clear.
func (s *Store) deleteRuntimeInfrastructureFaultIfLocalGeneration(
	ctx context.Context,
	localGeneration uint64,
	sharedToken, redisRunID string,
) (string, error) {
	s.fault.mu.Lock()
	defer s.fault.mu.Unlock()
	if s.fault.latched && s.fault.generation != localGeneration {
		return "local_fault_changed", nil
	}

	deleteRaw, err := s.faultDelete.Run(
		ctx,
		s.client,
		[]string{s.keys.runtimeInfrastructureFault(), s.keys.runtimeReconciliationProof()},
		sharedToken,
		redisRunID,
	).Result()
	if err != nil {
		return "", storeUnavailable(err, "breakerstore runtime fault latch delete")
	}
	deleteReply, ok := deleteRaw.([]interface{})
	if !ok || len(deleteReply) == 0 {
		return "", storeUnavailable(errors.New("unexpected runtime fault latch delete reply"), "breakerstore runtime fault latch delete")
	}
	deleteCode, _ := deleteReply[0].(string)
	if deleteCode == "cleared" || deleteCode == "already_clear" {
		s.fault.latched = false
		s.fault.token = ""
	}
	return deleteCode, nil
}

// RuntimeReconciliationGeneration freezes the local and shared fault generations immediately
// before a full reconciliation pass. Its fields are private so only BreakerStore can validate it.
type RuntimeReconciliationGeneration struct {
	localGeneration uint64
	sharedToken     string
	redisRunID      string
}

type RuntimeOriginControlProof struct {
	OriginID      int64
	BaseURLRevision int64
	StatusRevision  int64
	EffectiveStatus string
}

type RuntimeChannelAdmissionControlProof struct {
	ChannelID int64
	Revision  int64
	Payload   string
}

// RuntimeReconciliationProof is the complete PostgreSQL-derived control set validated by the
// reconciler. The clear commit verifies every listed Redis control atomically with the latch CAS.
type RuntimeReconciliationProof struct {
	Generation               RuntimeReconciliationGeneration
	OriginControls         []RuntimeOriginControlProof
	ChannelAdmissionControls []RuntimeChannelAdmissionControlProof
}

// BeginRuntimeReconciliation must be called immediately before the full reconciliation pass whose
// success will authorize a clear. Any infrastructure fault during or after that pass changes either
// the local generation or shared token and invalidates the proof.
func (s *Store) BeginRuntimeReconciliation(ctx context.Context) (generation RuntimeReconciliationGeneration, err error) {
	done := s.beginOperation(ctx, operationBeginRuntimeReconcile)
	defer func() { done(operationResultSuccess, err) }()

	s.localRuntimeInfrastructureFault(ctx)
	localGeneration, _, _ := s.fault.snapshot()
	res, err := s.faultBegin.Run(ctx, s.client, []string{s.keys.runtimeInfrastructureFault()}).Result()
	if err != nil {
		return RuntimeReconciliationGeneration{}, storeUnavailable(err, "breakerstore begin runtime reconciliation")
	}
	reply, ok := res.([]interface{})
	if !ok || len(reply) != 2 {
		return RuntimeReconciliationGeneration{}, storeUnavailable(errors.New("unexpected begin reconciliation reply"), "breakerstore begin runtime reconciliation")
	}
	redisRunID, runIDOK := redisString(reply[0])
	sharedToken, tokenOK := redisString(reply[1])
	if !runIDOK || !tokenOK || redisRunID == "" {
		return RuntimeReconciliationGeneration{}, storeUnavailable(errors.New("invalid begin reconciliation reply"), "breakerstore begin runtime reconciliation")
	}
	return RuntimeReconciliationGeneration{
		localGeneration: localGeneration,
		sharedToken:     sharedToken,
		redisRunID:      redisRunID,
	}, nil
}

// ClearRuntimeInfrastructureFaultAfterReconciliation is the only fault-latch clearing API.
// The caller must have completed the full Origin, Channel admission, critical setting, and
// durable-operation reconciliation immediately before calling it. This method then re-reads the
// PostgreSQL-derived epoch/revisions through in, validates marker and critical control payloads,
// and compare-and-deletes the exact shared fault generation. A concurrent fault cannot be cleared.
func (s *Store) ClearRuntimeInfrastructureFaultAfterReconciliation(
	ctx context.Context,
	in RuntimeReadinessInput,
	reconciliation RuntimeReconciliationProof,
) (result RuntimeReadinessResult, err error) {
	done := s.beginOperation(ctx, operationClearRuntimeFault)
	defer func() { done(runtimeReadinessOperationResult(result), err) }()

	keys := []string{
		s.keys.runtimeInfrastructureFault(),
		s.keys.stateIntegrityMarker(),
		s.keys.admissionRouteRate(),
		s.keys.admissionChannelRate(),
		s.keys.admissionGlobalConcurrency(),
		s.keys.runtimeControlSetting("gateway.circuit_breaker"),
		s.keys.runtimeControlSetting("gateway.routing_balance"),
		s.keys.runtimeReconciliationProof(),
	}
	if reconciliation.Generation.redisRunID == "" {
		return RuntimeReadinessResult{Reason: "control_proof_invalid"}, nil
	}
	seenOrigins := make(map[int64]struct{}, len(reconciliation.OriginControls))
	for _, origin := range reconciliation.OriginControls {
		if origin.OriginID <= 0 || origin.BaseURLRevision < 1 || origin.StatusRevision < 1 ||
			(origin.EffectiveStatus != "enabled" && origin.EffectiveStatus != "disabled" && origin.EffectiveStatus != "archived") {
			return RuntimeReadinessResult{Reason: "control_proof_invalid"}, nil
		}
		if _, exists := seenOrigins[origin.OriginID]; exists {
			return RuntimeReadinessResult{Reason: "control_proof_invalid"}, nil
		}
		seenOrigins[origin.OriginID] = struct{}{}
		keys = append(keys, s.keys.origin(origin.OriginID))
	}
	seenChannels := make(map[int64]struct{}, len(reconciliation.ChannelAdmissionControls))
	for _, channel := range reconciliation.ChannelAdmissionControls {
		if channel.ChannelID <= 0 || channel.Revision < 1 || channel.Payload == "" {
			return RuntimeReadinessResult{Reason: "control_proof_invalid"}, nil
		}
		if _, exists := seenChannels[channel.ChannelID]; exists {
			return RuntimeReadinessResult{Reason: "control_proof_invalid"}, nil
		}
		seenChannels[channel.ChannelID] = struct{}{}
		keys = append(keys, s.keys.admissionChannel(channel.ChannelID))
	}
	argv := append(runtimeReadinessArgs(in), reconciliation.Generation.redisRunID)
	proofRaw, err := s.faultProof.Run(ctx, s.client, keys[:7], argv...).Result()
	if err != nil {
		return RuntimeReadinessResult{}, storeUnavailable(err, "breakerstore runtime fault clear proof")
	}
	proofReply, ok := proofRaw.([]interface{})
	if !ok || len(proofReply) == 0 {
		return RuntimeReadinessResult{}, storeUnavailable(errors.New("unexpected runtime fault proof reply"), "breakerstore runtime fault clear proof")
	}
	code, _ := proofReply[0].(string)
	if code != "ready" {
		if code == runtimeRedisInstanceChanged {
			s.ensureRuntimeInfrastructureFault(ctx)
			code = RuntimeReadinessReasonStoreFaultLatched
		}
		return RuntimeReadinessResult{Reason: code}, nil
	}
	if len(proofReply) != 12 {
		return RuntimeReadinessResult{}, storeUnavailable(errors.New("incomplete runtime fault proof reply"), "breakerstore runtime fault clear proof")
	}
	sharedToken, _ := proofReply[1].(string)
	if sharedToken != reconciliation.Generation.sharedToken {
		return RuntimeReadinessResult{Reason: RuntimeReadinessReasonStoreFaultLatched}, nil
	}
	if !validRuntimeControlProofs(proofReply[2:]) {
		return RuntimeReadinessResult{Reason: "control_payload_mismatch"}, nil
	}

	clearArgs := append(runtimeReadinessArgs(in), reconciliation.Generation.redisRunID, proofReply[1])
	clearArgs = append(clearArgs, proofReply[2:]...)
	clearArgs = append(clearArgs, strconv.Itoa(len(reconciliation.OriginControls)), strconv.Itoa(len(reconciliation.ChannelAdmissionControls)))
	for _, origin := range reconciliation.OriginControls {
		clearArgs = append(clearArgs,
			strconv.FormatInt(origin.BaseURLRevision, 10),
			strconv.FormatInt(origin.StatusRevision, 10),
			origin.EffectiveStatus,
		)
	}
	for _, channel := range reconciliation.ChannelAdmissionControls {
		clearArgs = append(clearArgs,
			strconv.FormatInt(channel.Revision, 10),
			channel.Payload,
			HashPayload(channel.Payload),
		)
	}
	clearRaw, err := s.faultClear.Run(ctx, s.client, keys, clearArgs...).Result()
	if err != nil {
		return RuntimeReadinessResult{}, storeUnavailable(err, "breakerstore runtime fault clear commit")
	}
	clearReply, ok := clearRaw.([]interface{})
	if !ok || len(clearReply) == 0 {
		return RuntimeReadinessResult{}, storeUnavailable(errors.New("unexpected runtime fault clear reply"), "breakerstore runtime fault clear commit")
	}
	clearCode, _ := clearReply[0].(string)
	if clearCode != "verified" && clearCode != "already_clear" {
		if clearCode == "fault_changed" {
			clearCode = RuntimeReadinessReasonStoreFaultLatched
		} else if clearCode == runtimeRedisInstanceChanged {
			s.ensureRuntimeInfrastructureFault(ctx)
			clearCode = RuntimeReadinessReasonStoreFaultLatched
		}
		return RuntimeReadinessResult{Reason: clearCode}, nil
	}
	deleteCode, deleteErr := s.deleteRuntimeInfrastructureFaultIfLocalGeneration(
		ctx,
		reconciliation.Generation.localGeneration,
		reconciliation.Generation.sharedToken,
		reconciliation.Generation.redisRunID,
	)
	if deleteErr != nil {
		return RuntimeReadinessResult{}, deleteErr
	}
	if deleteCode != "cleared" && deleteCode != "already_clear" {
		if deleteCode == runtimeRedisInstanceChanged {
			s.ensureRuntimeInfrastructureFault(ctx)
		}
		_, token, _ := s.fault.snapshot()
		s.publishRuntimeInfrastructureFault(ctx, token, false)
		return RuntimeReadinessResult{Reason: RuntimeReadinessReasonStoreFaultLatched}, nil
	}
	return RuntimeReadinessResult{Ready: true, Reason: "ready"}, nil
}
