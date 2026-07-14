package config

import (
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/ThankCat/unio-api/internal/platform/failure"
)

// Config 保存服务启动所需的全部配置。
//
// 注意:限流全局默认、渠道熔断、流式 idle 超时、渠道 429 冷却、凭据 401 阈值、默认渠道超时
// 已迁移为运行时配置(app_settings,admin 后台可改、免重启生效),不再从 env 读取——
// 见 internal/service/appsettings 与 docs/production/DESIGN-env-to-runtime-settings-migration.md。
type Config struct {
	HTTP              HTTPConfig
	Log               LogConfig
	DB                DBConfig
	Redis             RedisConfig
	Worker            WorkerConfig
	Tracing           TracingConfig
	ModelCatalogSync  ModelCatalogSyncConfig
	ChannelTestWorker ChannelTestWorkerConfig
	Gateway           GatewayConfig
	Admin             AdminConfig
	Console           ConsoleConfig
	TokenEstimate     TokenEstimateConfig
}

// ChannelTestWorkerConfig 保存渠道自动检测 worker（阶段二）的配置。
//
// worker 周期性对所有启用渠道发一个最小合成 "hi" 探测，验证「连得上 + 凭据有效 + 模型可用」，
// 据此翻 channels.credential_valid（凭据失效自动摘除、检测通过自动恢复）并按 R1(b) 落检测日志。
// 探测复用 gateway 的 adapter 链路但不走计费/请求记录，故不污染统计、不给客户计费。
// 探测超时已迁移为运行时配置 admin_backend.channel_test_probe_timeout_ms（系统设置），不在此结构。
type ChannelTestWorkerConfig struct {
	// Enabled 来自 CHANNEL_TEST_WORKER_ENABLED（默认 true）。
	Enabled bool
	// Interval 来自 CHANNEL_TEST_WORKER_INTERVAL（默认 30m）：巡检间隔。
	Interval time.Duration
	// LogRetentionPerChannel 来自 CHANNEL_TEST_LOG_RETENTION_PER_CHANNEL（默认 200，须 > 0）：
	// 每渠道 channel_test_logs 保留最近 N 条，worker 每轮末尾清理更旧的。
	LogRetentionPerChannel int
}

// TokenEstimateConfig 保存输入 token 估算的媒体处理配置（对齐 new-api GetMediaToken 系列）。
//
// 输入 token 估算只对提取出的文本内容跑 tiktoken，图片走 tile/像素数学。这里控制图片估算的力度：
// CountMedia 关闭 → 图片按固定保守值（不解码/不抓取）；FetchRemoteImages 打开 → 抓取 http(s) URL
// 图片读取真实尺寸（内联 base64 图片始终本地解码，不受开关影响）。
type TokenEstimateConfig struct {
	// CountMedia 来自 TOKEN_ESTIMATE_COUNT_MEDIA（默认 true）。
	CountMedia bool
	// FetchRemoteImages 来自 TOKEN_ESTIMATE_FETCH_REMOTE_IMAGES（默认 false）。
	// 打开会在下单前抓取任意客户图片 URL，存在 SSRF/延迟风险，启用前应在网络层限制出站目标。
	FetchRemoteImages bool
	// FetchTimeout 来自 TOKEN_ESTIMATE_FETCH_TIMEOUT（默认 3s）；FetchMaxBytes 来自 TOKEN_ESTIMATE_FETCH_MAX_MB（默认 8MB）。
	FetchTimeout  time.Duration
	FetchMaxBytes int64
}

