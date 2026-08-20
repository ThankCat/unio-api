package auth

import (
	"testing"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/ThankCat/unio-gateway/internal/platform/store/sqlc"
)

func TestBalanceFromRowUsesAvailableAmount(t *testing.T) {
	got := balanceFromRow(sqlc.UserBalance{
		Balance:         mustNumeric(t, "12.5000000000"),
		ReservedBalance: mustNumeric(t, "2.2500000000"),
	}, true)

	if got != (Balance{Currency: "USD", Total: "12.5", Reserved: "2.25", Available: "10.25"}) {
		t.Fatalf("unexpected balance: %+v", got)
	}
}

func TestBalanceFromRowDefaultsMissingRowToZero(t *testing.T) {
	got := balanceFromRow(sqlc.UserBalance{}, false)
	if got != (Balance{Currency: "USD", Total: "0", Reserved: "0", Available: "0"}) {
		t.Fatalf("expected zero usd balance, got %+v", got)
	}
}

func mustNumeric(t *testing.T, raw string) pgtype.Numeric {
	t.Helper()
	var n pgtype.Numeric
	if err := n.Scan(raw); err != nil {
		t.Fatal(err)
	}
	return n
}
