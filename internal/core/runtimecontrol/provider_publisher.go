package runtimecontrol

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ThankCat/unio-gateway/internal/platform/breakerstore"
	"github.com/ThankCat/unio-gateway/internal/platform/failure"
	"github.com/ThankCat/unio-gateway/internal/platform/store/sqlc"
)

const (
	ProviderFenceKindOrigin = "origin"
	ProviderFenceKindStatus = "status"
)

const (
	fencePrepared  = "prepared"
	fenceCommitted = "committed"
	fenceAborted   = "aborted"
)

type ProviderRoutingEnvelope struct {
	Kind                  string `json:"kind"`
	ProviderID            int64  `json:"provider_id"`
	CurrentOriginRevision int64  `json:"current_origin_revision"`
	NextOriginRevision    int64  `json:"next_origin_revision"`
	CurrentStatusRevision int64  `json:"current_status_revision"`
	NextStatusRevision    int64  `json:"next_status_revision"`
	CurrentStatus         string `json:"current_status"`
	NextStatus            string `json:"next_status"`
	NextOrigin            string `json:"next_origin,omitempty"`
}

func CanonicalProviderRoutingOperation(envelope ProviderRoutingEnvelope) ([]byte, string, error) {
	if err := validateProviderRoutingEnvelope(envelope); err != nil {
		return nil, "", err
	}
	durable, err := json.Marshal(envelope)
	if err != nil {
		return nil, "", err
	}
	return durable, string(durable), nil
}

func ParseProviderRoutingEnvelope(raw []byte) (ProviderRoutingEnvelope, error) {
	var envelope ProviderRoutingEnvelope
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&envelope); err != nil {
		return ProviderRoutingEnvelope{}, fmt.Errorf("decode provider routing transition: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return ProviderRoutingEnvelope{}, errors.New("provider routing transition contains multiple JSON values")
		}
		return ProviderRoutingEnvelope{}, err
	}
	if err := validateProviderRoutingEnvelope(envelope); err != nil {
		return ProviderRoutingEnvelope{}, err
	}
	return envelope, nil
}

func validateProviderRoutingEnvelope(envelope ProviderRoutingEnvelope) error {
	if envelope.ProviderID <= 0 || envelope.CurrentOriginRevision < 1 || envelope.CurrentStatusRevision < 1 ||
		!validRoutingStatus(envelope.CurrentStatus) || !validRoutingStatus(envelope.NextStatus) {
		return errors.New("provider routing operation has invalid business fact")
	}
	switch envelope.Kind {
	case ProviderFenceKindOrigin:
		if envelope.NextOrigin == "" || envelope.NextOriginRevision != envelope.CurrentOriginRevision+1 ||
			envelope.NextStatusRevision != envelope.CurrentStatusRevision || envelope.NextStatus != envelope.CurrentStatus {
			return errors.New("invalid provider origin transition")
		}
	case ProviderFenceKindStatus:
		if envelope.NextOrigin != "" || envelope.NextOriginRevision != envelope.CurrentOriginRevision ||
			envelope.NextStatusRevision != envelope.CurrentStatusRevision+1 || envelope.NextStatus == envelope.CurrentStatus {
			return errors.New("invalid provider status transition")
		}
	default:
		return errors.New("unsupported provider routing operation kind")
	}
	return nil
}

func validRoutingStatus(status string) bool {
	return status == "enabled" || status == "disabled" || status == "archived"
}

type ProviderFenceRequest struct {
	Envelope ProviderRoutingEnvelope
	Token    string
	Payload  string

	Prepare func(context.Context) (breakerstore.FenceResult, error)
	Commit  func(context.Context) (breakerstore.FenceResult, error)
	Abort   func(context.Context) (breakerstore.FenceResult, error)

	ValidateLocked func(context.Context, pgx.Tx) error
	BusinessCommit func(context.Context, pgx.Tx) error
}

type ProviderFencePublisher struct {
	pool *pgxpool.Pool
}

func NewProviderFencePublisher(pool *pgxpool.Pool) *ProviderFencePublisher {
	if pool == nil {
		panic("runtimecontrol: provider fence publisher requires pool")
	}
	return &ProviderFencePublisher{pool: pool}
}

