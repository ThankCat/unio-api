//go:build blackbox

// Package sdkfixture 提供双协议 SDK 黑盒验收使用的共享 fixture：
// 装一个完整的 unio gateway HTTP server (httptest) + 真实 PostgreSQL + Redis + 一份
// 可用的 user / api key / channel / model / price / balance / credential。
//
// 客户 SDK 只需要把 base_url 指到 Fixture.BaseURL，把 Authorization 设成 Fixture.APIKey，
// 就能像调真实 OpenAI / Anthropic 一样调 Unio Gateway。
//
// 仅在 -tags=blackbox 下编译。普通 go build / go test 不会引入 openai-go / anthropic-sdk
// 等第三方 SDK，生产二进制无 SDK 残留。
package sdkfixture

import (
	"context"
	"encoding/json"
	"fmt"
	"math/big"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"
	"go.uber.org/zap"

	"github.com/ThankCat/unio-gateway/internal/bootstrap"
	"github.com/ThankCat/unio-gateway/internal/core/apikey"
	"github.com/ThankCat/unio-gateway/internal/core/billing"
	"github.com/ThankCat/unio-gateway/internal/platform/breakerstore"
	"github.com/ThankCat/unio-gateway/internal/platform/config"
	"github.com/ThankCat/unio-gateway/internal/platform/store/sqlc"
	adminchannel "github.com/ThankCat/unio-gateway/internal/service/admin/channel"
	"github.com/ThankCat/unio-gateway/internal/service/appsettings"
)

// 默认值。
const (
	defaultRedisAddr      = "localhost:6380"
	defaultRedisNamespace = "unio:blackbox"
)

// UpstreamMode 选择 fixture 上游模式。
type UpstreamMode int

const (
	// UpstreamReal 使用 gated 真实上游；RealUpstreamEnv 为空时使用现有 DeepSeek 配置。
	//
	// 前置 env：
	//   - DEEPSEEK_BLACKBOX=1
	//   - DEEPSEEK_API_KEY=<可用 key>
	//
	// 任一缺失即 t.Skip，与 adapter 层 DS-OAI / DS-ANT 黑盒约定一致。
	UpstreamReal UpstreamMode = iota

	// UpstreamMock 把 ProviderOrigin base_url 指向调用方传入的 httptest mock server，
	// 用于错误映射、fallback、Drop 字段、边界等不依赖真实上游的用例。
	UpstreamMock
)

// RealUpstreamEnv 描述一组 gated 真实上游环境变量。
//
// 字段只保存环境变量名，不保存对应值。Setup 在 fixture 内部读取 API key 并直接写入
// 临时 channel credential，调用方测试不会接触或输出真实上游密钥。
type RealUpstreamEnv struct {
	// GateEnv 必须等于 "1"，否则测试跳过。
	GateEnv string
	// BaseURLEnv 必须是无 endpoint path 的 http(s) API root。
	BaseURLEnv string
	// APIKeyEnv 缺失时测试跳过。
	APIKeyEnv string
	// ModelEnv 可选；设置后缺失时测试跳过，并作为默认 ModelID / UpstreamModel。
	ModelEnv string
}

// SetupOptions 定义 fixture 装配选项。
type SetupOptions struct {
	Mode UpstreamMode

	// RealUpstreamEnv 在 Mode=UpstreamReal 时可选。为空时保留现有 DeepSeek gate、
	// API key 与 protocol-specific root；非空时按描述解析任意兼容上游。
	RealUpstreamEnv *RealUpstreamEnv

	// UpstreamBaseURL 当 Mode=UpstreamMock 时必填，使用 ProviderOrigin root，
	// 例如 mockServer.URL；adapter 会追加完整的 /v1/... endpoint path。
	UpstreamBaseURL string
	// UpstreamAPIKey 当 Mode=UpstreamMock 时可选；为空时默认 "sk-test-mock"。
	UpstreamAPIKey string

	// AdapterKey 渠道使用的 adapter 注册键，默认 "deepseek"
	// （bootstrap.NewAdapterRegistry 已注册 DeepSeek OpenAI/Anthropic 双协议）。
	AdapterKey string

	// Protocol 当前 fixture 的 ingress / channel 协议，默认 "openai"。
	// Anthropic SDK 黑盒会传 "anthropic"。
	Protocol string

	// ModelID 客户在 SDK 里看到的模型名，默认 "deepseek-v4-flash"。
	ModelID string
	// UpstreamModel 上游真实模型名；默认与 ModelID 相同。
	UpstreamModel string

	// InitialBalanceUSD 用户初始可用余额（整数 USD），默认 10。
	// 注意：用 NUMERIC(20,10) 存，避免 float。
	InitialBalanceUSD int64

	// ChannelTimeoutMS 是 channels.timeout_ms（adapter 调上游的单次超时）。默认 60_000。
	// 超时映射用例可以设小一些（如 500ms）来快速触发。
	ChannelTimeoutMS int32
}

