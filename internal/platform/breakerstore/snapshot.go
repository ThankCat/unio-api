package breakerstore

import (
	"errors"
	"fmt"
	"math"
	"strconv"

	"github.com/ThankCat/unio-gateway/internal/platform/failure"
)

func parseSnapshotRow(scope Scope, id int64, value interface{}) (ScopeSnapshot, error) {
	row, ok := value.([]interface{})
	if !ok || len(row) < 2 {
		return ScopeSnapshot{}, errors.New("unexpected snapshot row")
	}
	code, ok := redisString(row[0])
	if !ok {
		return ScopeSnapshot{}, errors.New("snapshot row status is not a string")
	}
	nowMs, ok := redisInt64(row[1])
	if !ok {
		return ScopeSnapshot{}, errors.New("snapshot row timestamp is not an integer")
	}

	snap := ScopeSnapshot{Scope: scope, ID: id, State: StateClosed}
	switch code {
	case "absent":
		if len(row) != 2 {
			return ScopeSnapshot{}, errors.New("unexpected absent snapshot row")
		}
		return snap, nil
	case "present":
		if len(row) != 4 {
			return ScopeSnapshot{}, errors.New("unexpected present snapshot row")
		}
	default:
		return ScopeSnapshot{}, fmt.Errorf("unknown snapshot row status %q", code)
	}

	remaining, ok := redisInt64(row[2])
	if !ok || remaining < 0 {
		return ScopeSnapshot{}, errors.New("snapshot open remaining is invalid")
	}
	fields, ok := row[3].([]interface{})
	if !ok || len(fields)%2 != 0 {
		return ScopeSnapshot{}, errors.New("snapshot hash fields are invalid")
	}
	snap.Exists = true
	snap.OpenRemainingMs = remaining
	if err := applySnapshotFields(&snap, fields, nowMs); err != nil {
		return ScopeSnapshot{}, err
	}
	return snap, nil
}

