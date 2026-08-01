package appsettings

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"

	"github.com/ThankCat/unio-gateway/internal/platform/store/sqlc"
)

var gatewayDebugDurations = map[time.Duration]struct{}{
	5 * time.Minute:  {},
	15 * time.Minute: {},
	30 * time.Minute: {},
	60 * time.Minute: {},
}

var ErrGatewayDebugRequestInvalid = errors.New("gateway debug request invalid")

type GatewayLoggingControlSnapshot struct {
	Setting   GatewayLoggingDebugSessionSetting
	UpdatedAt time.Time
}

type gatewayLoggingCASQueries interface {
	UpdateAppSettingAtRevision(context.Context, sqlc.UpdateAppSettingAtRevisionParams) (sqlc.UpdateAppSettingAtRevisionRow, error)
}

func (s *Service) GatewayLoggingControl(ctx context.Context) (GatewayLoggingControlSnapshot, error) {
	record, err := s.store.Record(ctx, GatewayLoggingDebugSessionKey)
	if err != nil {
		return GatewayLoggingControlSnapshot{}, err
	}
	setting, err := DecodeGatewayLoggingDebugSession(record.Value)
	if err != nil {
		return GatewayLoggingControlSnapshot{}, err
	}
	return GatewayLoggingControlSnapshot{Setting: setting, UpdatedAt: record.UpdatedAt}, nil
}

func (s *Service) StartGatewayDebugSession(
	ctx context.Context,
	duration time.Duration,
	reason string,
	enabledByUserID int64,
	now time.Time,
) (GatewayLoggingControlSnapshot, error) {
	if _, ok := gatewayDebugDurations[duration]; !ok {
		return GatewayLoggingControlSnapshot{}, fmt.Errorf("%w: duration must be 5, 15, 30, or 60 minutes", ErrGatewayDebugRequestInvalid)
	}
	reason = sanitizeGatewayDebugReason(reason)
	if reason == "" || utf8.RuneCountInString(reason) > 200 {
		return GatewayLoggingControlSnapshot{}, fmt.Errorf("%w: reason must contain 1 to 200 characters", ErrGatewayDebugRequestInvalid)
	}
	if enabledByUserID < 0 {
		return GatewayLoggingControlSnapshot{}, fmt.Errorf("%w: operator id must be non-negative", ErrGatewayDebugRequestInvalid)
	}
	now = now.UTC()
	return s.updateGatewayLoggingControl(ctx, func(current GatewayLoggingDebugSessionSetting, nextRevision int64) (GatewayLoggingDebugSessionSetting, error) {
		sessionID := current.SessionID
		startedAt := current.StartedAt
		if sessionID == "" || !current.ExpiresAt.After(now) {
			generated, err := newGatewayDebugSessionID()
			if err != nil {
				return GatewayLoggingDebugSessionSetting{}, err
			}
			sessionID = generated
			startedAt = now
		}
		return GatewayLoggingDebugSessionSetting{
			SessionID:       sessionID,
			StartedAt:       startedAt,
			ExpiresAt:       now.Add(duration),
			Reason:          reason,
			EnabledByUserID: enabledByUserID,
			Revision:        nextRevision,
		}, nil
	})
}

func (s *Service) StopGatewayDebugSession(
	ctx context.Context,
	changedByUserID int64,
	now time.Time,
) (GatewayLoggingControlSnapshot, error) {
	if changedByUserID < 0 {
		return GatewayLoggingControlSnapshot{}, fmt.Errorf("%w: operator id must be non-negative", ErrGatewayDebugRequestInvalid)
	}
	now = now.UTC()
	current, err := s.GatewayLoggingControl(ctx)
	if err != nil {
		return GatewayLoggingControlSnapshot{}, err
	}
	if current.Setting.SessionID == "" {
		return current, nil
	}
	return s.updateGatewayLoggingControl(ctx, func(_ GatewayLoggingDebugSessionSetting, nextRevision int64) (GatewayLoggingDebugSessionSetting, error) {
		return GatewayLoggingDebugSessionSetting{
			StartedAt:       now,
			ExpiresAt:       now,
			Reason:          "manual",
			EnabledByUserID: changedByUserID,
			Revision:        nextRevision,
		}, nil
	})
}

func (s *Service) updateGatewayLoggingControl(
	ctx context.Context,
	build func(current GatewayLoggingDebugSessionSetting, nextRevision int64) (GatewayLoggingDebugSessionSetting, error),
) (GatewayLoggingControlSnapshot, error) {
	queries, ok := s.store.queries.(gatewayLoggingCASQueries)
	if !ok {
		return GatewayLoggingControlSnapshot{}, errors.New("gateway logging control requires CAS-capable settings store")
	}
	definition, ok := s.store.registry.Get(GatewayLoggingDebugSessionKey)
	if !ok {
		return GatewayLoggingControlSnapshot{}, errors.New("gateway logging control setting is not registered")
	}
	for attempt := 0; attempt < 3; attempt++ {
		record, err := s.store.Record(ctx, GatewayLoggingDebugSessionKey)
		if err != nil {
			return GatewayLoggingControlSnapshot{}, err
		}
		current, err := DecodeGatewayLoggingDebugSession(record.Value)
		if err != nil {
			return GatewayLoggingControlSnapshot{}, err
		}
		next, err := build(current, record.Revision+1)
		if err != nil {
			return GatewayLoggingControlSnapshot{}, err
		}
		raw, err := json.Marshal(next)
		if err != nil {
			return GatewayLoggingControlSnapshot{}, err
		}
		row, err := queries.UpdateAppSettingAtRevision(ctx, sqlc.UpdateAppSettingAtRevisionParams{
			Value:           raw,
			Description:     definition.Description,
			NextRevision:    record.Revision + 1,
			Key:             GatewayLoggingDebugSessionKey,
			CurrentRevision: record.Revision,
		})
		if errors.Is(err, pgx.ErrNoRows) {
			continue
		}
		if err != nil {
			return GatewayLoggingControlSnapshot{}, err
		}
		s.store.PublishCache(ctx, GatewayLoggingDebugSessionKey, raw)
		return GatewayLoggingControlSnapshot{Setting: next, UpdatedAt: row.UpdatedAt.Time}, nil
	}
	return GatewayLoggingControlSnapshot{}, errors.New("gateway logging control changed concurrently; retry the operation")
}

func sanitizeGatewayDebugReason(value string) string {
	value = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, value)
	return strings.TrimSpace(value)
}

func newGatewayDebugSessionID() (string, error) {
	value, err := newRuntimeControlToken()
	if err != nil {
		return "", fmt.Errorf("generate gateway debug session id: %w", err)
	}
	value = strings.TrimPrefix(value, "rctl_")
	if len(value) != 32 {
		return "", errors.New("generated gateway debug session id is invalid")
	}
	return "dbg_" + value, nil
}
