package ticket

import (
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestAttachmentSignerRoundTrip(t *testing.T) {
	signer, err := NewAttachmentSigner("0123456789abcdef-secret")
	if err != nil {
		t.Fatalf("NewAttachmentSigner: %v", err)
	}
	uid := uuid.New()
	now := time.Now()
	expiresAt := now.Add(time.Hour).Unix()
	sig := signer.Sign(uid, expiresAt)

	if !signer.Verify(uid, expiresAt, sig, now) {
		t.Fatal("valid signature rejected")
	}
	if signer.Verify(uid, expiresAt, sig, now.Add(2*time.Hour)) {
		t.Fatal("expired signature accepted")
	}
	if signer.Verify(uid, expiresAt, sig+"00", now) {
		t.Fatal("tampered signature accepted")
	}
	if signer.Verify(uuid.New(), expiresAt, sig, now) {
		t.Fatal("signature accepted for different uid")
	}
	if signer.Verify(uid, expiresAt+1, sig, now) {
		t.Fatal("signature accepted for different expiry")
	}
}

func TestAttachmentSignerRejectsShortSecret(t *testing.T) {
	if _, err := NewAttachmentSigner("short"); err == nil {
		t.Fatal("short secret accepted")
	}
}

func TestSignedPathVerifies(t *testing.T) {
	signer, err := NewAttachmentSigner("0123456789abcdef-secret")
	if err != nil {
		t.Fatalf("NewAttachmentSigner: %v", err)
	}
	uid := uuid.New()
	now := time.Now()
	path := signer.SignedPath(uid, now, time.Hour)

	if !strings.HasPrefix(path, "/v1/tickets/attachments/"+uid.String()+"?") {
		t.Fatalf("unexpected path: %s", path)
	}
	parsed, err := url.Parse(path)
	if err != nil {
		t.Fatalf("parse signed path: %v", err)
	}
	expiresAt, err := strconv.ParseInt(parsed.Query().Get("exp"), 10, 64)
	if err != nil {
		t.Fatalf("parse exp: %v", err)
	}
	if !signer.Verify(uid, expiresAt, parsed.Query().Get("sig"), now) {
		t.Fatal("signed path does not verify")
	}
}