// GatewayConfig 保存 gateway-server 进程级配置。
type GatewayConfig struct {
	// HTTPAddr 来自 GATEWAY_HTTP_ADDR；gateway-server 的监听地址。
	HTTPAddr string

	// MaxOutputTokensFallback 来自 AUTHORIZATION_MAX_OUTPUT_TOKENS_FALLBACK（默认 4096）。
	// 客户未显式给出输出上限、且候选模型 models.max_output_tokens 也未配置(NULL)时，
	// authorization 用它做保守冻结的输出 token 兜底上界。仅影响预冻结额度，不改写转发上游的请求体。
	MaxOutputTokensFallback int64

	// PartialAssumedCacheReadRatio 来自 PARTIAL_ASSUMED_CACHE_READ_RATIO（默认 0.6，取值 [0,1]）。
	// 无上游真实 usage 的流式 partial 结算：按此比例把估算输入拆成 cache_read / uncached 计费
	// （临时口径，最优是按历史真实缓存率预估——见 lifecycle/partial_stream.go 的 TODO）。
	PartialAssumedCacheReadRatio float64

	// MaxUpstreamResponseBytes 来自 GATEWAY_MAX_UPSTREAM_RESPONSE_MB（默认 8MB，按 MB 换算）。
	// 这是非流式上游响应体的防 OOM 上界：异常/恶意上游可能对一次非流式请求返回任意大的 body，
	// 整体读入内存会撑爆进程。超限时 adapter 返回 adapter_response_too_large 并释放冻结，不计费。
	// 仅约束非流式 body；流式 SSE 单事件大小由 adapter 内部常量约束，与此无关。
	MaxUpstreamResponseBytes int64
}

// AdminConfig 保存 admin-server 进程级配置与管理端认证配置。
type AdminConfig struct {
	// HTTPAddr 来自 ADMIN_HTTP_ADDR；admin-server 的监听地址。
	HTTPAddr string
	// APIToken 来自 ADMIN_API_TOKEN；单管理员极简版的静态访问 token。
	// 空值表示未配置，运行 admin-server 时启动期失败。
	APIToken string
}

// ConsoleConfig 保存 console-server 进程级配置。
type ConsoleConfig struct {
	// HTTPAddr 来自 CONSOLE_HTTP_ADDR；console-server 的监听地址。
	HTTPAddr string
}

// ModelCatalogSyncConfig 保存 models.dev 模型目录同步参数；默认关闭（opt-in），
// 启用前须确认 docs/datasources/MODELS_DEV_LICENSE.md 的 license 与 attribution。
type ModelCatalogSyncConfig struct {
	// Enabled 控制 worker 是否调度 models.dev 每日同步。
	Enabled bool
	// BaseURL 是 models.dev 站点根地址，可指向镜像/测试桩。
	BaseURL string
	// Interval 是两次成功同步之间的最小间隔（默认 24h，等效每日）。
	Interval time.Duration
	// HTTPTimeout 是单次拉取的超时。
	HTTPTimeout time.Duration
	// MaxResponseBytes 限制单个响应体大小，防御异常大响应。
	MaxResponseBytes int64
}

// TracingConfig 保存 OpenTelemetry trace 导出配置；默认关闭（opt-in）。
type TracingConfig struct {
	Enabled     bool
	Endpoint    string
	Insecure    bool
	ServiceName string
	SampleRatio float64
}

// HTTPConfig 保存所有 HTTP server 共享的超时配置；监听地址按服务独立配置，
// 见 GatewayConfig / AdminConfig / ConsoleConfig 的 HTTPAddr。
type HTTPConfig struct {
	ReadTimeout     time.Duration
	WriteTimeout    time.Duration
	IdleTimeout     time.Duration
	ShutdownTimeout time.Duration

	// MaxJSONBodyBytes 是单个 JSON 请求体的最大字节数（由 HTTP_MAX_JSON_BODY_MB 按 MB 换算）。
	// 这是防 OOM / zip bomb 的网关安全配置，与业务计费无关；超限返回 413。前置代理
	// （Nginx client_max_body_size）须 ≥ 此值，否则请求仍会在代理层被 413 拒绝。
	MaxJSONBodyBytes int64
}

// LogConfig 保存结构化日志配置。
type LogConfig struct {
	Level slog.Level
}

