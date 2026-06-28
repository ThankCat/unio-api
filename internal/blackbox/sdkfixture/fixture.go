//go:build blackbox

// Package sdkfixture 提供双协议 SDK 黑盒验收使用的共享 fixture：
// 装一个完整的 unio gateway HTTP server (httptest) + 真实 PostgreSQL + Redis + 一份
// 可用的 user / project / api key / channel / model / price / balance / credential。
//
// 客户 SDK 只需要把 base_url 指到 Fixture.BaseURL，把 Authorization 设成 Fixture.APIKey，
// 就能像调真实 OpenAI / Anthropic 一样调 Unio Gateway。
//
// 仅在 -tags=blackbox 下编译。普通 go build / go test 不会引入 openai-go / anthropic-sdk
// 等第三方 SDK，生产二进制无 SDK 残留。
package sdkfixture

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"math/big"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"

	"github.com/ThankCat/unio-api/internal/bootstrap"
	"github.com/ThankCat/unio-api/internal/core/apikey"
	"github.com/ThankCat/unio-api/internal/core/billing"
	"github.com/ThankCat/unio-api/internal/core/credential"
	"github.com/ThankCat/unio-api/internal/platform/config"
	"github.com/ThankCat/unio-api/internal/platform/store/sqlc"
)

// 默认值。
const (
	defaultRedisAddr      = "localhost:6380"
	defaultRedisNamespace = "unio:blackbox"
)

// UpstreamMode 选择 fixture 上游模式。
type UpstreamMode int

const (
	// UpstreamReal 使用真实 DeepSeek 上游。
	//
	// 前置 env：
	//   - DEEPSEEK_BLACKBOX=1
	//   - DEEPSEEK_API_KEY=<可用 key>
	//
	// 任一缺失即 t.Skip，与 adapter 层 DS-OAI / DS-ANT 黑盒约定一致。
	UpstreamReal UpstreamMode = iota

	// UpstreamMock 把 channel.base_url 指向调用方传入的 httptest mock server，
	// 用于错误映射、fallback、Drop 字段、边界等不依赖真实上游的用例。
	UpstreamMock
)

// SetupOptions 定义 fixture 装配选项。
type SetupOptions struct {
	Mode UpstreamMode

	// UpstreamBaseURL 当 Mode=UpstreamMock 时必填，例如 mockServer.URL + "/v1"。
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

	UserID    int64
	ProjectID int64
	APIKeyID  int64

	ProviderID  int64
	ChannelID   int64
	ModelDBID   int64
	PriceID     int64
	CostPriceID int64

	ModelID       string
	UpstreamModel string

	ctx                  context.Context
	cancel               context.CancelFunc
	redisClient          redis.UniversalClient
	suffix               int64
	fallbackChannelIDs   []int64
	fallbackCostPriceIDs []int64
}