// Fixture 是一个跑着的 unio gateway HTTP server 黑盒 fixture。
type Fixture struct {
	// Server 是 unio gateway 的 httptest server；测试结束自动 Close。
	Server *httptest.Server
	// BaseURL = Server.URL + "/v1"，喂给 openai-go SDK（它的 BaseURL 已包含 /v1 段）。
	BaseURL string
	// AnthropicBaseURL = Server.URL + "/"，喂给 anthropic-sdk-go SDK
	// （它的 BaseURL 期望根，内部自己拼 v1/messages）。
	AnthropicBaseURL string
	// APIKey 是 unio 颁发的 opaque API key，客户 SDK 用它做 Bearer token / x-api-key。
	APIKey string

	// Pool / Queries 给测试自己查 DB 校验 settlement / delivery audit 事实。
	Pool    *pgxpool.Pool
	Queries *sqlc.Queries

	UserID   int64
	APIKeyID int64
	RouteID  int64

	ProviderID     int64
	ChannelID      int64
	ModelDBID      int64
	ModelPriceID   int64
	ChannelPriceID int64

	ModelID       string
	UpstreamModel string

	ctx                     context.Context
	cancel                  context.CancelFunc
	redisClient             redis.UniversalClient
	breakerStore            *breakerstore.Store
	suffix                  int64
	fallbackChannelIDs      []int64
	fallbackChannelPriceIDs []int64
}

