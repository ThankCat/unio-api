// newkey 给 codex e2e 用户签发一把新 API Key（CLI 实测用，测完可删）。
package main

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ThankCat/unio-gateway/internal/core/apikey"
)

func main() {
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, "postgres://unio:951215chenhao@127.0.0.1:5432/unio?sslmode=disable")
	if err != nil {
		panic(err)
	}
	defer pool.Close()
	var userID int64
	if err := pool.QueryRow(ctx, `SELECT id FROM users WHERE display_name='Codex E2E' ORDER BY id DESC LIMIT 1`).Scan(&userID); err != nil {
		panic(err)
	}
	key, err := apikey.Generate()
	if err != nil {
		panic(err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO api_keys (user_id, name, key_prefix, key_suffix, key_hash)
		VALUES ($1, 'codex-cli-live', $2, $3, $4)
	`, userID, key.Prefix, key.Suffix, key.Hash); err != nil {
		panic(err)
	}
	fmt.Printf("user=%d key=%s\n", userID, key.Plaintext)
}
