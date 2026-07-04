// Command e2e-reqrecords 是「请求记录改造（批一列表富化 + 批二 route快照/reasoning/client_ip）」的
// 真实上游端到端回归：复用现有启用线路（route_channels 已配好的真实渠道/密钥），临时建一个
// 一次性用户 + 余额 + Key 绑定到该线路，在进程内启动【当前源码】的 gateway，打真实请求，
// 然后校验：
//
//	写路径（批二）：request_records.route_id 快照 / reasoning_effort 归一 / client_ip 落库。
//	读路径（批一）：admin query.RequestService.List 富化项 token/成本/扣费/时延/线路名/渠道链。
//
// 运行：cd unio-api && set -a && . ./.env && set +a && go run ./cmd/e2e-reqrecords
// 需放行外网（真实上游）。会产生极小额真实计费（max_tokens 很小）。临时数据不清理。
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"

	"github.com/ThankCat/unio-api/internal/bootstrap"
	"github.com/ThankCat/unio-api/internal/core/apikey"
	"github.com/ThankCat/unio-api/internal/platform/config"
	"github.com/ThankCat/unio-api/internal/platform/store/sqlc"
	"github.com/ThankCat/unio-api/internal/service/admin/query"
)

// 复用现有真实线路（route_channels 已挂真实渠道）。gpt-5.4-mini 最便宜。
const (
	reuseRouteID = 75
	testModel    = "gpt-5.4-mini"
	testClientIP = "203.0.113.7"
)

type h struct {
	pool   *pgxpool.Pool
	srv    *httptest.Server
	reqs   *query.RequestService
	userID int64
	keyID  int64
	apiKey string
	pass   int
	fail   int
}

func main() {
	if err := run(); err != nil {
		log.Fatalf("e2e-reqrecords INFRA FAILED: %v", err)
	}
}

func run() error {
	ctx := context.Background()
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("config.Load: %w", err)
	}
	pool, err := pgxpool.New(ctx, os.Getenv("DATABASE_URL"))
	if err != nil {
		return fmt.Errorf("pgxpool: %w", err)
	}
	defer pool.Close()
	rdb := redis.NewClient(&redis.Options{Addr: os.Getenv("REDIS_ADDR"), Password: os.Getenv("REDIS_PASSWORD")})
	defer rdb.Close()

	st := &h{pool: pool, reqs: query.NewRequestService(sqlc.New(pool))}

	// ---- seed 一次性用户 + 余额 + Key → 复用线路 75 ----
	suffix := time.Now().UnixNano()
	if err := pool.QueryRow(ctx,
		`INSERT INTO users (email, password_hash, display_name) VALUES ($1,'x','e2e-reqrec') RETURNING id`,
		fmt.Sprintf("e2e-reqrec-%d@example.test", suffix)).Scan(&st.userID); err != nil {
		return fmt.Errorf("create user: %w", err)
	}
	if _, err := pool.Exec(ctx,
		`INSERT INTO user_balances (user_id, currency, balance) VALUES ($1,'USD',100)
		 ON CONFLICT (user_id, currency) DO UPDATE SET balance = user_balances.balance + 100`,
		st.userID); err != nil {
		return fmt.Errorf("seed balance: %w", err)
	}
	gk, err := apikey.Generate()
	if err != nil {
		return fmt.Errorf("gen key: %w", err)
	}
	if err := pool.QueryRow(ctx,
		`INSERT INTO api_keys (user_id, name, key_prefix, key_hash, key_plaintext, route_id)
		 VALUES ($1,'e2e-reqrec',$2,$3,$4,$5) RETURNING id`,
		st.userID, gk.Prefix, gk.Hash, gk.Plaintext, reuseRouteID).Scan(&st.keyID); err != nil {
		return fmt.Errorf("create key: %w", err)
	}
	st.apiKey = gk.Plaintext

	// ---- 进程内起当前源码的 gateway ----
	app, err := bootstrap.NewGatewayServerApp(ctx, bootstrap.GatewayServerAppDeps{
		Logger: slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError})),
		Config: cfg, DB: pool, Redis: rdb,
	})
	if err != nil {
		return fmt.Errorf("NewGatewayServerApp: %w", err)
	}
	defer app.Shutdown(context.Background())
	st.srv = httptest.NewServer(app.Handler)
	defer st.srv.Close()

	fmt.Printf("=== 请求记录改造 真实回归（user=%d key=%d route=%d model=%s）===\n",
		st.userID, st.keyID, reuseRouteID, testModel)

	// 场景 A：普通 chat（带 X-Forwarded-For）→ 批一富化 + client_ip + route_id 快照。
	st.scenarioPlain(ctx)
	// 场景 B：带 reasoning_effort=high → reasoning_effort 归一落库。
	st.scenarioReasoning(ctx)

	fmt.Printf("\n=== 回归结束：PASS=%d FAIL=%d ===\n", st.pass, st.fail)
	if st.fail > 0 {
		os.Exit(1)
	}
	return nil
}

