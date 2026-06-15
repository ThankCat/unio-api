package bootstrap

import (
	"context"
	"errors"
	"testing"

	"github.com/ThankCat/unio-api/internal/core/credential"
	"github.com/ThankCat/unio-api/internal/core/routing"
	"github.com/ThankCat/unio-api/internal/platform/failure"
	"github.com/ThankCat/unio-api/internal/platform/store/sqlc"
	"github.com/jackc/pgx/v5/pgtype"
)

type fakeChatRouteStore struct {
	rows []sqlc.FindRouteCandidatesRow
}

func (s *fakeChatRouteStore) ModelExistsByID(ctx context.Context, requestedModelID string) (bool, error) {
	return true, nil
}

func (s *fakeChatRouteStore) ProjectCanUseModel(ctx context.Context, arg sqlc.ProjectCanUseModelParams) (bool, error) {
	return true, nil
}

func (s *fakeChatRouteStore) FindRouteCandidates(ctx context.Context, arg sqlc.FindRouteCandidatesParams) ([]sqlc.FindRouteCandidatesRow, error) {
	return s.rows, nil
}

func (s *fakeChatRouteStore) GetRouteByID(ctx context.Context, id int64) (sqlc.Route, error) {
	return sqlc.Route{}, errors.New("route not found")
}

func (s *fakeChatRouteStore) GetBuiltinCheapestRoute(ctx context.Context) (sqlc.Route, error) {
	return sqlc.Route{ID: 1, Name: "经济", Mode: "cheapest", PoolKind: "all", IsBuiltin: true, Status: "enabled"}, nil
}

func TestNewChatRouterRejectsMissingMasterKey(t *testing.T) {
	_, err := NewChatRouter(&fakeChatRouteStore{}, "")
	if err == nil {
		t.Fatal("expected missing master key error")
	}

	if got := failure.CodeOf(err); got != failure.CodeConfigMissing {
		t.Fatalf("expected code %q, got %q", failure.CodeConfigMissing, got)
	}
}

func TestNewChatRouterDecryptsStoredCredential(t *testing.T) {
	encrypted, err := credential.EncryptFixedTestCredential("sk-upstream-test")
	if err != nil {
		t.Fatalf("encrypt test credential: %v", err)
	}

	router, err := NewChatRouter(&fakeChatRouteStore{
		rows: []sqlc.FindRouteCandidatesRow{
			{
				ModelDbID:           7,
				ProviderID:          11,
				AdapterKey:          "openai",
				ChannelID:           13,
				BaseUrl:             "https://api.openai.example/v1",
				CredentialEncrypted: encrypted,
				TimeoutMs:           pgtype.Int4{Int32: 15000, Valid: true},
				UpstreamModel:       "gpt-4.1",
			},
		},
	}, credential.FixedTestMasterKeyBase64)
	if err != nil {
		t.Fatalf("NewChatRouter returned error: %v", err)
	}

	plan, err := router.PlanChat(context.Background(), routing.ChatRouteRequest{
		ProjectID:       1,
		ModelID:         "gpt-4.1",
		IngressProtocol: routing.ProtocolOpenAI,
	})
	if err != nil {
		t.Fatalf("PlanChat returned error: %v", err)
	}

	if len(plan.Candidates) != 1 {
		t.Fatalf("expected 1 candidate, got %d", len(plan.Candidates))
	}
	if plan.Candidates[0].Channel.APIKey != "sk-upstream-test" {
		t.Fatalf("expected decrypted upstream key, got %q", plan.Candidates[0].Channel.APIKey)
	}
}
