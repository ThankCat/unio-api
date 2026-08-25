package bootstrap

import (
	"context"
	"math/big"
	"testing"
	"time"

	"github.com/ThankCat/unio-gateway/internal/core/routing"
	"github.com/ThankCat/unio-gateway/internal/platform/store/sqlc"
	"github.com/jackc/pgx/v5/pgtype"
)

type fakeChatRouteStore struct {
	rows []sqlc.FindModelCandidatesRow
}

func (s *fakeChatRouteStore) ModelExistsByID(ctx context.Context, requestedModelID string) (bool, error) {
	return true, nil
}

func (s *fakeChatRouteStore) FindModelCandidates(ctx context.Context, arg sqlc.FindModelCandidatesParams) ([]sqlc.FindModelCandidatesRow, error) {
	// DEC-027：候选成本需可解析；本测试只验证凭据明文透传，故默认把行标记为绝对成本覆盖
	// （channel_price_id = channel_id），让 buildChatRouteCandidate 走覆盖路径拿到零成本快照。
	rows := make([]sqlc.FindModelCandidatesRow, len(s.rows))
	copy(rows, s.rows)
	for i := range rows {
		if rows[i].ChannelPriceID == 0 && rows[i].ChannelCostMultiplierID == 0 {
			rows[i].ChannelPriceID = rows[i].ChannelID
		}
		rows[i].BaseCurrency = "USD"
		rows[i].BasePricingUnit = "per_1m_tokens"
		rows[i].UncachedInputPrice = pgtype.Numeric{Int: big.NewInt(0), Valid: true}
		rows[i].OutputPrice = pgtype.Numeric{Int: big.NewInt(0), Valid: true}
		rows[i].CostCurrency = "USD"
		rows[i].CostPricingUnit = "per_1m_tokens"
		rows[i].UncachedInputCost = pgtype.Numeric{Int: big.NewInt(0), Valid: true}
		rows[i].OutputCost = pgtype.Numeric{Int: big.NewInt(0), Valid: true}
	}
	return rows, nil
}

// TestNewChatRouterUsesPlaintextCredential 验证渠道凭据明文存储（产品决策）：routing 直接把
// channels.credential 明文用作上游 API key，无解密环节。
func TestNewChatRouterUsesPlaintextCredential(t *testing.T) {
	router := NewChatRouter(&fakeChatRouteStore{
		rows: []sqlc.FindModelCandidatesRow{
			{
				ModelDbID:         7,
				ProviderID:        11,
				AdapterKey:        "openai",
				ChannelID:         13,
				Origin:            "https://api.openai.example/v1",
				Credential:        "sk-upstream-test",
				ResponseTimeoutMs: pgtype.Int4{Int32: 15000, Valid: true},
				UpstreamModel:     "gpt-4.1",
			},
		},
	}, 30*time.Second, nil)

	plan, err := router.PlanChat(context.Background(), routing.ChatRouteRequest{
		UserID:          1,
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
		t.Fatalf("expected plaintext upstream key, got %q", plan.Candidates[0].Channel.APIKey)
	}
}
