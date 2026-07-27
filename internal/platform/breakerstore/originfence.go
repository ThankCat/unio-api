package breakerstore

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"
)

const providerRoutingTerminalTTL = 24 * time.Hour

// InitProviderControl initializes a newly-created Provider control. Existing controls are never overwritten.
func (s *Store) InitProviderControl(ctx context.Context, providerID, baseURLRevision, statusRevision int64, effectiveStatus string) (bool, error) {
	res, err := s.epInitControl.Run(ctx, s.client, []string{s.keys.provider(providerID)},
		strconv.FormatInt(baseURLRevision, 10), strconv.FormatInt(statusRevision, 10), effectiveStatus).Result()
	if err != nil {
		return false, storeUnavailable(err, "breakerstore init provider control")
	}
	code, err := fenceCode(res)
	if err != nil {
		return false, storeUnavailable(err, "breakerstore init provider control")
	}
	return code == FenceResult("created"), nil
}

// RestoreMissingProviderControl installs PostgreSQL's current fact only when the control is absent.
// It is recovery-only and must never be called from request admission.
func (s *Store) RestoreMissingProviderControl(ctx context.Context, providerID, baseURLRevision, statusRevision int64, effectiveStatus string) (bool, error) {
	res, err := s.epRestoreControl.Run(ctx, s.client, []string{s.keys.provider(providerID)},
		strconv.FormatInt(baseURLRevision, 10), strconv.FormatInt(statusRevision, 10), effectiveStatus).Result()
	if err != nil {
		return false, storeUnavailable(err, "breakerstore restore provider control")
	}
	code, err := fenceCode(res)
	if err != nil {
		return false, storeUnavailable(err, "breakerstore restore provider control")
	}
	return code == FenceResult("installed"), nil
}

// FenceResult is the stable result returned by origin prepare/commit/abort scripts.
type FenceResult string

func (s *Store) providerRoutingOperationKey(token string) string {
	return s.keys.base + "provider-routing:v1:op:" + token
}

// PurgeProviderRuntime removes retired Provider control, breaker evidence and terminal routing
// operation records. Attempt permits and request/channel admission leases remain untouched so
// already-admitted requests can finish normally.
func (s *Store) PurgeProviderRuntime(ctx context.Context, providerID int64) error {
	if providerID <= 0 {
		return configInvalid("provider id must be positive")
	}
	keys := append([]string{s.keys.provider(providerID)}, s.allProviderEvidenceKeys(providerID)...)
	var cursor uint64
	pattern := s.keys.base + "provider-routing:v1:op:*"
	for {
		operationKeys, next, err := s.client.Scan(ctx, cursor, pattern, 100).Result()
		if err != nil {
			return storeUnavailable(err, "breakerstore scan provider routing operations")
		}
		for _, operationKey := range operationKeys {
			fields, err := s.client.HMGet(ctx, operationKey, "provider_id", "state").Result()
			if err != nil {
				return storeUnavailable(err, "breakerstore read provider routing operation")
			}
			if len(fields) != 2 || fmt.Sprint(fields[0]) != strconv.FormatInt(providerID, 10) {
				continue
			}
			state := fmt.Sprint(fields[1])
			if state == "committed" || state == "aborted" {
				keys = append(keys, operationKey)
			}
		}
		cursor = next
		if cursor == 0 {
			break
		}
	}
	if err := s.client.Del(ctx, keys...).Err(); err != nil {
		return storeUnavailable(err, "breakerstore purge provider runtime")
	}
	return nil
}

// PrepareProviderStatusRevision creates one status pending fence.
func (s *Store) PrepareProviderStatusRevision(ctx context.Context, providerID, currentStatusRev, nextStatusRev int64, nextEffectiveStatus, token, payload string) (FenceResult, error) {
	res, err := s.epPrepareStatus.Run(ctx, s.client,
		[]string{s.keys.provider(providerID), s.providerRoutingOperationKey(token)},
		strconv.FormatInt(currentStatusRev, 10), strconv.FormatInt(nextStatusRev, 10),
		nextEffectiveStatus, token, HashPayload(payload)).Result()
	return originFenceResult(res, err, "breakerstore prepare origin status")
}

// CommitProviderStatusRevision activates one prepared status fence.
func (s *Store) CommitProviderStatusRevision(ctx context.Context, providerID int64, token, payload string) (FenceResult, error) {
	keys := append([]string{s.keys.provider(providerID)}, s.allProviderEvidenceKeys(providerID)...)
	keys = append(keys, s.providerRoutingOperationKey(token))
	res, err := s.epCommitStatus.Run(ctx, s.client, keys,
		token, HashPayload(payload), providerRoutingTerminalTTL.Milliseconds()).Result()
	return originFenceResult(res, err, "breakerstore commit origin status")
}

// AbortProviderStatusRevision aborts one prepared status fence. The payload is required so a reused
// token with a different immutable request cannot terminate the first operation.
func (s *Store) AbortProviderStatusRevision(ctx context.Context, providerID int64, token, payload string) (FenceResult, error) {
	keys := append([]string{s.keys.provider(providerID)}, s.allProviderEvidenceKeys(providerID)...)
	keys = append(keys, s.providerRoutingOperationKey(token))
	res, err := s.epAbortStatus.Run(ctx, s.client, keys,
		token, HashPayload(payload), providerRoutingTerminalTTL.Milliseconds()).Result()
	return originFenceResult(res, err, "breakerstore abort origin status")
}

// PrepareOriginRevision creates one origin pending fence.
func (s *Store) PrepareOriginRevision(ctx context.Context, providerID, currentOriginRevision, nextOriginRevision int64, token, payload string) (FenceResult, error) {
	res, err := s.epPrepareOrigin.Run(ctx, s.client,
		[]string{s.keys.provider(providerID), s.providerRoutingOperationKey(token)},
		strconv.FormatInt(currentOriginRevision, 10), strconv.FormatInt(nextOriginRevision, 10),
		token, HashPayload(payload)).Result()
	return originFenceResult(res, err, "breakerstore prepare origin base url")
}

// CommitOriginRevision activates one prepared origin fence and clears Provider evidence.
func (s *Store) CommitOriginRevision(ctx context.Context, providerID int64, token, payload string) (FenceResult, error) {
	keys := append([]string{s.keys.provider(providerID)}, s.allProviderEvidenceKeys(providerID)...)
	keys = append(keys, s.providerRoutingOperationKey(token))
	res, err := s.epCommitOrigin.Run(ctx, s.client, keys,
		token, HashPayload(payload), providerRoutingTerminalTTL.Milliseconds()).Result()
	return originFenceResult(res, err, "breakerstore commit origin base url")
}

// AbortOriginRevision aborts one prepared origin fence.
func (s *Store) AbortOriginRevision(ctx context.Context, providerID int64, token, payload string) (FenceResult, error) {
	keys := append([]string{s.keys.provider(providerID)}, s.allProviderEvidenceKeys(providerID)...)
	keys = append(keys, s.providerRoutingOperationKey(token))
	res, err := s.epAbortOrigin.Run(ctx, s.client, keys,
		token, HashPayload(payload), providerRoutingTerminalTTL.Milliseconds()).Result()
	return originFenceResult(res, err, "breakerstore abort origin base url")
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
		return "", errors.New("unexpected provider fence reply")
	}
	code, ok := arr[0].(string)
	if !ok || code == "" {
		return "", errors.New("unexpected provider fence code")
	}
	return FenceResult(code), nil
}