func applySnapshotFields(snap *ScopeSnapshot, fields []interface{}, nowMs int64) error {
	m := make(map[string]string, len(fields)/2)
	for i := 0; i+1 < len(fields); i += 2 {
		key, ok := redisString(fields[i])
		if !ok || key == "" {
			return errors.New("snapshot field name is invalid")
		}
		value, ok := redisString(fields[i+1])
		if !ok {
			return fmt.Errorf("snapshot field %q is not a string", key)
		}
		m[key] = value
	}

	state := BreakerState(m["state"])
	if state == "" {
		state = StateClosed
	}
	if state != StateClosed && state != StateOpen && state != StateHalfOpen {
		return fmt.Errorf("snapshot breaker state %q is invalid", state)
	}
	snap.State = state

	var err error
	if snap.OpenLevel, err = optionalInt(m, "open_level"); err != nil {
		return err
	}
	if snap.WindowStartedAtMs, err = optionalInt64(m, "window_started_at_ms"); err != nil {
		return err
	}
	if snap.EligibleSuccesses, err = optionalInt64(m, "eligible_successes"); err != nil {
		return err
	}
	if snap.EligibleFailures, err = optionalInt64(m, "eligible_failures"); err != nil {
		return err
	}
	if snap.ConsecutiveFailures, err = optionalInt64(m, "consecutive_eligible_failures"); err != nil {
		return err
	}
	if snap.LastTransitionAtMs, err = optionalInt64(m, "last_transition_at_ms"); err != nil {
		return err
	}
	halfOpenLeaseUntilMs, err := optionalInt64(m, "half_open_lease_until_ms")
	if err != nil {
		return err
	}
	if halfOpenLeaseUntilMs > nowMs && m["half_open_permit_id"] != "" {
		snap.HalfOpenBusy = true
		snap.HalfOpenLeaseRemainingMs = halfOpenLeaseUntilMs - nowMs
	}
	if snap.OriginRevision, err = optionalInt64(m, "origin_revision"); err != nil {
		return err
	}
	if snap.StatusRevision, err = optionalInt64(m, "status_revision"); err != nil {
		return err
	}
	if snap.PendingOriginRevision, err = optionalInt64(m, "pending_origin_revision"); err != nil {
		return err
	}
	if snap.PendingStatusRevision, err = optionalInt64(m, "pending_status_revision"); err != nil {
		return err
	}
	if snap.ProviderID, err = optionalInt64(m, "provider_id"); err != nil {
		return err
	}
	if snap.ChannelConfigRevision, err = optionalInt64(m, "channel_config_revision"); err != nil {
		return err
	}
	if snap.StateGeneration, err = optionalInt64(m, "state_generation"); err != nil {
		return err
	}
	if snap.OriginFenceGeneration, err = optionalInt64(m, "origin_fence_generation"); err != nil {
		return err
	}
	if snap.StatusFenceGeneration, err = optionalInt64(m, "status_fence_generation"); err != nil {
		return err
	}
	for _, name := range []string{
		"last_failure_at_ms",
		"open_until_ms",
		"half_open_successes",
		"half_open_lease_until_ms",
	} {
		if _, err := optionalInt64(m, name); err != nil {
			return err
		}
	}
	for _, name := range []string{
		"state_generation",
		"origin_revision",
		"status_revision",
		"provider_id",
		"channel_config_revision",
		"origin_fence_generation",
		"status_fence_generation",
		"pending_origin_revision",
		"pending_status_revision",
	} {
		if err := requirePositiveIfPresent(m, name); err != nil {
			return err
		}
	}

	snap.SampleCount = snap.EligibleSuccesses + snap.EligibleFailures
	if snap.SampleCount < snap.EligibleSuccesses {
		return errors.New("snapshot sample count overflow")
	}
	if snap.SampleCount > 0 {
		snap.ErrorRate = float64(snap.EligibleFailures) / float64(snap.SampleCount)
	}
	snap.LastFailureCategory = m["last_failure_category"]

	controlPresent := m["control_present"]
	if controlPresent != "" && controlPresent != "0" && controlPresent != "1" {
		return errors.New("snapshot control_present is invalid")
	}
	snap.ControlPresent = controlPresent == "1"
	snap.EffectiveStatus = m["effective_status"]
	snap.OriginRevisionState = m["origin_revision_state"]
	snap.StatusRevisionState = m["status_revision_state"]
	if !validRevisionState(snap.OriginRevisionState) || !validRevisionState(snap.StatusRevisionState) {
		return errors.New("snapshot revision state is invalid")
	}
	if snap.ControlPresent {
		if snap.OriginRevision == 0 || snap.StatusRevision == 0 ||
			snap.OriginRevisionState == "" || snap.StatusRevisionState == "" ||
			!validEffectiveStatus(snap.EffectiveStatus) {
			return errors.New("snapshot provider control is incomplete")
		}
	}
	return nil
}

func classifyCandidateSnapshot(candidate SnapshotCandidateInput, origin, channel ScopeSnapshot) CandidateSnapshotStatus {
	if !origin.Exists || !origin.ControlPresent {
		return CandidateSnapshotRuntimeSyncRequired
	}
	if origin.OriginRevisionState == "pending" || origin.StatusRevisionState == "pending" {
		return CandidateSnapshotRuntimeSyncPending
	}
	if origin.OriginRevision < candidate.OriginRevision {
		return CandidateSnapshotRuntimeSyncRequired
	}
	if origin.OriginRevision > candidate.OriginRevision {
		return CandidateSnapshotStaleRevision
	}
	if origin.StatusRevision < candidate.ProviderStatusRevision {
		return CandidateSnapshotRuntimeSyncRequired
	}
	if origin.StatusRevision > candidate.ProviderStatusRevision {
		return CandidateSnapshotStaleStatusRevision
	}
	if !channel.Exists || channel.ChannelConfigRevision == 0 || channel.ProviderID == 0 ||
		channel.OriginRevision == 0 || channel.StatusRevision == 0 {
		return CandidateSnapshotNoSample
	}
	if channel.ChannelConfigRevision < candidate.ChannelConfigRevision {
		return CandidateSnapshotNoSample
	}
	if channel.ChannelConfigRevision > candidate.ChannelConfigRevision {
		return CandidateSnapshotStaleConfigRevision
	}
	if channel.ProviderID != candidate.ProviderID {
		return CandidateSnapshotStaleConfigRevision
	}
	if channel.OriginRevision < candidate.OriginRevision {
		return CandidateSnapshotNoSample
	}
	if channel.OriginRevision > candidate.OriginRevision {
		return CandidateSnapshotStaleRevision
	}
	if channel.StatusRevision < candidate.ProviderStatusRevision {
		return CandidateSnapshotNoSample
	}
	if channel.StatusRevision > candidate.ProviderStatusRevision {
		return CandidateSnapshotStaleStatusRevision
	}
	return CandidateSnapshotCurrent
}