// Setup 装一个 unio gateway 黑盒 fixture。
// 前置 env 缺失会 t.Skip；teardown 已通过 t.Cleanup 注册。
func Setup(t *testing.T, opts SetupOptions) *Fixture {
	t.Helper()

	databaseURL := os.Getenv("DATABASE_URL")
	if databaseURL == "" {
		t.Skip("DATABASE_URL is not set")
	}

	var (
		upstreamBaseURL string
		upstreamAPIKey  string
	)
	if opts.Mode == UpstreamReal && opts.RealUpstreamEnv == nil {
		if os.Getenv("DEEPSEEK_BLACKBOX") != "1" {
			t.Skip("DEEPSEEK_BLACKBOX is not set to 1")
		}
		if os.Getenv("DEEPSEEK_API_KEY") == "" {
			t.Skip("DEEPSEEK_API_KEY is not set")
		}
		upstreamAPIKey = os.Getenv("DEEPSEEK_API_KEY")
	} else if opts.Mode == UpstreamReal {
		upstreamBaseURL, upstreamAPIKey = resolveRealUpstream(t, &opts, *opts.RealUpstreamEnv)
	}
	if opts.Mode == UpstreamMock && opts.UpstreamBaseURL == "" {
		t.Fatalf("sdkfixture: UpstreamMock mode requires UpstreamBaseURL")
	}

	// defaults
	if opts.AdapterKey == "" {
		opts.AdapterKey = "deepseek"
	}
	if opts.Protocol == "" {
		opts.Protocol = "openai"
	}
	if opts.ModelID == "" {
		if opts.Protocol == "anthropic" {
			// DeepSeek 的 Anthropic origin 任意 model 名都回 deepseek-v4-flash，
			// 直接用 deepseek 名作客户可见 ID，避免引入"任意 claude 名 → mapping"复杂度。
			opts.ModelID = "deepseek-v4-flash"
		} else {
			opts.ModelID = "deepseek-v4-flash"
		}
	}
	if opts.UpstreamModel == "" {
		opts.UpstreamModel = opts.ModelID
	}
	if upstreamAPIKey == "" {
		upstreamAPIKey = opts.UpstreamAPIKey
	}
	if upstreamAPIKey == "" {
		if opts.Mode == UpstreamMock {
			upstreamAPIKey = "sk-test-mock"
		}
	}
	if opts.InitialBalanceUSD == 0 {
		opts.InitialBalanceUSD = 10
	}
	if opts.ChannelTimeoutMS == 0 {
		opts.ChannelTimeoutMS = 60000
	}

	ctx, cancel := context.WithCancel(context.Background())

	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		cancel()
		t.Fatalf("create postgres pool: %v", err)
	}
	pingCtx, pingCancel := context.WithTimeout(ctx, 5*time.Second)
	if err := pool.Ping(pingCtx); err != nil {
		pingCancel()
		pool.Close()
		cancel()
		t.Fatalf("ping postgres: %v", err)
	}
	pingCancel()

	redisAddr := os.Getenv("REDIS_ADDR")
	if redisAddr == "" {
		redisAddr = defaultRedisAddr
	}
	redisClient := redis.NewClient(&redis.Options{
		Addr:     redisAddr,
		Password: os.Getenv("REDIS_PASSWORD"),
	})
	pingCtx, pingCancel = context.WithTimeout(ctx, 5*time.Second)
	if err := redisClient.Ping(pingCtx).Err(); err != nil {
		pingCancel()
		_ = redisClient.Close()
		pool.Close()
		cancel()
		t.Skipf("redis unavailable at %s: %v", redisAddr, err)
	}
	pingCancel()

	f := &Fixture{
		Pool:          pool,
		Queries:       sqlc.New(pool),
		ModelID:       opts.ModelID,
		UpstreamModel: opts.UpstreamModel,
		ctx:           ctx,
		cancel:        cancel,
		redisClient:   redisClient,
		suffix:        time.Now().UnixNano(),
	}
	cleanupRegistered := false
	defer func() {
		if !cleanupRegistered {
			f.teardown(t)
		}
	}()

	if upstreamBaseURL == "" {
		upstreamBaseURL = opts.UpstreamBaseURL
	}
	if opts.Mode == UpstreamReal && opts.RealUpstreamEnv == nil {
		switch opts.Protocol {
		case "anthropic":
			// adapter 拼 <base>/v1/messages → https://api.deepseek.com/anthropic/v1/messages。
			upstreamBaseURL = "https://api.deepseek.com/anthropic"
		default:
			upstreamBaseURL = "https://api.deepseek.com"
		}
	}

	f.seed(t, opts, upstreamBaseURL, upstreamAPIKey)

	cfg := blackboxConfig()
	f.breakerStore = breakerstore.NewStore(redisClient, cfg.Redis.KeyNamespace)
	logger := zap.NewNop()
	if os.Getenv("BLACKBOX_DEBUG_LOGS") == "1" {
		logger = zap.NewExample()
		t.Cleanup(func() { _ = logger.Sync() })
	}

	// 运行时配置(app_settings)预置：限流放宽并关闭 breaker 门禁；Redis 故障仍固定 fail-closed。
	// (黑盒不验熔断)。经 SettingsStore.Set 写 DB+Redis,gateway 启动读到的即这些值;
	// 启动 seed 是 DO NOTHING,不会覆盖。
	f.seedRuntimeSettings(t, cfg)

	app, err := bootstrap.NewGatewayServerApp(ctx, bootstrap.GatewayServerAppDeps{
		Logger: logger,
		Config: cfg,
		DB:     pool,
		Redis:  redisClient,
	})
	if err != nil {
		t.Fatalf("NewGatewayServerApp: %v", err)
	}

	server := httptest.NewServer(app.Handler)
	f.Server = server
	f.BaseURL = server.URL + "/v1"
	f.AnthropicBaseURL = server.URL + "/"

	t.Cleanup(func() {
		server.Close()
		_ = app.Shutdown(context.Background())
		f.teardown(t)
	})
	// Registered after teardown so LIFO cleanup prints the last sanitized audit
	// snapshot while the fixture rows and database connection still exist.
	f.registerFailureAuditDiagnostics(t)
	cleanupRegistered = true

	return f
}

func resolveRealUpstream(t *testing.T, opts *SetupOptions, env RealUpstreamEnv) (string, string) {
	t.Helper()

	if env.GateEnv == "" || env.BaseURLEnv == "" || env.APIKeyEnv == "" {
		t.Fatal("sdkfixture: RealUpstreamEnv requires GateEnv, BaseURLEnv, and APIKeyEnv")
	}
	if os.Getenv(env.GateEnv) != "1" {
		t.Skipf("%s is not set to 1", env.GateEnv)
	}

	baseURL := strings.TrimSpace(os.Getenv(env.BaseURLEnv))
	if baseURL == "" {
		t.Skipf("%s is not set", env.BaseURLEnv)
	}
	baseURL = requireAPIRoot(t, env.BaseURLEnv, baseURL)

	apiKey := os.Getenv(env.APIKeyEnv)
	if apiKey == "" {
		t.Skipf("%s is not set", env.APIKeyEnv)
	}

	if env.ModelEnv != "" {
		model := strings.TrimSpace(os.Getenv(env.ModelEnv))
		if model == "" {
			t.Skipf("%s is not set", env.ModelEnv)
		}
		if opts.ModelID == "" {
			opts.ModelID = model
		}
		if opts.UpstreamModel == "" {
			opts.UpstreamModel = model
		}
	}

	return baseURL, apiKey
}

