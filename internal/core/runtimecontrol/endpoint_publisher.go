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

// Endpoint routing operation kinds match endpoint_routing_operations.kind.
const (
	EndpointFenceKindBaseURL             = "base_url"
	EndpointFenceKindStatus              = "status"
	EndpointFenceKindBaseURLStatus       = "base_url_status"
	EndpointFenceKindProviderStatusBatch = "provider_status_batch"
)

const (
	fencePrepared  = "prepared"
	fenceCommitted = "committed"
	fenceAborted   = "aborted"
)

// EndpointRoutingTransition is the durable, URL-free revision summary stored in PostgreSQL.
// Both revision families are always present so recovery can validate the full Endpoint identity.
type EndpointRoutingTransition struct {
	EndpointID int64 `json:"endpoint_id"`

	CurrentBaseURLRevision int64 `json:"current_base_url_revision"`
	NextBaseURLRevision    int64 `json:"next_base_url_revision"`
	CurrentStatusRevision  int64 `json:"current_status_revision"`
	NextStatusRevision     int64 `json:"next_status_revision"`

	CurrentEffectiveStatus string `json:"current_effective_status"`
	NextEffectiveStatus    string `json:"next_effective_status"`
}

// EndpointRoutingEnvelope is the strict JSONB shape stored in endpoint_routing_operations.transitions.
type EndpointRoutingEnvelope struct {
	Kind                  string                      `json:"kind"`
	ProviderID            int64                       `json:"provider_id"`
	CurrentProviderStatus string                      `json:"current_provider_status"`
	NextProviderStatus    string                      `json:"next_provider_status"`
	Transitions           []EndpointRoutingTransition `json:"transitions"`
}

type endpointRoutingPayload struct {
	Operation   EndpointRoutingEnvelope `json:"operation"`
	NextBaseURL string                  `json:"next_base_url,omitempty"`
}

// CanonicalEndpointRoutingOperation returns the durable URL-free transition JSON and the complete
// canonical payload used for payload_hash. nextBaseURL is present only for BaseURL/combined operations.
func CanonicalEndpointRoutingOperation(envelope EndpointRoutingEnvelope, nextBaseURL string, maxBatch int) ([]byte, string, error) {
	if err := validateEndpointRoutingEnvelope(envelope, nextBaseURL, maxBatch); err != nil {
		return nil, "", err
	}
	durable, err := json.Marshal(envelope)
	if err != nil {
		return nil, "", err
	}
	payload, err := json.Marshal(endpointRoutingPayload{Operation: envelope, NextBaseURL: nextBaseURL})
	if err != nil {
		return nil, "", err
	}
	return durable, string(payload), nil
}

// ParseEndpointRoutingEnvelope strictly decodes the durable transitions JSON.
func ParseEndpointRoutingEnvelope(raw []byte, maxBatch int) (EndpointRoutingEnvelope, error) {
	var envelope EndpointRoutingEnvelope
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&envelope); err != nil {
		return EndpointRoutingEnvelope{}, fmt.Errorf("decode endpoint routing transitions: %w", err)
	}
	if err := ensureEndpointJSONEOF(dec); err != nil {
		return EndpointRoutingEnvelope{}, err
	}
	nextBaseURL := ""
	if envelope.Kind == EndpointFenceKindBaseURL || envelope.Kind == EndpointFenceKindBaseURLStatus {
		// The URL is deliberately absent from durable transitions; validation of its presence belongs to
		// CanonicalEndpointRoutingOperation or db_committed recovery after reading the business row.
		nextBaseURL = "recovery-placeholder"
	}
	if err := validateEndpointRoutingEnvelope(envelope, nextBaseURL, maxBatch); err != nil {
		return EndpointRoutingEnvelope{}, err
	}
	return envelope, nil
}

func ensureEndpointJSONEOF(dec *json.Decoder) error {
	var extra any
	if err := dec.Decode(&extra); errors.Is(err, io.EOF) {
		return nil
	} else if err != nil {
		return fmt.Errorf("decode endpoint routing trailing JSON: %w", err)
	}
	return errors.New("endpoint routing transitions contain multiple JSON values")
}

