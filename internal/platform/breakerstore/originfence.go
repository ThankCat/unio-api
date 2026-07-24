package breakerstore

import (
	"context"
	"errors"
	"sort"
	"strconv"
	"time"

	"github.com/redis/go-redis/v9"
)

const originRoutingTerminalTTL = 24 * time.Hour

var (
	prepareOriginRoutingChangeScript = redis.NewScript(luaPrepareOriginRoutingChange)
	commitOriginRoutingChangeScript  = redis.NewScript(luaCommitOriginRoutingChange)
	abortOriginRoutingChangeScript   = redis.NewScript(luaAbortOriginRoutingChange)
	prepareOriginStatusBatchScript   = redis.NewScript(luaPrepareOriginStatusBatch)
	commitOriginStatusBatchScript    = redis.NewScript(luaCommitOriginStatusBatch)
	abortOriginStatusBatchScript     = redis.NewScript(luaAbortOriginStatusBatch)
	recoverOriginRoutingScript       = redis.NewScript(luaRecoverOriginRouting)
)

// InitOriginControl initializes a newly-created Origin control. Existing controls are never overwritten.
func (s *Store) InitOriginControl(ctx context.Context, originID, baseURLRevision, statusRevision int64, effectiveStatus string) (bool, error) {
	res, err := s.epInitControl.Run(ctx, s.client, []string{s.keys.origin(originID)},
		strconv.FormatInt(baseURLRevision, 10), strconv.FormatInt(statusRevision, 10), effectiveStatus).Result()
	if err != nil {
		return false, storeUnavailable(err, "breakerstore init origin control")
	}
	code, err := fenceCode(res)
	if err != nil {
		return false, storeUnavailable(err, "breakerstore init origin control")
	}
	return code == FenceResult("created"), nil
}

// RestoreMissingOriginControl installs PostgreSQL's current fact only when the control is absent.
// It is recovery-only and must never be called from request admission.
func (s *Store) RestoreMissingOriginControl(ctx context.Context, originID, baseURLRevision, statusRevision int64, effectiveStatus string) (bool, error) {
	res, err := s.epRestoreControl.Run(ctx, s.client, []string{s.keys.origin(originID)},
		strconv.FormatInt(baseURLRevision, 10), strconv.FormatInt(statusRevision, 10), effectiveStatus).Result()
	if err != nil {
		return false, storeUnavailable(err, "breakerstore restore origin control")
	}
	code, err := fenceCode(res)
	if err != nil {
		return false, storeUnavailable(err, "breakerstore restore origin control")
	}
	return code == FenceResult("installed"), nil
}

// FenceResult is the stable result returned by origin prepare/commit/abort scripts.
type FenceResult string

func (s *Store) originRoutingOperationKey(token string) string {
	return s.keys.base + "origin-routing:v1:op:" + token
}

// PrepareOriginStatusRevision creates one status pending fence.
func (s *Store) PrepareOriginStatusRevision(ctx context.Context, originID, currentStatusRev, nextStatusRev int64, nextEffectiveStatus, token, payload string) (FenceResult, error) {
	res, err := s.epPrepareStatus.Run(ctx, s.client,
		[]string{s.keys.origin(originID), s.originRoutingOperationKey(token)},
		strconv.FormatInt(currentStatusRev, 10), strconv.FormatInt(nextStatusRev, 10),
		nextEffectiveStatus, token, HashPayload(payload)).Result()
	return originFenceResult(res, err, "breakerstore prepare origin status")
}

// CommitOriginStatusRevision activates one prepared status fence.
func (s *Store) CommitOriginStatusRevision(ctx context.Context, originID int64, token, payload string) (FenceResult, error) {
	keys := append([]string{s.keys.origin(originID)}, s.allOriginEvidenceKeys(originID)...)
	keys = append(keys, s.originRoutingOperationKey(token))
	res, err := s.epCommitStatus.Run(ctx, s.client, keys,
		token, HashPayload(payload), originRoutingTerminalTTL.Milliseconds()).Result()
	return originFenceResult(res, err, "breakerstore commit origin status")
}