func parseSnapshotManyReply(in SnapshotManyInput, reply []interface{}) (SnapshotManyResult, error) {
	if _, ok := redisInt64(reply[1]); !ok {
		return SnapshotManyResult{}, storeUnavailable(errors.New("snapshot timestamp is invalid"), "breakerstore snapshot many")
	}
	revision, ok := redisInt64(reply[2])
	if !ok || revision != in.RoutingBalanceRevision {
		return SnapshotManyResult{}, storeUnavailable(errors.New("snapshot routing balance revision is invalid"), "breakerstore snapshot many")
	}
	// objective_v1 五项权重（成本 / 并发 / TTFT / 错误率 / Priority），之和必须为 100。
	weights := make([]int, 5)
	weightSum := 0
	for index := range weights {
		value, valid := redisInt64(reply[3+index])
		if !valid || value < 0 || value > 100 {
			return SnapshotManyResult{}, storeUnavailable(errors.New("snapshot objective routing weight is invalid"), "breakerstore snapshot many")
		}
		weights[index] = int(value)
		weightSum += int(value)
	}
	if weightSum != 100 {
		return SnapshotManyResult{}, storeUnavailable(errors.New("snapshot objective routing weights do not sum to 100"), "breakerstore snapshot many")
	}
	ttftWindowMs, ok := redisInt64(reply[8])
	if !ok || ttftWindowMs <= 0 {
		return SnapshotManyResult{}, storeUnavailable(errors.New("snapshot TTFT window is invalid"), "breakerstore snapshot many")
	}
	ttftPenaltyUnitMs, ok := redisInt64(reply[9])
	if !ok || ttftPenaltyUnitMs <= 0 {
		return SnapshotManyResult{}, storeUnavailable(errors.New("snapshot TTFT penalty unit is invalid"), "breakerstore snapshot many")
	}
	ttftPenaltyPoints, ok := redisFloat64(reply[10])
	if !ok || ttftPenaltyPoints <= 0 || ttftPenaltyPoints > 100 {
		return SnapshotManyResult{}, storeUnavailable(errors.New("snapshot TTFT penalty points are invalid"), "breakerstore snapshot many")
	}
	errorWindowMs, ok := redisInt64(reply[11])
	if !ok || errorWindowMs <= 0 {
		return SnapshotManyResult{}, storeUnavailable(errors.New("snapshot error window is invalid"), "breakerstore snapshot many")
	}
	errorPenaltyPoints, ok := redisFloat64(reply[12])
	if !ok || errorPenaltyPoints <= 0 || errorPenaltyPoints > 100 {
		return SnapshotManyResult{}, storeUnavailable(errors.New("snapshot error penalty points are invalid"), "breakerstore snapshot many")
	}
	breakerEnabled, ok := redisInt64(reply[13])
	if !ok || (breakerEnabled != 0 && breakerEnabled != 1) {
		return SnapshotManyResult{}, storeUnavailable(errors.New("snapshot breaker enabled flag is invalid"), "breakerstore snapshot many")
	}
	rows, ok := reply[14].([]interface{})
	if !ok || len(rows) != len(in.Candidates) {
		return SnapshotManyResult{}, storeUnavailable(errors.New("snapshot candidate rows are invalid"), "breakerstore snapshot many")
	}
	controlProofs, ok := reply[15].([]interface{})
	if !ok || len(controlProofs) != 3 {
		return SnapshotManyResult{}, storeUnavailable(errors.New("snapshot control proofs are invalid"), "breakerstore snapshot many")
	}
	for _, proof := range controlProofs {
		if !validSnapshotControlProof(proof) {
			return SnapshotManyResult{}, snapshotManyRejected(string(ReasonRuntimeSyncRequired))
		}
	}

	result := SnapshotManyResult{
		Candidates:                make([]CandidateSnapshot, 0, len(rows)),
		IntegrityRevision:         in.IntegrityRevision,
		GlobalConcurrencyRevision: in.GlobalConcurrencyRevision,
		CircuitBreakerRevision:    in.CircuitBreakerRevision,
		RoutingBalance: RoutingBalanceSnapshot{
			Revision:                     revision,
			CostWeightPct:                weights[0],
			ConcurrencyWeightPct:         weights[1],
			TTFTWeightPct:                weights[2],
			ErrorRateWeightPct:           weights[3],
			PriorityWeightPct:            weights[4],
			TTFTWindowMs:                 ttftWindowMs,
			TTFTPenaltyUnitMs:            ttftPenaltyUnitMs,
			TTFTPenaltyPointsPerUnit:     ttftPenaltyPoints,
			ErrorWindowMs:                errorWindowMs,
			ErrorPenaltyPointsPerPercent: errorPenaltyPoints,
		},
	}
	for index, raw := range rows {
		row, ok := raw.([]interface{})
		if !ok || len(row) != 9 {
			return SnapshotManyResult{}, storeUnavailable(errors.New("snapshot candidate row shape is invalid"), "breakerstore snapshot many")
		}
		values := make([]int64, 4)
		for i, source := range []int{0, 1, 3, 4} {
			values[i], ok = redisInt64(row[source])
			if !ok || values[i] < 0 {
				return SnapshotManyResult{}, storeUnavailable(errors.New("snapshot capacity fact is invalid"), "breakerstore snapshot many")
			}
		}
		if values[1] != 0 && values[1] != 1 {
			return SnapshotManyResult{}, storeUnavailable(errors.New("snapshot permission fact is invalid"), "breakerstore snapshot many")
		}
		permissionState, ok := redisString(row[2])
		if !ok || permissionState == "" {
			return SnapshotManyResult{}, storeUnavailable(errors.New("snapshot permission state is invalid"), "breakerstore snapshot many")
		}
		if !validSnapshotControlProof([]interface{}{row[7], row[8]}) {
			return SnapshotManyResult{}, snapshotManyRejected(string(ReasonRuntimeSyncRequired))
		}
		candidate := in.Candidates[index]
		origin, err := parseSnapshotRow(ScopeProvider, candidate.ProviderID, row[5])
		if err != nil {
			return SnapshotManyResult{}, storeUnavailable(err, "breakerstore snapshot many origin row")
		}
		channel, err := parseSnapshotRow(ScopeChannel, candidate.ChannelID, row[6])
		if err != nil {
			return SnapshotManyResult{}, storeUnavailable(err, "breakerstore snapshot many channel row")
		}
		status := classifyCandidateSnapshot(candidate, origin, channel)
		switch status {
		case CandidateSnapshotRuntimeSyncRequired, CandidateSnapshotRuntimeSyncPending,
			CandidateSnapshotStaleRevision, CandidateSnapshotStaleStatusRevision, CandidateSnapshotStaleConfigRevision:
			return SnapshotManyResult{}, snapshotManyRejected(string(status))
		}
		status = classifyCandidateGate(status, origin, channel, values[0], values[1] == 1, breakerEnabled == 1)
		result.Candidates = append(result.Candidates, CandidateSnapshot{
			Candidate: candidate, Status: status, Provider: origin, Channel: channel,
			Concurrency:         CapacityUsage{Used: values[2], Limit: values[3]},
			CooldownRemainingMs: values[0], ModelPermissionPaused: values[1] == 1,
			ModelPermissionRecheckState: permissionState,
		})
	}
	return result, nil
}

