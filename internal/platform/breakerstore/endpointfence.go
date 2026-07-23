package breakerstore

import (
	"context"
	"errors"
	"sort"
	"strconv"
	"time"

	"github.com/redis/go-redis/v9"
)

const endpointRoutingTerminalTTL = 24 * time.Hour

var (
	prepareEndpointRoutingChangeScript = redis.NewScript(luaPrepareEndpointRoutingChange)
	commitEndpointRoutingChangeScript  = redis.NewScript(luaCommitEndpointRoutingChange)
	abortEndpointRoutingChangeScript   = redis.NewScript(luaAbortEndpointRoutingChange)
	prepareEndpointStatusBatchScript   = redis.NewScript(luaPrepareEndpointStatusBatch)
	commitEndpointStatusBatchScript    = redis.NewScript(luaCommitEndpointStatusBatch)
	abortEndpointStatusBatchScript     = redis.NewScript(luaAbortEndpointStatusBatch)
	recoverEndpointRoutingScript       = redis.NewScript(luaRecoverEndpointRouting)
)

// InitEndpointControl initializes a newly-created Endpoint control. Existing controls are never overwritten.
func (s *Store) InitEndpointControl(ctx context.Context, endpointID, baseURLRevision, statusRevision int64, effectiveStatus string) (bool, error) {
	res, err := s.epInitControl.Run(ctx, s.client, []string{s.keys.endpoint(endpointID)},
		strconv.FormatInt(baseURLRevision, 10), strconv.FormatInt(statusRevision, 10), effectiveStatus).Result()
	if err != nil {
		return false, storeUnavailable(err, "breakerstore init endpoint control")
	}
	code, err := fenceCode(res)
	if err != nil {
		return false, storeUnavailable(err, "breakerstore init endpoint control")
	}
	return code == FenceResult("created"), nil
}

// RestoreMissingEndpointControl installs PostgreSQL's current fact only when the control is absent.
// It is recovery-only and must never be called from request admission.
func (s *Store) RestoreMissingEndpointControl(ctx context.Context, endpointID, baseURLRevision, statusRevision int64, effectiveStatus string) (bool, error) {
	res, err := s.epRestoreControl.Run(ctx, s.client, []string{s.keys.endpoint(endpointID)},
		strconv.FormatInt(baseURLRevision, 10), strconv.FormatInt(statusRevision, 10), effectiveStatus).Result()
	if err != nil {
		return false, storeUnavailable(err, "breakerstore restore endpoint control")
	}
	code, err := fenceCode(res)
	if err != nil {
		return false, storeUnavailable(err, "breakerstore restore endpoint control")
	}
	return code == FenceResult("installed"), nil
}

// FenceResult is the stable result returned by endpoint prepare/commit/abort scripts.
type FenceResult string

func (s *Store) endpointRoutingOperationKey(token string) string {
	return s.keys.base + "endpoint-routing:v1:op:" + token
}

// PrepareEndpointStatusRevision creates one status pending fence.
func (s *Store) PrepareEndpointStatusRevision(ctx context.Context, endpointID, currentStatusRev, nextStatusRev int64, nextEffectiveStatus, token, payload string) (FenceResult, error) {
	res, err := s.epPrepareStatus.Run(ctx, s.client,
		[]string{s.keys.endpoint(endpointID), s.endpointRoutingOperationKey(token)},
		strconv.FormatInt(currentStatusRev, 10), strconv.FormatInt(nextStatusRev, 10),
		nextEffectiveStatus, token, HashPayload(payload)).Result()
	return endpointFenceResult(res, err, "breakerstore prepare endpoint status")
}

// CommitEndpointStatusRevision activates one prepared status fence.
func (s *Store) CommitEndpointStatusRevision(ctx context.Context, endpointID int64, token, payload string) (FenceResult, error) {
	keys := append([]string{s.keys.endpoint(endpointID)}, s.allEndpointEvidenceKeys(endpointID)...)
	keys = append(keys, s.endpointRoutingOperationKey(token))
	res, err := s.epCommitStatus.Run(ctx, s.client, keys,
		token, HashPayload(payload), endpointRoutingTerminalTTL.Milliseconds()).Result()
	return endpointFenceResult(res, err, "breakerstore commit endpoint status")
}

