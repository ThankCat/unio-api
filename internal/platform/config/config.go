package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"go.uber.org/zap/zapcore"

	"github.com/ThankCat/unio-gateway/internal/platform/failure"
)

// Config 保存服务启动所需的全部配置。
//
// 注意:线路/渠道默认限流、渠道熔断、流式 idle 超时、渠道 429 冷却、凭据 401 阈值、默认渠道超时、
// 渠道自动巡检(开关/间隔/日志保留/探测超时)已迁移为运行时配置(app_settings,admin 后台可改、
// 免重启生效),不再从 env 读取——见 internal/service/appsettings 与 Unio Blueprint Gateway
// 运行控制文档。
type Config struct {
	HTTP             HTTPConfig
	Log              LogConfig
	GatewayLog       GatewayLogConfig
	DB               DBConfig
	Redis            RedisConfig
	Worker           WorkerConfig
	Tracing          TracingConfig
	ModelCatalogSync ModelCatalogSyncConfig
	Gateway          GatewayConfig
	Admin            AdminConfig
	Console          ConsoleConfig
	TokenEstimate    TokenEstimateConfig
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

	// InternalToken 来自 GATEWAY_INTERNAL_TOKEN；非空时挂载 /internal/v1/* 只读运维上游源站。
	// admin-server 用同一 token 拉取熔断快照；空表示关闭内部上游源站。
	InternalToken string

	// InstanceID 来自 GATEWAY_INSTANCE_ID；写入熔断快照的 instance 字段，便于多实例区分。
	// 空则内部 handler 回退为 hostname。
	InstanceID string

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

	// GatewayInternalURLs 来自 GATEWAY_INTERNAL_URLS（逗号分隔）；admin 拉取熔断快照的 gateway 基址列表。
	// 空且 InternalToken 非空时，若 GATEWAY_HTTP_ADDR 形如 ":port" 则默认 http://127.0.0.1:port。
	GatewayInternalURLs []string
	// GatewayInternalToken 来自 GATEWAY_INTERNAL_TOKEN（与 gateway 共用）；空则不拉取熔断快照。
	GatewayInternalToken string
	// LokiURL 来自 LOKI_URL；只供 admin-server 服务端查询，不暴露给浏览器。
	LokiURL string
}

// ConsoleConfig 保存 console-server 进程级配置。
type ConsoleConfig struct {
	// HTTPAddr 来自 CONSOLE_HTTP_ADDR；console-server 的监听地址。
	HTTPAddr string
}

// ModelCatalogSyncConfig 保存 models.dev 模型目录同步参数；默认关闭（opt-in），
// license 与 attribution 见仓库根目录 THIRD_PARTY_NOTICES.md。
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

	// GatewayMaxJSONBodyBytes / AdminMaxJSONBodyBytes 分别限制两个 ingress 的单个 JSON 请求体。
	// 这是防 OOM 的资源边界，与业务计费无关；超限返回 413。前置代理的 body 上限应与之匹配。
	GatewayMaxJSONBodyBytes int64
	AdminMaxJSONBodyBytes   int64
}

// 日志输出格式（LOG_FORMAT）。
const (
	LogFormatConsole = "console"
	LogFormatJSON    = "json"
)

// LogConfig 保存结构化日志配置。
type LogConfig struct {
	Level  zapcore.Level
	Format string // console | json
}

const (
	GatewayEnvironmentDevelopment = "development"
	GatewayEnvironmentTest        = "test"
	GatewayEnvironmentProduction  = "production"
)

