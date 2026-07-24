package breakerstore

import (
	"strings"

	"github.com/ThankCat/unio-gateway/internal/platform/failure"
)

const maxSnapshotCandidates = 1024
const maxLuaExactInteger int64 = 9007199254740991

func configInvalid(message string) error {
	return failure.New(failure.CodeConfigInvalid, failure.WithMessage(message))
}

func validateRequestAdmissionInput(in RequestAdmissionInput) error {
	if strings.TrimSpace(in.RequestAdmissionID) == "" || strings.TrimSpace(in.Fingerprint) == "" {
		return configInvalid("request admission id and fingerprint are required")
	}
	if in.RouteID <= 0 || in.UserID <= 0 {
		return configInvalid("request admission route and user ids must be positive")
	}
	if strings.TrimSpace(in.IntegrityEpoch) == "" || in.IntegrityRevision <= 0 {
		return configInvalid("request admission integrity epoch and revision are required")
	}
	if in.RouteRateRevision <= 0 || in.GlobalConcurrencyRevision <= 0 {
		return configInvalid("request admission control revisions must be positive")
	}
	for _, limit := range []*int64{
		in.RPMLimitOverride,
		in.RPDLimitOverride,
		in.TPMLimitOverride,
		in.ConcurrencyLimitOverride,
	} {
		if limit != nil && (*limit < 0 || *limit > maxLuaExactInteger) {
			return configInvalid("request admission limit overrides must be non-negative exact integers")
		}
	}
	return nil
}

func validateRequestLifecycleInput(requestAdmissionID string, routeID, userID int64, integrityEpoch string, integrityRevision int64) error {
	if strings.TrimSpace(requestAdmissionID) == "" {
		return configInvalid("request admission id is required")
	}
	if routeID <= 0 || userID <= 0 {
		return configInvalid("request admission route and user ids must be positive")
	}
	if strings.TrimSpace(integrityEpoch) == "" || integrityRevision <= 0 {
		return configInvalid("request admission integrity epoch and revision are required")
	}
	return nil
}

func validateReserveRequestTokensInput(requestAdmissionID string, routeID, userID, estimatedTokens int64) error {
	if strings.TrimSpace(requestAdmissionID) == "" {
		return configInvalid("request admission id is required")
	}
	if routeID <= 0 || userID <= 0 {
		return configInvalid("request admission route and user ids must be positive")
	}
	if estimatedTokens < 0 {
		return configInvalid("estimated request tokens must not be negative")
	}
	return nil
}

func validateAcquireAttemptInput(in AcquireAttemptInput) error {
	if strings.TrimSpace(in.PermitID) == "" {
		return configInvalid("attempt permit id is required")
	}
	if strings.TrimSpace(in.AdmissionFingerprint) == "" {
		return configInvalid("attempt admission fingerprint is required")
	}
	if strings.TrimSpace(in.RequestAdmissionID) == "" {
		return configInvalid("request admission id is required")
	}
	if strings.TrimSpace(in.IntegrityEpoch) == "" || in.IntegrityRevision <= 0 {
		return configInvalid("attempt integrity epoch and revision are required")
	}
	if in.OriginID <= 0 || in.ChannelID <= 0 || in.ModelID <= 0 {
		return configInvalid("origin, channel, and model ids must be positive")
	}
	if in.OriginBaseURLRevision <= 0 || in.OriginStatusRevision <= 0 || in.ChannelConfigRevision <= 0 {
		return configInvalid("origin and channel revisions must be positive")
	}
	if !in.UpstreamEndpoint.valid() {
		return configInvalid("unknown upstream operation")
	}
	if !in.RequestMode.valid() {
		return configInvalid("unknown request mode")
	}
	if in.ChannelAdmissionRevision <= 0 || in.ChannelRateRevision <= 0 ||
		in.GlobalConcurrencyRevision <= 0 || in.CircuitBreakerRevision <= 0 {
		return configInvalid("attempt control revisions must be positive")
	}
	if in.EstimatedInputTokens < 0 {
		return configInvalid("attempt token estimate must not be negative")
	}
	return nil
}

func validateFinishInput(permit AttemptPermit, outcome FinishOutcome) error {
	if err := validateAttemptPermit(permit); err != nil {
		return err
	}
	if !outcome.OriginOutcome.valid() || !outcome.ChannelOutcome.valid() {
		return configInvalid("unknown breaker outcome")
	}
	if !outcome.OriginEvidence.valid() {
		return configInvalid("unknown origin evidence category")
	}
	if outcome.OriginEvidence != OriginEvidenceNone &&
		(outcome.OriginOutcome != OutcomeIgnored || outcome.ChannelOutcome != OutcomeEligibleFailure) {
		return configInvalid("origin evidence requires ignored origin and eligible channel failure outcomes")
	}
	if outcome.FirstTokenMs != nil {
		if permit.RequestMode != ModeStream {
			return configInvalid("non-stream attempts cannot report first-token latency")
		}
		if *outcome.FirstTokenMs < 0 {
			return configInvalid("first-token latency must not be negative")
		}
	}
	if outcome.ChannelTPMActual != nil && *outcome.ChannelTPMActual < 0 {
		return configInvalid("actual channel token usage must not be negative")
	}
	return nil
}

func validateAttemptPermit(permit AttemptPermit) error {
	if strings.TrimSpace(permit.PermitID) == "" {
		return configInvalid("attempt permit id is required")
	}
	if strings.TrimSpace(permit.RequestAdmissionID) == "" {
		return configInvalid("permit request admission id is required")
	}
	if strings.TrimSpace(permit.IntegrityEpoch) == "" || permit.IntegrityRevision <= 0 {
		return configInvalid("permit integrity epoch and revision are required")
	}
	if permit.OriginID <= 0 || permit.ChannelID <= 0 || permit.ModelID <= 0 {
		return configInvalid("permit origin, channel, and model ids must be positive")
	}
	if permit.OriginBaseURLRevision <= 0 || permit.OriginStatusRevision <= 0 || permit.ChannelConfigRevision <= 0 {
		return configInvalid("permit origin and channel revisions must be positive")
	}
	if permit.OriginStateGeneration <= 0 || permit.ChannelStateGeneration <= 0 {
		return configInvalid("permit state generations must be positive")
	}
	if !permit.UpstreamEndpoint.valid() {
		return configInvalid("unknown permit upstream operation")
	}
	if !permit.RequestMode.valid() {
		return configInvalid("unknown permit request mode")
	}
	return nil
}

func validateSnapshotCandidate(candidate SnapshotCandidateInput) error {
	if candidate.OriginID <= 0 || candidate.ChannelID <= 0 {
		return configInvalid("snapshot origin and channel ids must be positive")
	}
	if candidate.OriginBaseURLRevision <= 0 || candidate.OriginStatusRevision <= 0 || candidate.ChannelConfigRevision <= 0 || candidate.ChannelAdmissionRevision <= 0 {
		return configInvalid("snapshot origin and channel revisions must be positive")
	}
	return nil
}

func validateSnapshotManyInput(in SnapshotManyInput) error {
	if strings.TrimSpace(in.IntegrityEpoch) == "" || in.IntegrityRevision <= 0 {
		return configInvalid("snapshot integrity epoch and revision are required")
	}
	if in.ChannelRateRevision <= 0 || in.GlobalConcurrencyRevision <= 0 ||
		in.CircuitBreakerRevision <= 0 || in.RoutingBalanceRevision <= 0 {
		return configInvalid("snapshot control revisions must be positive")
	}
	if in.ModelID <= 0 {
		return configInvalid("snapshot model id must be positive")
	}
	return nil
}
