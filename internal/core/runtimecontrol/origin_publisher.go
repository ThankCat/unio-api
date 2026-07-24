package runtimecontrol

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ThankCat/unio-gateway/internal/platform/breakerstore"
	"github.com/ThankCat/unio-gateway/internal/platform/failure"
	"github.com/ThankCat/unio-gateway/internal/platform/store/sqlc"
)

// Origin routing operation kinds match origin_routing_operations.kind.
const (
	OriginFenceKindBaseURL             = "base_url"
	OriginFenceKindStatus              = "status"
	OriginFenceKindBaseURLStatus       = "base_url_status"
	OriginFenceKindProviderStatusBatch = "provider_status_batch"
)

const (
	fencePrepared  = "prepared"
	fenceCommitted = "committed"
	fenceAborted   = "aborted"
)

// OriginRoutingTransition is the durable, URL-free revision summary stored in PostgreSQL.
// Both revision families are always present so recovery can validate the full Origin identity.
type OriginRoutingTransition struct {
	OriginID int64 `json:"origin_id"`

	CurrentBaseURLRevision int64 `json:"current_base_url_revision"`
	NextBaseURLRevision    int64 `json:"next_base_url_revision"`
	CurrentStatusRevision  int64 `json:"current_status_revision"`
	NextStatusRevision     int64 `json:"next_status_revision"`

	CurrentEffectiveStatus string `json:"current_effective_status"`
	NextEffectiveStatus    string `json:"next_effective_status"`
}

// OriginRoutingEnvelope is the strict JSONB shape stored in origin_routing_operations.transitions.
type OriginRoutingEnvelope struct {
	Kind                  string                      `json:"kind"`
	ProviderID            int64                       `json:"provider_id"`
	CurrentProviderStatus string                      `json:"current_provider_status"`
	NextProviderStatus    string                      `json:"next_provider_status"`
	Transitions           []OriginRoutingTransition `json:"transitions"`
}

type originRoutingPayload struct {
	Operation   OriginRoutingEnvelope `json:"operation"`
	NextBaseURL string                  `json:"next_base_url,omitempty"`
}

// CanonicalOriginRoutingOperation returns the durable URL-free transition JSON and the complete
// canonical payload used for payload_hash. nextBaseURL is present only for BaseURL/combined operations.
func CanonicalOriginRoutingOperation(envelope OriginRoutingEnvelope, nextBaseURL string, maxBatch int) ([]byte, string, error) {
	if err := validateOriginRoutingEnvelope(envelope, nextBaseURL, maxBatch); err != nil {
		return nil, "", err
	}
	durable, err := json.Marshal(envelope)
	if err != nil {
		return nil, "", err
	}
	payload, err := json.Marshal(originRoutingPayload{Operation: envelope, NextBaseURL: nextBaseURL})
	if err != nil {
		return nil, "", err
	}
	return durable, string(payload), nil
}

// ParseOriginRoutingEnvelope strictly decodes the durable transitions JSON.
func ParseOriginRoutingEnvelope(raw []byte, maxBatch int) (OriginRoutingEnvelope, error) {
	var envelope OriginRoutingEnvelope
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&envelope); err != nil {
		return OriginRoutingEnvelope{}, fmt.Errorf("decode origin routing transitions: %w", err)
	}
	if err := ensureOriginJSONEOF(dec); err != nil {
		return OriginRoutingEnvelope{}, err
	}
	nextBaseURL := ""
	if envelope.Kind == OriginFenceKindBaseURL || envelope.Kind == OriginFenceKindBaseURLStatus {
		// The URL is deliberately absent from durable transitions; validation of its presence belongs to
		// CanonicalOriginRoutingOperation or db_committed recovery after reading the business row.
		nextBaseURL = "recovery-placeholder"
	}
	if err := validateOriginRoutingEnvelope(envelope, nextBaseURL, maxBatch); err != nil {
		return OriginRoutingEnvelope{}, err
	}
	return envelope, nil
}

func ensureOriginJSONEOF(dec *json.Decoder) error {
	var extra any
	if err := dec.Decode(&extra); errors.Is(err, io.EOF) {
		return nil
	} else if err != nil {
		return fmt.Errorf("decode origin routing trailing JSON: %w", err)
	}
	return errors.New("origin routing transitions contain multiple JSON values")
}

