package bootstrap

import (
	"context"
	"testing"

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

func TestNewChatGatewayBuildsService(t *testing.T) {
	registry, err := NewAdapterRegistry(nil)
	if err != nil {
		t.Fatalf("NewAdapterRegistry returned error: %v", err)
	}

	service := NewChatGateway(
		fakeChatGatewayDB{},
		&sqlc.Queries{},
		fakeChatGatewayRouter{},
		registry,
		config.WorkerConfig{},
		config.CircuitBreakerConfig{},
		nil,
	)
	if service == nil {
		t.Fatal("expected chat gateway service")
	}
}