// AbortOriginStatusRevision aborts one prepared status fence. The payload is required so a reused
// token with a different immutable request cannot terminate the first operation.
func (s *Store) AbortOriginStatusRevision(ctx context.Context, originID int64, token, payload string) (FenceResult, error) {
	keys := append([]string{s.keys.origin(originID)}, s.allOriginEvidenceKeys(originID)...)
	keys = append(keys, s.originRoutingOperationKey(token))
	res, err := s.epAbortStatus.Run(ctx, s.client, keys,
		token, HashPayload(payload), originRoutingTerminalTTL.Milliseconds()).Result()
	return originFenceResult(res, err, "breakerstore abort origin status")
}

// PrepareOriginBaseURLRevision creates one BaseURL pending fence.
func (s *Store) PrepareOriginBaseURLRevision(ctx context.Context, originID, currentBaseURLRev, nextBaseURLRev int64, token, payload string) (FenceResult, error) {
	res, err := s.epPrepareBaseURL.Run(ctx, s.client,
		[]string{s.keys.origin(originID), s.originRoutingOperationKey(token)},
		strconv.FormatInt(currentBaseURLRev, 10), strconv.FormatInt(nextBaseURLRev, 10),
		token, HashPayload(payload)).Result()
	return originFenceResult(res, err, "breakerstore prepare origin base url")
}

// CommitOriginBaseURLRevision activates one prepared BaseURL fence and clears origin evidence.
func (s *Store) CommitOriginBaseURLRevision(ctx context.Context, originID int64, token, payload string) (FenceResult, error) {
	keys := append([]string{s.keys.origin(originID)}, s.allOriginEvidenceKeys(originID)...)
	keys = append(keys, s.originRoutingOperationKey(token))
	res, err := s.epCommitBaseURL.Run(ctx, s.client, keys,
		token, HashPayload(payload), originRoutingTerminalTTL.Milliseconds()).Result()
	return originFenceResult(res, err, "breakerstore commit origin base url")
}

// AbortOriginBaseURLRevision aborts one prepared BaseURL fence.
func (s *Store) AbortOriginBaseURLRevision(ctx context.Context, originID int64, token, payload string) (FenceResult, error) {
	keys := append([]string{s.keys.origin(originID)}, s.allOriginEvidenceKeys(originID)...)
	keys = append(keys, s.originRoutingOperationKey(token))
	res, err := s.epAbortBaseURL.Run(ctx, s.client, keys,
		token, HashPayload(payload), originRoutingTerminalTTL.Milliseconds()).Result()
	return originFenceResult(res, err, "breakerstore abort origin base url")
}

// OriginRoutingChange describes one atomic BaseURL + status revision change.
type OriginRoutingChange struct {
	OriginID          int64
	CurrentBaseURLRev   int64
	NextBaseURLRev      int64
	CurrentStatusRev    int64
	NextStatusRev       int64
	NextEffectiveStatus string
}

// PrepareOriginRoutingChange atomically prepares both revision fences on one Origin.
func (s *Store) PrepareOriginRoutingChange(ctx context.Context, change OriginRoutingChange, token, payload string) (FenceResult, error) {
	res, err := prepareOriginRoutingChangeScript.Run(ctx, s.client,
		[]string{s.keys.origin(change.OriginID), s.originRoutingOperationKey(token)},
		strconv.FormatInt(change.CurrentBaseURLRev, 10), strconv.FormatInt(change.NextBaseURLRev, 10),
		strconv.FormatInt(change.CurrentStatusRev, 10), strconv.FormatInt(change.NextStatusRev, 10),
		change.NextEffectiveStatus, token, HashPayload(payload)).Result()
	return originFenceResult(res, err, "breakerstore prepare origin routing change")
}

// CommitOriginRoutingChange atomically commits both revision fences and clears origin evidence once.
func (s *Store) CommitOriginRoutingChange(ctx context.Context, originID int64, token, payload string) (FenceResult, error) {
	keys := append([]string{s.keys.origin(originID)}, s.allOriginEvidenceKeys(originID)...)
	keys = append(keys, s.originRoutingOperationKey(token))
	res, err := commitOriginRoutingChangeScript.Run(ctx, s.client, keys,
		token, HashPayload(payload), originRoutingTerminalTTL.Milliseconds()).Result()
	return originFenceResult(res, err, "breakerstore commit origin routing change")
}

