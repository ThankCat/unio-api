//go:build ignore

// dev-seed 幂等初始化本地开发库：模型（含官方能力声明）+ 测试用户/项目/API Key。
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ThankCat/unio-api/internal/core/apikey"
	"github.com/ThankCat/unio-api/internal/platform/config"
	"github.com/ThankCat/unio-api/internal/platform/store/sqlc"
)

type capDecl struct {
	Key    string
	Level  string
	Limits json.RawMessage
}

type modelSpec struct {
	ModelID            string
	DisplayName        string
	OwnedBy            string
	CanonicalID        string // 非空则从 model_catalog 采纳元数据
	ContextWindow      int64
	MaxOutput          int64
	InputPriceUSDPerM  string
	OutputPriceUSDPerM string
	Capabilities       []capDecl
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "dev-seed: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	apiRoot, err := findAPIRoot()
	if err != nil {
		return err
	}
	_ = apiRoot
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	ctx := context.Background()
	pool, err := pgxpool.New(ctx, cfg.DB.URL)
	if err != nil {
		return fmt.Errorf("connect db: %w", err)
	}
	defer pool.Close()

	queries := sqlc.New(pool)

	specs := openAIModelSpecs()
	modelDBIDs := make(map[string]int64, len(specs))
	for _, spec := range specs {
		id, err := ensureModel(ctx, pool, queries, spec)
		if err != nil {
			return fmt.Errorf("model %s: %w", spec.ModelID, err)
		}
		modelDBIDs[spec.ModelID] = id
	}

	routeID, err := ensureDefaultRoute(ctx, pool)
	if err != nil {
		return err
	}

	keyPlain, keyPrefix, keyHash, err := ensureTestIdentity(ctx, pool, routeID)
	if err != nil {
		return err
	}

	fmt.Println("==> dev seed complete")
	fmt.Printf("用户:     test@unio.local (测试用户)\n")
	fmt.Printf("项目:     测试项目\n")
	fmt.Printf("线路:     route_id=%d (经济)\n", routeID)
	fmt.Printf("模型:     %s\n", strings.Join(sortedKeys(modelDBIDs), ", "))
	fmt.Println("余额:     USD 100.00")
	fmt.Println("")
	fmt.Println("API Key（仅显示一次，请保存）:")
	fmt.Println(keyPlain)
	fmt.Printf("\nGateway:  http://127.0.0.1%s\n", gatewayPort(cfg.Gateway.HTTPAddr))
	_ = keyPrefix
	_ = keyHash
	return nil
}

func openAIModelSpecs() []modelSpec {
	reasoningEfforts55 := json.RawMessage(`{"allowed":["none","low","medium","high","xhigh"],"default":"medium"}`)
	reasoningEfforts54 := json.RawMessage(`{"allowed":["none","low","medium","high","xhigh"],"default":"none"}`)

	frontierCaps := func(reasoningLimits json.RawMessage) []capDecl {
		return []capDecl{
			{Key: "text.input", Level: "full"},
			{Key: "text.output", Level: "full"},
			{Key: "image.input", Level: "full"},
			{Key: "file.input", Level: "full"},
			{Key: "audio.input", Level: "unsupported"},
			{Key: "audio.output", Level: "unsupported"},
			{Key: "image.output", Level: "unsupported"},
			{Key: "tools.function", Level: "full"},
			{Key: "tools.custom", Level: "full"},
			{Key: "tools.parallel", Level: "full"},
			{Key: "tools.choice_required", Level: "full"},
			{Key: "tools.builtin.web_search", Level: "full"},
			{Key: "tools.builtin.file_search", Level: "full"},
			{Key: "tools.builtin.code_interpreter", Level: "full"},
			{Key: "tools.builtin.computer_use", Level: "full"},
			{Key: "tools.builtin.image_generation", Level: "full"},
			{Key: "tools.builtin.mcp", Level: "full"},
			{Key: "reasoning.effort", Level: "full", Limits: reasoningLimits},
			{Key: "response_format.json_object", Level: "full"},
			{Key: "response_format.json_schema", Level: "full"},
			{Key: "prompt_cache", Level: "full"},
			{Key: "logprobs", Level: "full"},
			{Key: "service_tier", Level: "full"},
			{Key: "stream", Level: "full"},
			{Key: "stream.tools", Level: "full"},
			{Key: "stream.usage", Level: "full"},
			{Key: "server_state.store", Level: "full"},
			{Key: "server_state.background", Level: "full"},
			{Key: "responses.encrypted_content", Level: "full"},
			{Key: "responses.compact.native", Level: "full"},
		}
	}

	return []modelSpec{
		{
			ModelID:            "gpt-5.5",
			DisplayName:        "GPT-5.5",
			OwnedBy:            "openai",
			CanonicalID:        "openai/gpt-5.5",
			ContextWindow:      1050000,
			MaxOutput:          128000,
			InputPriceUSDPerM:  "5.00",
			OutputPriceUSDPerM: "30.00",
			Capabilities:       frontierCaps(reasoningEfforts55),
		},
		{
			ModelID:            "gpt-5.4",
			DisplayName:        "GPT-5.4",
			OwnedBy:            "openai",
			CanonicalID:        "openai/gpt-5.4",
			ContextWindow:      1050000,
			MaxOutput:          128000,
			InputPriceUSDPerM:  "2.50",
			OutputPriceUSDPerM: "15.00",
			Capabilities:       frontierCaps(reasoningEfforts54),
		},
		{
			ModelID:            "gpt-5.4-mini",
			DisplayName:        "GPT-5.4 mini",
			OwnedBy:            "openai",
			CanonicalID:        "openai/gpt-5.4-mini",
			ContextWindow:      400000,
			MaxOutput:          128000,
			InputPriceUSDPerM:  "0.75",
			OutputPriceUSDPerM: "4.50",
			Capabilities:       frontierCaps(reasoningEfforts54),
		},
	}
}

