package credential

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"io"
	"testing"

	"github.com/ThankCat/unio-api/internal/platform/failure"
)

func testMasterKey(t *testing.T) []byte {
	t.Helper()

	key := make([]byte, masterKeySize)
	if _, err := io.ReadFull(rand.Reader, key); err != nil {
		t.Fatalf("generate test master key: %v", err)
	}

	return key
}

func TestParseMasterKeyAcceptsValidBase64Key(t *testing.T) {
	raw := testMasterKey(t)
	encoded := base64.StdEncoding.EncodeToString(raw)

	got, err := ParseMasterKey(encoded)
	if err != nil {
		t.Fatalf("ParseMasterKey: %v", err)
	}

	if len(got) != masterKeySize {
		t.Fatalf("key length: got %d, want %d", len(got), masterKeySize)
	}
}

func TestParseMasterKeyTrimsWhitespace(t *testing.T) {
	raw := testMasterKey(t)
	encoded := "  " + base64.StdEncoding.EncodeToString(raw) + "  "

	got, err := ParseMasterKey(encoded)
	if err != nil {
		t.Fatalf("ParseMasterKey: %v", err)
	}

	if len(got) != masterKeySize {
		t.Fatalf("key length: got %d, want %d", len(got), masterKeySize)
	}
}

func TestParseMasterKeyRejectsEmpty(t *testing.T) {
	_, err := ParseMasterKey("   ")
	if err == nil {
		t.Fatal("ParseMasterKey: got nil error, want invalid master key")
	}

	if !errors.Is(err, ErrMasterKeyInvalid) {
		t.Fatalf("ParseMasterKey: got %v, want ErrMasterKeyInvalid", err)
	}
	if got := failure.CodeOf(err); got != failure.CodeCredentialMasterKeyInvalid {
		t.Fatalf("ParseMasterKey code: got %q, want %q", got, failure.CodeCredentialMasterKeyInvalid)
	}
}

func TestParseMasterKeyRejectsInvalidBase64(t *testing.T) {
	_, err := ParseMasterKey("not-valid-base64!!!")
	if err == nil {
		t.Fatal("ParseMasterKey: got nil error, want invalid master key")
	}

	if !errors.Is(err, ErrMasterKeyInvalid) {
		t.Fatalf("ParseMasterKey: got %v, want ErrMasterKeyInvalid", err)
	}
	if got := failure.CodeOf(err); got != failure.CodeCredentialMasterKeyInvalid {
		t.Fatalf("ParseMasterKey code: got %q, want %q", got, failure.CodeCredentialMasterKeyInvalid)
	}
}

func TestParseMasterKeyRejectsWrongLength(t *testing.T) {
	shortKey := base64.StdEncoding.EncodeToString([]byte("too-short"))

	_, err := ParseMasterKey(shortKey)
	if err == nil {
		t.Fatal("ParseMasterKey: got nil error, want invalid master key")
	}

	if !errors.Is(err, ErrMasterKeyInvalid) {
		t.Fatalf("ParseMasterKey: got %v, want ErrMasterKeyInvalid", err)
	}
	if got := failure.CodeOf(err); got != failure.CodeCredentialMasterKeyInvalid {
		t.Fatalf("ParseMasterKey code: got %q, want %q", got, failure.CodeCredentialMasterKeyInvalid)
	}
}

func TestNewAESGCMCipherRejectsWrongKeyLength(t *testing.T) {
	_, err := NewAESGCMCipher([]byte("short"))
	if err == nil {
		t.Fatal("NewAESGCMCipher: got nil error, want invalid master key")
	}

	if !errors.Is(err, ErrMasterKeyInvalid) {
		t.Fatalf("NewAESGCMCipher: got %v, want ErrMasterKeyInvalid", err)
	}
	if got := failure.CodeOf(err); got != failure.CodeCredentialMasterKeyInvalid {
		t.Fatalf("NewAESGCMCipher code: got %q, want %q", got, failure.CodeCredentialMasterKeyInvalid)
	}
}

func TestAESGCMCipherEncryptDecryptRoundTrip(t *testing.T) {
	c, err := NewAESGCMCipher(testMasterKey(t))
	if err != nil {
		t.Fatalf("NewAESGCMCipher: %v", err)
	}

	want := "sk-proj-test123"
	encrypted, err := c.Encrypt(want)
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}

	got, err := c.Decrypt(encrypted)
	if err != nil {
		t.Fatalf("Decrypt: %v", err)
	}

	if got != want {
		t.Fatalf("Decrypt: got %q, want %q", got, want)
	}
}