func validateOriginRoutingEnvelope(envelope OriginRoutingEnvelope, nextBaseURL string, maxBatch int) error {
	if envelope.ProviderID <= 0 || !validRoutingStatus(envelope.CurrentProviderStatus) || !validRoutingStatus(envelope.NextProviderStatus) {
		return errors.New("origin routing operation has invalid provider identity/status")
	}
	if maxBatch < 1 || maxBatch > 1024 {
		return errors.New("origin routing max batch must be within [1,1024]")
	}
	count := len(envelope.Transitions)
	if count == 0 || count > maxBatch {
		return errors.New("origin routing transition batch is empty or too large")
	}
	if envelope.Kind != OriginFenceKindProviderStatusBatch && count != 1 {
		return errors.New("single origin routing operation must have one transition")
	}
	if envelope.Kind == OriginFenceKindProviderStatusBatch && envelope.CurrentProviderStatus == envelope.NextProviderStatus {
		return errors.New("provider status batch requires a provider status change")
	}
	if (envelope.Kind == OriginFenceKindBaseURL || envelope.Kind == OriginFenceKindBaseURLStatus) != (nextBaseURL != "") {
		return errors.New("origin routing BaseURL payload is missing or unexpected")
	}
	var previous int64
	for _, transition := range envelope.Transitions {
		if transition.OriginID <= previous || transition.CurrentBaseURLRevision < 1 || transition.CurrentStatusRevision < 1 ||
			!validRoutingStatus(transition.CurrentEffectiveStatus) || !validRoutingStatus(transition.NextEffectiveStatus) {
			return errors.New("origin routing transitions must be positive, unique, ordered and have valid status")
		}
		previous = transition.OriginID
		switch envelope.Kind {
		case OriginFenceKindBaseURL:
			if transition.NextBaseURLRevision != transition.CurrentBaseURLRevision+1 ||
				transition.NextStatusRevision != transition.CurrentStatusRevision ||
				transition.NextEffectiveStatus != transition.CurrentEffectiveStatus ||
				envelope.CurrentProviderStatus != envelope.NextProviderStatus {
				return errors.New("invalid BaseURL transition")
			}
		case OriginFenceKindStatus:
			if transition.NextBaseURLRevision != transition.CurrentBaseURLRevision ||
				transition.NextStatusRevision != transition.CurrentStatusRevision+1 ||
				envelope.CurrentProviderStatus != envelope.NextProviderStatus {
				return errors.New("invalid status transition")
			}
		case OriginFenceKindBaseURLStatus:
			if transition.NextBaseURLRevision != transition.CurrentBaseURLRevision+1 ||
				transition.NextStatusRevision != transition.CurrentStatusRevision+1 ||
				envelope.CurrentProviderStatus != envelope.NextProviderStatus {
				return errors.New("invalid combined transition")
			}
		case OriginFenceKindProviderStatusBatch:
			if transition.NextBaseURLRevision != transition.CurrentBaseURLRevision ||
				transition.NextStatusRevision != transition.CurrentStatusRevision+1 {
				return errors.New("invalid provider status batch transition")
			}
		default:
			return errors.New("unsupported origin routing operation kind")
		}
	}
	return nil
}

// EffectiveOriginStatus combines Provider and Origin status for Redis admission control.
func EffectiveOriginStatus(providerStatus, originStatus string) string {
	if providerStatus == "archived" || originStatus == "archived" {
		return "archived"
	}
	if providerStatus != "enabled" || originStatus != "enabled" {
		return "disabled"
	}
	return "enabled"
}

func validRoutingStatus(status string) bool {
	return status == "enabled" || status == "disabled" || status == "archived"
}

// OriginFenceRequest describes one durable single or batch fence publication.
type OriginFenceRequest struct {
	Kind        string
	Token       string
	OriginID  int64  // zero for provider batch
	ProviderID  *int64 // required for every production operation
	Transitions []byte
	Payload     string // complete canonical payload; hash may include target BaseURL absent from Transitions
	MaxBatch    int

	Prepare func(ctx context.Context) (breakerstore.FenceResult, error)
	Commit  func(ctx context.Context) (breakerstore.FenceResult, error)
	Abort   func(ctx context.Context) (breakerstore.FenceResult, error)

	// ValidateLocked runs after Provider, ordered Origin rows and operation have been locked.
	ValidateLocked func(ctx context.Context, tx pgx.Tx) error
	// BusinessCommit runs in that same transaction before operation -> db_committed.
	BusinessCommit func(ctx context.Context, tx pgx.Tx) error
}

// OriginFencePublisher coordinates PostgreSQL durable operations and Redis origin fences.
type OriginFencePublisher struct {
	pool *pgxpool.Pool
}