func (publisher *ProviderFencePublisher) WithProviderLock(ctx context.Context, providerID int64, fn func(context.Context, pgx.Tx) error) error {
	if providerID <= 0 || fn == nil {
		return providerFenceInvalid("invalid provider lock request")
	}
	tx, err := publisher.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var lockedID int64
	if err := tx.QueryRow(ctx, `SELECT id FROM providers WHERE id=$1 FOR UPDATE`, providerID).Scan(&lockedID); err != nil {
		return err
	}
	if err := fn(ctx, tx); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (publisher *ProviderFencePublisher) Publish(ctx context.Context, request ProviderFenceRequest) (PublishResult, error) {
	if request.Token == "" || request.Payload == "" || request.Prepare == nil || request.Commit == nil ||
		request.Abort == nil || request.BusinessCommit == nil {
		return PublishResult{}, providerFenceInvalid("provider fence requires token, payload and callbacks")
	}
	if err := validateProviderRoutingEnvelope(request.Envelope); err != nil {
		return PublishResult{}, providerFenceInvalid(err.Error())
	}
	payloadHash := breakerstore.HashPayload(request.Payload)
	queries := sqlc.New(publisher.pool)
	operation, err := queries.CreateProviderRoutingOperation(ctx, sqlc.CreateProviderRoutingOperationParams{
		Token: request.Token, Kind: request.Envelope.Kind, ProviderID: request.Envelope.ProviderID,
		Transitions: mustProviderRoutingJSON(request.Envelope), PayloadHash: payloadHash,
	})
	if err != nil {
		operation, err = queries.GetProviderRoutingOperationByToken(ctx, request.Token)
		if err != nil {
			return PublishResult{}, providerFenceInvalid("another provider routing operation is active")
		}
	}
	if !sameProviderOperation(operation, request, payloadHash) {
		return PublishResult{}, providerFenceInvalid("provider routing token conflicts with immutable request")
	}
	switch operation.State {
	case "committed":
		return PublishResult{State: PublishCommitted}, nil
	case "aborted":
		return PublishResult{State: PublishAborted}, providerFenceInvalid("provider routing operation is aborted")
	case "db_committed":
		return publisher.finishRedisCommit(ctx, queries, request, payloadHash)
	case "preparing", "prepared":
	default:
		return PublishResult{}, providerFenceInvalid("provider routing operation has invalid state")
	}

	prepared, prepareErr := request.Prepare(ctx)
	if prepareErr != nil {
		return PublishResult{}, errors.Join(prepareErr, publisher.abortUncommitted(ctx, queries, request, payloadHash))
	}
	switch string(prepared) {
	case fencePrepared:
	case fenceAborted:
		_, _ = queries.MarkProviderRoutingOperationAborted(ctx, sqlc.MarkProviderRoutingOperationAbortedParams{Token: request.Token, PayloadHash: payloadHash})
		return PublishResult{State: PublishAborted}, providerFenceInvalid("provider fence is already aborted")
	case fenceCommitted:
		return PublishResult{State: PublishRuntimeSyncPending}, providerFenceInvalid("redis committed before durable business state")
	default:
		return PublishResult{State: PublishAborted}, errors.Join(providerFenceInvalid("provider fence prepare rejected"), publisher.abortUncommitted(ctx, queries, request, payloadHash))
	}
	if operation.State == "preparing" {
		rows, err := queries.MarkProviderRoutingOperationPrepared(ctx, sqlc.MarkProviderRoutingOperationPreparedParams{Token: request.Token, PayloadHash: payloadHash})
		if err != nil || rows != 1 {
			return PublishResult{}, errors.Join(err, publisher.abortUncommitted(ctx, queries, request, payloadHash))
		}
	}
	if err := publisher.commitBusiness(ctx, request, payloadHash); err != nil {
		return PublishResult{}, errors.Join(err, publisher.abortUncommitted(ctx, queries, request, payloadHash))
	}
	return publisher.finishRedisCommit(ctx, queries, request, payloadHash)
}

func (publisher *ProviderFencePublisher) commitBusiness(ctx context.Context, request ProviderFenceRequest, payloadHash string) error {
	tx, err := publisher.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var providerID int64
	if err := tx.QueryRow(ctx, `SELECT id FROM providers WHERE id=$1 FOR UPDATE`, request.Envelope.ProviderID).Scan(&providerID); err != nil {
		return err
	}
	var state, kind, hash string
	var transitions []byte
	if err := tx.QueryRow(ctx, `SELECT state, kind, payload_hash, transitions FROM provider_routing_operations WHERE token=$1 FOR UPDATE`, request.Token).
		Scan(&state, &kind, &hash, &transitions); err != nil {
		return err
	}
	if state != "prepared" || kind != request.Envelope.Kind || hash != payloadHash || !sameProviderTransitions(transitions, request.Envelope) {
		return providerFenceInvalid("provider routing operation changed before business commit")
	}
	if request.ValidateLocked != nil {
		if err := request.ValidateLocked(ctx, tx); err != nil {
			return err
		}
	}
	if err := request.BusinessCommit(ctx, tx); err != nil {
		return err
	}
	rows, err := sqlc.New(tx).MarkProviderRoutingOperationDBCommitted(ctx, sqlc.MarkProviderRoutingOperationDBCommittedParams{Token: request.Token, PayloadHash: payloadHash})
	if err != nil || rows != 1 {
		return failure.Wrap(failure.CodeRequestLogStoreFailed, err, failure.WithMessage("runtimecontrol: mark provider routing db_committed"))
	}
	return tx.Commit(ctx)
}

func (publisher *ProviderFencePublisher) finishRedisCommit(ctx context.Context, queries *sqlc.Queries, request ProviderFenceRequest, payloadHash string) (PublishResult, error) {
	result, err := request.Commit(ctx)
	if err != nil || string(result) != fenceCommitted {
		return PublishResult{State: PublishRuntimeSyncPending}, nil
	}
	rows, err := queries.MarkProviderRoutingOperationCommitted(ctx, sqlc.MarkProviderRoutingOperationCommittedParams{Token: request.Token, PayloadHash: payloadHash})
	if err != nil || rows != 1 {
		return PublishResult{State: PublishRuntimeSyncPending}, nil
	}
	return PublishResult{State: PublishCommitted}, nil
}

func (publisher *ProviderFencePublisher) abortUncommitted(ctx context.Context, queries *sqlc.Queries, request ProviderFenceRequest, payloadHash string) error {
	result, err := request.Abort(ctx)
	if err != nil {
		return err
	}
	if string(result) != fenceAborted {
		return providerFenceInvalid("provider fence abort rejected")
	}
	rows, err := queries.MarkProviderRoutingOperationAborted(ctx, sqlc.MarkProviderRoutingOperationAbortedParams{Token: request.Token, PayloadHash: payloadHash})
	if err != nil {
		return err
	}
	if rows == 1 {
		return nil
	}
	current, getErr := queries.GetProviderRoutingOperationByToken(ctx, request.Token)
	if getErr == nil && current.PayloadHash == payloadHash && current.State == "aborted" {
		return nil
	}
	return providerFenceInvalid("provider routing operation did not become aborted")
}

func sameProviderOperation(operation sqlc.ProviderRoutingOperation, request ProviderFenceRequest, payloadHash string) bool {
	return operation.Token == request.Token && operation.Kind == request.Envelope.Kind &&
		operation.ProviderID == request.Envelope.ProviderID && operation.PayloadHash == payloadHash &&
		sameProviderTransitions(operation.Transitions, request.Envelope)
}

func sameProviderTransitions(raw []byte, expected ProviderRoutingEnvelope) bool {
	actual, err := ParseProviderRoutingEnvelope(raw)
	return err == nil && actual == expected
}

func mustProviderRoutingJSON(envelope ProviderRoutingEnvelope) []byte {
	data, _ := json.Marshal(envelope)
	return data
}

func providerFenceInvalid(message string) error {
	return failure.New(failure.CodeConfigInvalid, failure.WithMessage("runtimecontrol: "+message))
}