func requireAPIRoot(t *testing.T, envName string, raw string) string {
	t.Helper()

	parsed, err := url.Parse(raw)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" ||
		parsed.User != nil || (parsed.Path != "" && parsed.Path != "/") || parsed.RawQuery != "" || parsed.Fragment != "" {
		t.Fatalf("sdkfixture: %s must be an http(s) API root without credentials, path, query, or fragment", envName)
	}
	parsed.Path = ""
	return parsed.String()
}

// AddFallbackChannel 给当前 fixture 的 model 再注册一个 fallback channel：
//
//   - 同 model_id；
//   - 更高 priority 数字（routing 按 priority asc，数字越小优先级越高，所以 fallback 数字应大于主 channel 的 10）；
//   - 独立的 base_url（通常指向另一个 mock upstream）；
//   - 自动 insert 同形状的 channel_cost_prices：生产中 admin 配置时 channel+model 启用前
//     必须配 cost_price，否则一旦命中 settle 时 FindActiveChannelCostPrice 会失败，
//     走 RecoverableChatSettlementExecutor 的 recovery 路径，请求终态被推迟到 worker。
//     fixture 必须忠实模拟「合法 admin 配置 + 副 channel 完整可用」的生产期望，
//     不允许测试默认走 recovery 路径来掩盖配置缺失。
//
// 返回 fallback channel 的数据库 id；teardown 时会一并清理。
func (f *Fixture) AddFallbackChannel(t *testing.T, baseURL string, priority int32) int64 {
	t.Helper()

	// P4 §4.4：base_url 归属 ProviderOrigin；fallback 建一个同 Provider 下的 enabled Origin。
	var fallbackOriginID int64
	if err := f.Pool.QueryRow(f.ctx, `
		INSERT INTO provider_origins (provider_id, name, base_url, status)
		VALUES ($1, $2, $3, 'enabled')
		RETURNING id
	`, f.ProviderID, fmt.Sprintf("blackbox-fallback-ep-%d", f.suffix), baseURL).Scan(&fallbackOriginID); err != nil {
		t.Fatalf("insert fallback provider origin: %v", err)
	}

	var fallbackID int64
	if err := f.Pool.QueryRow(f.ctx, `
		INSERT INTO channels (provider_id, provider_origin_id, name, protocol, adapter_key, credential, status, priority, timeout_ms)
		VALUES ($1, $2, $3, (SELECT protocol FROM channels WHERE id = $4), (SELECT adapter_key FROM channels WHERE id = $4), $5, 'enabled', $6, 60000)
		RETURNING id
	`, f.ProviderID, fallbackOriginID, fmt.Sprintf("blackbox-fallback-%d", f.suffix), f.ChannelID, "sk-fallback-test", priority).Scan(&fallbackID); err != nil {
		t.Fatalf("insert fallback channel: %v", err)
	}

	if _, err := f.Pool.Exec(f.ctx, `
		INSERT INTO channel_models (channel_id, model_id, upstream_model, status)
		VALUES ($1, $2, $3, 'enabled')
	`, fallbackID, f.ModelDBID, f.UpstreamModel); err != nil {
		t.Fatalf("insert fallback channel_model: %v", err)
	}
	if err := f.Queries.AddRouteChannel(f.ctx, sqlc.AddRouteChannelParams{
		RouteID:   f.RouteID,
		ChannelID: fallbackID,
	}); err != nil {
		t.Fatalf("bind fallback channel to route: %v", err)
	}

	// 复制主 channel 的渠道成本价形状（DEC-026：渠道只录成本），价格单元/币种/费率保持一致
	// （这只是黑盒 fixture，业务无关；只要 settle 时能命中该渠道成本即可）。
	fallbackCostPrice, err := f.Queries.CreateChannelPrice(f.ctx, sqlc.CreateChannelPriceParams{
		ChannelID:           fallbackID,
		ModelID:             f.ModelDBID,
		Currency:            "USD",
		PricingUnit:         billing.PricingUnitPer1MTokens,
		UncachedInputCost:   numericMinor(1_0000000000),
		OutputCost:          numericMinor(4_0000000000),
		CacheReadInputCost:  numericMinor(0_2500000000),
		ReasoningOutputCost: numericMinor(6_0000000000),
		Status:              "enabled",
		EffectiveFrom:       pgtype.Timestamptz{Time: time.Now().Add(-time.Hour), Valid: true},
		EffectiveTo:         pgtype.Timestamptz{Valid: false},
	})
	if err != nil {
		t.Fatalf("insert fallback channel cost price: %v", err)
	}
	f.fallbackChannelPriceIDs = append(f.fallbackChannelPriceIDs, fallbackCostPrice.ID)

	// 生产 Admin 创建 Origin/Channel 时会同步初始化 Redis control。fixture 在 Gateway 启动后
	// 直接写 DB，也必须完成同一动作；否则 P4 fail-closed 会把刚加入的 fallback 排除到下一次
	// 后台 reconciler 扫描之后，测试无法验证即时 fallback。
	if _, err := f.breakerStore.InitOriginControl(f.ctx, fallbackOriginID, 1, 1, "enabled"); err != nil {
		t.Fatalf("initialize fallback origin runtime control: %v", err)
	}
	fallbackChannel, err := f.Queries.GetChannel(f.ctx, fallbackID)
	if err != nil {
		t.Fatalf("read fallback channel for runtime control: %v", err)
	}
	payload, err := adminchannel.CanonicalAdmissionLimitsPayloadFromChannel(fallbackChannel)
	if err != nil {
		t.Fatalf("encode fallback channel admission control: %v", err)
	}
	target := f.breakerStore.ChannelAdmissionControl(fallbackID)
	if _, err := f.breakerStore.RestoreMissingControl(
		f.ctx, target, fallbackChannel.AdmissionLimitsRevision, payload,
	); err != nil {
		t.Fatalf("initialize fallback channel admission control: %v", err)
	}
	control, err := f.breakerStore.ReadControl(f.ctx, target, fallbackChannel.AdmissionLimitsRevision)
	if err != nil {
		t.Fatalf("verify fallback channel admission control: %v", err)
	}
	if control.SyncState != "active" || control.PendingRevision != 0 ||
		control.ActiveRevision != fallbackChannel.AdmissionLimitsRevision || control.ActivePayload != payload {
		t.Fatalf("fallback channel admission control is not active: %+v", control)
	}

	f.fallbackChannelIDs = append(f.fallbackChannelIDs, fallbackID)
	return fallbackID
}

