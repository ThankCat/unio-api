package auth

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

func TestSessionManagerRotatesAndRevokesRefreshTokens(t *testing.T) {
	mini := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mini.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	manager, err := NewSessionManager(client, "test", "01234567890123456789012345678901", time.Minute, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	userUID := "0198c9d7-0af1-7c42-a063-91d2922af371"
	pair, serviceErr := manager.Create(ctx, userUID)
	if serviceErr != nil {
		t.Fatal(serviceErr)
	}
	if pair.UserUID != userUID {
		t.Fatalf("expected public user id %s, got %s", userUID, pair.UserUID)
	}
	claims, parseErr := manager.parse(pair.AccessToken, accessTokenType)
	if parseErr != nil {
		t.Fatal(parseErr)
	}
	if claims.Subject != userUID {
		t.Fatalf("expected JWT sub %s, got %s", userUID, claims.Subject)
	}
	if authenticatedUID, authErr := manager.Authenticate(ctx, pair.AccessToken); authErr != nil || authenticatedUID != userUID {
		t.Fatalf("authenticate access token: uid=%q err=%v", authenticatedUID, authErr)
	}
	rotated, serviceErr := manager.Refresh(ctx, pair.RefreshToken)
	if serviceErr != nil {
		t.Fatalf("refresh: %v cause=%v", serviceErr, serviceErr.Cause)
	}
	if rotated.RefreshToken == pair.RefreshToken || rotated.AccessToken == pair.AccessToken {
		t.Fatal("refresh rotation did not issue new tokens")
	}
	if _, serviceErr := manager.Refresh(ctx, pair.RefreshToken); serviceErr == nil || serviceErr.Code != CodeRefreshTokenInvalid {
		t.Fatalf("old refresh token should be invalid, got %v", serviceErr)
	}
	if serviceErr := manager.Logout(ctx, rotated.RefreshToken); serviceErr != nil {
		t.Fatal(serviceErr)
	}
	if _, authErr := manager.Authenticate(ctx, rotated.AccessToken); authErr == nil || authErr.Code != CodeSessionInvalid {
		t.Fatalf("logged-out access token should be invalid, got %v", authErr)
	}
	if _, serviceErr := manager.Refresh(ctx, rotated.RefreshToken); serviceErr == nil || serviceErr.Code != CodeRefreshTokenInvalid {
		t.Fatalf("logged-out refresh token should be invalid, got %v", serviceErr)
	}
}

func TestSessionManagerRejectsRefreshTokenForAccessAuthentication(t *testing.T) {
	mini := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mini.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	manager, err := NewSessionManager(client, "test", "01234567890123456789012345678901", time.Minute, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	pair, serviceErr := manager.Create(context.Background(), "0198c9d7-0af1-7c42-a063-91d2922af371")
	if serviceErr != nil {
		t.Fatal(serviceErr)
	}
	if _, authErr := manager.Authenticate(context.Background(), pair.RefreshToken); authErr == nil || authErr.Code != CodeSessionInvalid {
		t.Fatalf("refresh token must not authenticate an access request, got %v", authErr)
	}
}

func TestPasswordHashRoundTrip(t *testing.T) {
	hash, err := HashPassword("Password1!")
	if err != nil {
		t.Fatal(err)
	}
	if !VerifyPassword(hash, "Password1!") {
		t.Fatal("password hash should verify")
	}
	if VerifyPassword(hash, "Password2!") {
		t.Fatal("different password should not verify")
	}
}