// AbortOriginRoutingChange atomically aborts both revision fences and clears origin evidence once.
func (s *Store) AbortOriginRoutingChange(ctx context.Context, originID int64, token, payload string) (FenceResult, error) {
	keys := append([]string{s.keys.origin(originID)}, s.allOriginEvidenceKeys(originID)...)
	keys = append(keys, s.originRoutingOperationKey(token))
	res, err := abortOriginRoutingChangeScript.Run(ctx, s.client, keys,
		token, HashPayload(payload), originRoutingTerminalTTL.Milliseconds()).Result()
	return originFenceResult(res, err, "breakerstore abort origin routing change")
}

// OriginStatusRevisionTransition is one member of a Provider status batch.
type OriginStatusRevisionTransition struct {
	OriginID          int64
	CurrentStatusRev    int64
	NextStatusRev       int64
	NextEffectiveStatus string
}

// PrepareOriginStatusRevisionBatch prepares an ordered, bounded Provider batch in one Lua invocation.
func (s *Store) PrepareOriginStatusRevisionBatch(ctx context.Context, providerID int64, transitions []OriginStatusRevisionTransition, maxBatch int, token, payload string) (FenceResult, error) {
	if err := validateOriginStatusBatch(providerID, transitions, maxBatch); err != nil {
		return "", err
	}
	keys := make([]string, 0, len(transitions)+1)
	argv := []interface{}{len(transitions), maxBatch, providerID, token, HashPayload(payload)}
	for _, transition := range transitions {
		keys = append(keys, s.keys.origin(transition.OriginID))
		argv = append(argv, transition.OriginID, transition.CurrentStatusRev, transition.NextStatusRev, transition.NextEffectiveStatus)
	}
	keys = append(keys, s.originRoutingOperationKey(token))
	res, err := prepareOriginStatusBatchScript.Run(ctx, s.client, keys, argv...).Result()
	return originFenceResult(res, err, "breakerstore prepare origin status batch")
}

// CommitOriginStatusRevisionBatch commits a whole Provider batch in one Lua invocation.
func (s *Store) CommitOriginStatusRevisionBatch(ctx context.Context, providerID int64, transitions []OriginStatusRevisionTransition, token, payload string) (FenceResult, error) {
	if err := validateOriginStatusBatch(providerID, transitions, 1024); err != nil {
		return "", err
	}
	keys := s.originBatchTerminalKeys(transitions, token)
	res, err := commitOriginStatusBatchScript.Run(ctx, s.client, keys,
		len(transitions), providerID, token, HashPayload(payload), originRoutingTerminalTTL.Milliseconds()).Result()
	return originFenceResult(res, err, "breakerstore commit origin status batch")
}

// AbortOriginStatusRevisionBatch aborts a whole Provider batch in one Lua invocation.
func (s *Store) AbortOriginStatusRevisionBatch(ctx context.Context, providerID int64, transitions []OriginStatusRevisionTransition, token, payload string) (FenceResult, error) {
	if err := validateOriginStatusBatch(providerID, transitions, 1024); err != nil {
		return "", err
	}
	keys := s.originBatchTerminalKeys(transitions, token)
	res, err := abortOriginStatusBatchScript.Run(ctx, s.client, keys,
		len(transitions), providerID, token, HashPayload(payload), originRoutingTerminalTTL.Milliseconds()).Result()
	return originFenceResult(res, err, "breakerstore abort origin status batch")
}

func (s *Store) originBatchTerminalKeys(transitions []OriginStatusRevisionTransition, token string) []string {
	keys := make([]string, 0, len(transitions)*7+1)
	for _, transition := range transitions {
		keys = append(keys, s.keys.origin(transition.OriginID))
	}
	for _, transition := range transitions {
		keys = append(keys, s.allOriginEvidenceKeys(transition.OriginID)...)
	}
	return append(keys, s.originRoutingOperationKey(token))
}

// OriginRoutingRecoveryTransition combines the immutable transition with PostgreSQL's currently
// locked business fact. Fact* is the only state recovery is allowed to install or activate.
type OriginRoutingRecoveryTransition struct {
	OriginID int64

	CurrentBaseURLRev int64
	NextBaseURLRev    int64
	CurrentStatusRev  int64
	NextStatusRev     int64
	CurrentEffective  string
	NextEffective     string

	FactBaseURLRev int64
	FactStatusRev  int64
	FactEffective  string
}

type OriginRoutingRecoveryMode string

