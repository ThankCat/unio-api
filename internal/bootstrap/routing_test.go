package bootstrap

import (
	"context"
	"math/big"
	"testing"
	"time"

	"github.com/ThankCat/unio-api/internal/core/routing"
	"github.com/ThankCat/unio-api/internal/platform/store/sqlc"
	"github.com/jackc/pgx/v5/pgtype"
)

type fakeChatRouteStore struct {
	rows []sqlc.FindRouteCandidatesRow
}

func (s *fakeChatRouteStore) ModelExistsByID(ctx context.Context, requestedModelID string) (bool, error) {
	return true, nil
}

func (s *fakeChatRouteStore) UserCanUseModel(ctx context.Context, arg sqlc.UserCanUseModelParams) (bool, error) {
	return true, nil
}

func (s *fakeChatRouteStore) FindRouteCandidates(ctx context.Context, arg sqlc.FindRouteCandidatesParams) ([]sqlc.FindRouteCandidatesRow, error) {
	return s.rows, nil
}

func (s *fakeChatRouteStore) GetRouteByID(ctx context.Context, id int64) (sqlc.Route, error) {
	return sqlc.Route{ID: id, Name: "test", Mode: "cheapest", PoolKind: "all", Status: "enabled", PriceRatio: pgtype.Numeric{Int: big.NewInt(1), Exp: 0, Valid: true}}, nil
}

func testRouteID() *int64 {
	id := int64(1)
	return &id
}

// TestNewChatRouterUsesPlaintextCredential 验证渠道凭据明文存储（产品决策）：routing 直接把
// channels.credential 明文用作上游 API key，无解密环节。
func TestNewChatRouterUsesPlaintextCredential(t *testing.T) {
	router := NewChatRouter(&fakeChatRouteStore{
		rows: []sqlc.FindRouteCandidatesRow{
			{
				ModelDbID:     7,
				ProviderID:    11,
				AdapterKey:    "openai",
				ChannelID:     13,
				BaseUrl:       "https://api.openai.example/v1",
				Credential:    "sk-upstream-test",
				TimeoutMs:     pgtype.Int4{Int32: 15000, Valid: true},
				UpstreamModel: "gpt-4.1",
			},
		},
	}, 30*time.Second, nil)

	plan, err := router.PlanChat(context.Background(), routing.ChatRouteRequest{
		UserID:          1,
		ModelID:         "gpt-4.1",
		IngressProtocol: routing.ProtocolOpenAI,
		RouteID:         testRouteID(),
	})
	if err != nil {
		t.Fatalf("PlanChat returned error: %v", err)
	}

	if len(plan.Candidates) != 1 {
		t.Fatalf("expected 1 candidate, got %d", len(plan.Candidates))
	}
	if plan.Candidates[0].Channel.APIKey != "sk-upstream-test" {
		t.Fatalf("expected plaintext upstream key, got %q", plan.Candidates[0].Channel.APIKey)
	}
}