func ensureModel(ctx context.Context, pool *pgxpool.Pool, q *sqlc.Queries, spec modelSpec) (int64, error) {
	var modelDBID int64
	err := pool.QueryRow(ctx, `SELECT id FROM models WHERE model_id = $1`, spec.ModelID).Scan(&modelDBID)
	if err == nil {
		if _, err := pool.Exec(ctx, `
			UPDATE models SET
				display_name = $2, owned_by = $3, status = 'enabled',
				context_window_tokens = $4, max_output_tokens = $5,
				input_price_usd_per_million_tokens = $6::numeric,
				output_price_usd_per_million_tokens = $7::numeric,
				updated_at = now()
			WHERE id = $1
		`, modelDBID, spec.DisplayName, spec.OwnedBy, spec.ContextWindow, spec.MaxOutput,
			spec.InputPriceUSDPerM, spec.OutputPriceUSDPerM); err != nil {
			return 0, err
		}
	} else {
		source := "manual"
		if spec.CanonicalID != "" {
			source = "catalog"
		}
		err = pool.QueryRow(ctx, `
			INSERT INTO models (
				model_id, display_name, owned_by, status, source,
				context_window_tokens, max_output_tokens,
				input_price_usd_per_million_tokens, output_price_usd_per_million_tokens
			) VALUES ($1, $2, $3, 'enabled', $4, $5, $6, $7::numeric, $8::numeric)
			RETURNING id
		`, spec.ModelID, spec.DisplayName, spec.OwnedBy, source,
			spec.ContextWindow, spec.MaxOutput, spec.InputPriceUSDPerM, spec.OutputPriceUSDPerM).Scan(&modelDBID)
		if err != nil {
			return 0, err
		}

		if spec.CanonicalID != "" {
			var fingerprint string
			if err := pool.QueryRow(ctx, `SELECT fingerprint FROM model_catalog WHERE canonical_id = $1`, spec.CanonicalID).Scan(&fingerprint); err == nil {
				_, _ = pool.Exec(ctx, `
					INSERT INTO model_catalog_links (model_id, canonical_id, adopted_fingerprint)
					VALUES ($1, $2, $3)
					ON CONFLICT (model_id) DO UPDATE SET canonical_id = EXCLUDED.canonical_id, adopted_fingerprint = EXCLUDED.adopted_fingerprint
				`, modelDBID, spec.CanonicalID, fingerprint)
			}
		}
	}

	if _, err := pool.Exec(ctx, `DELETE FROM model_capabilities WHERE model_id = $1`, modelDBID); err != nil {
		return 0, err
	}
	for _, c := range spec.Capabilities {
		limits := c.Limits
		if limits == nil {
			limits = json.RawMessage(nil)
		}
		if _, err := q.UpsertModelCapability(ctx, sqlc.UpsertModelCapabilityParams{
			ModelID:       modelDBID,
			CapabilityKey: c.Key,
			SupportLevel:  c.Level,
			Limits:        limits,
		}); err != nil {
			return 0, err
		}
	}
	return modelDBID, nil
}