// GatewayLogConfig 只控制 gateway-server。文件日志固定为 JSONL；控制台仅供本地开发。
type GatewayLogConfig struct {
	Environment    string
	BaselineLevel  zapcore.Level
	ConsoleEnabled bool
	FilePath       string
	MaxSizeMB      int
	MaxBackups     int
	MaxAgeDays     int
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

	logFormat, err := parseLogFormat(getEnv("LOG_FORMAT", LogFormatConsole))
	if err != nil {
		return Config{}, err
	}

	gatewayEnvironment, err := parseGatewayEnvironment(getEnv("GATEWAY_ENV", GatewayEnvironmentProduction))
	if err != nil {
		return Config{}, err
	}
	gatewayLogLevel, err := parseGatewayLogLevel(getEnv("GATEWAY_LOG_LEVEL", "info"))
	if err != nil {
		return Config{}, err
	}
	gatewayConsoleEnabled, err := getEnvBool(
		"GATEWAY_CONSOLE_ENABLED",
		gatewayEnvironment == GatewayEnvironmentDevelopment,
	)
	if err != nil {
		return Config{}, err
	}
	if gatewayEnvironment == GatewayEnvironmentProduction && gatewayLogLevel == zapcore.DebugLevel {
		return Config{}, failure.New(
			failure.CodeConfigInvalid,
			failure.WithMessage("GATEWAY_LOG_LEVEL=debug is forbidden when GATEWAY_ENV=production"),
		)
	}
	if gatewayEnvironment == GatewayEnvironmentProduction && gatewayConsoleEnabled {
		return Config{}, failure.New(
			failure.CodeConfigInvalid,
			failure.WithMessage("GATEWAY_CONSOLE_ENABLED=true is forbidden when GATEWAY_ENV=production"),
		)
	}
	gatewayLogMaxSizeDefault := 100
	if gatewayEnvironment == GatewayEnvironmentDevelopment {
		gatewayLogMaxSizeDefault = 25
	}
	gatewayLogMaxSizeMB, err := getEnvInt("GATEWAY_LOG_MAX_SIZE_MB", gatewayLogMaxSizeDefault)
	if err != nil {
		return Config{}, err
	}
	gatewayLogMaxBackups, err := getEnvInt("GATEWAY_LOG_MAX_BACKUPS", 20)
	if err != nil {
		return Config{}, err
	}
	gatewayLogMaxAgeDays, err := getEnvInt("GATEWAY_LOG_MAX_AGE_DAYS", 14)
	if err != nil {
		return Config{}, err
	}
	if gatewayLogMaxSizeMB <= 0 || gatewayLogMaxBackups <= 0 || gatewayLogMaxAgeDays <= 0 {
		return Config{}, failure.New(
			failure.CodeConfigInvalid,
			failure.WithMessage("gateway log rotation limits must be positive integers"),
		)
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

	// 旧 HTTP_MAX_JSON_BODY_MB 继续作为两个服务的兼容回退；独立配置优先。
	gatewayMaxJSONBodyDefaultMB := 32
	adminMaxJSONBodyDefaultMB := 4
	if os.Getenv("HTTP_MAX_JSON_BODY_MB") != "" {
		legacyMaxJSONBodyMB, err := getEnvInt("HTTP_MAX_JSON_BODY_MB", 0)
		if err != nil {
			return Config{}, err
		}
		if !validJSONBodyLimitMB(legacyMaxJSONBodyMB) {
			return Config{}, failure.New(
				failure.CodeConfigInvalid,
				failure.WithMessage("HTTP_MAX_JSON_BODY_MB must be between 1 and 256"),
			)
		}
		gatewayMaxJSONBodyDefaultMB = legacyMaxJSONBodyMB
		adminMaxJSONBodyDefaultMB = legacyMaxJSONBodyMB
	}
	gatewayMaxJSONBodyMB, err := getEnvInt("GATEWAY_MAX_JSON_BODY_MB", gatewayMaxJSONBodyDefaultMB)
	if err != nil {
		return Config{}, err
	}
	adminMaxJSONBodyMB, err := getEnvInt("ADMIN_MAX_JSON_BODY_MB", adminMaxJSONBodyDefaultMB)
	if err != nil {
		return Config{}, err
	}
	if !validJSONBodyLimitMB(gatewayMaxJSONBodyMB) || !validJSONBodyLimitMB(adminMaxJSONBodyMB) {
		return Config{}, failure.New(
			failure.CodeConfigInvalid,
			failure.WithMessage("gateway and admin JSON body limits must be between 1 and 256"),
		)
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
			ReadTimeout:             httpReadTimeout,
			WriteTimeout:            httpWriteTimeout,
			IdleTimeout:             httpIdleTimeout,
			ShutdownTimeout:         httpShutdownTimeout,
			GatewayMaxJSONBodyBytes: int64(gatewayMaxJSONBodyMB) << 20,
			AdminMaxJSONBodyBytes:   int64(adminMaxJSONBodyMB) << 20,
		},
		Log: LogConfig{
			Level:  logLevel,
			Format: logFormat,
		},
		GatewayLog: GatewayLogConfig{
			Environment:    gatewayEnvironment,
			BaselineLevel:  gatewayLogLevel,
			ConsoleEnabled: gatewayConsoleEnabled,
			FilePath:       getEnv("GATEWAY_LOG_FILE_PATH", "logs/gateway.jsonl"),
			MaxSizeMB:      gatewayLogMaxSizeMB,
			MaxBackups:     gatewayLogMaxBackups,
			MaxAgeDays:     gatewayLogMaxAgeDays,
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
		Tracing: TracingConfig{
			Enabled:     tracingEnabled,
			Endpoint:    getEnv("OTEL_EXPORTER_OTLP_ENDPOINT", ""),
			Insecure:    tracingInsecure,
			ServiceName: getEnv("OTEL_SERVICE_NAME", "unio-gateway"),
			SampleRatio: tracingSampleRatio,
		},
		Gateway: GatewayConfig{
			HTTPAddr:                     getEnv("GATEWAY_HTTP_ADDR", ":8520"),
			InternalToken:                getEnv("GATEWAY_INTERNAL_TOKEN", ""),
			InstanceID:                   getEnv("GATEWAY_INSTANCE_ID", ""),
			MaxOutputTokensFallback:      authorizationMaxOutputTokensFallback,
			PartialAssumedCacheReadRatio: partialAssumedCacheReadRatio,
			MaxUpstreamResponseBytes:     int64(gatewayMaxUpstreamResponseMB) << 20,
		},
		Admin: AdminConfig{
			HTTPAddr:             getEnv("ADMIN_HTTP_ADDR", ":8521"),
			APIToken:             getEnv("ADMIN_API_TOKEN", ""),
			GatewayInternalURLs:  resolveGatewayInternalURLs(),
			GatewayInternalToken: getEnv("GATEWAY_INTERNAL_TOKEN", ""),
			LokiURL:              getEnv("LOKI_URL", "http://127.0.0.1:3100"),
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

func validJSONBodyLimitMB(value int) bool {
	return value > 0 && value <= 256
}

// getEnv 读取字符串环境变量；未设置时返回 fallback。
func getEnv(key string, fallback string) string {
	value := os.Getenv(key)
	if value == "" {
		return fallback
	}
	return value
}

// resolveGatewayInternalURLs 解析 admin 拉取 gateway 熔断快照的基址列表。
// 优先 GATEWAY_INTERNAL_URLS（逗号分隔）；若为空且已配置 GATEWAY_INTERNAL_TOKEN，
// 且 GATEWAY_HTTP_ADDR 为 ":port" 形式，则默认本机 http://127.0.0.1:port（单机开发友好）。
func resolveGatewayInternalURLs() []string {
	raw := strings.TrimSpace(os.Getenv("GATEWAY_INTERNAL_URLS"))
	if raw != "" {
		parts := strings.Split(raw, ",")
		out := make([]string, 0, len(parts))
		for _, p := range parts {
			p = strings.TrimSpace(p)
			if p != "" {
				out = append(out, strings.TrimRight(p, "/"))
			}
		}
		return out
	}
	if strings.TrimSpace(os.Getenv("GATEWAY_INTERNAL_TOKEN")) == "" {
		return nil
	}
	addr := getEnv("GATEWAY_HTTP_ADDR", ":8520")
	if strings.HasPrefix(addr, ":") {
		return []string{"http://127.0.0.1" + addr}
	}
	return nil
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

// parseLogLevel 将环境变量中的日志级别转换为 zapcore.Level。
func parseLogLevel(value string) (zapcore.Level, error) {
	switch strings.ToLower(value) {
	case "", "info":
		return zapcore.InfoLevel, nil
	case "debug":
		return zapcore.DebugLevel, nil
	case "warn", "warning":
		return zapcore.WarnLevel, nil
	case "error":
		return zapcore.ErrorLevel, nil
	default:
		return zapcore.InfoLevel, failure.New(
			failure.CodeConfigUnsupported,
			failure.WithMessage(fmt.Sprintf("parse LOG_LEVEL: unsupported level %q", value)),
		)
	}
}

func parseGatewayEnvironment(value string) (string, error) {
	switch strings.ToLower(value) {
	case GatewayEnvironmentDevelopment, GatewayEnvironmentTest, GatewayEnvironmentProduction:
		return strings.ToLower(value), nil
	default:
		return "", failure.New(
			failure.CodeConfigUnsupported,
			failure.WithMessage(fmt.Sprintf("parse GATEWAY_ENV: unsupported environment %q", value)),
		)
	}
}

func parseGatewayLogLevel(value string) (zapcore.Level, error) {
	switch strings.ToLower(value) {
	case "", "info":
		return zapcore.InfoLevel, nil
	case "debug":
		return zapcore.DebugLevel, nil
	default:
		return zapcore.InfoLevel, failure.New(
			failure.CodeConfigUnsupported,
			failure.WithMessage(fmt.Sprintf("parse GATEWAY_LOG_LEVEL: unsupported level %q", value)),
		)
	}
}

// parseLogFormat 将环境变量中的日志格式转换为 console | json。
func parseLogFormat(value string) (string, error) {
	switch strings.ToLower(value) {
	case "", LogFormatConsole:
		return LogFormatConsole, nil
	case LogFormatJSON:
		return LogFormatJSON, nil
	default:
		return "", failure.New(
			failure.CodeConfigUnsupported,
			failure.WithMessage(fmt.Sprintf("parse LOG_FORMAT: unsupported format %q", value)),
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