// AbortEndpointStatusRevision aborts one prepared status fence. The payload is required so a reused
// token with a different immutable request cannot terminate the first operation.
func (s *Store) AbortEndpointStatusRevision(ctx context.Context, endpointID int64, token, payload string) (FenceResult, error) {
	keys := append([]string{s.keys.endpoint(endpointID)}, s.allEndpointEvidenceKeys(endpointID)...)
	keys = append(keys, s.endpointRoutingOperationKey(token))
	res, err := s.epAbortStatus.Run(ctx, s.client, keys,
		token, HashPayload(payload), endpointRoutingTerminalTTL.Milliseconds()).Result()
	return endpointFenceResult(res, err, "breakerstore abort endpoint status")
}

// PrepareEndpointBaseURLRevision creates one BaseURL pending fence.
func (s *Store) PrepareEndpointBaseURLRevision(ctx context.Context, endpointID, currentBaseURLRev, nextBaseURLRev int64, token, payload string) (FenceResult, error) {
	res, err := s.epPrepareBaseURL.Run(ctx, s.client,
		[]string{s.keys.endpoint(endpointID), s.endpointRoutingOperationKey(token)},
		strconv.FormatInt(currentBaseURLRev, 10), strconv.FormatInt(nextBaseURLRev, 10),
		token, HashPayload(payload)).Result()
	return endpointFenceResult(res, err, "breakerstore prepare endpoint base url")
}

// CommitEndpointBaseURLRevision activates one prepared BaseURL fence and clears endpoint evidence.
func (s *Store) CommitEndpointBaseURLRevision(ctx context.Context, endpointID int64, token, payload string) (FenceResult, error) {
	keys := append([]string{s.keys.endpoint(endpointID)}, s.allEndpointEvidenceKeys(endpointID)...)
	keys = append(keys, s.endpointRoutingOperationKey(token))
	res, err := s.epCommitBaseURL.Run(ctx, s.client, keys,
		token, HashPayload(payload), endpointRoutingTerminalTTL.Milliseconds()).Result()
	return endpointFenceResult(res, err, "breakerstore commit endpoint base url")
}

// AbortEndpointBaseURLRevision aborts one prepared BaseURL fence.
func (s *Store) AbortEndpointBaseURLRevision(ctx context.Context, endpointID int64, token, payload string) (FenceResult, error) {
	keys := append([]string{s.keys.endpoint(endpointID)}, s.allEndpointEvidenceKeys(endpointID)...)
	keys = append(keys, s.endpointRoutingOperationKey(token))
	res, err := s.epAbortBaseURL.Run(ctx, s.client, keys,
		token, HashPayload(payload), endpointRoutingTerminalTTL.Milliseconds()).Result()
	return endpointFenceResult(res, err, "breakerstore abort endpoint base url")
}

// EndpointRoutingChange describes one atomic BaseURL + status revision change.
type EndpointRoutingChange struct {
	EndpointID          int64
	CurrentBaseURLRev   int64
	NextBaseURLRev      int64
	CurrentStatusRev    int64
	NextStatusRev       int64
	NextEffectiveStatus string
}

// PrepareEndpointRoutingChange atomically prepares both revision fences on one Endpoint.
func (s *Store) PrepareEndpointRoutingChange(ctx context.Context, change EndpointRoutingChange, token, payload string) (FenceResult, error) {
	res, err := prepareEndpointRoutingChangeScript.Run(ctx, s.client,
		[]string{s.keys.endpoint(change.EndpointID), s.endpointRoutingOperationKey(token)},
		strconv.FormatInt(change.CurrentBaseURLRev, 10), strconv.FormatInt(change.NextBaseURLRev, 10),
		strconv.FormatInt(change.CurrentStatusRev, 10), strconv.FormatInt(change.NextStatusRev, 10),
		change.NextEffectiveStatus, token, HashPayload(payload)).Result()
	return endpointFenceResult(res, err, "breakerstore prepare endpoint routing change")
}

// CommitEndpointRoutingChange atomically commits both revision fences and clears endpoint evidence once.
func (s *Store) CommitEndpointRoutingChange(ctx context.Context, endpointID int64, token, payload string) (FenceResult, error) {
	keys := append([]string{s.keys.endpoint(endpointID)}, s.allEndpointEvidenceKeys(endpointID)...)
	keys = append(keys, s.endpointRoutingOperationKey(token))
	res, err := commitEndpointRoutingChangeScript.Run(ctx, s.client, keys,
		token, HashPayload(payload), endpointRoutingTerminalTTL.Milliseconds()).Result()
	return endpointFenceResult(res, err, "breakerstore commit endpoint routing change")
}