// DBConfig 保存 PostgreSQL 连接配置。
type DBConfig struct {
	URL               string
	MaxConns          int32
	MinConns          int32
	MaxConnLifetime   time.Duration
	MaxConnIdleTime   time.Duration
	HealthCheckPeriod time.Duration
}

// RedisConfig 保存 Redis client 连接配置。
type RedisConfig struct {
	Addr            string
	Password        string
	DB              int
	DialTimeout     time.Duration
	ReadTimeout     time.Duration
	WriteTimeout    time.Duration
	PoolSize        int
	MaxRetries      int
	MinRetryBackoff time.Duration
	MaxRetryBackoff time.Duration
	KeyNamespace    string
}

// WorkerConfig 保存后台 worker 调度与 recovery 参数。
type WorkerConfig struct {
	StartupTimeout                  time.Duration
	RunnerIdleInterval              time.Duration
	SettlementRecoveryLockTTL       time.Duration
	SettlementRecoveryInitialDelay  time.Duration
	SettlementRecoverySettleTimeout time.Duration

	// SettlementRecoveryMaxAttempts 是单条 settlement 补偿任务的最大自动重试次数（写入 job.max_attempts）。
	// 与退避一起决定「上游已成功但 settlement 反复失败」时的总补偿覆盖窗口；耗尽后任务转 dead 并由 worker
	// 收口（释放冻结 + 记风险敞口 + 请求标 failed）。应足够大以覆盖依赖（DB/网络）短时抖动，避免过早放弃。
	SettlementRecoveryMaxAttempts int32
	// SettlementRecoveryBackoffCap 是补偿重试指数退避的单次上限。退避序列 1s,2s,4s,... 增长到该上限后保持平稳，
	// 用于在不过早 dead 的前提下把总覆盖窗口拉长到分钟~小时级（兜底较长依赖故障）。
	SettlementRecoveryBackoffCap time.Duration
	// SettlementRecoveryBatchSize 是补偿 worker 单轮最多 claim/处理的任务数（P2-5）。
	// 批量排空把每轮固定开销（dead 收口 + exhausted 标记扫描）摊薄到多条 job，积压时显著加快排空；
	// 每条仍以 FOR UPDATE SKIP LOCKED 独立 claim，可与多副本 worker 并存。
	SettlementRecoveryBatchSize int32

	// OrphanReservationSweepAgeThreshold 是孤儿预授权的判定年龄阈值：仅清扫 authorized 且 created_at 早于
	// now-阈值、请求仍 running、且无 settlement 补偿任务的预授权。阈值应明显大于单请求最长可能耗时，避免误清在途请求。
	OrphanReservationSweepAgeThreshold time.Duration
	// OrphanReservationSweepBatchSize 是单轮扫描收口的最大孤儿预授权条数。
	OrphanReservationSweepBatchSize int32
}