// teardown 清理 DB 测试数据（按外键反序删除），关闭 Redis 与 pool。
func (f *Fixture) teardown(t *testing.T) {
	t.Helper()
	ctx := context.Background()
	deleteRows := func(label, query string, args ...any) {
		t.Helper()
		if _, err := f.Pool.Exec(ctx, query, args...); err != nil {
			t.Errorf("cleanup %s: %v", label, err)
		}
	}

	if f.APIKeyID != 0 {
		deleteRows("billing exceptions", `DELETE FROM ledger_billing_exceptions WHERE user_id = $1`, f.UserID)
		deleteRows("settlement recovery jobs", `DELETE FROM settlement_recovery_jobs WHERE request_record_id IN (SELECT id FROM request_records WHERE user_id = $1)`, f.UserID)
		deleteRows("cost exposures", `DELETE FROM channel_cost_exposures WHERE request_record_id IN (SELECT id FROM request_records WHERE user_id = $1)`, f.UserID)
		deleteRows("ledger reservations", `DELETE FROM ledger_reservations WHERE user_id = $1`, f.UserID)
		deleteRows("ledger entries", `DELETE FROM ledger_entries WHERE user_id = $1`, f.UserID)
		deleteRows("cost snapshots", `DELETE FROM cost_snapshots WHERE request_record_id IN (SELECT id FROM request_records WHERE user_id = $1)`, f.UserID)
		deleteRows("price snapshots", `DELETE FROM price_snapshots WHERE request_record_id IN (SELECT id FROM request_records WHERE user_id = $1)`, f.UserID)
		deleteRows("usage line items", `DELETE FROM usage_line_items WHERE usage_record_id IN (SELECT id FROM usage_records WHERE request_record_id IN (SELECT id FROM request_records WHERE user_id = $1))`, f.UserID)
		deleteRows("usage records", `DELETE FROM usage_records WHERE request_record_id IN (SELECT id FROM request_records WHERE user_id = $1)`, f.UserID)
		deleteRows("request attempts", `DELETE FROM request_attempts WHERE request_record_id IN (SELECT id FROM request_records WHERE user_id = $1)`, f.UserID)
		deleteRows("request records", `DELETE FROM request_records WHERE user_id = $1`, f.UserID)
		deleteRows("user balances", `DELETE FROM user_balances WHERE user_id = $1`, f.UserID)
		deleteRows("API key", `DELETE FROM api_keys WHERE id = $1`, f.APIKeyID)
	}
	if f.RouteID != 0 {
		// 线路被 api_keys.route_id 外键引用，必须在 api_keys 删除后再删。
		deleteRows("route", `DELETE FROM routes WHERE id = $1`, f.RouteID)
	}
	if f.ChannelPriceID != 0 {
		deleteRows("channel price", `DELETE FROM channel_prices WHERE id = $1`, f.ChannelPriceID)
	}
	if f.ModelPriceID != 0 {
		deleteRows("model price", `DELETE FROM model_prices WHERE id = $1`, f.ModelPriceID)
	}
	for _, fbCostID := range f.fallbackChannelPriceIDs {
		deleteRows("fallback channel price", `DELETE FROM channel_prices WHERE id = $1`, fbCostID)
	}
	for _, fbID := range f.fallbackChannelIDs {
		deleteRows("fallback channel model", `DELETE FROM channel_models WHERE channel_id = $1`, fbID)
		deleteRows("fallback runtime-control operations", `DELETE FROM runtime_control_operations WHERE channel_id = $1`, fbID)
		deleteRows("fallback channel", `DELETE FROM channels WHERE id = $1`, fbID)
	}
	if f.ChannelID != 0 && f.ModelDBID != 0 {
		deleteRows("channel model", `DELETE FROM channel_models WHERE channel_id = $1 AND model_id = $2`, f.ChannelID, f.ModelDBID)
	}
	if f.ChannelID != 0 {
		deleteRows("runtime-control operations", `DELETE FROM runtime_control_operations WHERE channel_id = $1`, f.ChannelID)
		deleteRows("channel", `DELETE FROM channels WHERE id = $1`, f.ChannelID)
	}
	if f.ProviderID != 0 {
		// P4 §4.2：channels 已删，先删该 Provider 下的 ProviderOrigin 再删 Provider（外键反序）。
		deleteRows("origin routing operations", `DELETE FROM origin_routing_operations WHERE provider_id = $1`, f.ProviderID)
		deleteRows("provider origins", `DELETE FROM provider_origins WHERE provider_id = $1`, f.ProviderID)
		deleteRows("provider", `DELETE FROM providers WHERE id = $1`, f.ProviderID)
	}
	if f.ModelDBID != 0 {
		deleteRows("model", `DELETE FROM models WHERE id = $1`, f.ModelDBID)
	}
	if f.UserID != 0 {
		deleteRows("user", `DELETE FROM users WHERE id = $1`, f.UserID)
	}

	if f.redisClient != nil {
		_ = f.redisClient.Close()
	}
	if f.Pool != nil {
		f.Pool.Close()
	}
	if f.cancel != nil {
		f.cancel()
	}
}

