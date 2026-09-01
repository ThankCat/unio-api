package auth

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/redis/go-redis/v9"
	"go.uber.org/zap"

	emailsvc "github.com/ThankCat/unio-gateway/internal/service/email"
)

type passwordUpdateDB struct {
	mu             sync.Mutex
	uid            pgtype.UUID
	email          string
	displayName    string
	passwordHash   pgtype.Text
	forceNoRows    bool
	unexpectedCall error
}

func (d *passwordUpdateDB) Exec(_ context.Context, query string, args ...interface{}) (pgconn.CommandTag, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if !strings.Contains(query, "UPDATE users") || len(args) != 2 {
		d.unexpectedCall = errors.New("unexpected password update Exec call")
		return pgconn.CommandTag{}, d.unexpectedCall
	}
	hash, ok := args[0].(pgtype.Text)
	if !ok {
		d.unexpectedCall = errors.New("password update hash is not pgtype.Text")
		return pgconn.CommandTag{}, d.unexpectedCall
	}
	if d.forceNoRows || (strings.Contains(query, "password_hash IS NULL") && d.passwordHash.Valid) ||
		(strings.Contains(query, "password_hash IS NOT NULL") && !d.passwordHash.Valid) {
		return pgconn.NewCommandTag("UPDATE 0"), nil
	}
	d.passwordHash = hash
	return pgconn.NewCommandTag("UPDATE 1"), nil
}

func (d *passwordUpdateDB) Query(context.Context, string, ...interface{}) (pgx.Rows, error) {
	return nil, errors.New("unexpected Query call")
}

func (d *passwordUpdateDB) QueryRow(_ context.Context, query string, _ ...interface{}) pgx.Row {
	d.mu.Lock()
	defer d.mu.Unlock()
	if !strings.Contains(query, "FROM users") {
		return passwordUpdateRow{err: errors.New("unexpected QueryRow call")}
	}
	return passwordUpdateRow{
		uid:          d.uid,
		email:        d.email,
		displayName:  d.displayName,
		passwordHash: d.passwordHash,
	}
}

func (d *passwordUpdateDB) configured() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.passwordHash.Valid
}

func (d *passwordUpdateDB) setConfigured(configured bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.passwordHash = pgtype.Text{String: "existing-hash", Valid: configured}
}

type passwordUpdateRow struct {
	uid          pgtype.UUID
	email        string
	displayName  string
	passwordHash pgtype.Text
	err          error
}

func (r passwordUpdateRow) Scan(dest ...interface{}) error {
	if r.err != nil {
		return r.err
	}
	if len(dest) != 8 {
		return errors.New("unexpected password user scan shape")
	}
	*dest[0].(*int64) = 42
	*dest[1].(*pgtype.UUID) = r.uid
	*dest[2].(*string) = r.email
	*dest[3].(*pgtype.Text) = r.passwordHash
	*dest[4].(*string) = r.displayName
	*dest[5].(*string) = "active"
	*dest[6].(*pgtype.Timestamptz) = pgtype.Timestamptz{Time: time.Now(), Valid: true}
	*dest[7].(*pgtype.Timestamptz) = pgtype.Timestamptz{Time: time.Now(), Valid: true}
	return nil
}

type passwordChallengeMailer struct {
	kind emailsvc.MessageKind
}

func (m *passwordChallengeMailer) SendVerificationCode(_ context.Context, in emailsvc.VerificationCodeMail) error {
	m.kind = in.Kind
	return nil
}

