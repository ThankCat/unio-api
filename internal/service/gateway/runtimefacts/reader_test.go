package runtimefacts_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/ThankCat/unio-gateway/internal/platform/failure"
	"github.com/ThankCat/unio-gateway/internal/platform/store/sqlc"
	"github.com/ThankCat/unio-gateway/internal/service/gateway/runtimefacts"
)

type queryStub struct {
	integrity    sqlc.GetAppSettingRecordRow
	integrityErr error
	admission    sqlc.GetGatewayAdmissionControlRevisionsRow
	admissionErr error
	routing      sqlc.GetGatewayRoutingControlRevisionsRow
	routingErr   error
}

func (q queryStub) GetAppSettingRecord(context.Context, string) (sqlc.GetAppSettingRecordRow, error) {
	return q.integrity, q.integrityErr
}

func (q queryStub) GetGatewayAdmissionControlRevisions(context.Context) (sqlc.GetGatewayAdmissionControlRevisionsRow, error) {
	return q.admission, q.admissionErr
}

func (q queryStub) GetGatewayRoutingControlRevisions(context.Context) (sqlc.GetGatewayRoutingControlRevisionsRow, error) {
	return q.routing, q.routingErr
}

func TestReaderIntegrityReturnsReadyEpochWithoutOtherControls(t *testing.T) {
	epoch := readyEpochJSON(t)
	reader := runtimefacts.NewReader(queryStub{integrity: sqlc.GetAppSettingRecordRow{
		Key:      "gateway.runtime_state_epoch",
		Value:    epoch,
		Revision: 11,
	}})

	got, err := reader.Integrity(context.Background())
	if err != nil {
		t.Fatalf("read integrity: %v", err)
	}
	if got.Epoch != "00112233445566778899aabbccddeeff" || got.Revision != 11 {
		t.Fatalf("unexpected integrity: %+v", got)
	}
}

func TestReaderAdmissionReturnsReadySnapshot(t *testing.T) {
	epoch := readyEpochJSON(t)
	reader := runtimefacts.NewReader(queryStub{admission: sqlc.GetGatewayAdmissionControlRevisionsRow{
		RuntimeStateEpochValue:           epoch,
		RuntimeStateEpochRevision:        7,
		RouteRateLimitDefaultsRevision:   3,
		ChannelRateLimitDefaultsRevision: 8,
		ConcurrencyDefaultsRevision:      4,
	}})

	got, err := reader.Admission(context.Background())
	if err != nil {
		t.Fatalf("read admission: %v", err)
	}
	if got.Revision != 7 || got.RouteRateLimits != 3 || got.ChannelRateLimits != 8 || got.Concurrency != 4 {
		t.Fatalf("unexpected admission revisions: %+v", got)
	}
}

func TestReaderRoutingReturnsReadySnapshot(t *testing.T) {
	epoch := readyEpochJSON(t)
	reader := runtimefacts.NewReader(queryStub{routing: sqlc.GetGatewayRoutingControlRevisionsRow{
		RuntimeStateEpochValue:    epoch,
		RuntimeStateEpochRevision: 9,
		CircuitBreakerRevision:    5,
		RoutingBalanceRevision:    6,
	}})

	got, err := reader.Routing(context.Background())
	if err != nil {
		t.Fatalf("read routing: %v", err)
	}
	if got.Revision != 9 || got.CircuitBreaker != 5 || got.RoutingBalance != 6 {
		t.Fatalf("unexpected routing revisions: %+v", got)
	}
}

func TestReaderFailsClosedForMissingOrInvalidFacts(t *testing.T) {
	t.Run("missing row", func(t *testing.T) {
		reader := runtimefacts.NewReader(queryStub{admissionErr: pgx.ErrNoRows})
		_, err := reader.Admission(context.Background())
		if failure.CodeOf(err) != failure.CodeGatewayRuntimeSyncRequired || !errors.Is(err, runtimefacts.ErrRuntimeSyncRequired) {
			t.Fatalf("unexpected error: code=%q err=%v", failure.CodeOf(err), err)
		}
	})

	for _, tc := range []struct {
		name            string
		routeRevision   int64
		channelRevision int64
	}{
		{name: "invalid route rate revision", routeRevision: 0, channelRevision: 2},
		{name: "invalid channel rate revision", routeRevision: 1, channelRevision: 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reader := runtimefacts.NewReader(queryStub{admission: sqlc.GetGatewayAdmissionControlRevisionsRow{
				RuntimeStateEpochValue:           readyEpochJSON(t),
				RuntimeStateEpochRevision:        1,
				RouteRateLimitDefaultsRevision:   tc.routeRevision,
				ChannelRateLimitDefaultsRevision: tc.channelRevision,
				ConcurrencyDefaultsRevision:      1,
			}})
			_, err := reader.Admission(context.Background())
			if failure.CodeOf(err) != failure.CodeGatewayRuntimeSyncRequired {
				t.Fatalf("unexpected error: code=%q err=%v", failure.CodeOf(err), err)
			}
		})
	}

	t.Run("recovering epoch", func(t *testing.T) {
		reader := runtimefacts.NewReader(queryStub{routing: sqlc.GetGatewayRoutingControlRevisionsRow{
			RuntimeStateEpochValue:    []byte(`{"epoch":"00112233445566778899aabbccddeeff","state":"recovering","reason":"bootstrap","activated_at":null}`),
			RuntimeStateEpochRevision: 1,
			CircuitBreakerRevision:    1,
			RoutingBalanceRevision:    1,
		}})
		_, err := reader.Routing(context.Background())
		if failure.CodeOf(err) != failure.CodeGatewayRuntimeStateLost || !errors.Is(err, runtimefacts.ErrRuntimeStateLost) {
			t.Fatalf("unexpected error: code=%q err=%v", failure.CodeOf(err), err)
		}
	})
}

func TestReaderClassifiesDatabaseFailure(t *testing.T) {
	reader := runtimefacts.NewReader(queryStub{routingErr: errors.New("database unavailable")})
	_, err := reader.Routing(context.Background())
	if failure.CodeOf(err) != failure.CodeDependencyPostgresUnavailable {
		t.Fatalf("unexpected error: code=%q err=%v", failure.CodeOf(err), err)
	}
}

func readyEpochJSON(t *testing.T) []byte {
	t.Helper()
	activatedAt := time.Date(2026, time.July, 22, 0, 0, 0, 0, time.UTC)
	raw, err := json.Marshal(map[string]any{
		"epoch":        "00112233445566778899aabbccddeeff",
		"state":        "ready",
		"reason":       "bootstrap",
		"activated_at": activatedAt,
	})
	if err != nil {
		t.Fatalf("marshal epoch fixture: %v", err)
	}
	return raw
}
