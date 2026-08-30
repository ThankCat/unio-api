// Package cdkey contains the small, shared CDKEY domain primitives.
package cdkey

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base32"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/jackc/pgx/v5/pgtype"
)

const (
	Currency    = "USD"
	MaxQuantity = 1000
)

var (
	// Amounts is the only set of values accepted by the generator and database.
	Amounts     = []string{"5", "10", "30", "50", "100", "200", "500"}
	codePattern = regexp.MustCompile(`^[A-Z0-9]{4}(?:-[A-Z0-9]{4}){3}$`)
)

// Status values persisted in cdkeys.status.
const (
	StatusUnused   = "unused"
	StatusRedeemed = "redeemed"
	StatusRevoked  = "revoked"
)

// Normalize removes formatting separators and uppercases a user supplied key.
// It accepts the canonical UNIO-XXXX-XXXX-XXXX representation as well as a
// compact value, which makes copy/paste from spreadsheets forgiving.
func Normalize(raw string) (string, error) {
	value := strings.ToUpper(strings.TrimSpace(raw))
	value = strings.ReplaceAll(value, " ", "")
	value = strings.ReplaceAll(value, "\t", "")
	value = strings.ReplaceAll(value, "\r", "")
	value = strings.ReplaceAll(value, "\n", "")
	value = strings.ReplaceAll(value, "-", "")
	if len(value) != 16 {
		return "", errors.New("invalid CDKEY")
	}
	// Require an alphanumeric compact form before inserting separators.
	for _, r := range value {
		if !(r >= 'A' && r <= 'Z') && !(r >= '0' && r <= '9') {
			return "", errors.New("invalid CDKEY")
		}
	}
	canonical := value[:4] + "-" + value[4:8] + "-" + value[8:12] + "-" + value[12:]
	if !codePattern.MatchString(canonical) {
		return "", errors.New("invalid CDKEY")
	}
	return canonical, nil
}

// Hash returns a stable SHA-256 digest of a canonical key.
func Hash(canonical string) string {
	sum := sha256.Sum256([]byte(canonical))
	return hex.EncodeToString(sum[:])
}

// Mask hides the middle of a canonical key. The prefix and suffix are useful
// for support verification without exposing a redeemable credential.
func Mask(canonical string) string {
	normalized, err := Normalize(canonical)
	if err != nil {
		return "****"
	}
	parts := strings.Split(normalized, "-")
	return parts[0] + "-****-****-" + parts[3]
}

// PrefixSuffix returns the non-sensitive parts persisted for list searching.
func PrefixSuffix(canonical string) (string, string) {
	normalized, err := Normalize(canonical)
	if err != nil {
		return "", ""
	}
	parts := strings.Split(normalized, "-")
	return parts[0], parts[3]
}

// Generate creates a cryptographically random canonical key.
func Generate() (string, error) {
	// 80 bits of entropy encoded as 16 base32 characters (without padding).
	buf := make([]byte, 10)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generate CDKEY entropy: %w", err)
	}
	compact := strings.TrimRight(base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(buf), "=")
	if len(compact) < 16 {
		return "", errors.New("generated CDKEY has invalid length")
	}
	compact = compact[:16]
	return compact[:4] + "-" + compact[4:8] + "-" + compact[8:12] + "-" + compact[12:], nil
}

// AmountNumeric parses one of the fixed USD denominations as pgtype.Numeric.
func AmountNumeric(raw string) (pgtype.Numeric, bool) {
	value := strings.TrimSpace(raw)
	for _, allowed := range Amounts {
		if value == allowed || value == allowed+".0" || value == allowed+".00" {
			var n pgtype.Numeric
			if n.Scan(allowed) == nil {
				return n, true
			}
		}
	}
	return pgtype.Numeric{}, false
}

// AmountString validates and canonicalizes a denomination for API responses.
func AmountString(raw string) (string, bool) {
	if _, ok := AmountNumeric(raw); !ok {
		return "", false
	}
	return strings.TrimSuffix(strings.TrimSuffix(strings.TrimSpace(raw), ".00"), ".0"), true
}

// IsStatus reports whether status is a persisted CDKEY state.
func IsStatus(value string) bool {
	switch value {
	case StatusUnused, StatusRedeemed, StatusRevoked:
		return true
	default:
		return false
	}
}
