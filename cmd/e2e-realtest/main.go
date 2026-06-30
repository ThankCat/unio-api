// Command e2e-realtest 是 DEC-026（线路=分组 + 倍率定价 + 渠道只录成本 + 凭据明文）的真实上游全量回归。
//
// 覆盖：OpenAI(/v1/chat/completions) 与 Anthropic(/v1/messages) 双协议 happy path、流式、长多轮对话、
// fallback 客价不变，以及异常链路（无可用渠道 / 鉴权失败 / 余额不足）。计费断言聚焦「售价费率 = 基准 × 倍率」
// （与 token 数无关，恒等），以及扣费/余额方向，避免对真实 token 量做脆弱断言。
//
// 凭据从环境变量读取（不硬编码进仓库）：
//
//	UNIO_E2E_OAI_BASEURL / UNIO_E2E_OAI_KEY / UNIO_E2E_OAI_MODEL（默认 gpt-5.5，OpenAI 兼容）
//	UNIO_E2E_ANT_BASEURL / UNIO_E2E_ANT_KEY / UNIO_E2E_ANT_MODEL（默认 claude-opus-4-6，Anthropic）
//
// 运行：source .env 注入 DATABASE_URL/REDIS_ADDR，导出上述上游变量，go run ./cmd/e2e-realtest（需放行外网）。
// 数据库数据不保留，程序结束不主动清理。
package main

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"log"
	"log/slog"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"

	"github.com/ThankCat/unio-api/internal/bootstrap"
	"github.com/ThankCat/unio-api/internal/core/apikey"
	"github.com/ThankCat/unio-api/internal/platform/config"
	"github.com/ThankCat/unio-api/internal/platform/store/sqlc"
)

// 基准价 in=2/out=10、线路倍率 1.5 → 客户售价 in=3/out=15；渠道成本 in=1/out=5（只录成本）。
const routeRatio = "1.5"

var (
	wantSaleIn  = "3.0000000000"
	wantSaleOut = "15.0000000000"
)

type harness struct {
	pool   *pgxpool.Pool
	q      *sqlc.Queries
	srv    *httptest.Server
	userID int64
	apiKey string

	pass int
	fail int
}

func main() {
	if err := run(); err != nil {
		log.Fatalf("e2e-realtest INFRA FAILED: %v", err)
	}
}