const (
	OriginRecoveryCommitted OriginRoutingRecoveryMode = "committed"
	OriginRecoveryAborted   OriginRoutingRecoveryMode = "aborted"
)

// OriginRoutingRecovery is accepted only by the durable PostgreSQL reconciler.
type OriginRoutingRecovery struct {
	Mode        OriginRoutingRecoveryMode
	Kind        string
	ProviderID  int64
	Token       string
	PayloadHash string
	Transitions []OriginRoutingRecoveryTransition
}

// RecoverOriginRouting atomically resolves an operation using locked PostgreSQL facts. It restores
// absent controls, but never overwrites an existing conflicting control.
func (s *Store) RecoverOriginRouting(ctx context.Context, in OriginRoutingRecovery) (FenceResult, error) {
	if in.Mode != OriginRecoveryCommitted && in.Mode != OriginRecoveryAborted {
		return "", configInvalid("invalid origin routing recovery mode")
	}
	if in.ProviderID <= 0 || in.Token == "" || in.PayloadHash == "" || len(in.Transitions) == 0 || len(in.Transitions) > 1024 {
		return "", configInvalid("invalid origin routing recovery")
	}
	if !sort.SliceIsSorted(in.Transitions, func(i, j int) bool { return in.Transitions[i].OriginID < in.Transitions[j].OriginID }) {
		return "", configInvalid("origin recovery transitions must be ordered")
	}
	keys := make([]string, 0, len(in.Transitions)*7+1)
	redisProviderID := ""
	if in.Kind == "provider_status_batch" {
		redisProviderID = strconv.FormatInt(in.ProviderID, 10)
	}
	argv := []interface{}{string(in.Mode), in.Kind, len(in.Transitions), redisProviderID, in.Token, in.PayloadHash, originRoutingTerminalTTL.Milliseconds()}
	var previous int64
	for _, transition := range in.Transitions {
		if transition.OriginID <= previous {
			return "", configInvalid("origin recovery transitions must be unique")
		}
		previous = transition.OriginID
		keys = append(keys, s.keys.origin(transition.OriginID))
		argv = append(argv,
			transition.OriginID,
			transition.CurrentBaseURLRev, transition.NextBaseURLRev,
			transition.CurrentStatusRev, transition.NextStatusRev,
			transition.CurrentEffective, transition.NextEffective,
			transition.FactBaseURLRev, transition.FactStatusRev, transition.FactEffective,
		)
	}
	for _, transition := range in.Transitions {
		keys = append(keys, s.allOriginEvidenceKeys(transition.OriginID)...)
	}
	keys = append(keys, s.originRoutingOperationKey(in.Token))
	res, err := recoverOriginRoutingScript.Run(ctx, s.client, keys, argv...).Result()
	return originFenceResult(res, err, "breakerstore recover origin routing")
}

func validateOriginStatusBatch(providerID int64, transitions []OriginStatusRevisionTransition, maxBatch int) error {
	if providerID <= 0 || maxBatch < 1 || maxBatch > 1024 || len(transitions) < 1 || len(transitions) > maxBatch {
		return configInvalid("origin status batch is invalid or too large")
	}
	var previous int64
	for _, transition := range transitions {
		if transition.OriginID <= previous || transition.CurrentStatusRev < 1 ||
			transition.NextStatusRev != transition.CurrentStatusRev+1 || !validOriginEffectiveStatus(transition.NextEffectiveStatus) {
			return configInvalid("origin status batch transitions must be ordered and valid")
		}
		previous = transition.OriginID
	}
	return nil
}

func validOriginEffectiveStatus(status string) bool {
	return status == "enabled" || status == "disabled" || status == "archived"
}

func originFenceResult(res interface{}, err error, message string) (FenceResult, error) {
	if err != nil {
		return "", storeUnavailable(err, message)
	}
	code, parseErr := fenceCode(res)
	if parseErr != nil {
		return "", storeUnavailable(parseErr, message)
	}
	return code, nil
}

func fenceCode(res interface{}) (FenceResult, error) {
	arr, ok := res.([]interface{})
	if !ok || len(arr) == 0 {
		return "", errors.New("unexpected origin fence reply")
	}
	code, ok := arr[0].(string)
	if !ok || code == "" {
		return "", errors.New("unexpected origin fence code")
	}
	return FenceResult(code), nil
}
