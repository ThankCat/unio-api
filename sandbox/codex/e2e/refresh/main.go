// refresh 手动触发一次账号令牌刷新（E2E 排障工具）。
package main

import (
	"context"
	"fmt"
	"os"

	"github.com/jackc/pgx/v5/pgxpool"
	"go.uber.org/zap"

	"github.com/ThankCat/unio-gateway/internal/platform/store/sqlc"
	"github.com/ThankCat/unio-gateway/internal/service/subscription"
)

func main() {
	pool, err := pgxpool.New(context.Background(), "postgres://unio:951215chenhao@127.0.0.1:5432/unio?sslmode=disable")
	if err != nil {
		fmt.Println(err)
		os.Exit(1)
	}
	defer pool.Close()
	queries := sqlc.New(pool)
	ctx := context.Background()

	row, err := queries.GetAccountOutboundCredential(ctx, 168)
	if err != nil {
		fmt.Println("load:", err)
		os.Exit(1)
	}
	creds, err := subscription.DecodeCredentials(row.Credentials)
	if err != nil {
		fmt.Println("decode:", err)
		os.Exit(1)
	}
	logger, _ := zap.NewDevelopment()
	outbound := subscription.NewOutbound(queries, subscription.NewTokenClient(nil, nil), nil, nil, logger)
	refreshed, err := outbound.RefreshAccount(ctx, 168, creds, "")
	if err != nil {
		fmt.Println("refresh:", err)
		os.Exit(1)
	}
	fmt.Println("refreshed, new expiry:", refreshed.ExpiresAt)
}
