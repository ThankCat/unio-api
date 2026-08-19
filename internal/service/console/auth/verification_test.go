package auth

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

func newTestVerificationStore(t *testing.T) (*VerificationStore, *redis.Client, *miniredis.Miniredis) {
	t.Helper()
	mini := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mini.Addr()})
	store, err := NewVerificationStore(client, "test", "01234567890123456789012345678901", "123456", nil)
	if err != nil {
		t.Fatal(err)
	}
	return store, client, mini
}

func TestVerificationChallengeLifecycle(t *testing.T) {
	store, client, _ := newTestVerificationStore(t)
	t.Cleanup(func() { _ = client.Close() })
	ctx := context.Background()

	challenge, err := store.Issue(ctx, "User@example.com", PurposeRegister, "127.0.0.1")
	if err != nil {
		t.Fatalf("issue challenge: %v", err)
	}
	if challenge.ID == "" || challenge.ExpiresIn != 600 || challenge.ResendAfter != 30 {
		t.Fatalf("unexpected challenge: %+v", challenge)
	}
	if _, err := store.Reserve(ctx, "User@example.com", PurposeLogin, "127.0.0.1", challenge.ID, "123456"); err == nil || err.Code != CodeVerificationChallengeUnavailable {
		t.Fatalf("cross-purpose verification should fail, got %v", err)
	}
	for attempt := 0; attempt < 4; attempt++ {
		_, err := store.Reserve(ctx, "User@example.com", PurposeRegister, "127.0.0.1", challenge.ID, "000000")
		if err == nil || err.Code != CodeVerificationCodeInvalid {
			t.Fatalf("wrong attempt %d: got %v", attempt+1, err)
		}
		wantRemaining := 4 - attempt
		if err.RemainingAttempts == nil || *err.RemainingAttempts != wantRemaining {
			t.Fatalf("wrong attempt %d: remaining attempts = %v, want %d", attempt+1, err.RemainingAttempts, wantRemaining)
		}
	}
	_, err = store.Reserve(ctx, "User@example.com", PurposeRegister, "127.0.0.1", challenge.ID, "000000")
	if err == nil || err.Code != CodeVerificationAttemptsExhausted {
		t.Fatalf("fifth wrong attempt: got %v", err)
	}
	if _, err := store.Reserve(ctx, "User@example.com", PurposeRegister, "127.0.0.1", challenge.ID, "123456"); err == nil || err.Code != CodeVerificationAttemptsExhausted {
		t.Fatalf("exhausted challenge accepted correct code: %v", err)
	}
}