func NewOriginFencePublisher(pool *pgxpool.Pool) *OriginFencePublisher {
	if pool == nil {
		panic("runtimecontrol: origin fence publisher requires pool")
	}
	return &OriginFencePublisher{pool: pool}
}

// WithOriginLocks runs a non-fenced PostgreSQL change under the same Provider -> Origin ID lock
// order. It is used only when an Origin business field changes without changing effective routing
// revisions, so no Redis publication is required.
func (p *OriginFencePublisher) WithOriginLocks(
	ctx context.Context,
	providerID int64,
	originIDs []int64,
	fn func(context.Context, pgx.Tx) error,
) error {
	if providerID <= 0 || fn == nil ||
		!sort.SliceIsSorted(originIDs, func(i, j int) bool { return originIDs[i] < originIDs[j] }) {
		return originFenceInvalid("invalid locked origin update")
	}
	for i, id := range originIDs {
		if id <= 0 || (i > 0 && originIDs[i-1] == id) {
			return originFenceInvalid("locked origin IDs must be positive and unique")
		}
	}
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var lockedProvider int64
	if err := tx.QueryRow(ctx, `SELECT id FROM providers WHERE id=$1 FOR UPDATE`, providerID).Scan(&lockedProvider); err != nil {
		return err
	}
	if len(originIDs) > 0 {
		rows, err := tx.Query(ctx, `SELECT id FROM provider_origins WHERE id = ANY($1::bigint[]) ORDER BY id FOR UPDATE`, originIDs)
		if err != nil {
			return err
		}
		lockedIDs := make([]int64, 0, len(originIDs))
		for rows.Next() {
			var id int64
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return err
			}
			lockedIDs = append(lockedIDs, id)
		}
		rows.Close()
		if rows.Err() != nil {
			return rows.Err()
		}
		if !equalInt64s(lockedIDs, originIDs) {
			return originFenceInvalid("locked origin target set changed")
		}
	}
	if err := fn(ctx, tx); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// Publish executes preparing -> Redis Prepare -> prepared -> locked business transaction/db_committed
// -> Redis Commit -> committed. PostgreSQL lock order is always Provider -> Origin ID -> operation.
func (p *OriginFencePublisher) Publish(ctx context.Context, req OriginFenceRequest) (PublishResult, error) {
	if req.Token == "" || req.ProviderID == nil || *req.ProviderID <= 0 || req.Payload == "" ||
		req.Prepare == nil || req.Commit == nil || req.Abort == nil || req.BusinessCommit == nil {
		return PublishResult{}, originFenceInvalid("origin fence requires provider/token/payload/callbacks")
	}
	if req.MaxBatch == 0 {
		req.MaxBatch = 1024
	}
	envelope, err := ParseOriginRoutingEnvelope(req.Transitions, req.MaxBatch)
	if err != nil {
		return PublishResult{}, originFenceInvalid(err.Error())
	}
	if envelope.Kind != req.Kind || envelope.ProviderID != *req.ProviderID {
		return PublishResult{}, originFenceInvalid("origin fence request conflicts with transitions")
	}
	originIDs := originIDsOf(envelope.Transitions)
	if req.Kind == OriginFenceKindProviderStatusBatch {
		if req.OriginID != 0 {
			return PublishResult{}, originFenceInvalid("provider batch must not set origin_id")
		}
	} else if req.OriginID <= 0 || len(originIDs) != 1 || originIDs[0] != req.OriginID {
		return PublishResult{}, originFenceInvalid("single origin target conflicts with transitions")
	}

	payloadHash := breakerstore.HashPayload(req.Payload)
	q := sqlc.New(p.pool)
	op, err := q.CreateOriginRoutingOperation(ctx, sqlc.CreateOriginRoutingOperationParams{
		Token:       req.Token,
		Kind:        req.Kind,
		ProviderID:  pgtype.Int8{Int64: *req.ProviderID, Valid: true},
		OriginID:  nullableOriginID(req.OriginID),
		Transitions: req.Transitions,
		PayloadHash: payloadHash,
	})
	if err != nil {
		op, err = q.GetOriginRoutingOperationByToken(ctx, req.Token)
		if err != nil {
			return PublishResult{}, originFenceInvalid("another origin routing operation is active")
		}
	}
	if !sameOriginOperation(op, req, payloadHash) {
		return PublishResult{}, originFenceInvalid("origin operation token conflicts with immutable request")
	}
	switch op.State {
	case "committed":
		return PublishResult{State: PublishCommitted}, nil
	case "aborted":
		return PublishResult{State: PublishAborted}, originFenceInvalid("origin operation is already aborted")
	case "db_committed":
		return p.finishRedisCommit(ctx, q, req, payloadHash)
	case "preparing", "prepared":
	default:
		return PublishResult{}, originFenceInvalid("origin operation has invalid durable state")
	}

	prepared, prepareErr := req.Prepare(ctx)
	if prepareErr != nil {
		abortErr := p.abortUncommitted(ctx, q, req, payloadHash)
		if abortErr != nil {
			return PublishResult{}, errors.Join(prepareErr, abortErr)
		}
		return PublishResult{}, prepareErr
	}
	switch string(prepared) {
	case fencePrepared:
	case fenceAborted:
		_, _ = q.MarkOriginRoutingOperationAborted(ctx, sqlc.MarkOriginRoutingOperationAbortedParams{Token: req.Token, PayloadHash: payloadHash})
		return PublishResult{State: PublishAborted}, originFenceInvalid("origin fence was already aborted")
	case fenceCommitted:
		return PublishResult{State: PublishRuntimeSyncPending}, originFenceInvalid("redis committed before durable business state")
	default:
		if abortErr := p.abortUncommitted(ctx, q, req, payloadHash); abortErr != nil {
			return PublishResult{}, abortErr
		}
		return PublishResult{State: PublishAborted}, originFenceInvalid("origin fence prepare rejected (" + string(prepared) + ")")
	}

	if op.State == "preparing" {
		rows, err := q.MarkOriginRoutingOperationPrepared(ctx, sqlc.MarkOriginRoutingOperationPreparedParams{Token: req.Token, PayloadHash: payloadHash})
		if err != nil || rows != 1 {
			abortErr := p.abortUncommitted(ctx, q, req, payloadHash)
			if err != nil {
				return PublishResult{}, errors.Join(err, abortErr)
			}
			return PublishResult{}, errors.Join(originFenceInvalid("origin operation could not become prepared"), abortErr)
		}
	}

	if err := p.commitBusiness(ctx, req, envelope, originIDs, payloadHash); err != nil {
		abortErr := p.abortUncommitted(ctx, q, req, payloadHash)
		if abortErr != nil {
			return PublishResult{}, errors.Join(err, abortErr)
		}
		return PublishResult{}, err
	}
	return p.finishRedisCommit(ctx, q, req, payloadHash)
}

