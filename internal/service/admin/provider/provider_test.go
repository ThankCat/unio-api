package provider_test

import (
	"testing"

	"github.com/ThankCat/unio-gateway/internal/service/admin/provider"
)

func TestNormalizeOrigin(t *testing.T) {
	tests := map[string]string{
		"HTTPS://Example.COM:443/v1/": "https://example.com/v1",
		"http://Example.COM:80":       "http://example.com",
		"https://[::1]:8443/root/":    "https://[::1]:8443/root",
	}
	for input, expected := range tests {
		actual, err := provider.NormalizeOrigin(input)
		if err != nil || actual != expected {
			t.Fatalf("NormalizeOrigin(%q) = %q, %v; want %q", input, actual, err, expected)
		}
	}
}

func TestNormalizeOriginRejectsUnsafeParts(t *testing.T) {
	for _, input := range []string{"", "ftp://example.com", "https://user@example.com", "https://example.com?q=1", "https://example.com/#x"} {
		if _, err := provider.NormalizeOrigin(input); err == nil {
			t.Fatalf("NormalizeOrigin(%q) unexpectedly succeeded", input)
		}
	}
}