// AbortEndpointRoutingChange atomically aborts both revision fences and clears endpoint evidence once.
func (s *Store) AbortEndpointRoutingChange(ctx context.Context, endpointID int64, token, payload string) (FenceResult, error) {
	keys := append([]string{s.keys.endpoint(endpointID)}, s.allEndpointEvidenceKeys(endpointID)...)
	keys = append(keys, s.endpointRoutingOperationKey(token))
	res, err := abortEndpointRoutingChangeScript.Run(ctx, s.client, keys,
		token, HashPayload(payload), endpointRoutingTerminalTTL.Milliseconds()).Result()
	return endpointFenceResult(res, err, "breakerstore abort endpoint routing change")
}

// EndpointStatusRevisionTransition is one member of a Provider status batch.
type EndpointStatusRevisionTransition struct {
	EndpointID          int64
	CurrentStatusRev    int64
	NextStatusRev       int64
	NextEffectiveStatus string
}

// PrepareEndpointStatusRevisionBatch prepares an ordered, bounded Provider batch in one Lua invocation.
func (s *Store) PrepareEndpointStatusRevisionBatch(ctx context.Context, providerID int64, transitions []EndpointStatusRevisionTransition, maxBatch int, token, payload string) (FenceResult, error) {
	if err := validateEndpointStatusBatch(providerID, transitions, maxBatch); err != nil {
		return "", err
	}
	keys := make([]string, 0, len(transitions)+1)
	argv := []interface{}{len(transitions), maxBatch, providerID, token, HashPayload(payload)}
	for _, transition := range transitions {
		keys = append(keys, s.keys.endpoint(transition.EndpointID))
		argv = append(argv, transition.EndpointID, transition.CurrentStatusRev, transition.NextStatusRev, transition.NextEffectiveStatus)
	}
	keys = append(keys, s.endpointRoutingOperationKey(token))
	res, err := prepareEndpointStatusBatchScript.Run(ctx, s.client, keys, argv...).Result()
	return endpointFenceResult(res, err, "breakerstore prepare endpoint status batch")
}

// CommitEndpointStatusRevisionBatch commits a whole Provider batch in one Lua invocation.
func (s *Store) CommitEndpointStatusRevisionBatch(ctx context.Context, providerID int64, transitions []EndpointStatusRevisionTransition, token, payload string) (FenceResult, error) {
	if err := validateEndpointStatusBatch(providerID, transitions, 1024); err != nil {
		return "", err
	}
	keys := s.endpointBatchTerminalKeys(transitions, token)
	res, err := commitEndpointStatusBatchScript.Run(ctx, s.client, keys,
		len(transitions), providerID, token, HashPayload(payload), endpointRoutingTerminalTTL.Milliseconds()).Result()
	return endpointFenceResult(res, err, "breakerstore commit endpoint status batch")
}

// AbortEndpointStatusRevisionBatch aborts a whole Provider batch in one Lua invocation.
func (s *Store) AbortEndpointStatusRevisionBatch(ctx context.Context, providerID int64, transitions []EndpointStatusRevisionTransition, token, payload string) (FenceResult, error) {
	if err := validateEndpointStatusBatch(providerID, transitions, 1024); err != nil {
		return "", err
	}
	keys := s.endpointBatchTerminalKeys(transitions, token)
	res, err := abortEndpointStatusBatchScript.Run(ctx, s.client, keys,
		len(transitions), providerID, token, HashPayload(payload), endpointRoutingTerminalTTL.Milliseconds()).Result()
	return endpointFenceResult(res, err, "breakerstore abort endpoint status batch")
}

func (s *Store) endpointBatchTerminalKeys(transitions []EndpointStatusRevisionTransition, token string) []string {
	keys := make([]string, 0, len(transitions)*7+1)
	for _, transition := range transitions {
		keys = append(keys, s.keys.endpoint(transition.EndpointID))
	}
	for _, transition := range transitions {
		keys = append(keys, s.allEndpointEvidenceKeys(transition.EndpointID)...)
	}
	return append(keys, s.endpointRoutingOperationKey(token))
}