func validSnapshotControlProof(raw interface{}) bool {
	proof, ok := raw.([]interface{})
	if !ok || len(proof) != 2 {
		return false
	}
	payload, ok := redisString(proof[0])
	if !ok || payload == "" {
		return false
	}
	hash, ok := redisString(proof[1])
	return ok && hash == HashPayload(payload)
}

func classifyCandidateGate(
	identity CandidateSnapshotStatus,
	origin, channel ScopeSnapshot,
	cooldownRemainingMs int64,
	permissionPaused, breakerEnabled bool,
) CandidateSnapshotStatus {
	if origin.EffectiveStatus != "enabled" {
		return CandidateSnapshotProviderDisabled
	}
	if cooldownRemainingMs > 0 {
		return CandidateSnapshotRateLimited
	}
	if permissionPaused {
		return CandidateSnapshotModelPermissionPaused
	}
	if !breakerEnabled {
		return identity
	}
	epGate := scopeGate(origin)
	chGate := scopeGate(channel)
	if epGate == CandidateSnapshotOpen || chGate == CandidateSnapshotOpen {
		return CandidateSnapshotOpen
	}
	if epGate == CandidateSnapshotHalfOpenBusy || chGate == CandidateSnapshotHalfOpenBusy {
		return CandidateSnapshotHalfOpenBusy
	}
	if epGate == CandidateSnapshotHalfOpen || chGate == CandidateSnapshotHalfOpen {
		return CandidateSnapshotHalfOpen
	}
	return identity
}

