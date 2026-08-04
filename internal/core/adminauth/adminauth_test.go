package adminauth_test

import (
	"context"
	"errors"
	"testing"

	"github.com/ThankCat/unio-gateway/internal/core/adminauth"
	"github.com/ThankCat/unio-gateway/internal/platform/failure"
)

const (
	testSessionToken = "s3cret-session-token"
	testUsername     = "admin"
	testPassword     = "s3cret-password"
)

// fakeSessions 覆盖三种会话结果：命中、未命中、存储故障。
type fakeSessions struct {
	valid string
	err   error
}

func (f fakeSessions) Validate(_ context.Context, token string) (bool, error) {
	if f.err != nil {
		return false, f.err
	}
	return token == f.valid, nil
}

func newSessionAuth(t *testing.T, sessions adminauth.SessionValidator) *adminauth.SessionAuthenticator {
	t.Helper()

	a, err := adminauth.NewSessionAuthenticator(sessions)
	if err != nil {
		t.Fatalf("new session authenticator: %v", err)
	}
	return a
}

func TestNewSessionAuthenticatorRequiresStore(t *testing.T) {
	if _, err := adminauth.NewSessionAuthenticator(nil); err == nil {
		t.Fatal("expected error for nil session store")
	} else if got := failure.CodeOf(err); got != failure.CodeConfigMissing {
		t.Fatalf("expected %q, got %q", failure.CodeConfigMissing, got)
	}
}

func TestAuthenticateAdminMissingToken(t *testing.T) {
	a := newSessionAuth(t, fakeSessions{valid: testSessionToken})

	if _, err := a.AuthenticateAdmin(context.Background(), ""); err == nil {
		t.Fatal("expected error for empty token")
	} else if got := failure.CodeOf(err); got != failure.CodeAdminAuthMissingToken {
		t.Fatalf("expected %q, got %q", failure.CodeAdminAuthMissingToken, got)
	}
}

func TestAuthenticateAdminExpiredSession(t *testing.T) {
	a := newSessionAuth(t, fakeSessions{valid: testSessionToken})

	if _, err := a.AuthenticateAdmin(context.Background(), "stale-token"); err == nil {
		t.Fatal("expected error for unknown token")
	} else if got := failure.CodeOf(err); got != failure.CodeAdminAuthSessionExpired {
		t.Fatalf("expected %q, got %q", failure.CodeAdminAuthSessionExpired, got)
	}
}

// TestAuthenticateAdminStoreFailureIsNotExpired 冻结一条运维关键区分：
// 会话存储故障必须原样上抛，绝不能降级成「登录已过期」——否则 Redis 抖动会表现为
// 管理员被反复登出，把基础设施故障伪装成正常的会话失效。
func TestAuthenticateAdminStoreFailureIsNotExpired(t *testing.T) {
	storeErr := failure.New(failure.CodeAdminSessionStoreFailed, failure.WithMessage("redis down"))
	a := newSessionAuth(t, fakeSessions{valid: testSessionToken, err: storeErr})

	_, err := a.AuthenticateAdmin(context.Background(), testSessionToken)
	if err == nil {
		t.Fatal("expected error when session store fails")
	}
	if got := failure.CodeOf(err); got != failure.CodeAdminSessionStoreFailed {
		t.Fatalf("expected %q, got %q", failure.CodeAdminSessionStoreFailed, got)
	}
	if errors.Is(err, adminauth.ErrSessionExpired) {
		t.Fatal("store failure must not be reported as an expired session")
	}
}

func TestAuthenticateAdminValidSession(t *testing.T) {
	a := newSessionAuth(t, fakeSessions{valid: testSessionToken})

	principal, err := a.AuthenticateAdmin(context.Background(), testSessionToken)
	if err != nil {
		t.Fatalf("authenticate valid session: %v", err)
	}
	if principal == nil || principal.Subject != adminauth.SubjectAdmin {
		t.Fatalf("expected subject %q, got %+v", adminauth.SubjectAdmin, principal)
	}
}

func TestNewStaticCredentialAuthenticatorRequiresBothFields(t *testing.T) {
	cases := []struct{ name, username, password string }{
		{"empty username", "", testPassword},
		{"blank username", "   ", testPassword},
		{"empty password", testUsername, ""},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := adminauth.NewStaticCredentialAuthenticator(tc.username, tc.password); err == nil {
				t.Fatal("expected config_missing error")
			} else if got := failure.CodeOf(err); got != failure.CodeConfigMissing {
				t.Fatalf("expected %q, got %q", failure.CodeConfigMissing, got)
			}
		})
	}
}

func TestAuthenticateCredentials(t *testing.T) {
	a, err := adminauth.NewStaticCredentialAuthenticator(testUsername, testPassword)
	if err != nil {
		t.Fatalf("new credential authenticator: %v", err)
	}

	principal, err := a.AuthenticateCredentials(context.Background(), testUsername, testPassword)
	if err != nil {
		t.Fatalf("authenticate valid credentials: %v", err)
	}
	if principal == nil || principal.Subject != adminauth.SubjectAdmin {
		t.Fatalf("expected subject %q, got %+v", adminauth.SubjectAdmin, principal)
	}

	// 用户名错、口令错、两者皆空都必须落到同一个错误码，
	// 否则调用方可以据此区分「用户名存在但口令错」，等于泄露了有效用户名。
	rejected := []struct{ name, username, password string }{
		{"wrong password", testUsername, "nope"},
		{"wrong username", "root", testPassword},
		{"both empty", "", ""},
		{"case sensitive username", "Admin", testPassword},
	}

	for _, tc := range rejected {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := a.AuthenticateCredentials(context.Background(), tc.username, tc.password); err == nil {
				t.Fatal("expected rejection")
			} else if got := failure.CodeOf(err); got != failure.CodeAdminAuthInvalidCredentials {
				t.Fatalf("expected %q, got %q", failure.CodeAdminAuthInvalidCredentials, got)
			}
		})
	}
}