// Load 从环境变量加载配置，并对需要解析的字段做启动期校验。
func Load() (Config, error) {
	loadDotEnvIfNeeded()

	redisDB, err := getEnvInt("REDIS_DB", 0)
	if err != nil {
		return Config{}, err
	}

	redisDialTimeout, err := getEnvDuration("REDIS_DIAL_TIMEOUT", 5*time.Second)
	if err != nil {
		return Config{}, err
	}

	redisReadTimeout, err := getEnvDuration("REDIS_READ_TIMEOUT", 3*time.Second)
	if err != nil {
		return Config{}, err
	}

	redisWriteTimeout, err := getEnvDuration("REDIS_WRITE_TIMEOUT", 3*time.Second)
	if err != nil {
		return Config{}, err
	}

	redisPoolSize, err := getEnvInt("REDIS_POOL_SIZE", 10)
	if err != nil {
		return Config{}, err
	}

	redisMaxRetries, err := getEnvInt("REDIS_MAX_RETRIES", 3)
	if err != nil {
		return Config{}, err
	}

	redisMinRetryBackoff, err := getEnvDuration("REDIS_MIN_RETRY_BACKOFF", 8*time.Millisecond)
	if err != nil {
		return Config{}, err
	}

	redisMaxRetryBackoff, err := getEnvDuration("REDIS_MAX_RETRY_BACKOFF", 512*time.Millisecond)
	if err != nil {
		return Config{}, err
	}

	logLevel, err := parseLogLevel(getEnv("LOG_LEVEL", "info"))
	if err != nil {
		return Config{}, err
	}

	httpReadTimeout, err := getEnvDuration("HTTP_READ_TIMEOUT", 10*time.Second)
	if err != nil {
		return Config{}, err
	}

	httpWriteTimeout, err := getEnvDuration("HTTP_WRITE_TIMEOUT", 30*time.Second)
	if err != nil {
		return Config{}, err
	}

	httpIdleTimeout, err := getEnvDuration("HTTP_IDLE_TIMEOUT", 60*time.Second)
	if err != nil {
		return Config{}, err
	}

	httpShutdownTimeout, err := getEnvDuration("HTTP_SHUTDOWN_TIMEOUT", 10*time.Second)
	if err != nil {
		return Config{}, err
	}

	// 默认 128MB：对齐 new-api 的统一上限方向，覆盖 Codex 长会话 + tool 大 payload；按需在 env 调小。
	httpMaxJSONBodyMB, err := getEnvInt("HTTP_MAX_JSON_BODY_MB", 128)
	if err != nil {
		return Config{}, err
	}

	postgresMaxConns, err := getEnvInt32("POSTGRES_MAX_CONNS", 10)
	if err != nil {
		return Config{}, err
	}

	postgresMinConns, err := getEnvInt32("POSTGRES_MIN_CONNS", 1)
	if err != nil {
		return Config{}, err
	}

	postgresMaxConnLifetime, err := getEnvDuration("POSTGRES_MAX_CONN_LIFETIME", time.Hour)
	if err != nil {
		return Config{}, err
	}

	postgresMaxConnIdleTime, err := getEnvDuration("POSTGRES_MAX_CONN_IDLE_TIME", 30*time.Minute)
	if err != nil {
		return Config{}, err
	}

	postgresHealthCheckPeriod, err := getEnvDuration("POSTGRES_HEALTH_CHECK_PERIOD", 5*time.Second)
	if err != nil {
		return Config{}, err
	}

	workerRunnerIdleInterval, err := getEnvDuration("WORKER_RUNNER_IDLE_INTERVAL", time.Second)
	if err != nil {
		return Config{}, err
	}

	workerStartupTimeout, err := getEnvDuration("WORKER_STARTUP_TIMEOUT", 5*time.Second)
	if err != nil {
		return Config{}, err
	}

	workerSettlementRecoveryLockTTL, err := getEnvDuration("WORKER_SETTLEMENT_RECOVERY_LOCK_TTL", 30*time.Second)
	if err != nil {
		return Config{}, err
	}

	workerSettlementRecoveryInitialDelay, err := getEnvDuration("WORKER_SETTLEMENT_RECOVERY_INITIAL_DELAY", 30*time.Second)
	if err != nil {
		return Config{}, err
	}

	workerSettlementRecoverySettleTimeout, err := getEnvDuration("WORKER_SETTLEMENT_RECOVERY_SETTLE_TIMEOUT", 10*time.Second)
	if err != nil {
		return Config{}, err
	}

	workerSettlementRecoveryMaxAttempts, err := getEnvInt32("WORKER_SETTLEMENT_RECOVERY_MAX_ATTEMPTS", 20)
	if err != nil {
		return Config{}, err
	}
	if workerSettlementRecoveryMaxAttempts <= 0 {
		return Config{}, failure.New(
			failure.CodeConfigInvalid,
			failure.WithMessage("WORKER_SETTLEMENT_RECOVERY_MAX_ATTEMPTS must be a positive integer"),
		)
	}

	workerSettlementRecoveryBackoffCap, err := getEnvDuration("WORKER_SETTLEMENT_RECOVERY_BACKOFF_CAP", 5*time.Minute)
	if err != nil {
		return Config{}, err
	}
	if workerSettlementRecoveryBackoffCap <= 0 {
		return Config{}, failure.New(
			failure.CodeConfigInvalid,
			failure.WithMessage("WORKER_SETTLEMENT_RECOVERY_BACKOFF_CAP must be a positive duration"),
		)
	}

	workerSettlementRecoveryBatchSize, err := getEnvInt32("WORKER_SETTLEMENT_RECOVERY_BATCH_SIZE", 16)
	if err != nil {
		return Config{}, err
	}
	if workerSettlementRecoveryBatchSize <= 0 {
		return Config{}, failure.New(
			failure.CodeConfigInvalid,
			failure.WithMessage("WORKER_SETTLEMENT_RECOVERY_BATCH_SIZE must be a positive integer"),
		)
	}

	workerOrphanReservationSweepAgeThreshold, err := getEnvDuration("WORKER_ORPHAN_RESERVATION_SWEEP_AGE_THRESHOLD", 15*time.Minute)
	if err != nil {
		return Config{}, err
	}
	if workerOrphanReservationSweepAgeThreshold <= 0 {
		return Config{}, failure.New(
			failure.CodeConfigInvalid,
			failure.WithMessage("WORKER_ORPHAN_RESERVATION_SWEEP_AGE_THRESHOLD must be a positive duration"),
		)
	}

	workerOrphanReservationSweepBatchSize, err := getEnvInt32("WORKER_ORPHAN_RESERVATION_SWEEP_BATCH_SIZE", 100)
	if err != nil {
		return Config{}, err
	}
	if workerOrphanReservationSweepBatchSize <= 0 {
		return Config{}, failure.New(
			failure.CodeConfigInvalid,
			failure.WithMessage("WORKER_ORPHAN_RESERVATION_SWEEP_BATCH_SIZE must be a positive integer"),
		)
	}

	modelCatalogSyncEnabled, err := getEnvBool("MODEL_CATALOG_SYNC_ENABLED", false)
	if err != nil {
		return Config{}, err
	}

	modelCatalogSyncInterval, err := getEnvDuration("MODEL_CATALOG_SYNC_INTERVAL", 24*time.Hour)
	if err != nil {
		return Config{}, err
	}

	modelCatalogSyncHTTPTimeout, err := getEnvDuration("MODEL_CATALOG_SYNC_HTTP_TIMEOUT", 30*time.Second)
	if err != nil {
		return Config{}, err
	}

	modelCatalogSyncMaxResponseBytes, err := getEnvInt("MODEL_CATALOG_SYNC_MAX_RESPONSE_BYTES", 16<<20)
	if err != nil {
		return Config{}, err
	}

	channelTestWorkerEnabled, err := getEnvBool("CHANNEL_TEST_WORKER_ENABLED", true)
	if err != nil {
		return Config{}, err
	}

	channelTestWorkerInterval, err := getEnvDuration("CHANNEL_TEST_WORKER_INTERVAL", 30*time.Minute)
	if err != nil {
		return Config{}, err
	}

	channelTestLogRetentionPerChannel, err := getEnvInt("CHANNEL_TEST_LOG_RETENTION_PER_CHANNEL", 200)
	if err != nil {
		return Config{}, err
	}
	if channelTestLogRetentionPerChannel <= 0 {
		return Config{}, failure.New(failure.CodeConfigInvalid, failure.WithMessage("CHANNEL_TEST_LOG_RETENTION_PER_CHANNEL must be > 0"))
	}

	tracingEnabled, err := getEnvBool("OTEL_TRACING_ENABLED", false)
	if err != nil {
		return Config{}, err
	}

	tracingInsecure, err := getEnvBool("OTEL_EXPORTER_OTLP_INSECURE", true)
	if err != nil {
		return Config{}, err
	}

	tracingSampleRatio, err := getEnvFloat("OTEL_TRACES_SAMPLER_RATIO", 1.0)
	if err != nil {
		return Config{}, err
	}

	partialAssumedCacheReadRatio, err := getEnvFloat("PARTIAL_ASSUMED_CACHE_READ_RATIO", 0.6)
	if err != nil {
		return Config{}, err
	}
	if partialAssumedCacheReadRatio < 0 || partialAssumedCacheReadRatio > 1 {
		return Config{}, failure.New(
			failure.CodeConfigInvalid,
			failure.WithMessage("PARTIAL_ASSUMED_CACHE_READ_RATIO must be within [0, 1]"),
		)
	}

	authorizationMaxOutputTokensFallback, err := getEnvInt64("AUTHORIZATION_MAX_OUTPUT_TOKENS_FALLBACK", 4096)
	if err != nil {
		return Config{}, err
	}
	if authorizationMaxOutputTokensFallback <= 0 {
		return Config{}, failure.New(
			failure.CodeConfigInvalid,
			failure.WithMessage("AUTHORIZATION_MAX_OUTPUT_TOKENS_FALLBACK must be a positive integer"),
		)
	}

	gatewayMaxUpstreamResponseMB, err := getEnvInt("GATEWAY_MAX_UPSTREAM_RESPONSE_MB", 8)
	if err != nil {
		return Config{}, err
	}
	if gatewayMaxUpstreamResponseMB <= 0 {
		return Config{}, failure.New(
			failure.CodeConfigInvalid,
			failure.WithMessage("GATEWAY_MAX_UPSTREAM_RESPONSE_MB must be a positive integer"),
		)
	}

	tokenEstimateCountMedia, err := getEnvBool("TOKEN_ESTIMATE_COUNT_MEDIA", true)
	if err != nil {
		return Config{}, err
	}
	tokenEstimateFetchRemoteImages, err := getEnvBool("TOKEN_ESTIMATE_FETCH_REMOTE_IMAGES", false)
	if err != nil {
		return Config{}, err
	}
	tokenEstimateFetchTimeout, err := getEnvDuration("TOKEN_ESTIMATE_FETCH_TIMEOUT", 3*time.Second)
	if err != nil {
		return Config{}, err
	}
	if tokenEstimateFetchTimeout <= 0 {
		return Config{}, failure.New(
			failure.CodeConfigInvalid,
			failure.WithMessage("TOKEN_ESTIMATE_FETCH_TIMEOUT must be a positive duration"),
		)
	}
	tokenEstimateFetchMaxMB, err := getEnvInt("TOKEN_ESTIMATE_FETCH_MAX_MB", 8)
	if err != nil {
		return Config{}, err
	}
	if tokenEstimateFetchMaxMB <= 0 {
		return Config{}, failure.New(
			failure.CodeConfigInvalid,
			failure.WithMessage("TOKEN_ESTIMATE_FETCH_MAX_MB must be a positive integer"),
		)
	}

	return Config{
		HTTP: HTTPConfig{
			ReadTimeout:      httpReadTimeout,
			WriteTimeout:     httpWriteTimeout,
			IdleTimeout:      httpIdleTimeout,
			ShutdownTimeout:  httpShutdownTimeout,
			MaxJSONBodyBytes: int64(httpMaxJSONBodyMB) << 20,
		},
		Log: LogConfig{
			Level: logLevel,
		},
		DB: DBConfig{
			URL:               getEnv("DATABASE_URL", ""),
			MaxConns:          postgresMaxConns,
			MinConns:          postgresMinConns,
			MaxConnLifetime:   postgresMaxConnLifetime,
			MaxConnIdleTime:   postgresMaxConnIdleTime,
			HealthCheckPeriod: postgresHealthCheckPeriod,
		},
		Redis: RedisConfig{
			Addr:            getEnv("REDIS_ADDR", "localhost:6380"),
			Password:        getEnv("REDIS_PASSWORD", ""),
			DB:              redisDB,
			DialTimeout:     redisDialTimeout,
			ReadTimeout:     redisReadTimeout,
			WriteTimeout:    redisWriteTimeout,
			PoolSize:        redisPoolSize,
			MaxRetries:      redisMaxRetries,
			MinRetryBackoff: redisMinRetryBackoff,
			MaxRetryBackoff: redisMaxRetryBackoff,
			KeyNamespace:    getEnv("REDIS_KEY_NAMESPACE", "unio:dev"),
		},
		Worker: WorkerConfig{
			StartupTimeout:                     workerStartupTimeout,
			RunnerIdleInterval:                 workerRunnerIdleInterval,
			SettlementRecoveryLockTTL:          workerSettlementRecoveryLockTTL,
			SettlementRecoveryInitialDelay:     workerSettlementRecoveryInitialDelay,
			SettlementRecoverySettleTimeout:    workerSettlementRecoverySettleTimeout,
			SettlementRecoveryMaxAttempts:      workerSettlementRecoveryMaxAttempts,
			SettlementRecoveryBackoffCap:       workerSettlementRecoveryBackoffCap,
			SettlementRecoveryBatchSize:        workerSettlementRecoveryBatchSize,
			OrphanReservationSweepAgeThreshold: workerOrphanReservationSweepAgeThreshold,
			OrphanReservationSweepBatchSize:    workerOrphanReservationSweepBatchSize,
		},
		ModelCatalogSync: ModelCatalogSyncConfig{
			Enabled:          modelCatalogSyncEnabled,
			BaseURL:          getEnv("MODEL_CATALOG_SYNC_BASE_URL", "https://models.dev"),
			Interval:         modelCatalogSyncInterval,
			HTTPTimeout:      modelCatalogSyncHTTPTimeout,
			MaxResponseBytes: int64(modelCatalogSyncMaxResponseBytes),
		},
		ChannelTestWorker: ChannelTestWorkerConfig{
			Enabled:                channelTestWorkerEnabled,
			Interval:               channelTestWorkerInterval,
			LogRetentionPerChannel: channelTestLogRetentionPerChannel,
		},
		Tracing: TracingConfig{
			Enabled:     tracingEnabled,
			Endpoint:    getEnv("OTEL_EXPORTER_OTLP_ENDPOINT", ""),
			Insecure:    tracingInsecure,
			ServiceName: getEnv("OTEL_SERVICE_NAME", "unio-gateway"),
			SampleRatio: tracingSampleRatio,
		},
		Gateway: GatewayConfig{
			HTTPAddr:                     getEnv("GATEWAY_HTTP_ADDR", ":8520"),
			MaxOutputTokensFallback:      authorizationMaxOutputTokensFallback,
			PartialAssumedCacheReadRatio: partialAssumedCacheReadRatio,
			MaxUpstreamResponseBytes:     int64(gatewayMaxUpstreamResponseMB) << 20,
		},
		Admin: AdminConfig{
			HTTPAddr: getEnv("ADMIN_HTTP_ADDR", ":8521"),
			APIToken: getEnv("ADMIN_API_TOKEN", ""),
		},
		Console: ConsoleConfig{
			HTTPAddr: getEnv("CONSOLE_HTTP_ADDR", ":8522"),
		},
		TokenEstimate: TokenEstimateConfig{
			CountMedia:        tokenEstimateCountMedia,
			FetchRemoteImages: tokenEstimateFetchRemoteImages,
			FetchTimeout:      tokenEstimateFetchTimeout,
			FetchMaxBytes:     int64(tokenEstimateFetchMaxMB) << 20,
		},
	}, nil
}

