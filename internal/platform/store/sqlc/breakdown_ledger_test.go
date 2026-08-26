package sqlc_test

import (
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
)

func assertBreakdownFacts(t *testing.T, name string, terminal, succeeded, tokens int64, revenue, cost pgtype.Numeric) {
	t.Helper()

	if terminal != 1 || succeeded != 1 || tokens != 140 {
		t.Fatalf("%s facts = terminal %d, succeeded %d, tokens %d; want 1, 1, 140", name, terminal, succeeded, tokens)
	}
	assertNumericEquals(t, revenue, 11)
	assertNumericEquals(t, cost, 7)
}
