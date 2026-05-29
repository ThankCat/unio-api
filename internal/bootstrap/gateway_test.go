package bootstrap

import (
	"context"
	"testing"

	"github.com/ThankCat/unio-api/internal/core/adapter"
	"github.com/ThankCat/unio-api/internal/core/routing"
	"github.com/ThankCat/unio-api/internal/platform/config"
	"github.com/ThankCat/unio-api/internal/platform/store/sqlc"
	"github.com/jackc/pgx/v5"
)

type fakeChatGatewayDB struct{}

func (db fakeChatGatewayDB) Begin(ctx context.Context) (pgx.Tx, error) {
	return nil, nil
}

type fakeChatGatewayRouter struct{}

func (r fakeChatGatewayRouter) PlanChat(ctx context.Context, req routing.ChatRouteRequest) (routing.ChatRoutePlan, error) {
	return routing.ChatRoutePlan{}, nil
}

type fakeChatGatewayRegistry struct{}

func (r fakeChatGatewayRegistry) Chat(adapterKey string) (adapter.ChatAdapter, bool) {
	return nil, false
}

func (r fakeChatGatewayRegistry) StreamChat(adapterKey string) (adapter.StreamChatAdapter, bool) {
	return nil, false
}

func (r fakeChatGatewayRegistry) ChatInputTokenizer(adapterKey string) (adapter.ChatInputTokenizer, bool) {
	return nil, false
}

func TestNewChatGatewayBuildsService(t *testing.T) {
	service := NewChatGateway(
		fakeChatGatewayDB{},
		&sqlc.Queries{},
		fakeChatGatewayRouter{},
		fakeChatGatewayRegistry{},
		config.WorkerConfig{},
		config.CircuitBreakerConfig{},
		nil,
	)
	if service == nil {
		t.Fatal("expected chat gateway service")
	}
}