func (p *OriginFencePublisher) commitBusiness(
	ctx context.Context,
	req OriginFenceRequest,
	envelope OriginRoutingEnvelope,
	originIDs []int64,
	payloadHash string,
) error {
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return failure.Wrap(failure.CodeRequestLogStoreFailed, err, failure.WithMessage("runtimecontrol: begin origin business tx"))
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var lockedProviderID int64
	if err := tx.QueryRow(ctx, `SELECT id FROM providers WHERE id=$1 FOR UPDATE`, envelope.ProviderID).Scan(&lockedProviderID); err != nil {
		return failure.Wrap(failure.CodeRequestLogStoreFailed, err, failure.WithMessage("runtimecontrol: lock origin provider"))
	}
	rows, err := tx.Query(ctx, `SELECT id FROM provider_origins WHERE id = ANY($1::bigint[]) ORDER BY id FOR UPDATE`, originIDs)
	if err != nil {
		return failure.Wrap(failure.CodeRequestLogStoreFailed, err, failure.WithMessage("runtimecontrol: lock provider origins"))
	}
	lockedIDs := make([]int64, 0, len(originIDs))
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		lockedIDs = append(lockedIDs, id)
	}
	rows.Close()
	if rows.Err() != nil {
		return rows.Err()
	}
	if !equalInt64s(lockedIDs, originIDs) {
		return originFenceInvalid("origin target set changed before business commit")
	}

	var lockedState, lockedKind, lockedHash string
	var lockedTransitions []byte
	if err := tx.QueryRow(ctx, `SELECT state, kind, payload_hash, transitions FROM origin_routing_operations WHERE token=$1 FOR UPDATE`, req.Token).
		Scan(&lockedState, &lockedKind, &lockedHash, &lockedTransitions); err != nil {
		return failure.Wrap(failure.CodeRequestLogStoreFailed, err, failure.WithMessage("runtimecontrol: lock origin operation"))
	}
	if lockedState != "prepared" || lockedKind != req.Kind || lockedHash != payloadHash || !sameOriginTransitionJSON(lockedTransitions, req.Transitions) {
		return originFenceInvalid("origin operation changed before business commit")
	}
	if req.ValidateLocked != nil {
		if err := req.ValidateLocked(ctx, tx); err != nil {
			return err
		}
	}
	if err := req.BusinessCommit(ctx, tx); err != nil {
		return err
	}
	changed, err := sqlc.New(tx).MarkOriginRoutingOperationDBCommitted(ctx, sqlc.MarkOriginRoutingOperationDBCommittedParams{
		Token: req.Token, PayloadHash: payloadHash,
	})
	if err != nil {
		return failure.Wrap(failure.CodeRequestLogStoreFailed, err, failure.WithMessage("runtimecontrol: mark origin db_committed"))
	}
	if changed != 1 {
		return originFenceInvalid("origin operation did not become db_committed")
	}
	if err := tx.Commit(ctx); err != nil {
		return failure.Wrap(failure.CodeRequestLogStoreFailed, err, failure.WithMessage("runtimecontrol: commit origin business tx"))
	}
	return nil
}

