package breakerstore

import (
	"context"
	"testing"

	"github.com/ThankCat/unio-gateway/internal/platform/failure"
)

func TestAttemptPermitLiveness(t *testing.T) {
	store, client, _ := newTestStore(t)
	ctx := context.Background()

	active, err := store.IsAttemptPermitActive(ctx, "missing")
	if err != nil || active {
		t.Fatalf("missing permit = (%v, %v), want inactive without error", active, err)
	}

	if err := client.HSet(ctx, store.keys.permit("active"), "status", "active").Err(); err != nil {
		t.Fatalf("seed active permit: %v", err)
	}
	active, err = store.IsAttemptPermitActive(ctx, "active")
	if err != nil || !active {
		t.Fatalf("active permit = (%v, %v), want active", active, err)
	}

	if err := client.HSet(ctx, store.keys.permit("finished"), "status", "finished").Err(); err != nil {
		t.Fatalf("seed finished permit: %v", err)
	}
	active, err = store.IsAttemptPermitActive(ctx, "finished")
	if err != nil || active {
		t.Fatalf("finished permit = (%v, %v), want inactive", active, err)
	}

	if err := client.HSet(ctx, store.keys.permit("corrupt"), "status", "unexpected").Err(); err != nil {
		t.Fatalf("seed corrupt permit: %v", err)
	}
	if _, err := store.IsAttemptPermitActive(ctx, "corrupt"); failure.CodeOf(err) != failure.CodeGatewayRuntimeSyncRequired {
		t.Fatalf("corrupt permit error = %v, want runtime sync required", err)
	}
}
