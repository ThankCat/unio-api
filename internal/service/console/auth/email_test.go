package auth

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/ThankCat/unio-gateway/internal/platform/store/sqlc"
	consoleservice "github.com/ThankCat/unio-gateway/internal/service/console"
)

type registrationEmailDB struct {
	exists bool
	err    error
}

func (d registrationEmailDB) Exec(context.Context, string, ...interface{}) (pgconn.CommandTag, error) {
	return pgconn.CommandTag{}, errors.New("unexpected Exec call")
}

func (d registrationEmailDB) Query(context.Context, string, ...interface{}) (pgx.Rows, error) {
	return nil, errors.New("unexpected Query call")
}

func (d registrationEmailDB) QueryRow(context.Context, string, ...interface{}) pgx.Row {
	return registrationEmailRow{exists: d.exists, err: d.err}
}

type registrationEmailRow struct {
	exists bool
	err    error
}

func (r registrationEmailRow) Scan(dest ...interface{}) error {
	if r.err != nil {
		return r.err
	}
	value, ok := dest[0].(*bool)
	if !ok {
		return errors.New("registration email query expected a boolean destination")
	}
	*value = r.exists
	return nil
}

func TestRandomEmailCheckDelayStaysWithinReservedRange(t *testing.T) {
	for i := 0; i < 100; i++ {
		delay := randomEmailCheckDelay()
		if delay < time.Second || delay >= 2*time.Second {
			t.Fatalf("delay %s is outside [1s, 2s)", delay)
		}
	}
}

func TestCheckEmailRequiresActiveAccount(t *testing.T) {
	service := &Service{
		queries:         sqlc.New(registrationEmailDB{exists: true}),
		emailCheckDelay: func() time.Duration { return 0 },
	}
	if err := service.CheckEmail(context.Background(), "invalid"); err == nil || err.Code != CodeInvalidEmail {
		t.Fatalf("expected invalid email error, got %v", err)
	}
	if err := service.CheckEmail(context.Background(), "user@example.com"); err != nil {
		t.Fatalf("expected active email check, got %v", err)
	}

	for _, tc := range []struct {
		name     string
		exists   bool
		queryErr error
		wantCode string
	}{
		{name: "unknown account", wantCode: CodeInvalidCredentials},
		{name: "database failure", queryErr: errors.New("database unavailable"), wantCode: consoleservice.CodeRequestUnavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			service := &Service{
				queries:         sqlc.New(registrationEmailDB{exists: tc.exists, err: tc.queryErr}),
				emailCheckDelay: func() time.Duration { return 0 },
			}
			err := service.CheckEmail(context.Background(), "user@example.com")
			if err == nil || err.Code != tc.wantCode {
				t.Fatalf("expected error code %q, got %v", tc.wantCode, err)
			}
		})
	}
}

func TestCheckRegistrationEmailReturnsOpaqueAvailabilityResult(t *testing.T) {
	for _, tc := range []struct {
		name     string
		exists   bool
		queryErr error
		wantCode string
	}{
		{name: "available"},
		{name: "existing account", exists: true, wantCode: CodeRegistrationUnavailable},
		{name: "database failure", queryErr: errors.New("database unavailable"), wantCode: consoleservice.CodeRequestUnavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			service := &Service{
				queries:         sqlc.New(registrationEmailDB{exists: tc.exists, err: tc.queryErr}),
				emailCheckDelay: func() time.Duration { return 0 },
			}
			err := service.CheckRegistrationEmail(context.Background(), "user@example.com")
			if tc.wantCode == "" {
				if err != nil {
					t.Fatalf("expected available email, got %v", err)
				}
				return
			}
			if err == nil || err.Code != tc.wantCode {
				t.Fatalf("expected error code %q, got %v", tc.wantCode, err)
			}
			if tc.wantCode == CodeRegistrationUnavailable {
				if err.Param != "email" || err.Message != "This email address is unavailable for registration." {
					t.Fatalf("unexpected opaque registration error: %+v", err)
				}
			}
		})
	}
}
