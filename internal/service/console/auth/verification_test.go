package auth

import (
	"context"
	"strings"
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

func TestRandomVerificationCodesAreSixDigits(t *testing.T) {
	mini := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mini.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	store, err := NewVerificationStore(client, "test", "01234567890123456789012345678901", "", nil)
	if err != nil {
		t.Fatal(err)
	}

	seen := make(map[string]struct{})
	for i := 0; i < 32; i++ {
		code, codeErr := store.newCode()
		if codeErr != nil {
			t.Fatal(codeErr)
		}
		if len(code) != 6 || strings.Trim(code, "0123456789") != "" {
			t.Fatalf("unexpected code format %q", code)
		}
		seen[code] = struct{}{}
	}
	if len(seen) == 1 {
		t.Fatalf("random generator returned one repeated value: %v", seen)
	}
}

func TestPasswordChallengePurposeIsBoundToAccountAndPurpose(t *testing.T) {
	store, client, _ := newTestVerificationStore(t)
	t.Cleanup(func() { _ = client.Close() })
	ctx := context.Background()

	challenge, _, err := store.Issue(ctx, "user@example.com", PurposePasswordSet, "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	purpose, purposeErr := store.PasswordPurpose(ctx, "user@example.com", challenge.ID)
	if purposeErr != nil || purpose != PurposePasswordSet {
		t.Fatalf("unexpected password purpose: purpose=%q err=%v", purpose, purposeErr)
	}
	if _, crossUserErr := store.PasswordPurpose(ctx, "other@example.com", challenge.ID); crossUserErr == nil || crossUserErr.Code != CodeVerificationChallengeUnavailable {
		t.Fatalf("cross-user challenge should be unavailable, got %v", crossUserErr)
	}

	publicChallenge, _, issueErr := store.Issue(ctx, "user@example.com", PurposeLogin, "127.0.0.1")
	if issueErr != nil {
		t.Fatal(issueErr)
	}
	if _, publicErr := store.PasswordPurpose(ctx, "user@example.com", publicChallenge.ID); publicErr == nil || publicErr.Code != CodeVerificationChallengeUnavailable {
		t.Fatalf("public-purpose challenge should be unavailable, got %v", publicErr)
	}
}

func TestVerificationChallengeLifecycle(t *testing.T) {
	store, client, _ := newTestVerificationStore(t)
	t.Cleanup(func() { _ = client.Close() })
	ctx := context.Background()

	challenge, _, err := store.Issue(ctx, "User@example.com", PurposeRegister, "127.0.0.1")
	if err != nil {
		t.Fatalf("issue challenge: %v", err)
	}
	if challenge.ID == "" || challenge.ExpiresIn != 600 || challenge.ResendAfter != 60 {
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

	first, _, err := store.Issue(ctx, "user@example.com", PurposeLogin, "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	// 60 秒重发窗口（send_email_purpose {60s,1}）过后才允许再次签发。
	store.now = func() time.Time { return time.Now().Add(61 * time.Second) }
	second, _, err := store.Issue(ctx, "user@example.com", PurposeLogin, "127.0.0.1")
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

	challenge, _, err := store.Issue(ctx, "user@example.com", PurposePasswordReset, "127.0.0.1")
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

	challenge, _, err := store.Issue(ctx, "user@example.com", PurposePasswordReset, "127.0.0.1")
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