// getEnv 读取字符串环境变量；未设置时返回 fallback。
func getEnv(key string, fallback string) string {
	value := os.Getenv(key)
	if value == "" {
		return fallback
	}
	return value
}

// getEnvInt 读取整数配置；格式错误时让启动流程尽早失败。
func getEnvInt(key string, fallback int) (int, error) {
	value := os.Getenv(key)
	if value == "" {
		return fallback, nil
	}

	n, err := strconv.Atoi(value)
	if err != nil {
		return 0, failure.Wrap(
			failure.CodeConfigInvalid,
			err,
			failure.WithMessage(fmt.Sprintf("parse %s as int", key)),
		)
	}

	return n, nil
}

// getEnvInt32 读取 int32 配置；格式或范围错误时让启动流程尽早失败。
func getEnvInt32(key string, fallback int32) (int32, error) {
	value := os.Getenv(key)
	if value == "" {
		return fallback, nil
	}

	n, err := strconv.ParseInt(value, 10, 32)
	if err != nil {
		return 0, failure.Wrap(
			failure.CodeConfigInvalid,
			err,
			failure.WithMessage(fmt.Sprintf("parse %s as int32", key)),
		)
	}

	return int32(n), nil
}

// getEnvInt64 读取 int64 配置；格式错误时让启动流程尽早失败。
func getEnvInt64(key string, fallback int64) (int64, error) {
	value := os.Getenv(key)
	if value == "" {
		return fallback, nil
	}

	n, err := strconv.ParseInt(value, 10, 64)
	if err != nil {
		return 0, failure.Wrap(
			failure.CodeConfigInvalid,
			err,
			failure.WithMessage(fmt.Sprintf("parse %s as int64", key)),
		)
	}

	return n, nil
}

