package cdkey

import (
	"regexp"
	"strings"
	"testing"
)

func TestGenerateReturnsCanonicalUNIOKey(t *testing.T) {
	pattern := regexp.MustCompile(`^UNIO-[A-Z0-9]{4}(?:-[A-Z0-9]{4}){3}$`)
	for range 100 {
		key, err := Generate()
		if err != nil {
			t.Fatalf("Generate() error = %v", err)
		}
		if !pattern.MatchString(key) {
			t.Fatalf("Generate() = %q, want canonical UNIO CDKEY", key)
		}
	}
}

func TestNormalizeAcceptsNewFormatsAndRejectsLegacy(t *testing.T) {
	tests := []struct {
		name    string
		raw     string
		want    string
		wantErr bool
	}{
		{name: "canonical", raw: "UNIO-ABCD-EFGH-IJKL-MNOP", want: "UNIO-ABCD-EFGH-IJKL-MNOP"},
		{name: "lowercase and whitespace", raw: "  unio-abcd-efgh-ijkl-mnop\n", want: "UNIO-ABCD-EFGH-IJKL-MNOP"},
		{name: "prefixed compact", raw: "UNIOABCDEFGHIJKLMNOP", want: "UNIO-ABCD-EFGH-IJKL-MNOP"},
		{name: "prefixed compact with separator", raw: "UNIO-ABCDEFGHIJKLMNOP", want: "UNIO-ABCD-EFGH-IJKL-MNOP"},
		{name: "legacy grouped", raw: "ABCD-EFGH-IJKL-MNOP", wantErr: true},
		{name: "legacy compact", raw: "ABCDEFGHIJKLMNOP", wantErr: true},
		{name: "wrong prefix", raw: "TEST-ABCD-EFGH-IJKL-MNOP", wantErr: true},
		{name: "invalid character", raw: "UNIO-ABCD-EFGH-IJKL-MNO!", wantErr: true},
		{name: "invalid length", raw: "UNIO-ABCD-EFGH-IJKL", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Normalize(tt.raw)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("Normalize(%q) unexpectedly succeeded with %q", tt.raw, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("Normalize(%q) error = %v", tt.raw, err)
			}
			if got != tt.want {
				t.Fatalf("Normalize(%q) = %q, want %q", tt.raw, got, tt.want)
			}
		})
	}
}

func TestMaskAndPrefixSuffixUseRandomParts(t *testing.T) {
	canonical := "UNIO-ABCD-EFGH-IJKL-MNOP"
	if got := Mask(canonical); got != "UNIO-ABCD-****-****-MNOP" {
		t.Fatalf("Mask() = %q", got)
	}
	prefix, suffix := PrefixSuffix(canonical)
	if prefix != "ABCD" || suffix != "MNOP" {
		t.Fatalf("PrefixSuffix() = %q, %q", prefix, suffix)
	}
	if got := Mask("ABCD-EFGH-IJKL-MNOP"); got != "****" {
		t.Fatalf("Mask(legacy) = %q, want redacted invalid marker", got)
	}
	if strings.Contains(Mask(canonical), "EFGH") || strings.Contains(Mask(canonical), "IJKL") {
		t.Fatal("Mask() exposed a middle group")
	}
}
