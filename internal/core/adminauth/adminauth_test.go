package adminauth_test

import (
	"context"
	"testing"

	"github.com/ThankCat/unio-api/internal/core/adminauth"
	"github.com/ThankCat/unio-api/internal/platform/failure"
)

func TestNewStaticTokenAuthenticatorEmptyTokenFails(t *testing.T) {
	for _, token := range []string{"", "   "} {
		if _, err := adminauth.NewStaticTokenAuthenticator(token); err == nil {
			t.Fatalf("expected error for token %q", token)
		} else if got := failure.CodeOf(err); got != failure.CodeConfigMissing {
			t.Fatalf("expected %q, got %q", failure.CodeConfigMissing, got)
		}
	}
}

func TestAuthenticateAdminMissingToken(t *testing.T) {
	authenticator, err := adminauth.NewStaticTokenAuthenticator("s3cret-token")
	if err != nil {
		t.Fatalf("new authenticator: %v", err)
	}

	if _, err := authenticator.AuthenticateAdmin(context.Background(), ""); err == nil {
		t.Fatal("expected error for empty token")
	} else if got := failure.CodeOf(err); got != failure.CodeAdminAuthMissingToken {
		t.Fatalf("expected %q, got %q", failure.CodeAdminAuthMissingToken, got)
	}
}

func TestAuthenticateAdminInvalidToken(t *testing.T) {
	authenticator, err := adminauth.NewStaticTokenAuthenticator("s3cret-token")
	if err != nil {
		t.Fatalf("new authenticator: %v", err)
	}

	if _, err := authenticator.AuthenticateAdmin(context.Background(), "wrong-token"); err == nil {
		t.Fatal("expected error for wrong token")
	} else if got := failure.CodeOf(err); got != failure.CodeAdminAuthInvalidToken {
		t.Fatalf("expected %q, got %q", failure.CodeAdminAuthInvalidToken, got)
	}
}

func TestAuthenticateAdminValidToken(t *testing.T) {
	authenticator, err := adminauth.NewStaticTokenAuthenticator("s3cret-token")
	if err != nil {
		t.Fatalf("new authenticator: %v", err)
	}

	principal, err := authenticator.AuthenticateAdmin(context.Background(), "s3cret-token")
	if err != nil {
		t.Fatalf("authenticate valid token: %v", err)
	}

	if principal == nil || principal.Subject != adminauth.SubjectAdmin {
		t.Fatalf("expected subject %q, got %+v", adminauth.SubjectAdmin, principal)
	}
}
