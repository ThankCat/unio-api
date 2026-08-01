package appsettings

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/ThankCat/unio-gateway/internal/platform/store/sqlc"
)

type gatewayLoggingControlQueries struct {
	*fakeQueries
	revision  int64
	updatedAt time.Time
}

func (q *gatewayLoggingControlQueries) GetAppSettingRecord(_ context.Context, key string) (sqlc.GetAppSettingRecordRow, error) {
	value, ok := q.data[key]
	if !ok {
		return sqlc.GetAppSettingRecordRow{}, pgx.ErrNoRows
	}
	return sqlc.GetAppSettingRecordRow{
		Key: key, Value: append([]byte(nil), value...), Revision: q.revision,
		UpdatedAt: timestamp(q.updatedAt),
	}, nil
}

func (q *gatewayLoggingControlQueries) UpdateAppSettingAtRevision(
	_ context.Context,
	arg sqlc.UpdateAppSettingAtRevisionParams,
) (sqlc.UpdateAppSettingAtRevisionRow, error) {
	current := q.data[arg.Key]
	if q.revision != arg.CurrentRevision || arg.NextRevision != arg.CurrentRevision+1 || bytes.Equal(current, arg.Value) {
		return sqlc.UpdateAppSettingAtRevisionRow{}, pgx.ErrNoRows
	}
	q.revision = arg.NextRevision
	q.updatedAt = q.updatedAt.Add(time.Second)
	q.data[arg.Key] = append([]byte(nil), arg.Value...)
	return sqlc.UpdateAppSettingAtRevisionRow{
		Key: arg.Key, Value: arg.Value, Description: arg.Description,
		Revision: q.revision, UpdatedAt: timestamp(q.updatedAt),
	}, nil
}

func TestGatewayLoggingControlLifecycle(t *testing.T) {
	now := time.Date(2026, time.August, 1, 8, 0, 0, 0, time.UTC)
	base := newFakeQueries()
	definition, _ := DefaultRegistry().Get(GatewayLoggingDebugSessionKey)
	base.data[GatewayLoggingDebugSessionKey] = append([]byte(nil), definition.Default...)
	queries := &gatewayLoggingControlQueries{fakeQueries: base, revision: 1, updatedAt: now.Add(-time.Hour)}
	service := NewService(newTestStore(queries))

	started, err := service.StartGatewayDebugSession(
		context.Background(), 15*time.Minute, " investigate\nTTFT ", 0, now,
	)
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	if started.Setting.SessionID == "" || started.Setting.Reason != "investigateTTFT" || started.Setting.Revision != 2 {
		t.Fatalf("started = %+v", started)
	}
	if !started.Setting.StartedAt.Equal(now) || !started.Setting.ExpiresAt.Equal(now.Add(15*time.Minute)) {
		t.Fatalf("unexpected start window: %+v", started.Setting)
	}

	extendedAt := now.Add(2 * time.Minute)
	extended, err := service.StartGatewayDebugSession(
		context.Background(), 30*time.Minute, "continued diagnosis", 0, extendedAt,
	)
	if err != nil {
		t.Fatalf("extend: %v", err)
	}
	if extended.Setting.SessionID != started.Setting.SessionID || !extended.Setting.StartedAt.Equal(now) ||
		!extended.Setting.ExpiresAt.Equal(extendedAt.Add(30*time.Minute)) || extended.Setting.Revision != 3 {
		t.Fatalf("extended = %+v", extended)
	}

	stopped, err := service.StopGatewayDebugSession(context.Background(), 0, extendedAt.Add(time.Minute))
	if err != nil {
		t.Fatalf("stop: %v", err)
	}
	if stopped.Setting.SessionID != "" || stopped.Setting.Revision != 4 || stopped.Setting.Reason != "manual" {
		t.Fatalf("stopped = %+v", stopped)
	}
}

func TestGatewayLoggingControlRejectsUnsupportedDuration(t *testing.T) {
	service := NewService(newTestStore(newFakeQueries()))
	if _, err := service.StartGatewayDebugSession(
		context.Background(), 10*time.Minute, "investigate", 0, time.Now(),
	); err == nil {
		t.Fatal("expected unsupported duration error")
	}
}

func TestGatewayLoggingControlCountsReasonCharacters(t *testing.T) {
	now := time.Date(2026, time.August, 1, 8, 0, 0, 0, time.UTC)
	base := newFakeQueries()
	definition, _ := DefaultRegistry().Get(GatewayLoggingDebugSessionKey)
	base.data[GatewayLoggingDebugSessionKey] = append([]byte(nil), definition.Default...)
	queries := &gatewayLoggingControlQueries{fakeQueries: base, revision: 1, updatedAt: now.Add(-time.Hour)}
	service := NewService(newTestStore(queries))

	if _, err := service.StartGatewayDebugSession(context.Background(), 15*time.Minute, strings.Repeat("慢", 200), 0, now); err != nil {
		t.Fatalf("200 Unicode characters must be accepted: %v", err)
	}
	if _, err := service.StartGatewayDebugSession(context.Background(), 15*time.Minute, strings.Repeat("慢", 201), 0, now); err == nil {
		t.Fatal("201 Unicode characters must be rejected")
	}
}

func timestamp(value time.Time) (result pgtype.Timestamptz) {
	result.Time = value
	result.Valid = true
	return result
}