// getEnvBool 读取布尔配置；格式错误时让启动流程尽早失败。
func getEnvBool(key string, fallback bool) (bool, error) {
	value := os.Getenv(key)
	if value == "" {
		return fallback, nil
	}

	b, err := strconv.ParseBool(value)
	if err != nil {
		return false, failure.Wrap(
			failure.CodeConfigInvalid,
			err,
			failure.WithMessage(fmt.Sprintf("parse %s as bool", key)),
		)
	}

	return b, nil
}

// getEnvFloat 读取浮点配置；格式错误时让启动流程尽早失败。
func getEnvFloat(key string, fallback float64) (float64, error) {
	value := os.Getenv(key)
	if value == "" {
		return fallback, nil
	}

	f, err := strconv.ParseFloat(value, 64)
	if err != nil {
		return 0, failure.Wrap(
			failure.CodeConfigInvalid,
			err,
			failure.WithMessage(fmt.Sprintf("parse %s as float", key)),
		)
	}

	return f, nil
}

// parseLogLevel 将环境变量中的日志级别转换为 slog.Level。
func parseLogLevel(value string) (slog.Level, error) {
	switch strings.ToLower(value) {
	case "", "info":
		return slog.LevelInfo, nil
	case "debug":
		return slog.LevelDebug, nil
	case "warn", "warning":
		return slog.LevelWarn, nil
	case "error":
		return slog.LevelError, nil
	default:
		return slog.LevelInfo, failure.New(
			failure.CodeConfigUnsupported,
			failure.WithMessage(fmt.Sprintf("parse LOG_LEVEL: unsupported level %q", value)),
		)
	}
}

// getEnvDuration 读取 duration 配置；格式错误时让启动流程尽早失败。
func getEnvDuration(key string, fallback time.Duration) (time.Duration, error) {
	value := os.Getenv(key)
	if value == "" {
		return fallback, nil
	}

	d, err := time.ParseDuration(value)
	if err != nil {
		return 0, failure.Wrap(
			failure.CodeConfigInvalid,
			err,
			failure.WithMessage(fmt.Sprintf("parse %s as duration", key)),
		)
	}

	return d, nil
}