func validateEndpointRoutingEnvelope(envelope EndpointRoutingEnvelope, nextBaseURL string, maxBatch int) error {
	if envelope.ProviderID <= 0 || !validRoutingStatus(envelope.CurrentProviderStatus) || !validRoutingStatus(envelope.NextProviderStatus) {
		return errors.New("endpoint routing operation has invalid provider identity/status")
	}
	if maxBatch < 1 || maxBatch > 1024 {
		return errors.New("endpoint routing max batch must be within [1,1024]")
	}
	count := len(envelope.Transitions)
	if count == 0 || count > maxBatch {
		return errors.New("endpoint routing transition batch is empty or too large")
	}
	if envelope.Kind != EndpointFenceKindProviderStatusBatch && count != 1 {
		return errors.New("single endpoint routing operation must have one transition")
	}
	if envelope.Kind == EndpointFenceKindProviderStatusBatch && envelope.CurrentProviderStatus == envelope.NextProviderStatus {
		return errors.New("provider status batch requires a provider status change")
	}
	if (envelope.Kind == EndpointFenceKindBaseURL || envelope.Kind == EndpointFenceKindBaseURLStatus) != (nextBaseURL != "") {
		return errors.New("endpoint routing BaseURL payload is missing or unexpected")
	}
	var previous int64
	for _, transition := range envelope.Transitions {
		if transition.EndpointID <= previous || transition.CurrentBaseURLRevision < 1 || transition.CurrentStatusRevision < 1 ||
			!validRoutingStatus(transition.CurrentEffectiveStatus) || !validRoutingStatus(transition.NextEffectiveStatus) {
			return errors.New("endpoint routing transitions must be positive, unique, ordered and have valid status")
		}
		previous = transition.EndpointID
		switch envelope.Kind {
		case EndpointFenceKindBaseURL:
			if transition.NextBaseURLRevision != transition.CurrentBaseURLRevision+1 ||
				transition.NextStatusRevision != transition.CurrentStatusRevision ||
				transition.NextEffectiveStatus != transition.CurrentEffectiveStatus ||
				envelope.CurrentProviderStatus != envelope.NextProviderStatus {
				return errors.New("invalid BaseURL transition")
			}
		case EndpointFenceKindStatus:
			if transition.NextBaseURLRevision != transition.CurrentBaseURLRevision ||
				transition.NextStatusRevision != transition.CurrentStatusRevision+1 ||
				envelope.CurrentProviderStatus != envelope.NextProviderStatus {
				return errors.New("invalid status transition")
			}
		case EndpointFenceKindBaseURLStatus:
			if transition.NextBaseURLRevision != transition.CurrentBaseURLRevision+1 ||
				transition.NextStatusRevision != transition.CurrentStatusRevision+1 ||
				envelope.CurrentProviderStatus != envelope.NextProviderStatus {
				return errors.New("invalid combined transition")
			}
		case EndpointFenceKindProviderStatusBatch:
			if transition.NextBaseURLRevision != transition.CurrentBaseURLRevision ||
				transition.NextStatusRevision != transition.CurrentStatusRevision+1 {
				return errors.New("invalid provider status batch transition")
			}
		default:
			return errors.New("unsupported endpoint routing operation kind")
		}
	}
	return nil
}

// EffectiveEndpointStatus combines Provider and Endpoint status for Redis admission control.
func EffectiveEndpointStatus(providerStatus, endpointStatus string) string {
	if providerStatus == "archived" || endpointStatus == "archived" {
		return "archived"
	}
	if providerStatus != "enabled" || endpointStatus != "enabled" {
		return "disabled"
	}
	return "enabled"
}

func validRoutingStatus(status string) bool {
	return status == "enabled" || status == "disabled" || status == "archived"
}

// EndpointFenceRequest describes one durable single or batch fence publication.
type EndpointFenceRequest struct {
	Kind        string
	Token       string
	EndpointID  int64  // zero for provider batch
	ProviderID  *int64 // required for every production operation
	Transitions []byte
	Payload     string // complete canonical payload; hash may include target BaseURL absent from Transitions
	MaxBatch    int

	Prepare func(ctx context.Context) (breakerstore.FenceResult, error)
	Commit  func(ctx context.Context) (breakerstore.FenceResult, error)
	Abort   func(ctx context.Context) (breakerstore.FenceResult, error)

	// ValidateLocked runs after Provider, ordered Endpoint rows and operation have been locked.
	ValidateLocked func(ctx context.Context, tx pgx.Tx) error
	// BusinessCommit runs in that same transaction before operation -> db_committed.
	BusinessCommit func(ctx context.Context, tx pgx.Tx) error
}