func TestVerificationChallengeCanBeConsumedAndSuperseded(t *testing.T) {
	store, client, _ := newTestVerificationStore(t)
	t.Cleanup(func() { _ = client.Close() })
	ctx := context.Background()

	first, err := store.Issue(ctx, "user@example.com", PurposeLogin, "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	store.now = func() time.Time { return time.Now().Add(31 * time.Second) }
	second, err := store.Issue(ctx, "user@example.com", PurposeLogin, "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Reserve(ctx, "user@example.com", PurposeLogin, "127.0.0.1", first.ID, "123456"); err == nil || err.Code != CodeVerificationChallengeUnavailable {
		t.Fatalf("superseded challenge should be unavailable, got %v", err)
	}
	reservation, err := store.Reserve(ctx, "user@example.com", PurposeLogin, "127.0.0.1", second.ID, "123456")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Commit(ctx, "user@example.com", PurposeLogin, reservation); err != nil {
		t.Fatalf("commit challenge: %v", err)
	}
	if _, err := store.Reserve(ctx, "user@example.com", PurposeLogin, "127.0.0.1", second.ID, "123456"); err == nil || err.Code != CodeVerificationChallengeUnavailable {
		t.Fatalf("consumed challenge should be unavailable, got %v", err)
	}
}

func TestPasswordResetGrantIsShortLivedAndSingleUse(t *testing.T) {
	store, client, mini := newTestVerificationStore(t)
	t.Cleanup(func() { _ = client.Close() })
	ctx := context.Background()

	challenge, err := store.Issue(ctx, "user@example.com", PurposePasswordReset, "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	challengeReservation, err := store.Reserve(
		ctx, "user@example.com", PurposePasswordReset, "127.0.0.1", challenge.ID, "123456",
	)
	if err != nil {
		t.Fatal(err)
	}
	grant, issueErr := store.IssuePasswordResetGrant(
		ctx,
		"user@example.com",
		challengeReservation,
		"0198c9d7-0af1-7c42-a063-91d2922af371",
	)
	if issueErr != nil {
		t.Fatal(issueErr)
	}
	if !validPasswordResetToken(grant.Token) || grant.ExpiresIn != 600 {
		t.Fatalf("unexpected password reset grant: %+v", grant)
	}
	if _, reserveErr := store.Reserve(
		ctx, "user@example.com", PurposePasswordReset, "127.0.0.1", challenge.ID, "123456",
	); reserveErr == nil || reserveErr.Code != CodeVerificationChallengeUnavailable {
		t.Fatalf("consumed reset challenge should be unavailable, got %v", reserveErr)
	}
	for _, key := range mini.Keys() {
		if key == grant.Token {
			t.Fatal("raw password reset credential must not be used as a Redis key")
		}
	}

	reservation, reserveErr := store.ReservePasswordResetGrant(ctx, grant.Token)
	if reserveErr != nil {
		t.Fatal(reserveErr)
	}
	if reservation.UserUID != "0198c9d7-0af1-7c42-a063-91d2922af371" {
		t.Fatalf("unexpected reset subject %q", reservation.UserUID)
	}
	if _, concurrentErr := store.ReservePasswordResetGrant(ctx, grant.Token); concurrentErr == nil || concurrentErr.Code != CodePasswordResetTokenUnavailable {
		t.Fatalf("concurrent reset credential use should fail, got %v", concurrentErr)
	}
	store.now = func() time.Time { return time.Now().Add(reservationTTL + time.Second) }
	recoveredReservation, recoveredErr := store.ReservePasswordResetGrant(ctx, grant.Token)
	if recoveredErr != nil {
		t.Fatalf("stale reset reservation should recover: %v", recoveredErr)
	}
	if releaseErr := store.ReleasePasswordResetGrant(ctx, recoveredReservation); releaseErr != nil {
		t.Fatal(releaseErr)
	}
	store.now = time.Now
	reservation, reserveErr = store.ReservePasswordResetGrant(ctx, grant.Token)
	if reserveErr != nil {
		t.Fatal(reserveErr)
	}
	if releaseErr := store.ReleasePasswordResetGrant(ctx, reservation); releaseErr != nil {
		t.Fatal(releaseErr)
	}
	retryReservation, retryErr := store.ReservePasswordResetGrant(ctx, grant.Token)
	if retryErr != nil {
		t.Fatalf("released reset credential should be reusable: %v", retryErr)
	}
	if commitErr := store.CommitPasswordResetGrant(ctx, retryReservation); commitErr != nil {
		t.Fatal(commitErr)
	}
	if _, consumedErr := store.ReservePasswordResetGrant(ctx, grant.Token); consumedErr == nil || consumedErr.Code != CodePasswordResetTokenUnavailable {
		t.Fatalf("consumed reset credential should fail, got %v", consumedErr)
	}
}

func TestPasswordResetGrantExpires(t *testing.T) {
	store, client, mini := newTestVerificationStore(t)
	t.Cleanup(func() { _ = client.Close() })
	ctx := context.Background()

	challenge, err := store.Issue(ctx, "user@example.com", PurposePasswordReset, "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	reservation, err := store.Reserve(
		ctx, "user@example.com", PurposePasswordReset, "127.0.0.1", challenge.ID, "123456",
	)
	if err != nil {
		t.Fatal(err)
	}
	grant, issueErr := store.IssuePasswordResetGrant(ctx, "user@example.com", reservation, "0198c9d7-0af1-7c42-a063-91d2922af371")
	if issueErr != nil {
		t.Fatal(issueErr)
	}
	mini.FastForward(passwordResetGrantTTL)
	if _, reserveErr := store.ReservePasswordResetGrant(ctx, grant.Token); reserveErr == nil || reserveErr.Code != CodePasswordResetTokenUnavailable {
		t.Fatalf("expired reset credential should fail, got %v", reserveErr)
	}
}