func (st *h) scenarioPlain(ctx context.Context) {
	body := chatBody(testModel, "Reply with exactly: hello world", 16, nil)
	status, resp := st.post(ctx, body, map[string]string{"X-Forwarded-For": testClientIP + ", 10.0.0.1"})
	ok := status == 200
	st.check("A 普通 chat 成功", ok, fmt.Sprintf("status=%d", status), resp)
	if !ok {
		return
	}
	time.Sleep(900 * time.Millisecond) // 等异步结算收口

	reqID, routeID, effort, clientIP, recStatus := st.latestRecord(ctx)
	st.check("A route_id 快照=线路", routeID != nil && *routeID == reuseRouteID,
		fmt.Sprintf("route_id=%v", deref64(routeID)), "")
	st.check("A client_ip=XFF 首个", clientIP == testClientIP, fmt.Sprintf("client_ip=%q", clientIP), "")
	st.check("A 未发 reasoning → effort 空", effort == "", fmt.Sprintf("effort=%q", effort), "")
	st.check("A 记录状态 succeeded", recStatus == "succeeded", fmt.Sprintf("status=%q", recStatus), "")

	// 读路径：admin List 富化项。
	st.assertListEnrichment(ctx, reqID)
}

func (st *h) scenarioReasoning(ctx context.Context) {
	body := chatBody(testModel, "Say hi.", 16, ptr("high"))
	status, resp := st.post(ctx, body, nil)
	// 上游是否接受该参数不影响断言：reasoning_effort 在建记录时即捕获（路由前）。
	st.check("B reasoning 请求已受理", status == 200 || (status >= 400 && status < 600),
		fmt.Sprintf("status=%d", status), resp)
	time.Sleep(700 * time.Millisecond)

	_, _, effort, _, _ := st.latestRecord(ctx)
	st.check("B reasoning_effort 归一=high", effort == "high", fmt.Sprintf("effort=%q", effort), resp)
}

// assertListEnrichment 用 admin 只读查询服务验证批一列表富化（真实跑一遍新 SQL + 映射）。
func (st *h) assertListEnrichment(ctx context.Context, reqID string) {
	items, total, err := st.reqs.List(ctx, query.RequestListParams{
		UserID: &st.userID, Limit: 20, Offset: 0, SortField: "created_at", SortDesc: true,
	})
	if err != nil {
		st.check("读路径 List 无错误", false, err.Error(), "")
		return
	}
	st.check("读路径 List 返回本用户记录", total >= 1 && len(items) >= 1, fmt.Sprintf("total=%d len=%d", total, len(items)), "")
	var it *query.RequestListItem
	for i := range items {
		if items[i].RequestID == reqID {
			it = &items[i]
			break
		}
	}
	if it == nil {
		st.check("读路径 命中目标请求", false, "target request not in list", "")
		return
	}
	st.check("富化·token 非零", it.UncachedInputTokens > 0 || it.OutputTokens > 0,
		fmt.Sprintf("in=%d out=%d", it.UncachedInputTokens, it.OutputTokens), "")
	st.check("富化·用户扣费非空", it.UserChargeUSD != nil, fmt.Sprintf("charge=%v", derefs(it.UserChargeUSD)), "")
	st.check("富化·平台成本非空", it.TotalCostUSD != nil, fmt.Sprintf("cost=%v", derefs(it.TotalCostUSD)), "")
	st.check("富化·总耗时非空", it.LatencyMs != nil, fmt.Sprintf("latency=%v", deref64(it.LatencyMs)), "")
	st.check("富化·线路名非空", it.RouteName != nil && *it.RouteName != "", fmt.Sprintf("route=%v", derefs(it.RouteName)), "")
	st.check("富化·渠道链非空", it.ChannelChain != "", fmt.Sprintf("chain=%q", it.ChannelChain), "")
}

// ---------- helpers ----------

func (st *h) latestRecord(ctx context.Context) (reqID string, routeID *int64, effort, clientIP, status string) {
	var rID *int64
	var eff, ip *string
	_ = st.pool.QueryRow(ctx, `
		SELECT request_id, route_id, reasoning_effort, client_ip, status
		FROM request_records WHERE user_id=$1 ORDER BY id DESC LIMIT 1`, st.userID).
		Scan(&reqID, &rID, &eff, &ip, &status)
	return reqID, rID, derefs(eff), derefs(ip), status
}

func (st *h) post(ctx context.Context, body []byte, extra map[string]string) (int, string) {
	req, _ := http.NewRequestWithContext(ctx, "POST", st.srv.URL+"/v1/chat/completions", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+st.apiKey)
	req.Header.Set("Content-Type", "application/json")
	for k, v := range extra {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, err.Error()
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

func chatBody(model, content string, maxTokens int, effort *string) []byte {
	m := map[string]any{
		"model":      model,
		"max_tokens": maxTokens,
		"messages":   []map[string]string{{"role": "user", "content": content}},
	}
	if effort != nil {
		m["reasoning_effort"] = *effort
	}
	b, _ := json.Marshal(m)
	return b
}

func (st *h) check(name string, ok bool, detail, body string) {
	if ok {
		st.pass++
		fmt.Printf("  PASS  %s — %s\n", name, detail)
		return
	}
	st.fail++
	fmt.Printf("  FAIL  %s — %s\n", name, detail)
	if body != "" {
		fmt.Printf("        body: %s\n", truncate(body, 300))
	}
}

func ptr(s string) *string { return &s }
func derefs(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
func deref64(v *int64) int64 {
	if v == nil {
		return -1
	}
	return *v
}
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
