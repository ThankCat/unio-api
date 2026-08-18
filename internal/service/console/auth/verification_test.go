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