// Setup 装一个 unio gateway 黑盒 fixture。
// 前置 env 缺失会 t.Skip；teardown 已通过 t.Cleanup 注册。
func Setup(t *testing.T, opts SetupOptions) *Fixture {
	t.Helper()

	databaseURL := os.Getenv("DATABASE_URL")
	if databaseURL == "" {
		t.Skip("DATABASE_URL is not set")
	}

	if opts.Mode == UpstreamReal {
		if os.Getenv("DEEPSEEK_BLACKBOX") != "1" {
			t.Skip("DEEPSEEK_BLACKBOX is not set to 1")
		}
		if os.Getenv("DEEPSEEK_API_KEY") == "" {
			t.Skip("DEEPSEEK_API_KEY is not set")
		}
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
			// DeepSeek 的 Anthropic endpoint 任意 model 名都回 deepseek-v4-flash，
			// 直接用 deepseek 名作客户可见 ID，避免引入"任意 claude 名 → mapping"复杂度。
			opts.ModelID = "deepseek-v4-flash"
		} else {
			opts.ModelID = "deepseek-v4-flash"
		}
	}
	if opts.UpstreamModel == "" {
		opts.UpstreamModel = opts.ModelID
	}
	upstreamAPIKey := opts.UpstreamAPIKey
	if upstreamAPIKey == "" {
		if opts.Mode == UpstreamReal {
			upstreamAPIKey = os.Getenv("DEEPSEEK_API_KEY")
		} else {
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

	upstreamBaseURL := opts.UpstreamBaseURL
	if opts.Mode == UpstreamReal {
		switch opts.Protocol {
		case "anthropic":
			// adapter 拼 <base>/v1/messages → https://api.deepseek.com/anthropic/v1/messages。
			upstreamBaseURL = "https://api.deepseek.com/anthropic"
		default:
			upstreamBaseURL = "https://api.deepseek.com/v1"
		}
	}

	f.seed(t, opts, upstreamBaseURL, upstreamAPIKey)

	cfg := blackboxConfig()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	app, err := bootstrap.NewGatewayServerApp(ctx, bootstrap.GatewayServerAppDeps{
		Logger: logger,
		Config: cfg,
		DB:     pool,
		Redis:  redisClient,
	})
	if err != nil {
		f.teardown(t)
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

	return f
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

	credentialEncrypted, err := credential.EncryptFixedTestCredential("sk-fallback-test")
	if err != nil {
		t.Fatalf("encrypt fallback channel credential: %v", err)
	}

	var fallbackID int64
	if err := f.Pool.QueryRow(f.ctx, `
		INSERT INTO channels (provider_id, name, protocol, adapter_key, base_url, credential_encrypted, status, priority, timeout_ms)
		VALUES ($1, $2, (SELECT protocol FROM channels WHERE id = $3), (SELECT adapter_key FROM channels WHERE id = $3), $4, $5, 'enabled', $6, 60000)
		RETURNING id
	`, f.ProviderID, fmt.Sprintf("blackbox-fallback-%d", f.suffix), f.ChannelID, baseURL, credentialEncrypted, priority).Scan(&fallbackID); err != nil {
		t.Fatalf("insert fallback channel: %v", err)
	}

	if _, err := f.Pool.Exec(f.ctx, `
		INSERT INTO channel_models (channel_id, model_id, upstream_model, status)
		VALUES ($1, $2, $3, 'enabled')
	`, fallbackID, f.ModelDBID, f.UpstreamModel); err != nil {
		t.Fatalf("insert fallback channel_model: %v", err)
	}

	// 复制主 channel 的 cost_price 形状，价格单元/币种/费率保持一致（这只是黑盒 fixture，
	// 业务无关；只要 FindActiveChannelCostPrice 在 settle 时能命中即可）。
	fallbackCostPrice, err := f.Queries.CreateChannelCostPrice(f.ctx, sqlc.CreateChannelCostPriceParams{
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
	f.fallbackCostPriceIDs = append(f.fallbackCostPriceIDs, fallbackCostPrice.ID)

	f.fallbackChannelIDs = append(f.fallbackChannelIDs, fallbackID)
	return fallbackID
}

// teardown 清理 DB 测试数据（按外键反序删除），关闭 Redis 与 pool。
func (f *Fixture) teardown(t *testing.T) {
	ctx := context.Background()

	if f.APIKeyID != 0 {
		_, _ = f.Pool.Exec(ctx, `DELETE FROM ledger_billing_exceptions WHERE user_id = $1`, f.UserID)
		_, _ = f.Pool.Exec(ctx, `DELETE FROM ledger_reservations WHERE user_id = $1`, f.UserID)
		_, _ = f.Pool.Exec(ctx, `DELETE FROM ledger_entries WHERE user_id = $1`, f.UserID)
		_, _ = f.Pool.Exec(ctx, `DELETE FROM settlement_recovery_jobs WHERE request_record_id IN (SELECT id FROM request_records WHERE user_id = $1)`, f.UserID)
		_, _ = f.Pool.Exec(ctx, `DELETE FROM cost_snapshots WHERE request_record_id IN (SELECT id FROM request_records WHERE user_id = $1)`, f.UserID)
		_, _ = f.Pool.Exec(ctx, `DELETE FROM price_snapshots WHERE request_record_id IN (SELECT id FROM request_records WHERE user_id = $1)`, f.UserID)
		_, _ = f.Pool.Exec(ctx, `DELETE FROM usage_line_items WHERE request_record_id IN (SELECT id FROM request_records WHERE user_id = $1)`, f.UserID)
		_, _ = f.Pool.Exec(ctx, `DELETE FROM usage_records WHERE request_record_id IN (SELECT id FROM request_records WHERE user_id = $1)`, f.UserID)
		_, _ = f.Pool.Exec(ctx, `DELETE FROM request_attempts WHERE request_record_id IN (SELECT id FROM request_records WHERE user_id = $1)`, f.UserID)
		_, _ = f.Pool.Exec(ctx, `DELETE FROM request_records WHERE user_id = $1`, f.UserID)
		_, _ = f.Pool.Exec(ctx, `DELETE FROM user_balances WHERE user_id = $1`, f.UserID)
		_, _ = f.Pool.Exec(ctx, `DELETE FROM api_keys WHERE id = $1`, f.APIKeyID)
	}
	if f.CostPriceID != 0 {
		_, _ = f.Pool.Exec(ctx, `DELETE FROM channel_cost_prices WHERE id = $1`, f.CostPriceID)
	}
	if f.PriceID != 0 {
		_, _ = f.Pool.Exec(ctx, `DELETE FROM prices WHERE id = $1`, f.PriceID)
	}
	for _, fbCostID := range f.fallbackCostPriceIDs {
		_, _ = f.Pool.Exec(ctx, `DELETE FROM channel_cost_prices WHERE id = $1`, fbCostID)
	}
	for _, fbID := range f.fallbackChannelIDs {
		_, _ = f.Pool.Exec(ctx, `DELETE FROM channel_models WHERE channel_id = $1`, fbID)
		_, _ = f.Pool.Exec(ctx, `DELETE FROM channels WHERE id = $1`, fbID)
	}
	if f.ChannelID != 0 && f.ModelDBID != 0 {
		_, _ = f.Pool.Exec(ctx, `DELETE FROM channel_models WHERE channel_id = $1 AND model_id = $2`, f.ChannelID, f.ModelDBID)
	}
	if f.ChannelID != 0 {
		_, _ = f.Pool.Exec(ctx, `DELETE FROM channels WHERE id = $1`, f.ChannelID)
	}
	if f.ProviderID != 0 {
		_, _ = f.Pool.Exec(ctx, `DELETE FROM providers WHERE id = $1`, f.ProviderID)
	}
	if f.ModelDBID != 0 {
		_, _ = f.Pool.Exec(ctx, `DELETE FROM models WHERE id = $1`, f.ModelDBID)
	}
	if f.ProjectID != 0 {
		_, _ = f.Pool.Exec(ctx, `DELETE FROM projects WHERE id = $1`, f.ProjectID)
	}
	if f.UserID != 0 {
		_, _ = f.Pool.Exec(ctx, `DELETE FROM users WHERE id = $1`, f.UserID)
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

	// user / project / api key
	user, err := f.Queries.CreateUser(f.ctx, sqlc.CreateUserParams{
		Email:        fmt.Sprintf("blackbox-%d@example.test", suffix),
		PasswordHash: "blackbox-password-hash",
		DisplayName:  "Blackbox SDK Test User",
	})
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	f.UserID = user.ID

	project, err := f.Queries.CreateProject(f.ctx, sqlc.CreateProjectParams{
		UserID: user.ID,
		Name:   fmt.Sprintf("blackbox-project-%d", suffix),
	})
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	f.ProjectID = project.ID

	generatedKey, err := apikey.Generate()
	if err != nil {
		t.Fatalf("generate api key: %v", err)
	}
	storedKey, err := f.Queries.CreateAPIKey(f.ctx, sqlc.CreateAPIKeyParams{
		ProjectID: project.ID,
		Name:      "blackbox sdk key",
		KeyPrefix: generatedKey.Prefix,
		KeyHash:   generatedKey.Hash,
		ExpiresAt: pgtype.Timestamptz{Valid: false},
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

	credentialEncrypted, err := credential.EncryptFixedTestCredential(upstreamAPIKey)
	if err != nil {
		t.Fatalf("encrypt channel credential: %v", err)
	}

	var channelID int64
	if err := f.Pool.QueryRow(f.ctx, `
		INSERT INTO channels (provider_id, name, protocol, adapter_key, base_url, credential_encrypted, status, priority, timeout_ms)
		VALUES ($1, $2, $3, $4, $5, $6, 'enabled', 10, $7)
		RETURNING id
	`, providerID, fmt.Sprintf("blackbox-channel-%d", suffix), opts.Protocol, opts.AdapterKey, upstreamBaseURL, credentialEncrypted, opts.ChannelTimeoutMS).Scan(&channelID); err != nil {
		t.Fatalf("insert channel: %v", err)
	}
	f.ChannelID = channelID

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

	// 客户售价 / 平台成本价
	// 单价采用与 settlement_test 一致的 per_1m_tokens NUMERIC(20,10) 表达。
	price, err := f.Queries.CreatePrice(f.ctx, sqlc.CreatePriceParams{
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
		t.Fatalf("create price: %v", err)
	}
	f.PriceID = price.ID

	costPrice, err := f.Queries.CreateChannelCostPrice(f.ctx, sqlc.CreateChannelCostPriceParams{
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
	f.CostPriceID = costPrice.ID

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
//
// 重点：
//   - CredentialMasterKey 使用 credential.FixedTestMasterKeyBase64，与 seed 加密路径一致。
//   - RateLimit fail_open，避免偶发 Redis 抖动让黑盒挂掉。
//   - CircuitBreaker 关闭，黑盒不验熔断（熔断有专门单测）。
func blackboxConfig() config.Config {
	return config.Config{
		Credential: config.CredentialConfig{
			MasterKey: credential.FixedTestMasterKeyBase64,
		},
		Redis: config.RedisConfig{
			KeyNamespace: defaultRedisNamespace,
		},
		RateLimit: config.RateLimitConfig{
			DefaultRPM:    10000,
			FailurePolicy: "fail_open",
		},
		Worker: config.WorkerConfig{
			StartupTimeout:                  5 * time.Second,
			RunnerIdleInterval:              time.Second,
			SettlementRecoveryLockTTL:       30 * time.Second,
			SettlementRecoveryInitialDelay:  30 * time.Second,
			SettlementRecoverySettleTimeout: 10 * time.Second,
		},
		CircuitBreaker: config.CircuitBreakerConfig{
			Enabled: false,
		},
	}
}

// numericMinor 构造 NUMERIC(20,10) 的最小整数表示（exp=-10）。
// 例如 numericMinor(2_0000000000) 表示十进制 2。
func numericMinor(units int64) pgtype.Numeric {
	return pgtype.Numeric{Int: big.NewInt(units), Exp: -10, Valid: true}
}