// EndpointRoutingRecoveryTransition combines the immutable transition with PostgreSQL's currently
// locked business fact. Fact* is the only state recovery is allowed to install or activate.
type EndpointRoutingRecoveryTransition struct {
	EndpointID int64

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

type EndpointRoutingRecoveryMode string

const (
	EndpointRecoveryCommitted EndpointRoutingRecoveryMode = "committed"
	EndpointRecoveryAborted   EndpointRoutingRecoveryMode = "aborted"
)

// EndpointRoutingRecovery is accepted only by the durable PostgreSQL reconciler.
type EndpointRoutingRecovery struct {
	Mode        EndpointRoutingRecoveryMode
	Kind        string
	ProviderID  int64
	Token       string
	PayloadHash string
	Transitions []EndpointRoutingRecoveryTransition
}

// RecoverEndpointRouting atomically resolves an operation using locked PostgreSQL facts. It restores
// absent controls, but never overwrites an existing conflicting control.
func (s *Store) RecoverEndpointRouting(ctx context.Context, in EndpointRoutingRecovery) (FenceResult, error) {
	if in.Mode != EndpointRecoveryCommitted && in.Mode != EndpointRecoveryAborted {
		return "", configInvalid("invalid endpoint routing recovery mode")
	}
	if in.ProviderID <= 0 || in.Token == "" || in.PayloadHash == "" || len(in.Transitions) == 0 || len(in.Transitions) > 1024 {
		return "", configInvalid("invalid endpoint routing recovery")
	}
	if !sort.SliceIsSorted(in.Transitions, func(i, j int) bool { return in.Transitions[i].EndpointID < in.Transitions[j].EndpointID }) {
		return "", configInvalid("endpoint recovery transitions must be ordered")
	}
	keys := make([]string, 0, len(in.Transitions)*7+1)
	redisProviderID := ""
	if in.Kind == "provider_status_batch" {
		redisProviderID = strconv.FormatInt(in.ProviderID, 10)
	}
	argv := []interface{}{string(in.Mode), in.Kind, len(in.Transitions), redisProviderID, in.Token, in.PayloadHash, endpointRoutingTerminalTTL.Milliseconds()}
	var previous int64
	for _, transition := range in.Transitions {
		if transition.EndpointID <= previous {
			return "", configInvalid("endpoint recovery transitions must be unique")
		}
		previous = transition.EndpointID
		keys = append(keys, s.keys.endpoint(transition.EndpointID))
		argv = append(argv,
			transition.EndpointID,
			transition.CurrentBaseURLRev, transition.NextBaseURLRev,
			transition.CurrentStatusRev, transition.NextStatusRev,
			transition.CurrentEffective, transition.NextEffective,
			transition.FactBaseURLRev, transition.FactStatusRev, transition.FactEffective,
		)
	}
	for _, transition := range in.Transitions {
		keys = append(keys, s.allEndpointEvidenceKeys(transition.EndpointID)...)
	}
	keys = append(keys, s.endpointRoutingOperationKey(in.Token))
	res, err := recoverEndpointRoutingScript.Run(ctx, s.client, keys, argv...).Result()
	return endpointFenceResult(res, err, "breakerstore recover endpoint routing")
}

func validateEndpointStatusBatch(providerID int64, transitions []EndpointStatusRevisionTransition, maxBatch int) error {
	if providerID <= 0 || maxBatch < 1 || maxBatch > 1024 || len(transitions) < 1 || len(transitions) > maxBatch {
		return configInvalid("endpoint status batch is invalid or too large")
	}
	var previous int64
	for _, transition := range transitions {
		if transition.EndpointID <= previous || transition.CurrentStatusRev < 1 ||
			transition.NextStatusRev != transition.CurrentStatusRev+1 || !validEndpointEffectiveStatus(transition.NextEffectiveStatus) {
			return configInvalid("endpoint status batch transitions must be ordered and valid")
		}
		previous = transition.EndpointID
	}
	return nil
}

func validEndpointEffectiveStatus(status string) bool {
	return status == "enabled" || status == "disabled" || status == "archived"
}

func endpointFenceResult(res interface{}, err error, message string) (FenceResult, error) {
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
		return "", errors.New("unexpected endpoint fence reply")
	}
	code, ok := arr[0].(string)
	if !ok || code == "" {
		return "", errors.New("unexpected endpoint fence code")
	}
	return FenceResult(code), nil
}
