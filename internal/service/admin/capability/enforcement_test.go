package capability_test

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/ThankCat/unio-api/internal/platform/config"
	"github.com/ThankCat/unio-api/internal/platform/store/sqlc"
	capadmin "github.com/ThankCat/unio-api/internal/service/admin/capability"
)

type fakeEnforcementStore struct {
	rows []sqlc.CountRequestsByCapabilityResultRow
}

func (s *fakeEnforcementStore) CountRequestsByCapabilityResult(context.Context, sqlc.CountRequestsByCapabilityResultParams) ([]sqlc.CountRequestsByCapabilityResultRow, error) {
	return s.rows, nil
}

func TestEnforcementModesReflectConfig(t *testing.T) {
	svc := capadmin.NewEnforcementService(&fakeEnforcementStore{}, config.CapabilityConfig{
		EnforceOpenAIChat:        true,
		EnforceAnthropicMessages: false,
		EnforceOpenAIResponses:   false,
	})

	modes := svc.Modes()
	if len(modes) != 3 {
		t.Fatalf("expected 3 surfaces, got %d", len(modes))
	}

	bySurface := map[string]bool{}
	for _, m := range modes {
		bySurface[m.Surface] = m.Enforced
	}
	if !bySurface["openai_chat"] {
		t.Fatalf("expected openai_chat enforced")
	}
	if bySurface["anthropic_messages"] || bySurface["openai_responses"] {
		t.Fatalf("expected other surfaces in observe")
	}
}

func TestObserveSummaryMapsNullResult(t *testing.T) {
	store := &fakeEnforcementStore{rows: []sqlc.CountRequestsByCapabilityResultRow{
		{CapabilityCheckResult: pgtype.Text{Valid: false}, Total: 7},
		{CapabilityCheckResult: pgtype.Text{String: "model_unavailable", Valid: true}, Total: 2},
	}}
	svc := capadmin.NewEnforcementService(store, config.CapabilityConfig{})

	results, err := svc.ObserveSummary(context.Background(), nil, nil)
	if err != nil {
		t.Fatalf("observe summary: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("expected 2 buckets, got %d", len(results))
	}
	if results[0].Result != nil {
		t.Fatalf("expected NULL result mapped to nil, got %v", *results[0].Result)
	}
	if results[1].Result == nil || *results[1].Result != "model_unavailable" {
		t.Fatalf("expected model_unavailable bucket, got %v", results[1].Result)
	}
}