func run() error {
	ctx := context.Background()

	oaiBase := os.Getenv("UNIO_E2E_OAI_BASEURL")
	oaiKey := os.Getenv("UNIO_E2E_OAI_KEY")
	oaiModel := envOr("UNIO_E2E_OAI_MODEL", "gpt-5.5")
	antBase := os.Getenv("UNIO_E2E_ANT_BASEURL")
	antKey := os.Getenv("UNIO_E2E_ANT_KEY")
	antModel := envOr("UNIO_E2E_ANT_MODEL", "claude-opus-4-6")
	if oaiKey == "" || oaiBase == "" {
		return fmt.Errorf("UNIO_E2E_OAI_BASEURL/KEY required")
	}

	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("config.Load: %w", err)
	}
	pool, err := pgxpool.New(ctx, os.Getenv("DATABASE_URL"))
	if err != nil {
		return fmt.Errorf("pgxpool: %w", err)
	}
	defer pool.Close()
	redisClient := redis.NewClient(&redis.Options{Addr: os.Getenv("REDIS_ADDR"), Password: os.Getenv("REDIS_PASSWORD")})
	defer redisClient.Close()

	h := &harness{pool: pool, q: sqlc.New(pool)}
	suffix := time.Now().UnixNano()

	// ---- seed：用户 + 余额 + 自建线路(cheapest/all, 倍率 1.5) + key→线路 ----
	// 内置线路已移除(000057),线路改为必建必绑;这里自建一条专用线路。
	user, err := h.q.CreateUser(ctx, sqlc.CreateUserParams{Email: fmt.Sprintf("e2e-%d@example.test", suffix), PasswordHash: "x", DisplayName: "e2e"})
	if err != nil {
		return fmt.Errorf("create user: %w", err)
	}
	h.userID = user.ID
	if err := h.q.EnsureUserBalance(ctx, sqlc.EnsureUserBalanceParams{UserID: user.ID, Currency: "USD"}); err != nil {
		return fmt.Errorf("ensure balance: %w", err)
	}
	if _, err := h.q.AddUserBalance(ctx, sqlc.AddUserBalanceParams{Amount: num(1000_0000000000), UserID: user.ID, Currency: "USD"}); err != nil {
		return fmt.Errorf("add balance: %w", err)
	}
	var routeID int64
	if err := pool.QueryRow(ctx, `
		INSERT INTO routes (name, mode, pool_kind, status, price_ratio)
		VALUES ($1, 'cheapest', 'all', 'enabled', $2) RETURNING id
	`, fmt.Sprintf("e2e-route-%d", suffix), routeRatio).Scan(&routeID); err != nil {
		return fmt.Errorf("create e2e route: %w", err)
	}
	gk, err := apikey.Generate()
	if err != nil {
		return fmt.Errorf("gen key: %w", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO api_keys (user_id, name, key_prefix, key_hash, key_plaintext, route_id) VALUES ($1,'e2e',$2,$3,$4,$5)`,
		user.ID, gk.Prefix, gk.Hash, gk.Plaintext, routeID); err != nil {
		return fmt.Errorf("create key: %w", err)
	}
	h.apiKey = gk.Plaintext

	providerID, err := h.insertProvider(ctx, fmt.Sprintf("starapi-%d", suffix))
	if err != nil {
		return err
	}

	// OpenAI 渠道 + 模型（base 直接用 .../v1，adapter 拼 /chat/completions）。
	oaiChannelID, err := h.insertChannel(ctx, providerID, fmt.Sprintf("oai-%d", suffix), "openai", "openai", oaiBase, oaiKey)
	if err != nil {
		return err
	}
	oaiModelDBID, err := h.insertPricedModel(ctx, oaiChannelID, oaiModel, "openai", oaiModel)
	if err != nil {
		return err
	}

	// Anthropic 渠道 + 模型（base 去掉尾部 /v1，adapter 拼 /v1/messages）。
	antEnabled := antBase != "" && antKey != ""
	var antModelDBID int64
	if antEnabled {
		antChannelID, err := h.insertChannel(ctx, providerID, fmt.Sprintf("ant-%d", suffix), "anthropic", "anthropic", strings.TrimSuffix(strings.TrimRight(antBase, "/"), "/v1"), antKey)
		if err != nil {
			return err
		}
		antModelDBID, err = h.insertPricedModel(ctx, antChannelID, antModel, "anthropic", antModel)
		if err != nil {
			return err
		}
		_ = antModelDBID
	}

	// 异常用模型：绑定到 OpenAI 渠道但「无任何定价」（无 model_prices/channel_prices）→ 无可用渠道。
	noPriceModel := fmt.Sprintf("gpt-noprice-%d", suffix)
	if _, err := h.insertBoundModelNoPrice(ctx, oaiChannelID, noPriceModel); err != nil {
		return err
	}
	_ = oaiModelDBID

	// ---- 起网关 ----
	app, err := bootstrap.NewGatewayServerApp(ctx, bootstrap.GatewayServerAppDeps{
		Logger: slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError})),
		Config: cfg, DB: pool, Redis: redisClient,
	})
	if err != nil {
		return fmt.Errorf("NewGatewayServerApp: %w", err)
	}
	defer app.Shutdown(context.Background())
	h.srv = httptest.NewServer(app.Handler)
	defer h.srv.Close()

	fmt.Printf("=== 真实回归开始（user=%d, route 倍率=%s, 期望售价 in=%s out=%s）===\n", user.ID, routeRatio, wantSaleIn, wantSaleOut)

	// ==== 场景 ====
	h.scenarioOpenAIHappy(ctx, oaiModel)
	h.scenarioOpenAIStream(ctx, oaiModel)
	h.scenarioOpenAILongConversation(ctx, oaiModel)
	if antEnabled {
		h.scenarioAnthropicHappy(ctx, antModel)
	} else {
		fmt.Printf("[skip] Anthropic 场景（未设 UNIO_E2E_ANT_BASEURL/KEY）\n")
	}
	h.scenarioFallbackPriceInvariant(ctx, providerID, oaiModelDBID, oaiModel, suffix)
	h.scenarioNoAvailableChannel(ctx, noPriceModel)
	h.scenarioBadAuth(ctx, oaiModel)
	h.scenarioInsufficientBalance(ctx, oaiModel, routeID, suffix)

	fmt.Printf("\n=== 回归结束：PASS=%d FAIL=%d ===\n", h.pass, h.fail)
	if h.fail > 0 {
		os.Exit(1)
	}
	return nil
}

// ---------- 场景 ----------

func (h *harness) scenarioOpenAIHappy(ctx context.Context, model string) {
	bal0 := h.balance(ctx)
	body := chatBody(model, []msg{{"user", "Reply with exactly: hello world"}}, 20, false)
	status, resp, _ := h.httpDo(ctx, "POST", "/v1/chat/completions", bearer(h.apiKey), body)
	ok := status == 200 && strings.Contains(resp, "choices")
	h.check("OpenAI happy chat", ok, fmt.Sprintf("status=%d", status), resp)
	if ok {
		h.assertBilling(ctx, "OpenAI happy 计费", bal0)
	}
}

func (h *harness) scenarioOpenAIStream(ctx context.Context, model string) {
	debit0 := h.debit(ctx)
	body := chatBody(model, []msg{{"user", "Count from 1 to 5."}}, 60, true)
	status, chunks, doneOK := h.httpStream(ctx, "/v1/chat/completions", bearer(h.apiKey), body)
	ok := status == 200 && chunks > 0 && doneOK
	h.check("OpenAI streaming", ok, fmt.Sprintf("status=%d chunks=%d done=%v", status, chunks, doneOK), "")
	if ok {
		time.Sleep(800 * time.Millisecond) // 流式结算在流结束后异步收口
		debit1 := h.debit(ctx)
		h.check("OpenAI streaming 扣费入账", cmpDecGreater(debit1, debit0), fmt.Sprintf("debit %s→%s", debit0, debit1), "")
	}
}

func (h *harness) scenarioOpenAILongConversation(ctx context.Context, model string) {
	bal0 := h.balance(ctx)
	// 多轮上下文：交替 user/assistant，最后再问一轮，验证长上下文链路 + 计费随 token 放大。
	conv := []msg{
		{"system", "You are a concise assistant. Answer in one short sentence."},
		{"user", "My name is Chen and I am building an LLM gateway called Unio."},
		{"assistant", "Nice to meet you, Chen — Unio sounds like a solid gateway project."},
		{"user", "It routes by tiers with a price multiplier per route."},
		{"assistant", "Got it: per-route multipliers let you tier pricing cleanly."},
		{"user", "What is my name and what am I building? Answer in one sentence."},
	}
	body := chatBody(model, conv, 60, false)
	status, resp, _ := h.httpDo(ctx, "POST", "/v1/chat/completions", bearer(h.apiKey), body)
	ok := status == 200 && strings.Contains(resp, "choices")
	h.check("OpenAI 长多轮对话", ok, fmt.Sprintf("status=%d", status), resp)
	if ok {
		h.assertBilling(ctx, "长对话计费", bal0)
	}
}

func (h *harness) scenarioAnthropicHappy(ctx context.Context, model string) {
	bal0 := h.balance(ctx)
	body := []byte(fmt.Sprintf(`{"model":%q,"max_tokens":20,"messages":[{"role":"user","content":"Reply with exactly: hello world"}]}`, model))
	hdr := map[string]string{"x-api-key": h.apiKey, "anthropic-version": "2023-06-01", "Content-Type": "application/json"}
	status, resp, _ := h.httpDo(ctx, "POST", "/v1/messages", hdr, body)
	ok := status == 200 && (strings.Contains(resp, "content") || strings.Contains(resp, "text"))
	h.check("Anthropic happy messages", ok, fmt.Sprintf("status=%d", status), resp)
	if ok {
		h.assertBilling(ctx, "Anthropic happy 计费", bal0)
	}
}

func (h *harness) scenarioFallbackPriceInvariant(ctx context.Context, providerID, modelDBID int64, model string, suffix int64) {
	// 同线路插一个「更便宜但连不上」的渠道，优先级 0 先被选中 → 失败 → 回退真实渠道。
	brokenID, err := h.insertChannel(ctx, providerID, fmt.Sprintf("broken-%d", suffix), "openai", "openai", "http://127.0.0.1:1/v1", "sk-broken")
	if err != nil {
		h.check("Fallback 准备(坏渠道)", false, err.Error(), "")
		return
	}
	if _, err := h.pool.Exec(ctx, `INSERT INTO channel_models (channel_id, model_id, upstream_model, status) VALUES ($1,$2,$3,'enabled')`, brokenID, modelDBID, model); err != nil {
		h.check("Fallback 准备(绑定)", false, err.Error(), "")
		return
	}
	// 坏渠道成本更低（cheapest 先选它）+ 优先级靠前。
	if _, err := h.q.CreateChannelPrice(ctx, sqlc.CreateChannelPriceParams{
		ChannelID: brokenID, ModelID: modelDBID, Currency: "USD", PricingUnit: "per_1m_tokens",
		UncachedInputCost: num(0_1000000000), OutputCost: num(0_1000000000),
		Status: "enabled", EffectiveFrom: ts(time.Now().Add(-time.Hour)),
	}); err != nil {
		h.check("Fallback 准备(成本)", false, err.Error(), "")
		return
	}

	bal0 := h.balance(ctx)
	body := chatBody(model, []msg{{"user", "Reply with exactly: hello world"}}, 20, false)
	status, resp, _ := h.httpDo(ctx, "POST", "/v1/chat/completions", bearer(h.apiKey), body)
	ok := status == 200 && strings.Contains(resp, "choices")
	h.check("Fallback 回退成功", ok, fmt.Sprintf("status=%d", status), resp)
	if ok {
		// 客价不变：售价费率仍 = 基准 × 倍率（与命中哪条渠道、渠道成本无关）。
		h.assertBilling(ctx, "Fallback 客价不变", bal0)
	}
}

func (h *harness) scenarioNoAvailableChannel(ctx context.Context, noPriceModel string) {
	body := chatBody(noPriceModel, []msg{{"user", "hi"}}, 10, false)
	status, resp, _ := h.httpDo(ctx, "POST", "/v1/chat/completions", bearer(h.apiKey), body)
	// 模型已绑定但无定价 → 无可用候选 → 拒绝：503 model_unavailable（或 4xx）。不得 2xx、不得计费。
	ok := status == 503 || status == 404 || (status >= 400 && status < 500)
	h.check("异常·无可用渠道(未定价模型)", ok, fmt.Sprintf("status=%d", status), resp)
}

func (h *harness) scenarioBadAuth(ctx context.Context, model string) {
	body := chatBody(model, []msg{{"user", "hi"}}, 10, false)
	status, resp, _ := h.httpDo(ctx, "POST", "/v1/chat/completions", bearer("unio_sk_invalidinvalidinvalid"), body)
	ok := status == 401
	h.check("异常·鉴权失败", ok, fmt.Sprintf("status=%d", status), resp)
}

func (h *harness) scenarioInsufficientBalance(ctx context.Context, model string, routeID, suffix int64) {
	// 另起一个余额为 0 的用户/项目/key，验证余额闸门拦截。
	u, err := h.q.CreateUser(ctx, sqlc.CreateUserParams{Email: fmt.Sprintf("e2e-poor-%d@example.test", suffix), PasswordHash: "x", DisplayName: "poor"})
	if err != nil {
		h.check("异常·余额不足 准备", false, err.Error(), "")
		return
	}
	if err := h.q.EnsureUserBalance(ctx, sqlc.EnsureUserBalanceParams{UserID: u.ID, Currency: "USD"}); err != nil {
		h.check("异常·余额不足 准备", false, err.Error(), "")
		return
	}
	gk, _ := apikey.Generate()
	if _, err := h.pool.Exec(ctx, `INSERT INTO api_keys (user_id, name, key_prefix, key_hash, key_plaintext, route_id) VALUES ($1,'poor',$2,$3,$4,$5)`,
		u.ID, gk.Prefix, gk.Hash, gk.Plaintext, routeID); err != nil {
		h.check("异常·余额不足 准备", false, err.Error(), "")
		return
	}
	body := chatBody(model, []msg{{"user", "hi"}}, 10, false)
	status, resp, _ := h.httpDo(ctx, "POST", "/v1/chat/completions", bearer(gk.Plaintext), body)
	ok := status == 402 || status == 403 || status == 429 || (status >= 400 && strings.Contains(strings.ToLower(resp), "balance"))
	h.check("异常·余额不足拦截", ok, fmt.Sprintf("status=%d", status), resp)
}

// ---------- 计费断言 ----------

func (h *harness) assertBilling(ctx context.Context, label string, bal0 string) {
	time.Sleep(300 * time.Millisecond)
	var saleIn, saleOut string
	_ = h.pool.QueryRow(ctx, `
		SELECT ps.uncached_input_price::text, ps.output_price::text
		FROM price_snapshots ps JOIN request_records rr ON rr.id = ps.request_record_id
		WHERE rr.user_id=$1 ORDER BY ps.id DESC LIMIT 1`, h.userID).Scan(&saleIn, &saleOut)
	bal1 := h.balance(ctx)
	rateOK := saleIn == wantSaleIn && saleOut == wantSaleOut
	balOK := cmpDecGreater(bal0, bal1) // 余额下降
	h.check(label+"·售价=基准×倍率", rateOK, fmt.Sprintf("snapshot sale in=%s out=%s (want %s/%s)", saleIn, saleOut, wantSaleIn, wantSaleOut), "")
	h.check(label+"·余额扣减", balOK, fmt.Sprintf("balance %s→%s", bal0, bal1), "")
}

func (h *harness) balance(ctx context.Context) string {
	var b string
	_ = h.pool.QueryRow(ctx, `SELECT balance::text FROM user_balances WHERE user_id=$1 AND currency='USD'`, h.userID).Scan(&b)
	return b
}

func (h *harness) debit(ctx context.Context) string {
	var d string
	_ = h.pool.QueryRow(ctx, `SELECT COALESCE(SUM(amount),0)::text FROM ledger_entries WHERE user_id=$1 AND entry_type='debit'`, h.userID).Scan(&d)
	return d
}

// ---------- seed helpers ----------

func (h *harness) insertProvider(ctx context.Context, slug string) (int64, error) {
	var id int64
	err := h.pool.QueryRow(ctx, `INSERT INTO providers (slug, name, status) VALUES ($1,$1,'enabled') RETURNING id`, slug).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("insert provider: %w", err)
	}
	return id, nil
}

func (h *harness) insertChannel(ctx context.Context, providerID int64, name, protocol, adapterKey, baseURL, credential string) (int64, error) {
	var id int64
	err := h.pool.QueryRow(ctx, `
		INSERT INTO channels (provider_id, name, protocol, adapter_key, base_url, credential, status, priority, timeout_ms)
		VALUES ($1,$2,$3,$4,$5,$6,'enabled',$7,60000) RETURNING id`,
		providerID, name, protocol, adapterKey, baseURL, credential, channelPriority(name)).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("insert channel %s: %w", name, err)
	}
	return id, nil
}

// channelPriority：坏渠道优先级 0（先被选中触发 fallback），真实渠道 100。
func channelPriority(name string) int32 {
	if strings.HasPrefix(name, "broken") {
		return 0
	}
	return 100
}

// insertPricedModel 建模型 + 绑定 + 渠道成本(1/4) + 模型基准价(2/10)。
func (h *harness) insertPricedModel(ctx context.Context, channelID int64, modelID, ownedBy, upstreamModel string) (int64, error) {
	var modelDBID int64
	if err := h.pool.QueryRow(ctx, `
		INSERT INTO models (model_id, display_name, owned_by, status, max_output_tokens)
		VALUES ($1,$1,$2,'enabled',128000) RETURNING id`, modelID, ownedBy).Scan(&modelDBID); err != nil {
		return 0, fmt.Errorf("insert model %s: %w", modelID, err)
	}
	if _, err := h.pool.Exec(ctx, `INSERT INTO channel_models (channel_id, model_id, upstream_model, status) VALUES ($1,$2,$3,'enabled')`,
		channelID, modelDBID, upstreamModel); err != nil {
		return 0, fmt.Errorf("bind %s: %w", modelID, err)
	}
	if _, err := h.q.CreateChannelPrice(ctx, sqlc.CreateChannelPriceParams{
		ChannelID: channelID, ModelID: modelDBID, Currency: "USD", PricingUnit: "per_1m_tokens",
		UncachedInputCost: num(1_0000000000), OutputCost: num(5_0000000000),
		Status: "enabled", EffectiveFrom: ts(time.Now().Add(-time.Hour)),
	}); err != nil {
		return 0, fmt.Errorf("channel cost %s: %w", modelID, err)
	}
	if _, err := h.q.CreateModelPrice(ctx, sqlc.CreateModelPriceParams{
		ModelID: modelDBID, Currency: "USD", PricingUnit: "per_1m_tokens",
		UncachedInputPrice: num(2_0000000000), OutputPrice: num(10_0000000000),
		Status: "enabled", EffectiveFrom: ts(time.Now().Add(-time.Hour)),
	}); err != nil {
		return 0, fmt.Errorf("model base price %s: %w", modelID, err)
	}
	return modelDBID, nil
}

// insertBoundModelNoPrice 建模型 + 绑定到渠道，但「不配任何价」→ 路由无候选。
func (h *harness) insertBoundModelNoPrice(ctx context.Context, channelID int64, modelID string) (int64, error) {
	var modelDBID int64
	if err := h.pool.QueryRow(ctx, `
		INSERT INTO models (model_id, display_name, owned_by, status, max_output_tokens)
		VALUES ($1,$1,'openai','enabled',128000) RETURNING id`, modelID).Scan(&modelDBID); err != nil {
		return 0, fmt.Errorf("insert noprice model: %w", err)
	}
	if _, err := h.pool.Exec(ctx, `INSERT INTO channel_models (channel_id, model_id, upstream_model, status) VALUES ($1,$2,$3,'enabled')`,
		channelID, modelDBID, modelID); err != nil {
		return 0, fmt.Errorf("bind noprice model: %w", err)
	}
	return modelDBID, nil
}

// ---------- HTTP helpers ----------

type msg struct{ role, content string }

func chatBody(model string, msgs []msg, maxTokens int, stream bool) []byte {
	var b strings.Builder
	b.WriteString(fmt.Sprintf(`{"model":%q,"max_tokens":%d,"stream":%v,"messages":[`, model, maxTokens, stream))
	for i, m := range msgs {
		if i > 0 {
			b.WriteString(",")
		}
		b.WriteString(fmt.Sprintf(`{"role":%q,"content":%q}`, m.role, m.content))
	}
	b.WriteString("]}")
	return []byte(b.String())
}

func bearer(key string) map[string]string {
	return map[string]string{"Authorization": "Bearer " + key, "Content-Type": "application/json"}
}

func (h *harness) httpDo(ctx context.Context, method, path string, headers map[string]string, body []byte) (int, string, error) {
	req, _ := http.NewRequestWithContext(ctx, method, h.srv.URL+path, bytes.NewReader(body))
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, "", err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b), nil
}

func (h *harness) httpStream(ctx context.Context, path string, headers map[string]string, body []byte) (int, int, bool) {
	req, _ := http.NewRequestWithContext(ctx, "POST", h.srv.URL+path, bytes.NewReader(body))
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	req.Header.Set("Accept", "text/event-stream")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, 0, false
	}
	defer resp.Body.Close()
	chunks, done := 0, false
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if payload == "[DONE]" {
			done = true
			continue
		}
		chunks++
	}
	return resp.StatusCode, chunks, done
}

// ---------- 结果 ----------

func (h *harness) check(name string, ok bool, detail, body string) {
	if ok {
		h.pass++
		fmt.Printf("  PASS  %s — %s\n", name, detail)
		return
	}
	h.fail++
	fmt.Printf("  FAIL  %s — %s\n", name, detail)
	if body != "" {
		fmt.Printf("        body: %s\n", truncate(body, 400))
	}
}

func num(units int64) pgtype.Numeric {
	return pgtype.Numeric{Int: big.NewInt(units), Exp: -10, Valid: true}
}
func ts(t time.Time) pgtype.Timestamptz { return pgtype.Timestamptz{Time: t, Valid: true} }
func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "...(truncated)"
}

// cmpDecGreater 报告十进制字符串 a > b（用 big.Rat 精确比较；解析失败按 false）。
func cmpDecGreater(a, b string) bool {
	ra, oka := new(big.Rat).SetString(a)
	rb, okb := new(big.Rat).SetString(b)
	if !oka || !okb {
		return false
	}
	return ra.Cmp(rb) > 0
}
