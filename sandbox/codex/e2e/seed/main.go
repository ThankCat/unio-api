// seed 是号池改造的 Dev 端到端验证装配器（docs/changes/2026-09-02-account-pool 批次十）。
//
// 它在本地 dev 库里幂等装配一条完整的号池链路：
// 订阅 Provider（含充值汇率）→ 池型渠道（adapter=codex）→ 模型与定价（渠道成本倍率 0）→
// 从 sub2api 导出文件导入真实账号并启用 → 测试用户 + API Key + 余额。
// 只用于开发环境验证，不属于生产装配。
//
// 用法：
//
//	DATABASE_URL=postgres://... go run ./sandbox/codex/e2e/seed <sub2api-file.json>
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"math/big"
	"os"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ThankCat/unio-gateway/internal/core/apikey"
	"github.com/ThankCat/unio-gateway/internal/core/ledger"
	"github.com/ThankCat/unio-gateway/internal/platform/store/sqlc"
	"github.com/ThankCat/unio-gateway/internal/service/subscription"
)

const (
	providerSlug = "codex-e2e"
	channelName  = "codex-pool-e2e"
	modelID      = "gpt-5.5"
	userEmail    = "codex-e2e@unio.local"
)

func main() {
	if len(os.Args) < 2 {
		fail("usage: seed <sub2api-file.json>")
	}
	databaseURL := os.Getenv("DATABASE_URL")
	if databaseURL == "" {
		fail("DATABASE_URL is required")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, databaseURL)
	must(err, "connect postgres")
	defer pool.Close()
	queries := sqlc.New(pool)

	// 1. 订阅 Provider：origin=https://chatgpt.com、结算币种 USD、充值汇率 1.0（D-02）。
	var providerID int64
	err = pool.QueryRow(ctx, `
		INSERT INTO providers (slug, name, origin, status, currency)
		VALUES ($1, 'Codex 订阅 E2E', 'https://chatgpt.com', 'enabled', 'USD')
		ON CONFLICT (slug) DO UPDATE SET status = 'enabled', updated_at = now()
		RETURNING id
	`, providerSlug).Scan(&providerID)
	must(err, "upsert provider")
	_, err = pool.Exec(ctx, `
		INSERT INTO provider_recharge_rates (provider_id, provider_currency, rate, status, source, effective_from)
		SELECT $1, 'USD', 1.0, 'enabled', 'manual', now() - interval '1 hour'
		WHERE NOT EXISTS (
			SELECT 1 FROM provider_recharge_rates
			WHERE provider_id = $1 AND status = 'enabled'
			  AND effective_from <= now() AND (effective_to IS NULL OR effective_to > now())
		)
	`, providerID)
	must(err, "ensure recharge rate")

	// 2. 池型渠道：adapter=codex，只登记 responses 四槽；池不持凭据。
	var channelID int64
	err = pool.QueryRow(ctx, `
		INSERT INTO channels (provider_id, name, protocols, adapter_key, credential, status, priority,
		                      supply_form, account_default_concurrency)
		VALUES ($1, $2, ARRAY['openai'], 'codex', '', 'enabled', 10, 'pool', 3)
		ON CONFLICT (provider_id, name) DO UPDATE SET status = 'enabled', updated_at = now()
		RETURNING id
	`, providerID, channelName).Scan(&channelID)
	must(err, "upsert pool channel")
	ensureCapacityControlNote(channelID)

	// 3. 模型 + 基准价 + 渠道成本倍率 0 + 绑定。
	var modelDBID int64
	err = pool.QueryRow(ctx, `
		INSERT INTO models (model_id, display_name, owned_by, status)
		VALUES ($1, $1, 'openai', 'enabled')
		ON CONFLICT (model_id) DO UPDATE SET status = 'enabled', updated_at = now()
		RETURNING id
	`, modelID).Scan(&modelDBID)
	must(err, "upsert model")
	_, err = pool.Exec(ctx, `
		INSERT INTO model_prices (model_id, currency, pricing_unit, uncached_input_price, output_price,
		                          sale_price_ratio, status, effective_from)
		SELECT $1, 'USD', 'per_1m_tokens', 2, 8, 1, 'enabled', now() - interval '1 hour'
		WHERE NOT EXISTS (
			SELECT 1 FROM model_prices WHERE model_id = $1 AND status = 'enabled'
			  AND effective_from <= now() AND (effective_to IS NULL OR effective_to > now())
		)
	`, modelDBID)
	must(err, "ensure model price")
	_, err = pool.Exec(ctx, `
		INSERT INTO channel_cost_multipliers (channel_id, model_id, multiplier, status, effective_from)
		SELECT $1, NULL, 0, 'enabled', now() - interval '1 hour'
		WHERE NOT EXISTS (
			SELECT 1 FROM channel_cost_multipliers WHERE channel_id = $1 AND model_id IS NULL AND status = 'enabled'
			  AND effective_from <= now() AND (effective_to IS NULL OR effective_to > now())
		)
	`, channelID)
	must(err, "ensure zero cost multiplier")
	_, err = pool.Exec(ctx, `
		INSERT INTO channel_models (channel_id, model_id, upstream_model, status)
		VALUES ($1, $2, $3, 'enabled')
		ON CONFLICT (channel_id, model_id) DO UPDATE SET status = 'enabled', upstream_model = $3, updated_at = now()
	`, channelID, modelDBID, modelID)
	must(err, "bind channel model")

	// 4. 导入真实账号并启用。
	raw, err := os.ReadFile(os.Args[1])
	must(err, "read sub2api file")
	accounts, err := subscription.ParseSub2APIData(raw)
	must(err, "parse sub2api file")
	results, err := subscription.ImportAccounts(ctx, queries, channelID, accounts)
	must(err, "import accounts")
	for _, item := range results {
		if item.Imported {
			_, err = pool.Exec(ctx, `
				UPDATE subscription_accounts SET status = 'enabled', config_revision = config_revision + 1, updated_at = now()
				WHERE id = $1
			`, item.AccountID)
			must(err, "enable imported account")
			fmt.Printf("account imported+enabled: id=%d display=%s\n", item.AccountID, item.DisplayName)
			continue
		}
		fmt.Printf("account skipped: %s (%s)\n", item.DisplayName, item.Reason)
		// 已存在：确保它归属本渠道并启用（幂等重跑）。
		var existingID int64
		if scanErr := pool.QueryRow(ctx,
			`SELECT id FROM subscription_accounts WHERE platform='openai' AND upstream_account_id=$1`,
			item.UpstreamAccountID,
		).Scan(&existingID); scanErr == nil {
			_, _ = pool.Exec(ctx, `UPDATE subscription_accounts SET channel_id=$2, status='enabled', updated_at=now() WHERE id=$1`, existingID, channelID)
			fmt.Printf("existing account re-enabled: id=%d\n", existingID)
		}
	}

	// 渠道 capacity_revision +1，逼运行态围栏丢弃旧候选快照（账号集合变了）。
	_, err = pool.Exec(ctx, `UPDATE channels SET capacity_revision = capacity_revision + 1, updated_at = now() WHERE id = $1`, channelID)
	must(err, "bump capacity revision")

	// 5. 测试用户 + API Key + 余额。
	var userID int64
	err = pool.QueryRow(ctx, `
		INSERT INTO users (uid, email, password_hash, display_name, status)
		VALUES ($1, $2, 'e2e', 'Codex E2E', 'active')
		ON CONFLICT (lower(email)) DO UPDATE SET status = 'active', updated_at = now()
		RETURNING id
	`, uuid.NewString(), userEmail).Scan(&userID)
	must(err, "upsert user")

	key, err := apikey.Generate()
	must(err, "generate api key")
	_, err = pool.Exec(ctx, `
		INSERT INTO api_keys (user_id, name, key_prefix, key_suffix, key_hash)
		VALUES ($1, 'codex-e2e', $2, $3, $4)
	`, userID, key.Prefix, key.Suffix, key.Hash)
	must(err, "create api key")

	ledgerService := ledger.NewService(pool, queries)
	amount := pgtype.Numeric{Int: big.NewInt(100), Exp: 0, Valid: true}
	_, err = ledgerService.Credit(ctx, ledger.CreditParams{
		UserID:         userID,
		Amount:         amount,
		Currency:       "USD",
		IdempotencyKey: fmt.Sprintf("codex-e2e-credit-%d", time.Now().UnixNano()),
		Reason:         "codex e2e seed",
	})
	must(err, "credit balance")

	summary := map[string]any{
		"provider_id": providerID,
		"channel_id":  channelID,
		"model":       modelID,
		"user_id":     userID,
		"api_key":     key.Plaintext,
	}
	out, _ := json.MarshalIndent(summary, "", "  ")
	fmt.Println(string(out))
}

// ensureCapacityControlNote 提示：dev gateway 启动时会自动 restore 缺失的渠道容量 control，
// 种子程序不直接写 Redis。
func ensureCapacityControlNote(channelID int64) {
	fmt.Printf("channel %d ready (capacity control will be restored by gateway startup reconciler)\n", channelID)
}

func must(err error, what string) {
	if err != nil {
		fail(fmt.Sprintf("%s: %v", what, err))
	}
}

func fail(message string) {
	fmt.Fprintln(os.Stderr, "error:", message)
	os.Exit(1)
}