func ensureDefaultRoute(ctx context.Context, pool *pgxpool.Pool) (int64, error) {
	var id int64
	err := pool.QueryRow(ctx, `SELECT id FROM routes WHERE name = '经济' AND status = 'enabled' ORDER BY id LIMIT 1`).Scan(&id)
	return id, err
}

func ensureTestIdentity(ctx context.Context, pool *pgxpool.Pool, routeID int64) (plain, prefix, hash string, err error) {
	var userID int64
	err = pool.QueryRow(ctx, `SELECT id FROM users WHERE email = 'test@unio.local'`).Scan(&userID)
	if err != nil {
		err = pool.QueryRow(ctx, `
			INSERT INTO users (email, password_hash, display_name)
			VALUES ('test@unio.local', 'dev-no-login', '测试用户')
			RETURNING id
		`).Scan(&userID)
		if err != nil {
			return "", "", "", err
		}
	}

	var projectID int64
	err = pool.QueryRow(ctx, `SELECT id FROM projects WHERE user_id = $1 ORDER BY id LIMIT 1`, userID).Scan(&projectID)
	if err != nil {
		err = pool.QueryRow(ctx, `
			INSERT INTO projects (user_id, name, default_route_id)
			VALUES ($1, '测试项目', $2)
			RETURNING id
		`, userID, routeID).Scan(&projectID)
		if err != nil {
			return "", "", "", err
		}
	} else {
		_, _ = pool.Exec(ctx, `UPDATE projects SET default_route_id = $2, updated_at = now() WHERE id = $1`, projectID, routeID)
	}

	_, err = pool.Exec(ctx, `
		INSERT INTO user_balances (user_id, currency, balance, reserved_balance)
		VALUES ($1, 'USD', 100.0000000000, 0)
		ON CONFLICT (user_id, currency) DO UPDATE SET balance = 100.0000000000, reserved_balance = 0, updated_at = now()
	`, userID)
	if err != nil {
		return "", "", "", err
	}

	var keyID int64
	keyErr := pool.QueryRow(ctx, `
		SELECT id FROM api_keys WHERE project_id = $1 AND name = '测试 Key' ORDER BY id LIMIT 1
	`, projectID).Scan(&keyID)

	gen, err := apikey.Generate()
	if err != nil {
		return "", "", "", err
	}
	if keyErr == nil {
		_, err = pool.Exec(ctx, `
			UPDATE api_keys SET key_prefix = $2, key_hash = $3, route_id = $4,
				disabled_at = NULL, revoked_at = NULL, updated_at = now()
			WHERE id = $1
		`, keyID, gen.Prefix, gen.Hash, routeID)
	} else {
		_, err = pool.Exec(ctx, `
			INSERT INTO api_keys (project_id, name, key_prefix, key_hash, route_id)
			VALUES ($1, '测试 Key', $2, $3, $4)
		`, projectID, gen.Prefix, gen.Hash, routeID)
	}
	return gen.Plaintext, gen.Prefix, gen.Hash, err
}

func findAPIRoot() (string, error) {
	wd, err := os.Getwd()
	if err != nil {
		return "", err
	}
	dir := wd
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("go.mod not found from %s", wd)
		}
		dir = parent
	}
}

func sortedKeys(m map[string]int64) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	for i := 0; i < len(out); i++ {
		for j := i + 1; j < len(out); j++ {
			if out[j] < out[i] {
				out[i], out[j] = out[j], out[i]
			}
		}
	}
	return out
}

func gatewayPort(addr string) string {
	if i := strings.LastIndex(addr, ":"); i >= 0 {
		return addr[i:]
	}
	if strings.HasPrefix(addr, ":") {
		return addr
	}
	return ":" + addr
}