// seed 插入 fixture 所需的全部业务数据。
func (f *Fixture) seed(t *testing.T, opts SetupOptions, upstreamBaseURL string, upstreamAPIKey string) {
	t.Helper()

	suffix := f.suffix

	// user / api key
	user, err := f.Queries.CreateUser(f.ctx, sqlc.CreateUserParams{
		Email:        fmt.Sprintf("blackbox-%d@example.test", suffix),
		PasswordHash: "blackbox-password-hash",
		DisplayName:  "Blackbox SDK Test User",
	})
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	f.UserID = user.ID

	generatedKey, err := apikey.Generate()
	if err != nil {
		t.Fatalf("generate api key: %v", err)
	}

	// 线路必填：先建线路供 API Key 绑定，渠道创建后再显式加入线路池。
	route, err := f.Queries.CreateRoute(f.ctx, sqlc.CreateRouteParams{
		Name:       fmt.Sprintf("blackbox-route-%d", suffix),
		Mode:       "balanced",
		Status:     "enabled",
		PriceRatio: pgtype.Numeric{Int: big.NewInt(1), Exp: 0, Valid: true},
	})
	if err != nil {
		t.Fatalf("create route: %v", err)
	}
	f.RouteID = route.ID

	storedKey, err := f.Queries.CreateAPIKey(f.ctx, sqlc.CreateAPIKeyParams{
		UserID:    user.ID,
		Name:      "blackbox sdk key",
		KeyPrefix: generatedKey.Prefix,
		KeyHash:   generatedKey.Hash,
		ExpiresAt: pgtype.Timestamptz{Valid: false},
		RouteID:   route.ID,
	})
	if err != nil {
		t.Fatalf("create api key: %v", err)
	}
	f.APIKeyID = storedKey.ID
	f.APIKey = generatedKey.Plaintext

	// provider / channel / model / channel_model
	providerSlug := fmt.Sprintf("blackbox-provider-%d", suffix)
	var providerID int64
	if err := f.Pool.QueryRow(f.ctx, `
		INSERT INTO providers (slug, name, status)
		VALUES ($1, $2, 'enabled')
		RETURNING id
	`, providerSlug, providerSlug).Scan(&providerID); err != nil {
		t.Fatalf("insert provider: %v", err)
	}
	f.ProviderID = providerID

	// P4 §4.4：base_url 归属 ProviderOrigin；主 channel 建一个同 Provider 下的 enabled Origin。
	var originID int64
	if err := f.Pool.QueryRow(f.ctx, `
		INSERT INTO provider_origins (provider_id, name, base_url, status)
		VALUES ($1, $2, $3, 'enabled')
		RETURNING id
	`, providerID, fmt.Sprintf("blackbox-ep-%d", suffix), upstreamBaseURL).Scan(&originID); err != nil {
		t.Fatalf("insert provider origin: %v", err)
	}

	var channelID int64
	if err := f.Pool.QueryRow(f.ctx, `
		INSERT INTO channels (provider_id, provider_origin_id, name, protocol, adapter_key, credential, status, priority, timeout_ms)
		VALUES ($1, $2, $3, $4, $5, $6, 'enabled', 10, $7)
		RETURNING id
	`, providerID, originID, fmt.Sprintf("blackbox-channel-%d", suffix), opts.Protocol, opts.AdapterKey, upstreamAPIKey, opts.ChannelTimeoutMS).Scan(&channelID); err != nil {
		t.Fatalf("insert channel: %v", err)
	}
	f.ChannelID = channelID
	if err := f.Queries.AddRouteChannel(f.ctx, sqlc.AddRouteChannelParams{RouteID: route.ID, ChannelID: channelID}); err != nil {
		t.Fatalf("bind channel to route: %v", err)
	}

	var modelDBID int64
	if err := f.Pool.QueryRow(f.ctx, `
		INSERT INTO models (model_id, display_name, owned_by, status)
		VALUES ($1, $2, $3, 'enabled')
		RETURNING id
	`, opts.ModelID, opts.ModelID, opts.AdapterKey).Scan(&modelDBID); err != nil {
		t.Fatalf("insert model: %v", err)
	}
	f.ModelDBID = modelDBID

	if _, err := f.Pool.Exec(f.ctx, `
		INSERT INTO channel_models (channel_id, model_id, upstream_model, status)
		VALUES ($1, $2, $3, 'enabled')
	`, channelID, modelDBID, opts.UpstreamModel); err != nil {
		t.Fatalf("insert channel_model: %v", err)
	}

	// DEC-026：模型基准售价(model_prices) + 渠道成本价(channel_prices)。
	// 客户最终售价 = 基准价 × 线路倍率；单价采用 per_1m_tokens NUMERIC(20,10) 表达。
	modelPrice, err := f.Queries.CreateModelPrice(f.ctx, sqlc.CreateModelPriceParams{
		ModelID:              modelDBID,
		Currency:             "USD",
		PricingUnit:          billing.PricingUnitPer1MTokens,
		UncachedInputPrice:   numericMinor(2_0000000000),
		OutputPrice:          numericMinor(8_0000000000),
		CacheReadInputPrice:  numericMinor(0_5000000000),
		ReasoningOutputPrice: numericMinor(12_0000000000),
		Status:               "enabled",
		EffectiveFrom:        pgtype.Timestamptz{Time: time.Now().Add(-time.Hour), Valid: true},
		EffectiveTo:          pgtype.Timestamptz{Valid: false},
	})
	if err != nil {
		t.Fatalf("create model price: %v", err)
	}
	f.ModelPriceID = modelPrice.ID

	costPrice, err := f.Queries.CreateChannelPrice(f.ctx, sqlc.CreateChannelPriceParams{
		ChannelID:           channelID,
		ModelID:             modelDBID,
		Currency:            "USD",
		PricingUnit:         billing.PricingUnitPer1MTokens,
		UncachedInputCost:   numericMinor(1_0000000000),
		OutputCost:          numericMinor(4_0000000000),
		CacheReadInputCost:  numericMinor(0_2500000000),
		ReasoningOutputCost: numericMinor(6_0000000000),
		Status:              "enabled",
		EffectiveFrom:       pgtype.Timestamptz{Time: time.Now().Add(-time.Hour), Valid: true},
		EffectiveTo:         pgtype.Timestamptz{Valid: false},
	})
	if err != nil {
		t.Fatalf("create channel cost price: %v", err)
	}
	f.ChannelPriceID = costPrice.ID

	// 余额：先 ensure 行，再加 N USD（NUMERIC，per_1m_tokens 同精度）。
	if err := f.Queries.EnsureUserBalance(f.ctx, sqlc.EnsureUserBalanceParams{
		UserID:   user.ID,
		Currency: "USD",
	}); err != nil {
		t.Fatalf("ensure user balance: %v", err)
	}
	if _, err := f.Queries.AddUserBalance(f.ctx, sqlc.AddUserBalanceParams{
		Amount:   numericMinor(opts.InitialBalanceUSD * 1_0000000000),
		UserID:   user.ID,
		Currency: "USD",
	}); err != nil {
		t.Fatalf("add user balance: %v", err)
	}
}

