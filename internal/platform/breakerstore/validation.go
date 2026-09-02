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
	if in.UserID <= 0 {
		return configInvalid("request admission user id must be positive")
	}
	if strings.TrimSpace(in.IntegrityEpoch) == "" || in.IntegrityRevision <= 0 {
		return configInvalid("request admission integrity epoch and revision are required")
	}
	if in.RequestRateRevision <= 0 || in.GlobalConcurrencyRevision <= 0 {
		return configInvalid("request admission control revisions must be positive")
	}
	for _, limit := range []*int64{
		in.RPMLimitOverride,
		in.RPDLimitOverride,
		in.ConcurrencyLimitOverride,
	} {
		if limit != nil && (*limit < 0 || *limit > maxLuaExactInteger) {
			return configInvalid("request admission limit overrides must be non-negative exact integers")
		}
	}
	return nil
}

func validateRequestLifecycleInput(requestAdmissionID string, userID int64, integrityEpoch string, integrityRevision int64) error {
	if strings.TrimSpace(requestAdmissionID) == "" {
		return configInvalid("request admission id is required")
	}
	if userID <= 0 {
		return configInvalid("request admission user id must be positive")
	}
	if strings.TrimSpace(integrityEpoch) == "" || integrityRevision <= 0 {
		return configInvalid("request admission integrity epoch and revision are required")
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
	if in.ProviderID <= 0 || in.ChannelID <= 0 || in.ModelID <= 0 {
		return configInvalid("origin, channel, and model ids must be positive")
	}
	if in.OriginRevision <= 0 || in.ProviderStatusRevision <= 0 || in.ChannelConfigRevision <= 0 {
		return configInvalid("origin and channel revisions must be positive")
	}
	if !in.UpstreamEndpoint.valid() {
		return configInvalid("unknown upstream operation")
	}
	if !in.RequestMode.valid() {
		return configInvalid("unknown request mode")
	}
	if in.ChannelCapacityRevision <= 0 ||
		in.GlobalConcurrencyRevision <= 0 || in.CircuitBreakerRevision <= 0 {
		return configInvalid("attempt control revisions must be positive")
	}
	if in.InputEstimate < 0 || in.InputEstimate > maxLuaExactInteger {
		return configInvalid("attempt token estimate must be a non-negative Lua-exact integer")
	}
	if err := validateAttemptAccount(in.AccountID, in.AccountConfigRevision, &in.AccountConcurrencyLimit); err != nil {
		return err
	}
	return nil
}

// validateAttemptAccount 守住账号维度的「要么整套给齐、要么整套为零」。
// 只给上限不给 ID 是最危险的一种传参错误：脚本会静默跳过全部账号门禁，
// 表现为账号并发形同虚设，而不是任何一处报错。
func validateAttemptAccount(accountID, configRevision int64, concurrencyLimit *int64) error {
	if accountID < 0 {
		return configInvalid("attempt account id must not be negative")
	}
	if accountID == 0 {
		if configRevision != 0 || (concurrencyLimit != nil && *concurrencyLimit != 0) {
			return configInvalid("attempt account fields require a positive account id")
		}
		return nil
	}
	if configRevision <= 0 {
		return configInvalid("attempt account config revision must be positive")
	}
	if concurrencyLimit != nil && (*concurrencyLimit < 0 || *concurrencyLimit > maxLuaExactInteger) {
		return configInvalid("attempt account concurrency limit must be a non-negative Lua-exact integer")
	}
	return nil
}

func validateFinishInput(permit AttemptPermit, outcome FinishOutcome) error {
	if err := validateAttemptPermit(permit); err != nil {
		return err
	}
	if !outcome.ProviderOutcome.valid() || !outcome.ChannelOutcome.valid() {
		return configInvalid("unknown breaker outcome")
	}
	if !outcome.ProviderEvidence.valid() {
		return configInvalid("unknown origin evidence category")
	}
	if outcome.ProviderEvidence != ProviderEvidenceNone &&
		(outcome.ProviderOutcome != OutcomeIgnored || outcome.ChannelOutcome != OutcomeEligibleFailure) {
		return configInvalid("origin evidence requires ignored origin and eligible channel failure outcomes")
	}
	if !outcome.RequestWriteState.valid() {
		return configInvalid("unknown request write state")
	}
	if outcome.RequestWriteState == RequestWriteNotStarted && !outcome.ResponseHeadersReceived && !outcome.FirstTokenEligible {
		return configInvalid("finish requires upstream interaction evidence")
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
	if permit.ProviderID <= 0 || permit.ChannelID <= 0 || permit.ModelID <= 0 {
		return configInvalid("permit origin, channel, and model ids must be positive")
	}
	if permit.OriginRevision <= 0 || permit.ProviderStatusRevision <= 0 || permit.ChannelConfigRevision <= 0 {
		return configInvalid("permit origin and channel revisions must be positive")
	}
	if permit.ProviderStateGeneration <= 0 || permit.ChannelStateGeneration <= 0 {
		return configInvalid("permit state generations must be positive")
	}
	if err := validateAttemptAccount(permit.AccountID, permit.AccountConfigRevision, nil); err != nil {
		return err
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
	if candidate.ProviderID <= 0 || candidate.ChannelID <= 0 {
		return configInvalid("snapshot provider and channel ids must be positive")
	}
	if candidate.OriginRevision <= 0 || candidate.ProviderStatusRevision <= 0 || candidate.ChannelConfigRevision <= 0 || candidate.ChannelCapacityRevision <= 0 {
		return configInvalid("snapshot provider and channel revisions must be positive")
	}
	return nil
}

func validateSnapshotManyInput(in SnapshotManyInput) error {
	if strings.TrimSpace(in.IntegrityEpoch) == "" || in.IntegrityRevision <= 0 {
		return configInvalid("snapshot integrity epoch and revision are required")
	}
	if in.GlobalConcurrencyRevision <= 0 ||
		in.CircuitBreakerRevision <= 0 || in.RoutingBalanceRevision <= 0 {
		return configInvalid("snapshot control revisions must be positive")
	}
	if in.ModelID <= 0 {
		return configInvalid("snapshot model id must be positive")
	}
	return nil
}
