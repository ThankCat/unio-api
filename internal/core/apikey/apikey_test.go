package apikey_test

import (
	"strings"
	"testing"

	"github.com/ThankCat/unio-gateway/internal/core/apikey"
)

func TestGenerate(t *testing.T) {
	key, err := apikey.Generate()
	if err != nil {
		t.Fatalf("generate api key: %v", err)
	}

	if key.Plaintext == "" || key.Prefix == "" || key.Hash == "" {
		t.Fatal("expected plaintext, prefix, and hash to be non-empty")
	}

	if !strings.HasPrefix(key.Plaintext, "unio_sk_") {
		t.Fatal("expected plaintext to start with unio_sk_")
	}

	if len(key.Prefix) >= len(key.Plaintext) {
		t.Fatal("expected prefix to be shorter than plaintext")
	}

	if apikey.Hash(key.Plaintext) == key.Plaintext {
		t.Fatal("expected hash to differ from plaintext")
	}

	if !apikey.Verify(key.Plaintext, key.Hash) {
		t.Fatal("expected generated key to verify")
	}
}

func TestGeneratePlaintextIsBase62(t *testing.T) {
	key, err := apikey.Generate()
	if err != nil {
		t.Fatalf("generate api key: %v", err)
	}

	random := strings.TrimPrefix(key.Plaintext, "unio_sk_")
	if random == key.Plaintext {
		t.Fatal("expected plaintext to start with unio_sk_")
	}

	for _, c := range random {
		isDigit := c >= '0' && c <= '9'
		isUpper := c >= 'A' && c <= 'Z'
		isLower := c >= 'a' && c <= 'z'
		if !isDigit && !isUpper && !isLower {
			t.Fatalf("expected only base62 chars in random part, got %q", c)
		}
	}
}

func TestGenerateUniqueKeys(t *testing.T) {
	key1, _ := apikey.Generate()
	key2, _ := apikey.Generate()

	if key1 == key2 {
		t.Fatal("expected generated keys to be unique")
	}
}

func TestVerifyWrongKey(t *testing.T) {
	key, _ := apikey.Generate()
	if apikey.Verify("something", key.Hash) == true {
		t.Fatal("expected wrong key to fail verification")
	}
}

func TestPrefixShortPlaintext(t *testing.T) {
	if apikey.Prefix("abc") != "abc" {
		t.Fatal("expected short plaintext prefix to be returned unchanged")
	}
}