// blackboxConfig 返回 fixture 使用的最小可用 config。
// 限流/熔断已迁移为运行时配置,由 seedRuntimeSettings 写入 app_settings(见 Setup)。
func blackboxConfig() config.Config {
	redisNamespace := os.Getenv("REDIS_KEY_NAMESPACE")
	if redisNamespace == "" {
		redisNamespace = defaultRedisNamespace
	}

	return config.Config{
		Redis: config.RedisConfig{
			KeyNamespace: redisNamespace,
		},
		Worker: config.WorkerConfig{
			StartupTimeout:                  5 * time.Second,
			RunnerIdleInterval:              time.Second,
			SettlementRecoveryLockTTL:       30 * time.Second,
			SettlementRecoveryInitialDelay:  30 * time.Second,
			SettlementRecoverySettleTimeout: 10 * time.Second,
		},
	}
}

// seedRuntimeSettings 在 gateway app 构建前写入黑盒需要的运行时配置:
//   - 限流默认 RPM=10000；P4 固定 fail-closed，不再写入已删除的 fail_open 字段；
//   - 熔断关闭,黑盒不验熔断(熔断有专门单测);
//   - 429 冷却关闭:对齐迁移前 blackboxConfig 未设 Gateway 配置的零值行为——SDK 对 429 会自动
//     重试,若冷却开启,重试会因唯一渠道在冷却中而拿到 503,破坏「上游 429→客户 429」断言
//     (冷却行为有专门单测)。
//
// 隔离原则:**只写配置的 Redis 命名空间,绝不写 DB**。app_settings 表是全局单行,
// DATABASE_URL 常指向开发库,若经 SettingsStore.Set 写透 DB 会把黑盒专用值(如熔断关闭)持久
// 覆盖运维真实配置。SettingsStore 读序为 本地→Redis→DB,黑盒命名空间的 Redis 命中即生效,
// 与 dev(unio:dev)互不可见;gateway 启动初值与 applier 周期读取走同一路径,行为一致。
func (f *Fixture) seedRuntimeSettings(t *testing.T, cfg config.Config) {
	t.Helper()

	values := map[string]string{
		appsettings.GatewayRouteRateLimitDefaultsKey:   `{"rpm":10000,"tpm":0,"rpd":0}`,
		appsettings.GatewayChannelRateLimitDefaultsKey: `{"rpm":10000,"tpm":0,"rpd":0}`,
		appsettings.GatewayCircuitBreakerKey:           `{"enabled":false,"window_ms":30000,"min_requests":20,"failure_ratio":0.5,"consecutive_failures":3,"consecutive_window_ms":10000,"half_open_successes":2,"attempt_permit_ttl_ms":30000,"attempt_permit_renew_interval_ms":10000,"attempt_permit_terminal_ttl_ms":300000,"origin_base_url_revision_endpoint_ttl_ms":86400000,"origin_status_revision_endpoint_ttl_ms":86400000,"origin_status_batch_max":256,"open_durations_ms":[15000,30000,60000,120000,300000],"origin_ambiguous_distinct_channels":2,"origin_ambiguous_distinct_models":2}`,
		appsettings.GatewayChannelCooldownKey:          `{"cooldown_ms":0,"cap_ms":0}`,
		appsettings.GatewayRoutingTraceKey:             `{"sample_rate":1,"retention_days":7,"cleanup_batch_size":500,"cleanup_interval_ms":3600000}`,
	}
	registry := appsettings.DefaultRegistry()
	for key, value := range values {
		def, ok := registry.Get(key)
		if !ok {
			t.Fatalf("seed runtime settings: key %q not registered", key)
		}
		// 用注册表校验器把关,避免黑盒 fixture 里的手写 JSON 与 codec 脱节。
		if err := def.Validate(json.RawMessage(value)); err != nil {
			t.Fatalf("seed runtime settings: %s invalid: %v", key, err)
		}
		redisKey := fmt.Sprintf("%s:settings:%s", cfg.Redis.KeyNamespace, key)
		if err := f.redisClient.Set(f.ctx, redisKey, value, 0).Err(); err != nil {
			t.Fatalf("seed runtime settings: redis set %s: %v", key, err)
		}
	}
}

// numericMinor 构造 NUMERIC(20,10) 的最小整数表示（exp=-10）。
// 例如 numericMinor(2_0000000000) 表示十进制 2。
func numericMinor(units int64) pgtype.Numeric {
	return pgtype.Numeric{Int: big.NewInt(units), Exp: -10, Valid: true}
}