func (p *OriginFencePublisher) finishRedisCommit(ctx context.Context, q *sqlc.Queries, req OriginFenceRequest, payloadHash string) (PublishResult, error) {
	result, err := req.Commit(ctx)
	if err != nil || string(result) != fenceCommitted {
		return PublishResult{State: PublishRuntimeSyncPending}, nil
	}
	rows, err := q.MarkOriginRoutingOperationCommitted(ctx, sqlc.MarkOriginRoutingOperationCommittedParams{Token: req.Token, PayloadHash: payloadHash})
	if err != nil || rows != 1 {
		return PublishResult{State: PublishRuntimeSyncPending}, nil
	}
	return PublishResult{State: PublishCommitted}, nil
}

func (p *OriginFencePublisher) abortUncommitted(ctx context.Context, q *sqlc.Queries, req OriginFenceRequest, payloadHash string) error {
	result, err := req.Abort(ctx)
	if err != nil {
		return err
	}
	if string(result) != fenceAborted {
		return originFenceInvalid("origin fence abort rejected (" + string(result) + ")")
	}
	rows, err := q.MarkOriginRoutingOperationAborted(ctx, sqlc.MarkOriginRoutingOperationAbortedParams{Token: req.Token, PayloadHash: payloadHash})
	if err != nil {
		return failure.Wrap(failure.CodeRequestLogStoreFailed, err, failure.WithMessage("runtimecontrol: mark origin aborted"))
	}
	if rows == 1 {
		return nil
	}
	current, getErr := q.GetOriginRoutingOperationByToken(ctx, req.Token)
	if getErr == nil && current.PayloadHash == payloadHash && current.State == "aborted" {
		return nil
	}
	return originFenceInvalid("origin operation did not become aborted")
}

func nullableOriginID(id int64) pgtype.Int8 {
	if id <= 0 {
		return pgtype.Int8{}
	}
	return pgtype.Int8{Int64: id, Valid: true}
}

func originIDsOf(transitions []OriginRoutingTransition) []int64 {
	ids := make([]int64, len(transitions))
	for i, transition := range transitions {
		ids[i] = transition.OriginID
	}
	return ids
}

func equalInt64s(a, b []int64) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func sameOriginOperation(op sqlc.OriginRoutingOperation, req OriginFenceRequest, payloadHash string) bool {
	return op.Token == req.Token && op.Kind == req.Kind && op.PayloadHash == payloadHash &&
		op.ProviderID.Valid && req.ProviderID != nil && op.ProviderID.Int64 == *req.ProviderID &&
		((req.OriginID <= 0 && !op.OriginID.Valid) || (op.OriginID.Valid && op.OriginID.Int64 == req.OriginID)) &&
		sameOriginTransitionJSON(op.Transitions, req.Transitions)
}

func sameOriginTransitionJSON(left, right []byte) bool {
	a, err := ParseOriginRoutingEnvelope(left, 1024)
	if err != nil {
		return false
	}
	b, err := ParseOriginRoutingEnvelope(right, 1024)
	if err != nil {
		return false
	}
	if a.Kind != b.Kind || a.ProviderID != b.ProviderID ||
		a.CurrentProviderStatus != b.CurrentProviderStatus || a.NextProviderStatus != b.NextProviderStatus ||
		len(a.Transitions) != len(b.Transitions) {
		return false
	}
	for i := range a.Transitions {
		if a.Transitions[i] != b.Transitions[i] {
			return false
		}
	}
	return true
}

func originFenceInvalid(message string) error {
	return failure.New(failure.CodeConfigInvalid, failure.WithMessage("runtimecontrol: "+message))
}
