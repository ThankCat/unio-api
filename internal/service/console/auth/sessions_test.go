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
	pair, serviceErr := manager.Create(ctx, userUID, SessionMeta{IP: "203.0.113.10", UserAgent: "test-agent"})
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
	pair, serviceErr := manager.Create(context.Background(), "0198c9d7-0af1-7c42-a063-91d2922af371", SessionMeta{})
	if serviceErr != nil {
		t.Fatal(serviceErr)
	}
	if _, authErr := manager.Authenticate(context.Background(), pair.RefreshToken); authErr == nil || authErr.Code != CodeSessionInvalid {
		t.Fatalf("refresh token must not authenticate an access request, got %v", authErr)
	}
}

func TestSessionManagerListsAndSelectivelyRevokesSessions(t *testing.T) {
	mini := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mini.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	manager, err := NewSessionManager(client, "test", "01234567890123456789012345678901", time.Minute, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	// JWT 校验用真实时钟，假时钟只能在真实 now 附近推进，否则令牌一签出来就过期。
	base := time.Now().UTC()
	clock := base
	manager.now = func() time.Time { return clock }

	ctx := context.Background()
	userUID := "0198c9d7-0af1-7c42-a063-91d2922af371"
	otherUID := "0198c9d7-0af1-7c42-a063-91d2922af372"

	first, serviceErr := manager.Create(ctx, userUID, SessionMeta{IP: "203.0.113.10", UserAgent: "Mac Chrome"})
	if serviceErr != nil {
		t.Fatal(serviceErr)
	}
	// 推进量收在 JWT 5 秒 leeway 内，签出的令牌立即可用。
	clock = base.Add(2 * time.Second)
	second, serviceErr := manager.Create(ctx, userUID, SessionMeta{IP: "198.51.100.7", UserAgent: "iPhone Safari"})
	if serviceErr != nil {
		t.Fatal(serviceErr)
	}
	foreign, serviceErr := manager.Create(ctx, otherUID, SessionMeta{IP: "192.0.2.1", UserAgent: "Edge"})
	if serviceErr != nil {
		t.Fatal(serviceErr)
	}

	sessions, listErr := manager.ListSessions(ctx, userUID)
	if listErr != nil {
		t.Fatal(listErr)
	}
	if len(sessions) != 2 {
		t.Fatalf("sessions = %d, want 2", len(sessions))
	}
	// 新会话排前面，元数据完整。
	if sessions[0].SID != second.SessionID || sessions[0].IP != "198.51.100.7" || sessions[0].UserAgent != "iPhone Safari" {
		t.Fatalf("unexpected first session: %+v", sessions[0])
	}
	if sessions[0].CreatedAt.IsZero() || sessions[0].LastSeenAt.IsZero() {
		t.Fatal("session timestamps should be recorded")
	}

	// 吊销不属于自己的 sid 应为无操作：他人会话保持有效。
	if revokeErr := manager.RevokeSession(ctx, userUID, foreign.SessionID); revokeErr != nil {
		t.Fatal(revokeErr)
	}
	if _, authErr := manager.Authenticate(ctx, foreign.AccessToken); authErr != nil {
		t.Fatalf("foreign session must stay valid, got %v", authErr)
	}

	// 注销指定会话：目标失效，其余保留。
	if revokeErr := manager.RevokeSession(ctx, userUID, first.SessionID); revokeErr != nil {
		t.Fatal(revokeErr)
	}
	if _, authErr := manager.Authenticate(ctx, first.AccessToken); authErr == nil {
		t.Fatal("revoked session should be invalid")
	}
	if _, authErr := manager.Authenticate(ctx, second.AccessToken); authErr != nil {
		t.Fatalf("remaining session should stay valid, got %v", authErr)
	}

	// 改密场景：踢掉其他、保留当前。
	clock = base.Add(4 * time.Second)
	third, serviceErr := manager.Create(ctx, userUID, SessionMeta{IP: "203.0.113.99", UserAgent: "Windows Edge"})
	if serviceErr != nil {
		t.Fatal(serviceErr)
	}
	if exceptErr := manager.RevokeUserExcept(ctx, userUID, second.SessionID); exceptErr != nil {
		t.Fatal(exceptErr)
	}
	if _, authErr := manager.Authenticate(ctx, third.AccessToken); authErr == nil {
		t.Fatal("other session should be revoked after password change")
	}
	if _, authErr := manager.Authenticate(ctx, second.AccessToken); authErr != nil {
		t.Fatalf("current session should survive, got %v", authErr)
	}
	remaining, listErr := manager.ListSessions(ctx, userUID)
	if listErr != nil {
		t.Fatal(listErr)
	}
	if len(remaining) != 1 || remaining[0].SID != second.SessionID {
		t.Fatalf("remaining sessions = %+v, want only the kept one", remaining)
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