// EndpointFencePublisher coordinates PostgreSQL durable operations and Redis endpoint fences.
type EndpointFencePublisher struct {
	pool *pgxpool.Pool
}

func NewEndpointFencePublisher(pool *pgxpool.Pool) *EndpointFencePublisher {
	if pool == nil {
		panic("runtimecontrol: endpoint fence publisher requires pool")
	}
	return &EndpointFencePublisher{pool: pool}
}

// WithEndpointLocks runs a non-fenced PostgreSQL change under the same Provider -> Endpoint ID lock
// order. It is used only when an Endpoint business field changes without changing effective routing
// revisions, so no Redis publication is required.
func (p *EndpointFencePublisher) WithEndpointLocks(
	ctx context.Context,
	providerID int64,
	endpointIDs []int64,
	fn func(context.Context, pgx.Tx) error,
) error {
	if providerID <= 0 || fn == nil ||
		!sort.SliceIsSorted(endpointIDs, func(i, j int) bool { return endpointIDs[i] < endpointIDs[j] }) {
		return endpointFenceInvalid("invalid locked endpoint update")
	}
	for i, id := range endpointIDs {
		if id <= 0 || (i > 0 && endpointIDs[i-1] == id) {
			return endpointFenceInvalid("locked endpoint IDs must be positive and unique")
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
	if len(endpointIDs) > 0 {
		rows, err := tx.Query(ctx, `SELECT id FROM provider_endpoints WHERE id = ANY($1::bigint[]) ORDER BY id FOR UPDATE`, endpointIDs)
		if err != nil {
			return err
		}
		lockedIDs := make([]int64, 0, len(endpointIDs))
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
		if !equalInt64s(lockedIDs, endpointIDs) {
			return endpointFenceInvalid("locked endpoint target set changed")
		}
	}
	if err := fn(ctx, tx); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// Publish executes preparing -> Redis Prepare -> prepared -> locked business transaction/db_committed
// -> Redis Commit -> committed. PostgreSQL lock order is always Provider -> Endpoint ID -> operation.
func (p *EndpointFencePublisher) Publish(ctx context.Context, req EndpointFenceRequest) (PublishResult, error) {
	if req.Token == "" || req.ProviderID == nil || *req.ProviderID <= 0 || req.Payload == "" ||
		req.Prepare == nil || req.Commit == nil || req.Abort == nil || req.BusinessCommit == nil {
		return PublishResult{}, endpointFenceInvalid("endpoint fence requires provider/token/payload/callbacks")
	}
	if req.MaxBatch == 0 {
		req.MaxBatch = 1024
	}
	envelope, err := ParseEndpointRoutingEnvelope(req.Transitions, req.MaxBatch)
	if err != nil {
		return PublishResult{}, endpointFenceInvalid(err.Error())
	}
	if envelope.Kind != req.Kind || envelope.ProviderID != *req.ProviderID {
		return PublishResult{}, endpointFenceInvalid("endpoint fence request conflicts with transitions")
	}
	endpointIDs := endpointIDsOf(envelope.Transitions)
	if req.Kind == EndpointFenceKindProviderStatusBatch {
		if req.EndpointID != 0 {
			return PublishResult{}, endpointFenceInvalid("provider batch must not set endpoint_id")
		}
	} else if req.EndpointID <= 0 || len(endpointIDs) != 1 || endpointIDs[0] != req.EndpointID {
		return PublishResult{}, endpointFenceInvalid("single endpoint target conflicts with transitions")
	}

	payloadHash := breakerstore.HashPayload(req.Payload)
	q := sqlc.New(p.pool)
	op, err := q.CreateEndpointRoutingOperation(ctx, sqlc.CreateEndpointRoutingOperationParams{
		Token:       req.Token,
		Kind:        req.Kind,
		ProviderID:  pgtype.Int8{Int64: *req.ProviderID, Valid: true},
		EndpointID:  nullableEndpointID(req.EndpointID),
		Transitions: req.Transitions,
		PayloadHash: payloadHash,
	})
	if err != nil {
		op, err = q.GetEndpointRoutingOperationByToken(ctx, req.Token)
		if err != nil {
			return PublishResult{}, endpointFenceInvalid("another endpoint routing operation is active")
		}
	}
	if !sameEndpointOperation(op, req, payloadHash) {
		return PublishResult{}, endpointFenceInvalid("endpoint operation token conflicts with immutable request")
	}
	switch op.State {
	case "committed":
		return PublishResult{State: PublishCommitted}, nil
	case "aborted":
		return PublishResult{State: PublishAborted}, endpointFenceInvalid("endpoint operation is already aborted")
	case "db_committed":
		return p.finishRedisCommit(ctx, q, req, payloadHash)
	case "preparing", "prepared":
	default:
		return PublishResult{}, endpointFenceInvalid("endpoint operation has invalid durable state")
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
		_, _ = q.MarkEndpointRoutingOperationAborted(ctx, sqlc.MarkEndpointRoutingOperationAbortedParams{Token: req.Token, PayloadHash: payloadHash})
		return PublishResult{State: PublishAborted}, endpointFenceInvalid("endpoint fence was already aborted")
	case fenceCommitted:
		return PublishResult{State: PublishRuntimeSyncPending}, endpointFenceInvalid("redis committed before durable business state")
	default:
		if abortErr := p.abortUncommitted(ctx, q, req, payloadHash); abortErr != nil {
			return PublishResult{}, abortErr
		}
		return PublishResult{State: PublishAborted}, endpointFenceInvalid("endpoint fence prepare rejected (" + string(prepared) + ")")
	}

	if op.State == "preparing" {
		rows, err := q.MarkEndpointRoutingOperationPrepared(ctx, sqlc.MarkEndpointRoutingOperationPreparedParams{Token: req.Token, PayloadHash: payloadHash})
		if err != nil || rows != 1 {
			abortErr := p.abortUncommitted(ctx, q, req, payloadHash)
			if err != nil {
				return PublishResult{}, errors.Join(err, abortErr)
			}
			return PublishResult{}, errors.Join(endpointFenceInvalid("endpoint operation could not become prepared"), abortErr)
		}
	}

	if err := p.commitBusiness(ctx, req, envelope, endpointIDs, payloadHash); err != nil {
		abortErr := p.abortUncommitted(ctx, q, req, payloadHash)
		if abortErr != nil {
			return PublishResult{}, errors.Join(err, abortErr)
		}
		return PublishResult{}, err
	}
	return p.finishRedisCommit(ctx, q, req, payloadHash)
}

func (p *EndpointFencePublisher) commitBusiness(
	ctx context.Context,
	req EndpointFenceRequest,
	envelope EndpointRoutingEnvelope,
	endpointIDs []int64,
	payloadHash string,
) error {
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return failure.Wrap(failure.CodeRequestLogStoreFailed, err, failure.WithMessage("runtimecontrol: begin endpoint business tx"))
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var lockedProviderID int64
	if err := tx.QueryRow(ctx, `SELECT id FROM providers WHERE id=$1 FOR UPDATE`, envelope.ProviderID).Scan(&lockedProviderID); err != nil {
		return failure.Wrap(failure.CodeRequestLogStoreFailed, err, failure.WithMessage("runtimecontrol: lock endpoint provider"))
	}
	rows, err := tx.Query(ctx, `SELECT id FROM provider_endpoints WHERE id = ANY($1::bigint[]) ORDER BY id FOR UPDATE`, endpointIDs)
	if err != nil {
		return failure.Wrap(failure.CodeRequestLogStoreFailed, err, failure.WithMessage("runtimecontrol: lock provider endpoints"))
	}
	lockedIDs := make([]int64, 0, len(endpointIDs))
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
	if !equalInt64s(lockedIDs, endpointIDs) {
		return endpointFenceInvalid("endpoint target set changed before business commit")
	}

	var lockedState, lockedKind, lockedHash string
	var lockedTransitions []byte
	if err := tx.QueryRow(ctx, `SELECT state, kind, payload_hash, transitions FROM endpoint_routing_operations WHERE token=$1 FOR UPDATE`, req.Token).
		Scan(&lockedState, &lockedKind, &lockedHash, &lockedTransitions); err != nil {
		return failure.Wrap(failure.CodeRequestLogStoreFailed, err, failure.WithMessage("runtimecontrol: lock endpoint operation"))
	}
	if lockedState != "prepared" || lockedKind != req.Kind || lockedHash != payloadHash || !sameEndpointTransitionJSON(lockedTransitions, req.Transitions) {
		return endpointFenceInvalid("endpoint operation changed before business commit")
	}
	if req.ValidateLocked != nil {
		if err := req.ValidateLocked(ctx, tx); err != nil {
			return err
		}
	}
	if err := req.BusinessCommit(ctx, tx); err != nil {
		return err
	}
	changed, err := sqlc.New(tx).MarkEndpointRoutingOperationDBCommitted(ctx, sqlc.MarkEndpointRoutingOperationDBCommittedParams{
		Token: req.Token, PayloadHash: payloadHash,
	})
	if err != nil {
		return failure.Wrap(failure.CodeRequestLogStoreFailed, err, failure.WithMessage("runtimecontrol: mark endpoint db_committed"))
	}
	if changed != 1 {
		return endpointFenceInvalid("endpoint operation did not become db_committed")
	}
	if err := tx.Commit(ctx); err != nil {
		return failure.Wrap(failure.CodeRequestLogStoreFailed, err, failure.WithMessage("runtimecontrol: commit endpoint business tx"))
	}
	return nil
}

func (p *EndpointFencePublisher) finishRedisCommit(ctx context.Context, q *sqlc.Queries, req EndpointFenceRequest, payloadHash string) (PublishResult, error) {
	result, err := req.Commit(ctx)
	if err != nil || string(result) != fenceCommitted {
		return PublishResult{State: PublishRuntimeSyncPending}, nil
	}
	rows, err := q.MarkEndpointRoutingOperationCommitted(ctx, sqlc.MarkEndpointRoutingOperationCommittedParams{Token: req.Token, PayloadHash: payloadHash})
	if err != nil || rows != 1 {
		return PublishResult{State: PublishRuntimeSyncPending}, nil
	}
	return PublishResult{State: PublishCommitted}, nil
}

func (p *EndpointFencePublisher) abortUncommitted(ctx context.Context, q *sqlc.Queries, req EndpointFenceRequest, payloadHash string) error {
	result, err := req.Abort(ctx)
	if err != nil {
		return err
	}
	if string(result) != fenceAborted {
		return endpointFenceInvalid("endpoint fence abort rejected (" + string(result) + ")")
	}
	rows, err := q.MarkEndpointRoutingOperationAborted(ctx, sqlc.MarkEndpointRoutingOperationAbortedParams{Token: req.Token, PayloadHash: payloadHash})
	if err != nil {
		return failure.Wrap(failure.CodeRequestLogStoreFailed, err, failure.WithMessage("runtimecontrol: mark endpoint aborted"))
	}
	if rows == 1 {
		return nil
	}
	current, getErr := q.GetEndpointRoutingOperationByToken(ctx, req.Token)
	if getErr == nil && current.PayloadHash == payloadHash && current.State == "aborted" {
		return nil
	}
	return endpointFenceInvalid("endpoint operation did not become aborted")
}

func nullableEndpointID(id int64) pgtype.Int8 {
	if id <= 0 {
		return pgtype.Int8{}
	}
	return pgtype.Int8{Int64: id, Valid: true}
}

func endpointIDsOf(transitions []EndpointRoutingTransition) []int64 {
	ids := make([]int64, len(transitions))
	for i, transition := range transitions {
		ids[i] = transition.EndpointID
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

func sameEndpointOperation(op sqlc.EndpointRoutingOperation, req EndpointFenceRequest, payloadHash string) bool {
	return op.Token == req.Token && op.Kind == req.Kind && op.PayloadHash == payloadHash &&
		op.ProviderID.Valid && req.ProviderID != nil && op.ProviderID.Int64 == *req.ProviderID &&
		((req.EndpointID <= 0 && !op.EndpointID.Valid) || (op.EndpointID.Valid && op.EndpointID.Int64 == req.EndpointID)) &&
		sameEndpointTransitionJSON(op.Transitions, req.Transitions)
}

func sameEndpointTransitionJSON(left, right []byte) bool {
	a, err := ParseEndpointRoutingEnvelope(left, 1024)
	if err != nil {
		return false
	}
	b, err := ParseEndpointRoutingEnvelope(right, 1024)
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

func endpointFenceInvalid(message string) error {
	return failure.New(failure.CodeConfigInvalid, failure.WithMessage("runtimecontrol: "+message))
}