func newPasswordUpdateService(t *testing.T, configured bool) (*Service, *passwordUpdateDB, *SessionManager, *redis.Client, *passwordChallengeMailer) {
	t.Helper()
	mini := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mini.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	secret := "01234567890123456789012345678901"
	verification, err := NewVerificationStore(client, "test", secret, "123456", nil)
	if err != nil {
		t.Fatal(err)
	}
	sessions, err := NewSessionManager(client, "test", secret, 15*time.Minute, 24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	limiter, err := NewPasswordLoginLimiter(client, "test", secret, nil)
	if err != nil {
		t.Fatal(err)
	}
	userUUID := uuid.MustParse("0198c9d7-0af1-7c42-a063-91d2922af371")
	db := &passwordUpdateDB{
		uid:            pgtype.UUID{Bytes: [16]byte(userUUID), Valid: true},
		email:          "user@example.com",
		displayName:    "用户2026",
		passwordHash:   pgtype.Text{String: "existing-hash", Valid: configured},
		forceNoRows:    false,
		unexpectedCall: nil,
	}
	service, err := NewService(db, verification, sessions, limiter, zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	mailer := &passwordChallengeMailer{}
	service.WithCodeMailer(mailer)
	return service, db, sessions, client, mailer
}

func TestPasswordUpdateStateAndSessionPolicy(t *testing.T) {
	for _, tt := range []struct {
		name             string
		configured       bool
		wantMailKind     emailsvc.MessageKind
		otherSessionLive bool
	}{
		{name: "set password", wantMailKind: emailsvc.KindVerificationPasswordSet, otherSessionLive: true},
		{name: "change password", configured: true, wantMailKind: emailsvc.KindVerificationPasswordChange},
	} {
		t.Run(tt.name, func(t *testing.T) {
			service, db, sessions, _, mailer := newPasswordUpdateService(t, tt.configured)
			ctx := context.Background()
			uid := uuidString(db.uid)
			current, currentErr := sessions.Create(ctx, uid, SessionMeta{IP: "127.0.0.1", UserAgent: "current"})
			if currentErr != nil {
				t.Fatal(currentErr)
			}
			other, otherErr := sessions.Create(ctx, uid, SessionMeta{IP: "127.0.0.2", UserAgent: "other"})
			if otherErr != nil {
				t.Fatal(otherErr)
			}

			challenge, challengeErr := service.SendPasswordChallenge(ctx, current.AccessToken, "127.0.0.1", "zh-CN")
			if challengeErr != nil {
				t.Fatal(challengeErr)
			}
			if mailer.kind != tt.wantMailKind {
				t.Fatalf("mail kind = %q, want %q", mailer.kind, tt.wantMailKind)
			}
			if updateErr := service.UpdatePassword(ctx, current.AccessToken, challenge.ID, "123456", "Password2!", "127.0.0.1"); updateErr != nil {
				t.Fatal(updateErr)
			}
			if !db.configured() {
				t.Fatal("password should be configured")
			}
			if _, authErr := sessions.Authenticate(ctx, current.AccessToken); authErr != nil {
				t.Fatalf("current session should remain active: %v", authErr)
			}
			_, otherAuthErr := sessions.Authenticate(ctx, other.AccessToken)
			if tt.otherSessionLive && otherAuthErr != nil {
				t.Fatalf("other session should remain active: %v", otherAuthErr)
			}
			if !tt.otherSessionLive && (otherAuthErr == nil || otherAuthErr.Code != CodeSessionInvalid) {
				t.Fatalf("other session should be revoked, got %v", otherAuthErr)
			}
		})
	}
}

func TestPasswordUpdateRejectsChangedStateAndConsumesConditionalRace(t *testing.T) {
	service, db, sessions, _, _ := newPasswordUpdateService(t, false)
	ctx := context.Background()
	pair, sessionErr := sessions.Create(ctx, uuidString(db.uid), SessionMeta{})
	if sessionErr != nil {
		t.Fatal(sessionErr)
	}
	challenge, challengeErr := service.SendPasswordChallenge(ctx, pair.AccessToken, "127.0.0.1", "en")
	if challengeErr != nil {
		t.Fatal(challengeErr)
	}
	db.setConfigured(true)
	if err := service.UpdatePassword(ctx, pair.AccessToken, challenge.ID, "123456", "Password2!", "127.0.0.1"); err == nil || err.Code != CodePasswordStateChanged {
		t.Fatalf("changed state should return conflict, got %v", err)
	}

	service, db, sessions, _, _ = newPasswordUpdateService(t, false)
	pair, sessionErr = sessions.Create(ctx, uuidString(db.uid), SessionMeta{})
	if sessionErr != nil {
		t.Fatal(sessionErr)
	}
	challenge, challengeErr = service.SendPasswordChallenge(ctx, pair.AccessToken, "127.0.0.1", "en")
	if challengeErr != nil {
		t.Fatal(challengeErr)
	}
	db.forceNoRows = true
	if err := service.UpdatePassword(ctx, pair.AccessToken, challenge.ID, "123456", "Password2!", "127.0.0.1"); err == nil || err.Code != CodePasswordStateChanged {
		t.Fatalf("conditional race should return conflict, got %v", err)
	}
	if err := service.UpdatePassword(ctx, pair.AccessToken, challenge.ID, "123456", "Password2!", "127.0.0.1"); err == nil || err.Code != CodeVerificationChallengeUnavailable {
		t.Fatalf("race challenge should be consumed, got %v", err)
	}
}

func TestPasswordLoginRejectsPasswordlessAccount(t *testing.T) {
	service, _, _, _, _ := newPasswordUpdateService(t, false)
	if _, _, err := service.PasswordLogin(context.Background(), "user@example.com", "Password1!", "127.0.0.1", "test"); err == nil || err.Code != CodeInvalidCredentials {
		t.Fatalf("passwordless login should return opaque credentials error, got %v", err)
	}
}
