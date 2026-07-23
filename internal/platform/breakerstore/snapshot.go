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
	if snap.TTFTEWMAMs, err = optionalFloat64(m, "ttft_ewma_ms"); err != nil {
		return err
	}
	if snap.TTFTSamples, err = optionalInt64(m, "ttft_samples"); err != nil {
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
	if snap.BaseURLRevision, err = optionalInt64(m, "base_url_revision"); err != nil {
		return err
	}
	if snap.StatusRevision, err = optionalInt64(m, "status_revision"); err != nil {
		return err
	}
	if snap.PendingBaseURLRevision, err = optionalInt64(m, "pending_base_url_revision"); err != nil {
		return err
	}
	if snap.PendingStatusRevision, err = optionalInt64(m, "pending_status_revision"); err != nil {
		return err
	}
	if snap.ProviderEndpointID, err = optionalInt64(m, "provider_endpoint_id"); err != nil {
		return err
	}
	if snap.ChannelConfigRevision, err = optionalInt64(m, "channel_config_revision"); err != nil {
		return err
	}
	if snap.StateGeneration, err = optionalInt64(m, "state_generation"); err != nil {
		return err
	}
	if snap.BaseURLFenceGeneration, err = optionalInt64(m, "base_url_fence_generation"); err != nil {
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
		"base_url_revision",
		"status_revision",
		"provider_endpoint_id",
		"channel_config_revision",
		"base_url_fence_generation",
		"status_fence_generation",
		"pending_base_url_revision",
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
	snap.BaseURLRevisionState = m["base_url_revision_state"]
	snap.StatusRevisionState = m["status_revision_state"]
	if !validRevisionState(snap.BaseURLRevisionState) || !validRevisionState(snap.StatusRevisionState) {
		return errors.New("snapshot revision state is invalid")
	}
	if snap.ControlPresent {
		if snap.BaseURLRevision == 0 || snap.StatusRevision == 0 ||
			snap.BaseURLRevisionState == "" || snap.StatusRevisionState == "" ||
			!validEffectiveStatus(snap.EffectiveStatus) {
			return errors.New("snapshot endpoint control is incomplete")
		}
	}
	_, hasTTFT := m["ttft_ewma_ms"]
	_, hasTTFTSamples := m["ttft_samples"]
	if hasTTFT != hasTTFTSamples || (snap.TTFTSamples > 0 && !hasTTFT) {
		return errors.New("snapshot TTFT fields are inconsistent")
	}
	return nil
}

func classifyCandidateSnapshot(candidate SnapshotCandidateInput, endpoint, channel ScopeSnapshot) CandidateSnapshotStatus {
	if !endpoint.Exists || !endpoint.ControlPresent {
		return CandidateSnapshotRuntimeSyncRequired
	}
	if endpoint.BaseURLRevisionState == "pending" || endpoint.StatusRevisionState == "pending" {
		return CandidateSnapshotRuntimeSyncPending
	}
	if endpoint.BaseURLRevision < candidate.EndpointBaseURLRevision {
		return CandidateSnapshotRuntimeSyncRequired
	}
	if endpoint.BaseURLRevision > candidate.EndpointBaseURLRevision {
		return CandidateSnapshotStaleRevision
	}
	if endpoint.StatusRevision < candidate.EndpointStatusRevision {
		return CandidateSnapshotRuntimeSyncRequired
	}
	if endpoint.StatusRevision > candidate.EndpointStatusRevision {
		return CandidateSnapshotStaleStatusRevision
	}
	if !channel.Exists || channel.ChannelConfigRevision == 0 || channel.ProviderEndpointID == 0 ||
		channel.BaseURLRevision == 0 || channel.StatusRevision == 0 {
		return CandidateSnapshotNoSample
	}
	if channel.ChannelConfigRevision < candidate.ChannelConfigRevision {
		return CandidateSnapshotNoSample
	}
	if channel.ChannelConfigRevision > candidate.ChannelConfigRevision {
		return CandidateSnapshotStaleConfigRevision
	}
	if channel.ProviderEndpointID != candidate.EndpointID {
		return CandidateSnapshotStaleConfigRevision
	}
	if channel.BaseURLRevision < candidate.EndpointBaseURLRevision {
		return CandidateSnapshotNoSample
	}
	if channel.BaseURLRevision > candidate.EndpointBaseURLRevision {
		return CandidateSnapshotStaleRevision
	}
	if channel.StatusRevision < candidate.EndpointStatusRevision {
		return CandidateSnapshotNoSample
	}
	if channel.StatusRevision > candidate.EndpointStatusRevision {
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
	targetMs, ok := redisInt64(reply[3])
	if !ok || targetMs <= 0 {
		return SnapshotManyResult{}, storeUnavailable(errors.New("snapshot TTFT target is invalid"), "breakerstore snapshot many")
	}
	ttftWeight, ok := redisFloat64(reply[4])
	if !ok || ttftWeight < 0 || ttftWeight > 1 {
		return SnapshotManyResult{}, storeUnavailable(errors.New("snapshot TTFT weight is invalid"), "breakerstore snapshot many")
	}
	minimumFactor, ok := redisFloat64(reply[5])
	if !ok || minimumFactor <= 0 || minimumFactor > 1 {
		return SnapshotManyResult{}, storeUnavailable(errors.New("snapshot minimum routing factor is invalid"), "breakerstore snapshot many")
	}
	costWeight, ok := redisFloat64(reply[6])
	if !ok || costWeight < 0 || costWeight > 1 {
		return SnapshotManyResult{}, storeUnavailable(errors.New("snapshot cost weight is invalid"), "breakerstore snapshot many")
	}
	breakerEnabled, ok := redisInt64(reply[7])
	if !ok || (breakerEnabled != 0 && breakerEnabled != 1) {
		return SnapshotManyResult{}, storeUnavailable(errors.New("snapshot breaker enabled flag is invalid"), "breakerstore snapshot many")
	}
	rows, ok := reply[8].([]interface{})
	if !ok || len(rows) != len(in.Candidates) {
		return SnapshotManyResult{}, storeUnavailable(errors.New("snapshot candidate rows are invalid"), "breakerstore snapshot many")
	}
	controlProofs, ok := reply[9].([]interface{})
	if !ok || len(controlProofs) != 4 {
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
		ChannelRateRevision:       in.ChannelRateRevision,
		GlobalConcurrencyRevision: in.GlobalConcurrencyRevision,
		CircuitBreakerRevision:    in.CircuitBreakerRevision,
		RoutingBalance: RoutingBalanceSnapshot{
			Revision: revision, TTFTTargetMs: targetMs, TTFTWeight: ttftWeight,
			CostWeight: costWeight, MinimumRoutingFactor: minimumFactor,
		},
	}
	for index, raw := range rows {
		row, ok := raw.([]interface{})
		if !ok || len(row) != 15 {
			return SnapshotManyResult{}, storeUnavailable(errors.New("snapshot candidate row shape is invalid"), "breakerstore snapshot many")
		}
		values := make([]int64, 10)
		for i, source := range []int{0, 1, 3, 4, 5, 6, 7, 8, 9, 10} {
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
		if !validSnapshotControlProof([]interface{}{row[13], row[14]}) {
			return SnapshotManyResult{}, snapshotManyRejected(string(ReasonRuntimeSyncRequired))
		}
		candidate := in.Candidates[index]
		endpoint, err := parseSnapshotRow(ScopeEndpoint, candidate.EndpointID, row[11])
		if err != nil {
			return SnapshotManyResult{}, storeUnavailable(err, "breakerstore snapshot many endpoint row")
		}
		channel, err := parseSnapshotRow(ScopeChannel, candidate.ChannelID, row[12])
		if err != nil {
			return SnapshotManyResult{}, storeUnavailable(err, "breakerstore snapshot many channel row")
		}
		status := classifyCandidateSnapshot(candidate, endpoint, channel)
		switch status {
		case CandidateSnapshotRuntimeSyncRequired, CandidateSnapshotRuntimeSyncPending,
			CandidateSnapshotStaleRevision, CandidateSnapshotStaleStatusRevision, CandidateSnapshotStaleConfigRevision:
			return SnapshotManyResult{}, snapshotManyRejected(string(status))
		}
		status = classifyCandidateGate(status, endpoint, channel, values[0], values[1] == 1, breakerEnabled == 1)
		result.Candidates = append(result.Candidates, CandidateSnapshot{
			Candidate: candidate, Status: status, Endpoint: endpoint, Channel: channel,
			Concurrency:         CapacityUsage{Used: values[2], Limit: values[3]},
			RPM:                 CapacityUsage{Used: values[4], Limit: values[5]},
			RPD:                 CapacityUsage{Used: values[6], Limit: values[7]},
			TPM:                 CapacityUsage{Used: values[8], Limit: values[9]},
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
	endpoint, channel ScopeSnapshot,
	cooldownRemainingMs int64,
	permissionPaused, breakerEnabled bool,
) CandidateSnapshotStatus {
	if endpoint.EffectiveStatus != "enabled" {
		return CandidateSnapshotEndpointDisabled
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
	epGate := scopeGate(endpoint)
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

func optionalFloat64(fields map[string]string, name string) (float64, error) {
	value, ok := fields[name]
	if !ok || value == "" {
		return 0, nil
	}
	parsed, err := strconv.ParseFloat(value, 64)
	if err != nil || math.IsNaN(parsed) || math.IsInf(parsed, 0) || parsed < 0 {
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