func TestAESGCMCipherEncryptProducesDifferentCiphertextForSamePlaintext(t *testing.T) {
	c, err := NewAESGCMCipher(testMasterKey(t))
	if err != nil {
		t.Fatalf("NewAESGCMCipher: %v", err)
	}

	plaintext := "sk-proj-test123"

	first, err := c.Encrypt(plaintext)
	if err != nil {
		t.Fatalf("first Encrypt: %v", err)
	}

	second, err := c.Encrypt(plaintext)
	if err != nil {
		t.Fatalf("second Encrypt: %v", err)
	}

	if string(first) == string(second) {
		t.Fatal("expected different ciphertext for same plaintext")
	}

	if got, err := c.Decrypt(first); err != nil || got != plaintext {
		t.Fatalf("Decrypt first: got %q err=%v", got, err)
	}
	if got, err := c.Decrypt(second); err != nil || got != plaintext {
		t.Fatalf("Decrypt second: got %q err=%v", got, err)
	}
}

func TestAESGCMCipherEncryptRejectsEmptyPlaintext(t *testing.T) {
	c, err := NewAESGCMCipher(testMasterKey(t))
	if err != nil {
		t.Fatalf("NewAESGCMCipher: %v", err)
	}

	_, err = c.Encrypt("   ")
	if err == nil {
		t.Fatal("Encrypt: got nil error, want empty plaintext error")
	}

	if !errors.Is(err, ErrPlaintextEmpty) {
		t.Fatalf("Encrypt: got %v, want ErrPlaintextEmpty", err)
	}
	if got := failure.CodeOf(err); got != failure.CodeCredentialEncryptFailed {
		t.Fatalf("Encrypt code: got %q, want %q", got, failure.CodeCredentialEncryptFailed)
	}
}

func TestAESGCMCipherDecryptRejectsTooShortCiphertext(t *testing.T) {
	c, err := NewAESGCMCipher(testMasterKey(t))
	if err != nil {
		t.Fatalf("NewAESGCMCipher: %v", err)
	}

	_, err = c.Decrypt([]byte{1, 2, 3})
	if err == nil {
		t.Fatal("Decrypt: got nil error, want invalid ciphertext")
	}

	if !errors.Is(err, ErrCiphertextInvalid) {
		t.Fatalf("Decrypt: got %v, want ErrCiphertextInvalid", err)
	}
	if got := failure.CodeOf(err); got != failure.CodeCredentialCiphertextInvalid {
		t.Fatalf("Decrypt code: got %q, want %q", got, failure.CodeCredentialCiphertextInvalid)
	}
}

func TestAESGCMCipherDecryptRejectsTamperedCiphertext(t *testing.T) {
	c, err := NewAESGCMCipher(testMasterKey(t))
	if err != nil {
		t.Fatalf("NewAESGCMCipher: %v", err)
	}

	encrypted, err := c.Encrypt("sk-proj-test123")
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}

	tampered := append([]byte(nil), encrypted...)
	tampered[len(tampered)-1] ^= 0xFF

	_, err = c.Decrypt(tampered)
	if err == nil {
		t.Fatal("Decrypt: got nil error, want decrypt failed")
	}

	if got := failure.CodeOf(err); got != failure.CodeCredentialDecryptFailed {
		t.Fatalf("Decrypt code: got %q, want %q", got, failure.CodeCredentialDecryptFailed)
	}
}

func TestAESGCMCipherDecryptRejectsWrongKey(t *testing.T) {
	encryptCipher, err := NewAESGCMCipher(testMasterKey(t))
	if err != nil {
		t.Fatalf("NewAESGCMCipher encrypt: %v", err)
	}

	decryptCipher, err := NewAESGCMCipher(testMasterKey(t))
	if err != nil {
		t.Fatalf("NewAESGCMCipher decrypt: %v", err)
	}

	encrypted, err := encryptCipher.Encrypt("sk-proj-test123")
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}

	_, err = decryptCipher.Decrypt(encrypted)
	if err == nil {
		t.Fatal("Decrypt: got nil error, want decrypt failed")
	}

	if got := failure.CodeOf(err); got != failure.CodeCredentialDecryptFailed {
		t.Fatalf("Decrypt code: got %q, want %q", got, failure.CodeCredentialDecryptFailed)
	}
}

func TestParseMasterKeyRoundTripWithAESGCMCipher(t *testing.T) {
	raw := testMasterKey(t)
	encoded := base64.StdEncoding.EncodeToString(raw)

	key, err := ParseMasterKey(encoded)
	if err != nil {
		t.Fatalf("ParseMasterKey: %v", err)
	}

	c, err := NewAESGCMCipher(key)
	if err != nil {
		t.Fatalf("NewAESGCMCipher: %v", err)
	}

	want := "sk-proj-from-env"
	encrypted, err := c.Encrypt(want)
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}

	got, err := c.Decrypt(encrypted)
	if err != nil {
		t.Fatalf("Decrypt: %v", err)
	}

	if got != want {
		t.Fatalf("Decrypt: got %q, want %q", got, want)
	}
}

var _ Cipher = (*AESGCMCipher)(nil)
