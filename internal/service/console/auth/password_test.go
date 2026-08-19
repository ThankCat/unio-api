package auth

import (
	"strings"
	"testing"
)

func TestValidatePasswordDistinguishesExcessiveLength(t *testing.T) {
	if err := ValidatePassword("short"); err == nil || err.Code != CodeInvalidPassword {
		t.Fatalf("short password: got %v", err)
	}
	if err := ValidatePassword(strings.Repeat("a", 129)); err == nil || err.Code != CodePasswordTooLong {
		t.Fatalf("long password: got %v", err)
	}
	if err := ValidatePassword("Password1!"); err != nil {
		t.Fatalf("valid password: %v", err)
	}
}