func scopeGate(snapshot ScopeSnapshot) CandidateSnapshotStatus {
	if !snapshot.Exists {
		return CandidateSnapshotCurrent
	}
	switch snapshot.State {
	case StateOpen:
		if snapshot.OpenRemainingMs > 0 {
			return CandidateSnapshotOpen
		}
		return CandidateSnapshotHalfOpen
	case StateHalfOpen:
		if snapshot.HalfOpenBusy {
			return CandidateSnapshotHalfOpenBusy
		}
		return CandidateSnapshotHalfOpen
	default:
		return CandidateSnapshotCurrent
	}
}

func snapshotManyRejected(reason string) error {
	code := failure.CodeGatewayRuntimeSyncRequired
	if reason == string(ReasonRuntimeStateLost) || reason == string(ReasonStaleIntegrityEpoch) {
		code = failure.CodeGatewayRuntimeStateLost
	} else if reason == string(ReasonBreakerStoreUnavailable) {
		code = failure.CodeGatewayBreakerStoreUnavailable
	}
	return failure.New(
		code,
		failure.WithMessage("gateway candidate runtime snapshot was rejected"),
		failure.WithField("reason", reason),
	)
}

func optionalInt(fields map[string]string, name string) (int, error) {
	value, ok := fields[name]
	if !ok || value == "" {
		return 0, nil
	}
	parsed, err := strconv.Atoi(value)
	if err != nil || parsed < 0 {
		return 0, fmt.Errorf("snapshot field %q is invalid", name)
	}
	return parsed, nil
}

func optionalInt64(fields map[string]string, name string) (int64, error) {
	value, ok := fields[name]
	if !ok || value == "" {
		return 0, nil
	}
	parsed, err := strconv.ParseInt(value, 10, 64)
	if err != nil || parsed < 0 {
		return 0, fmt.Errorf("snapshot field %q is invalid", name)
	}
	return parsed, nil
}

func requirePositiveIfPresent(fields map[string]string, name string) error {
	value, exists := fields[name]
	if !exists || value == "" {
		return nil
	}
	parsed, err := strconv.ParseInt(value, 10, 64)
	if err != nil || parsed <= 0 {
		return fmt.Errorf("snapshot field %q must be positive", name)
	}
	return nil
}

func redisString(value interface{}) (string, bool) {
	switch typed := value.(type) {
	case string:
		return typed, true
	case []byte:
		return string(typed), true
	default:
		return "", false
	}
}

func redisInt64(value interface{}) (int64, bool) {
	switch typed := value.(type) {
	case int64:
		return typed, true
	case string:
		parsed, err := strconv.ParseInt(typed, 10, 64)
		return parsed, err == nil
	case []byte:
		parsed, err := strconv.ParseInt(string(typed), 10, 64)
		return parsed, err == nil
	default:
		return 0, false
	}
}

func redisFloat64(value interface{}) (float64, bool) {
	switch typed := value.(type) {
	case float64:
		return typed, !math.IsNaN(typed) && !math.IsInf(typed, 0)
	case int64:
		return float64(typed), true
	case string:
		parsed, err := strconv.ParseFloat(typed, 64)
		return parsed, err == nil && !math.IsNaN(parsed) && !math.IsInf(parsed, 0)
	case []byte:
		parsed, err := strconv.ParseFloat(string(typed), 64)
		return parsed, err == nil && !math.IsNaN(parsed) && !math.IsInf(parsed, 0)
	default:
		return 0, false
	}
}

func validRevisionState(state string) bool {
	return state == "" || state == "active" || state == "pending"
}

func validEffectiveStatus(status string) bool {
	return status == "enabled" || status == "disabled" || status == "archived"
}
