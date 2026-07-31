package requests

import (
	"encoding/json"
	"testing"

	"github.com/ThankCat/unio-gateway/internal/service/admin/query"
)

func TestRequestListItemDTOIncludesRoutingSampleLocation(t *testing.T) {
	routeID := int64(7)
	attemptID := int64(19)
	dto := toRequestListItemDTO(query.RequestListItem{
		RouteID:             &routeID,
		ScoringAttemptID:    &attemptID,
		ScoringDimensions:   []string{"ttft", "error"},
		ScoringErrorFailure: true,
	})

	payload, err := json.Marshal(dto)
	if err != nil {
		t.Fatalf("marshal request list item: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(payload, &got); err != nil {
		t.Fatalf("decode request list item: %v", err)
	}
	if got["route_id"] != float64(routeID) || got["scoring_attempt_id"] != float64(attemptID) {
		t.Fatalf("routing location fields = %#v", got)
	}
	if got["scoring_error_failure"] != true {
		t.Fatalf("scoring_error_failure = %#v, want true", got["scoring_error_failure"])
	}
	dimensions, ok := got["scoring_dimensions"].([]any)
	if !ok || len(dimensions) != 2 || dimensions[0] != "ttft" || dimensions[1] != "error" {
		t.Fatalf("scoring_dimensions = %#v", got["scoring_dimensions"])
	}
}

func TestAttemptDTOIncludesTimeoutAndScoringFacts(t *testing.T) {
	phase := "first_token"
	dto := toAttemptDTO(query.Attempt{
		UpstreamTimeoutPhase: &phase,
		TTFTScoringSample:    false,
		ErrorScoringSample:   true,
		ErrorScoringFailure:  true,
	})

	payload, err := json.Marshal(dto)
	if err != nil {
		t.Fatalf("marshal attempt: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(payload, &got); err != nil {
		t.Fatalf("decode attempt: %v", err)
	}
	if got["upstream_timeout_phase"] != phase || got["ttft_scoring_sample"] != false ||
		got["error_scoring_sample"] != true || got["error_scoring_failure"] != true {
		t.Fatalf("attempt timeout/scoring facts = %#v", got)
	}
}
