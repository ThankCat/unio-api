package appsettings

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// 本文件登记 gateway 热路径运行时配置。P4 的 breaker、rate/concurrency defaults 与
// routing balance 由 Redis committed runtime control 驱动；其余配置继续通过 settings applier
// 热更新。429 cooldown 是 Redis 全局事实，不属于已删除的 timeout/5xx 进程内失败软冷却。
//
// 单位约定(用户决策):时长一律 int 毫秒,字段/key 带 _ms 后缀(对齐 channels.timeout_ms 惯例),
// 不用 "10m" 之类的 duration 字符串;比例用 (0,1] 浮点;计数用普通整数。

// gateway 配置在 app_settings 中的 key。
const (
	GatewayCircuitBreakerKey           = "gateway.circuit_breaker"
	GatewayRouteRateLimitDefaultsKey   = "gateway.route_rate_limit_defaults"
	GatewayChannelRateLimitDefaultsKey = "gateway.channel_rate_limit_defaults"
	GatewayStreamIdleTimeoutKey        = "gateway.stream_idle_timeout_ms"
	GatewayChannelCooldownKey          = "gateway.channel_ratelimit_cooldown"
	GatewayCredential401ThresholdKey   = "gateway.credential_401_threshold"
	GatewayDefaultChannelTimeoutKey    = "gateway.default_channel_timeout_ms"
	GatewayConcurrencyDefaultsKey      = "gateway.concurrency_defaults"
	GatewayRoutingStickyKey            = "gateway.routing_sticky"
	GatewayRoutingBalanceKey           = "gateway.routing_balance"
)

func msToDuration(ms int64) time.Duration {
	return time.Duration(ms) * time.Millisecond
}

func durationToMs(d time.Duration) int64 {
	return d.Milliseconds()
}

// strictUnmarshal 拒绝未知字段的 JSON 解码:防止旧格式字段名(如 "cooldown":"5s")被静默忽略、
// 缺省字段落 0 造成行为突变;也能在后台拼错字段名时立刻报错而非默默丢弃。
func strictUnmarshal(raw []byte, v any) error {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	return dec.Decode(v)
}

// ---- 熔断器 ----

// CircuitBreakerSettings 是 P4 Origin/Channel 全局熔断与 permit 生命周期配置。
// OpenDuration 仅供 Phase E 删除前的旧进程内 breaker 兼容使用，JSON 不再包含该字段。
type CircuitBreakerSettings struct {
	Enabled                             bool
	Window                              time.Duration
	MinRequests                         int
	FailureRatio                        float64
	ConsecutiveFailures                 int
	ConsecutiveWindow                   time.Duration
	HalfOpenSuccesses                   int
	AttemptPermitTTL                    time.Duration
	AttemptPermitRenewInterval          time.Duration
	AttemptPermitTerminalTTL            time.Duration
	OriginBaseURLRevisionOperationTTL time.Duration
	OriginStatusRevisionOperationTTL  time.Duration
	OriginStatusBatchMax              int
	OpenDurations                       []time.Duration
	OriginAmbiguousDistinctChannels   int
	OriginAmbiguousDistinctModels     int

	OpenDuration time.Duration
}

// DefaultCircuitBreakerSettings 返回 P4 §4.8 的已决议默认值。
func DefaultCircuitBreakerSettings() CircuitBreakerSettings {
	return CircuitBreakerSettings{
		Enabled:                             true,
		Window:                              30 * time.Second,
		MinRequests:                         20,
		FailureRatio:                        0.5,
		ConsecutiveFailures:                 3,
		ConsecutiveWindow:                   10 * time.Second,
		HalfOpenSuccesses:                   2,
		AttemptPermitTTL:                    30 * time.Second,
		AttemptPermitRenewInterval:          10 * time.Second,
		AttemptPermitTerminalTTL:            5 * time.Minute,
		OriginBaseURLRevisionOperationTTL: 24 * time.Hour,
		OriginStatusRevisionOperationTTL:  24 * time.Hour,
		OriginStatusBatchMax:              256,
		OpenDurations:                       []time.Duration{15 * time.Second, 30 * time.Second, time.Minute, 2 * time.Minute, 5 * time.Minute},
		OriginAmbiguousDistinctChannels:   2,
		OriginAmbiguousDistinctModels:     2,
		OpenDuration:                        15 * time.Second,
	}
}

type circuitBreakerDoc struct {
	Enabled                               bool    `json:"enabled"`
	WindowMs                              int64   `json:"window_ms"`
	MinRequests                           int     `json:"min_requests"`
	FailureRatio                          float64 `json:"failure_ratio"`
	ConsecutiveFailures                   int     `json:"consecutive_failures"`
	ConsecutiveWindowMs                   int64   `json:"consecutive_window_ms"`
	HalfOpenSuccesses                     int     `json:"half_open_successes"`
	AttemptPermitTTLMs                    int64   `json:"attempt_permit_ttl_ms"`
	AttemptPermitRenewIntervalMs          int64   `json:"attempt_permit_renew_interval_ms"`
	AttemptPermitTerminalTTLMs            int64   `json:"attempt_permit_terminal_ttl_ms"`
	OriginBaseURLRevisionOperationTTLMs int64   `json:"origin_base_url_revision_operation_ttl_ms"`
	OriginStatusRevisionOperationTTLMs  int64   `json:"origin_status_revision_operation_ttl_ms"`
	OriginStatusBatchMax                int     `json:"origin_status_batch_max"`
	OpenDurationsMs                       []int64 `json:"open_durations_ms"`
	OriginAmbiguousDistinctChannels     int     `json:"origin_ambiguous_distinct_channels"`
	OriginAmbiguousDistinctModels       int     `json:"origin_ambiguous_distinct_models"`
}

func encodeCircuitBreakerSettings(s CircuitBreakerSettings) json.RawMessage {
	openDurations := make([]int64, 0, len(s.OpenDurations))
	for _, d := range s.OpenDurations {
		openDurations = append(openDurations, durationToMs(d))
	}
	raw, err := json.Marshal(circuitBreakerDoc{
		Enabled:                               s.Enabled,
		WindowMs:                              durationToMs(s.Window),
		MinRequests:                           s.MinRequests,
		FailureRatio:                          s.FailureRatio,
		ConsecutiveFailures:                   s.ConsecutiveFailures,
		ConsecutiveWindowMs:                   durationToMs(s.ConsecutiveWindow),
		HalfOpenSuccesses:                     s.HalfOpenSuccesses,
		AttemptPermitTTLMs:                    durationToMs(s.AttemptPermitTTL),
		AttemptPermitRenewIntervalMs:          durationToMs(s.AttemptPermitRenewInterval),
		AttemptPermitTerminalTTLMs:            durationToMs(s.AttemptPermitTerminalTTL),
		OriginBaseURLRevisionOperationTTLMs: durationToMs(s.OriginBaseURLRevisionOperationTTL),
		OriginStatusRevisionOperationTTLMs:  durationToMs(s.OriginStatusRevisionOperationTTL),
		OriginStatusBatchMax:                s.OriginStatusBatchMax,
		OpenDurationsMs:                       openDurations,
		OriginAmbiguousDistinctChannels:     s.OriginAmbiguousDistinctChannels,
		OriginAmbiguousDistinctModels:       s.OriginAmbiguousDistinctModels,
	})
	if err != nil {
		panic(fmt.Sprintf("appsettings: encode circuit breaker settings: %v", err))
	}
	return raw
}

// DecodeCircuitBreakerSettings 解码并校验熔断器配置(时长字段为 int 毫秒;拒绝未知/旧格式字段)。
func DecodeCircuitBreakerSettings(raw []byte) (CircuitBreakerSettings, error) {
	var doc circuitBreakerDoc
	if err := strictUnmarshal(raw, &doc); err != nil {
		return CircuitBreakerSettings{}, err
	}
	s := CircuitBreakerSettings{
		Enabled:                             doc.Enabled,
		Window:                              msToDuration(doc.WindowMs),
		MinRequests:                         doc.MinRequests,
		FailureRatio:                        doc.FailureRatio,
		ConsecutiveFailures:                 doc.ConsecutiveFailures,
		ConsecutiveWindow:                   msToDuration(doc.ConsecutiveWindowMs),
		HalfOpenSuccesses:                   doc.HalfOpenSuccesses,
		AttemptPermitTTL:                    msToDuration(doc.AttemptPermitTTLMs),
		AttemptPermitRenewInterval:          msToDuration(doc.AttemptPermitRenewIntervalMs),
		AttemptPermitTerminalTTL:            msToDuration(doc.AttemptPermitTerminalTTLMs),
		OriginBaseURLRevisionOperationTTL: msToDuration(doc.OriginBaseURLRevisionOperationTTLMs),
		OriginStatusRevisionOperationTTL:  msToDuration(doc.OriginStatusRevisionOperationTTLMs),
		OriginStatusBatchMax:              doc.OriginStatusBatchMax,
		OriginAmbiguousDistinctChannels:   doc.OriginAmbiguousDistinctChannels,
		OriginAmbiguousDistinctModels:     doc.OriginAmbiguousDistinctModels,
	}
	if doc.WindowMs <= 0 {
		return CircuitBreakerSettings{}, errors.New("window_ms must be > 0")
	}
	if s.MinRequests < 2 {
		return CircuitBreakerSettings{}, errors.New("min_requests must be >= 2")
	}
	if s.FailureRatio <= 0 || s.FailureRatio > 1 {
		return CircuitBreakerSettings{}, errors.New("failure_ratio must be within (0, 1]")
	}
	if s.ConsecutiveFailures < 1 || doc.ConsecutiveWindowMs <= 0 {
		return CircuitBreakerSettings{}, errors.New("consecutive_failures and consecutive_window_ms must be > 0")
	}
	if s.HalfOpenSuccesses < 2 {
		return CircuitBreakerSettings{}, errors.New("half_open_successes must be >= 2")
	}
	if doc.AttemptPermitTTLMs <= 0 || doc.AttemptPermitRenewIntervalMs <= 0 || doc.AttemptPermitTerminalTTLMs < doc.AttemptPermitTTLMs {
		return CircuitBreakerSettings{}, errors.New("invalid attempt permit ttl settings")
	}
	if doc.AttemptPermitRenewIntervalMs*3 > doc.AttemptPermitTTLMs {
		return CircuitBreakerSettings{}, errors.New("attempt_permit_renew_interval_ms * 3 must be <= attempt_permit_ttl_ms")
	}
	if doc.OriginBaseURLRevisionOperationTTLMs <= 0 || doc.OriginStatusRevisionOperationTTLMs <= 0 {
		return CircuitBreakerSettings{}, errors.New("origin revision operation ttl must be > 0")
	}
	if s.OriginStatusBatchMax < 1 || s.OriginStatusBatchMax > 1024 {
		return CircuitBreakerSettings{}, errors.New("origin_status_batch_max must be within [1, 1024]")
	}
	if len(doc.OpenDurationsMs) == 0 {
		return CircuitBreakerSettings{}, errors.New("open_durations_ms must not be empty")
	}
	for i, ms := range doc.OpenDurationsMs {
		if ms <= 0 || (i > 0 && ms < doc.OpenDurationsMs[i-1]) {
			return CircuitBreakerSettings{}, errors.New("open_durations_ms must be positive and non-decreasing")
		}
		s.OpenDurations = append(s.OpenDurations, msToDuration(ms))
	}
	if s.OriginAmbiguousDistinctChannels < 2 || s.OriginAmbiguousDistinctModels < 2 {
		return CircuitBreakerSettings{}, errors.New("origin ambiguous distinct thresholds must be >= 2")
	}
	s.OpenDuration = s.OpenDurations[0]
	return s, nil
}

func circuitBreakerDefinition() Definition {
	return Definition{
		Key:      GatewayCircuitBreakerKey,
		Category: "gateway",
		Label:    "全局熔断器",
		Description: "Origin 与渠道共享的 Redis 熔断状态机及 attempt permit 生命周期。" +
			"支持快速连续失败、比例触发、half-open 双成功恢复和分级退避；时长单位毫秒。" +
			"enabled=false 只关闭 breaker 门禁，不关闭 permit、Origin 围栏或限额；Redis 故障始终拒绝准入。",
		HotReload: true,
		Default:   encodeCircuitBreakerSettings(DefaultCircuitBreakerSettings()),
		Validate: func(raw json.RawMessage) error {
			_, err := DecodeCircuitBreakerSettings(raw)
			return err
		},
	}
}

// GatewayCircuitBreaker 读取当前生效的熔断器配置(经 store 多层读取,解码失败回默认)。
func GatewayCircuitBreaker(ctx context.Context, store *SettingsStore) CircuitBreakerSettings {
	s, err := DecodeCircuitBreakerSettings(store.Raw(ctx, GatewayCircuitBreakerKey))
	if err != nil {
		return DefaultCircuitBreakerSettings()
	}
	return s
}

// ---- 线路/渠道限流默认 ----

// RateLimitDefaultsSettings 是线路或渠道使用的 RPM/TPM/RPD 默认。
// RPM/TPM/RPD 为 0 表示该维度默认不限；具体主体可在 routes/channels 行覆盖。
type RateLimitDefaultsSettings struct {
	RPM int64
	TPM int64
	RPD int64
}

// DefaultRateLimitDefaultsSettings 按 DEC-053/DEC-054 默认三维均不限；显式线路或渠道限额仍可覆盖。
func DefaultRateLimitDefaultsSettings() RateLimitDefaultsSettings {
	return RateLimitDefaultsSettings{RPM: 0, TPM: 0, RPD: 0}
}

type rateLimitDefaultsDoc struct {
	RPM int64 `json:"rpm"`
	TPM int64 `json:"tpm"`
	RPD int64 `json:"rpd"`
}

func encodeRateLimitDefaultsSettings(s RateLimitDefaultsSettings) json.RawMessage {
	raw, err := json.Marshal(rateLimitDefaultsDoc{RPM: s.RPM, TPM: s.TPM, RPD: s.RPD})
	if err != nil {
		panic(fmt.Sprintf("appsettings: encode rate limit defaults: %v", err))
	}
	return raw
}

// DecodeRateLimitDefaultsSettings 解码并校验默认限流配置(拒绝未知字段)。
func DecodeRateLimitDefaultsSettings(raw []byte) (RateLimitDefaultsSettings, error) {
	var doc rateLimitDefaultsDoc
	if err := strictUnmarshal(raw, &doc); err != nil {
		return RateLimitDefaultsSettings{}, err
	}
	s := RateLimitDefaultsSettings{RPM: doc.RPM, TPM: doc.TPM, RPD: doc.RPD}
	if s.RPM < 0 || s.TPM < 0 || s.RPD < 0 {
		return RateLimitDefaultsSettings{}, errors.New("rpm/tpm/rpd must be zero or positive")
	}
	return s, nil
}

func routeRateLimitDefaultsDefinition() Definition {
	return Definition{
		Key:      GatewayRouteRateLimitDefaultsKey,
		Category: "gateway",
		Label:    "线路默认限流(RPM/TPM/RPD)",
		Description: "线路未单独配置时，按(线路,用户)生效的默认上限，0=该维度不限。" +
			"Redis revisioned control 是执行权威；Redis 或 BreakerStore 故障固定拒绝准入，不提供绕过开关。",
		HotReload: true,
		Default:   encodeRateLimitDefaultsSettings(DefaultRateLimitDefaultsSettings()),
		Validate: func(raw json.RawMessage) error {
			_, err := DecodeRateLimitDefaultsSettings(raw)
			return err
		},
	}
}

func channelRateLimitDefaultsDefinition() Definition {
	return Definition{
		Key:      GatewayChannelRateLimitDefaultsKey,
		Category: "gateway",
		Label:    "渠道默认限流(RPM/TPM/RPD)",
		Description: "渠道未单独配置时生效的默认上限，0=该维度不限。" +
			"渠道限流只影响候选渠道准入，命中后跳过该渠道并继续 fallback；Redis 故障固定拒绝准入。",
		HotReload: true,
		Default:   encodeRateLimitDefaultsSettings(DefaultRateLimitDefaultsSettings()),
		Validate: func(raw json.RawMessage) error {
			_, err := DecodeRateLimitDefaultsSettings(raw)
			return err
		},
	}
}

// GatewayRouteRateLimitDefaults 读取当前生效的线路默认限流(解码失败回默认)。
func GatewayRouteRateLimitDefaults(ctx context.Context, store *SettingsStore) RateLimitDefaultsSettings {
	s, err := DecodeRateLimitDefaultsSettings(store.Raw(ctx, GatewayRouteRateLimitDefaultsKey))
	if err != nil {
		return DefaultRateLimitDefaultsSettings()
	}
	return s
}

// GatewayChannelRateLimitDefaults 读取当前生效的渠道默认限流(解码失败回默认)。
func GatewayChannelRateLimitDefaults(ctx context.Context, store *SettingsStore) RateLimitDefaultsSettings {
	s, err := DecodeRateLimitDefaultsSettings(store.Raw(ctx, GatewayChannelRateLimitDefaultsKey))
	if err != nil {
		return DefaultRateLimitDefaultsSettings()
	}
	return s
}

// ---- 渠道 429 冷却 ----

// ChannelCooldownSettings 是上游 429 时的渠道冷却参数。
// Cooldown 是无 Retry-After 时的默认冷却(<=0 表示此情形不冷却);
// Cap 是对 Retry-After 建议值的封顶(<=0 表示不额外封顶)。
type ChannelCooldownSettings struct {
	Cooldown time.Duration
	Cap      time.Duration
}

// DefaultChannelCooldownSettings 与原 GATEWAY_CHANNEL_RATELIMIT_COOLDOWN(_CAP) env 默认一致。
func DefaultChannelCooldownSettings() ChannelCooldownSettings {
	return ChannelCooldownSettings{Cooldown: 5 * time.Second, Cap: 5 * time.Minute}
}

type channelCooldownDoc struct {
	CooldownMs int64 `json:"cooldown_ms"`
	CapMs      int64 `json:"cap_ms"`
}

func encodeChannelCooldownSettings(s ChannelCooldownSettings) json.RawMessage {
	raw, err := json.Marshal(channelCooldownDoc{
		CooldownMs: durationToMs(s.Cooldown),
		CapMs:      durationToMs(s.Cap),
	})
	if err != nil {
		panic(fmt.Sprintf("appsettings: encode channel cooldown: %v", err))
	}
	return raw
}

// DecodeChannelCooldownSettings 解码并校验渠道 429 冷却配置(int 毫秒;0 合法=关闭,负数非法;拒绝未知/旧格式字段)。
func DecodeChannelCooldownSettings(raw []byte) (ChannelCooldownSettings, error) {
	var doc channelCooldownDoc
	if err := strictUnmarshal(raw, &doc); err != nil {
		return ChannelCooldownSettings{}, err
	}
	if doc.CooldownMs < 0 {
		return ChannelCooldownSettings{}, errors.New("cooldown_ms must not be negative")
	}
	if doc.CapMs < 0 {
		return ChannelCooldownSettings{}, errors.New("cap_ms must not be negative")
	}
	return ChannelCooldownSettings{
		Cooldown: msToDuration(doc.CooldownMs),
		Cap:      msToDuration(doc.CapMs),
	}, nil
}

func channelCooldownDefinition() Definition {
	return Definition{
		Key:      GatewayChannelCooldownKey,
		Category: "gateway",
		Label:    "渠道 429 冷却",
		Description: "上游 429 未给 Retry-After 时套用 cooldown_ms(0=此情形不冷却);" +
			"cap_ms 封顶 Retry-After 建议值(0=不额外封顶)。单位毫秒。冷却窗口内 routing fallback 直接跳过该渠道。",
		HotReload: true,
		Default:   encodeChannelCooldownSettings(DefaultChannelCooldownSettings()),
		Validate: func(raw json.RawMessage) error {
			_, err := DecodeChannelCooldownSettings(raw)
			return err
		},
	}
}

// GatewayChannelCooldown 读取当前生效的渠道 429 冷却配置(解码失败回默认)。
func GatewayChannelCooldown(ctx context.Context, store *SettingsStore) ChannelCooldownSettings {
	s, err := DecodeChannelCooldownSettings(store.Raw(ctx, GatewayChannelCooldownKey))
	if err != nil {
		return DefaultChannelCooldownSettings()
	}
	return s
}

// ---- 标量项:流式 idle 超时 / 凭据 401 阈值 / 默认渠道超时 ----

// DefaultStreamIdleTimeoutSetting 与原 GATEWAY_STREAM_IDLE_TIMEOUT env 默认一致。
const DefaultStreamIdleTimeoutSetting = 10 * time.Minute

// DefaultChannelTimeoutSetting 与原 bootstrap/routing 的 30s 硬编码一致。
const DefaultChannelTimeoutSetting = 30 * time.Second

// DefaultCredential401Threshold 与原 GATEWAY_CHANNEL_CREDENTIAL_401_THRESHOLD env 默认一致。
const DefaultCredential401Threshold = 3

func encodeMsSetting(d time.Duration) json.RawMessage {
	return json.RawMessage(fmt.Sprintf("%d", durationToMs(d)))
}

// DecodePositiveMsSetting 解码 int 毫秒标量值,要求 > 0,返回 time.Duration。
func DecodePositiveMsSetting(raw []byte) (time.Duration, error) {
	var ms int64
	if err := json.Unmarshal(raw, &ms); err != nil {
		return 0, fmt.Errorf("value must be an integer of milliseconds: %w", err)
	}
	if ms <= 0 {
		return 0, errors.New("milliseconds must be > 0")
	}
	return msToDuration(ms), nil
}

// DecodePositiveIntSetting 解码整数值,要求 > 0。
func DecodePositiveIntSetting(raw []byte) (int, error) {
	var n int
	if err := json.Unmarshal(raw, &n); err != nil {
		return 0, err
	}
	if n <= 0 {
		return 0, errors.New("value must be > 0")
	}
	return n, nil
}

func encodeBoolSetting(v bool) json.RawMessage {
	if v {
		return json.RawMessage("true")
	}
	return json.RawMessage("false")
}

// DecodeBoolSetting 解码 JSON 布尔标量。
func DecodeBoolSetting(raw []byte) (bool, error) {
	var v bool
	if err := json.Unmarshal(raw, &v); err != nil {
		return false, fmt.Errorf("value must be a boolean: %w", err)
	}
	return v, nil
}

func streamIdleTimeoutDefinition() Definition {
	return Definition{
		Key:      GatewayStreamIdleTimeoutKey,
		Category: "gateway",
		Label:    "流式 idle 超时",
		Description: "流式上游「相邻两次流活动之间」的最大静默时长看门狗,兜底半开/挂死连接。单位毫秒。" +
			"必须显著大于上游合法的最长静默阶段(如慢速图像生成),否则会误杀正常长任务流。",
		HotReload: true,
		Default:   encodeMsSetting(DefaultStreamIdleTimeoutSetting),
		Validate: func(raw json.RawMessage) error {
			_, err := DecodePositiveMsSetting(raw)
			return err
		},
	}
}

// GatewayStreamIdleTimeout 读取当前生效的流式 idle 超时(解码失败回默认)。
func GatewayStreamIdleTimeout(ctx context.Context, store *SettingsStore) time.Duration {
	d, err := DecodePositiveMsSetting(store.Raw(ctx, GatewayStreamIdleTimeoutKey))
	if err != nil {
		return DefaultStreamIdleTimeoutSetting
	}
	return d
}

func credential401ThresholdDefinition() Definition {
	return Definition{
		Key:      GatewayCredential401ThresholdKey,
		Category: "gateway",
		Label:    "凭据失效 401 阈值",
		Description: "某渠道「连续」这么多次上游 401 后,凭据闸门把 channels.credential_valid 翻 false 持久摘除," +
			"直到渠道检测通过才恢复。单位:次,必须 > 0。",
		HotReload: true,
		Default:   json.RawMessage(fmt.Sprintf("%d", DefaultCredential401Threshold)),
		Validate: func(raw json.RawMessage) error {
			_, err := DecodePositiveIntSetting(raw)
			return err
		},
	}
}

// GatewayCredential401Threshold 读取当前生效的 401 阈值(解码失败回默认)。
func GatewayCredential401Threshold(ctx context.Context, store *SettingsStore) int {
	n, err := DecodePositiveIntSetting(store.Raw(ctx, GatewayCredential401ThresholdKey))
	if err != nil {
		return DefaultCredential401Threshold
	}
	return n
}

// ---- 在途并发全局默认（DEC-029） ----

// ConcurrencyDefaultsSettings 是两级在途并发上限的全局默认（0=该级不限）。
// KeyLimit 作用于「线路+用户」（ingress 中间件，多余并发立即 429）；
// ChannelLimit 作用于渠道（attempt runner，满员跳过该候选 fallback）。渠道行 concurrency_limit 可覆盖。
type ConcurrencyDefaultsSettings struct {
	KeyLimit     int64
	ChannelLimit int64
}

// DefaultConcurrencyDefaultsSettings 默认两级均不限（0）：并发限制是选择性开启的保护，
// 避免默认值误伤合法的 agent 并发扇出；建议按客户端重试行为设置（如 Claude Code 重试 10 次 → key 设 3~5）。
func DefaultConcurrencyDefaultsSettings() ConcurrencyDefaultsSettings {
	return ConcurrencyDefaultsSettings{KeyLimit: 0, ChannelLimit: 0}
}

type concurrencyDefaultsDoc struct {
	KeyLimit     int64 `json:"key_limit"`
	ChannelLimit int64 `json:"channel_limit"`
}

func encodeConcurrencyDefaultsSettings(s ConcurrencyDefaultsSettings) json.RawMessage {
	raw, err := json.Marshal(concurrencyDefaultsDoc(s))
	if err != nil {
		panic(fmt.Sprintf("appsettings: encode concurrency defaults: %v", err))
	}
	return raw
}

// DecodeConcurrencyDefaultsSettings 解码并校验在途并发全局默认（拒绝未知字段；各值 >=0，0=不限）。
func DecodeConcurrencyDefaultsSettings(raw []byte) (ConcurrencyDefaultsSettings, error) {
	var doc concurrencyDefaultsDoc
	if err := strictUnmarshal(raw, &doc); err != nil {
		return ConcurrencyDefaultsSettings{}, err
	}
	s := ConcurrencyDefaultsSettings(doc)
	if s.KeyLimit < 0 || s.ChannelLimit < 0 {
		return ConcurrencyDefaultsSettings{}, errors.New("key_limit/channel_limit must be zero or positive")
	}
	return s, nil
}

func concurrencyDefaultsDefinition() Definition {
	return Definition{
		Key:      GatewayConcurrencyDefaultsKey,
		Category: "gateway",
		Label:    "在途并发全局默认",
		Description: "「同时进行中」请求数上限（含整段流式传输），0=不限。key_limit 作用于线路+用户" +
			"（ingress，超出立即 429，专防客户端自动重试风暴堆积慢请求）；channel_limit 作用于渠道" +
			"（满员跳过该候选 fallback），渠道行 concurrency_limit 可覆盖。进程内计数，多实例各自独立。",
		HotReload: true,
		Default:   encodeConcurrencyDefaultsSettings(DefaultConcurrencyDefaultsSettings()),
		Validate: func(raw json.RawMessage) error {
			_, err := DecodeConcurrencyDefaultsSettings(raw)
			return err
		},
	}
}

// GatewayConcurrencyDefaults 读取当前生效的在途并发全局默认（解码失败回默认）。
func GatewayConcurrencyDefaults(ctx context.Context, store *SettingsStore) ConcurrencyDefaultsSettings {
	s, err := DecodeConcurrencyDefaultsSettings(store.Raw(ctx, GatewayConcurrencyDefaultsKey))
	if err != nil {
		return DefaultConcurrencyDefaultsSettings()
	}
	return s
}

// ---- balanced 容量调度 ----

// RoutingBalanceSettings 是 balanced 的容量、错误率与 stream-only TTFT 组合权重配置。
// Enabled/WeightByRemaining 仅供 Phase E 删除前的旧评分器兼容，JSON 不再包含这两个字段。
type RoutingBalanceSettings struct {
	TTFTTarget           time.Duration
	TTFTWeight           float64
	CostWeight           float64
	MinimumRoutingFactor float64
	TTFTEWMAAlpha        float64

	Enabled           bool
	WeightByRemaining bool
}

func DefaultRoutingBalanceSettings() RoutingBalanceSettings {
	return RoutingBalanceSettings{
		TTFTTarget:           2 * time.Second,
		TTFTWeight:           0.35,
		CostWeight:           0.5,
		MinimumRoutingFactor: 0.05,
		TTFTEWMAAlpha:        0.2,
		Enabled:              true,
		WeightByRemaining:    true,
	}
}

type routingBalanceDoc struct {
	TTFTTargetMs         int64   `json:"ttft_target_ms"`
	TTFTWeight           float64 `json:"ttft_weight"`
	CostWeight           float64 `json:"cost_weight"`
	MinimumRoutingFactor float64 `json:"minimum_routing_factor"`
	TTFTEWMAAlpha        float64 `json:"ttft_ewma_alpha"`
}

func encodeRoutingBalanceSettings(s RoutingBalanceSettings) json.RawMessage {
	raw, err := json.Marshal(routingBalanceDoc{
		TTFTTargetMs:         durationToMs(s.TTFTTarget),
		TTFTWeight:           s.TTFTWeight,
		CostWeight:           s.CostWeight,
		MinimumRoutingFactor: s.MinimumRoutingFactor,
		TTFTEWMAAlpha:        s.TTFTEWMAAlpha,
	})
	if err != nil {
		panic(fmt.Sprintf("appsettings: encode routing balance: %v", err))
	}
	return raw
}

func DecodeRoutingBalanceSettings(raw []byte) (RoutingBalanceSettings, error) {
	var doc routingBalanceDoc
	if err := strictUnmarshal(raw, &doc); err != nil {
		return RoutingBalanceSettings{}, err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return RoutingBalanceSettings{}, err
	}
	for _, name := range []string{"ttft_target_ms", "ttft_weight", "minimum_routing_factor", "ttft_ewma_alpha"} {
		value, ok := fields[name]
		if !ok || bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return RoutingBalanceSettings{}, fmt.Errorf("%s is required", name)
		}
	}
	_, hasCostWeight := fields["cost_weight"]
	if len(fields) != 4 && !(len(fields) == 5 && hasCostWeight) {
		return RoutingBalanceSettings{}, errors.New("routing balance payload must use the legacy four-field or current five-field shape")
	}
	if hasCostWeight && bytes.Equal(bytes.TrimSpace(fields["cost_weight"]), []byte("null")) {
		return RoutingBalanceSettings{}, errors.New("cost_weight must be within [0, 1]")
	}
	if doc.TTFTTargetMs <= 0 {
		return RoutingBalanceSettings{}, errors.New("ttft_target_ms must be > 0")
	}
	if doc.TTFTWeight < 0 || doc.TTFTWeight > 1 {
		return RoutingBalanceSettings{}, errors.New("ttft_weight must be within [0, 1]")
	}
	if doc.CostWeight < 0 || doc.CostWeight > 1 {
		return RoutingBalanceSettings{}, errors.New("cost_weight must be within [0, 1]")
	}
	if doc.MinimumRoutingFactor <= 0 || doc.MinimumRoutingFactor > 1 {
		return RoutingBalanceSettings{}, errors.New("minimum_routing_factor must be within (0, 1]")
	}
	if doc.TTFTEWMAAlpha <= 0 || doc.TTFTEWMAAlpha > 1 {
		return RoutingBalanceSettings{}, errors.New("ttft_ewma_alpha must be within (0, 1]")
	}
	return RoutingBalanceSettings{
		TTFTTarget:           msToDuration(doc.TTFTTargetMs),
		TTFTWeight:           doc.TTFTWeight,
		CostWeight:           doc.CostWeight,
		MinimumRoutingFactor: doc.MinimumRoutingFactor,
		TTFTEWMAAlpha:        doc.TTFTEWMAAlpha,
		Enabled:              true,
		WeightByRemaining:    true,
	}, nil
}

func routingBalanceDefinition() Definition {
	return Definition{
		Key:      GatewayRoutingBalanceKey,
		Category: "gateway",
		Label:    "线路负载均衡",
		Description: "balanced 在线路显式渠道池内组合容量、成本、客观错误率与 stream-only TTFT EWMA。" +
			"流式和非流式调度共用流式 TTFT 样本；无样本时延迟项保持中性。",
		HotReload: true,
		Default:   encodeRoutingBalanceSettings(DefaultRoutingBalanceSettings()),
		Validate: func(raw json.RawMessage) error {
			_, err := DecodeRoutingBalanceSettings(raw)
			return err
		},
	}
}

func GatewayRoutingBalance(ctx context.Context, store *SettingsStore) RoutingBalanceSettings {
	s, err := DecodeRoutingBalanceSettings(store.Raw(ctx, GatewayRoutingBalanceKey))
	if err != nil {
		return DefaultRoutingBalanceSettings()
	}
	return s
}

// ---- 会话粘性路由全局默认（大 uncache 缺口 P0） ----

// RoutingStickySettings 是跨协议会话 sticky 的全局默认配置。
// 线路行 sticky_enabled 可覆盖 EnabledDefault（NULL=继承此默认）；TTL 为绝对过期（bind/改绑时设置，
// 命中不刷新，R2），与上游 prompt cache TTL 解耦。TPMWait/TPMWaitJitter 供 P1 队首短等消费。
type RoutingStickySettings struct {
	EnabledDefault bool
	TTL            time.Duration
	TPMWait        time.Duration
	TPMWaitJitter  time.Duration
}

// DefaultRoutingStickySettings 默认开启 sticky：TTL 60min、队首短等 500ms + 100ms 抖动。
func DefaultRoutingStickySettings() RoutingStickySettings {
	return RoutingStickySettings{
		EnabledDefault: true,
		TTL:            time.Hour,
		TPMWait:        500 * time.Millisecond,
		TPMWaitJitter:  100 * time.Millisecond,
	}
}

type routingStickyDoc struct {
	EnabledDefault  bool  `json:"enabled_default"`
	TTLMs           int64 `json:"ttl_ms"`
	TPMWaitMs       int64 `json:"tpm_wait_ms"`
	TPMWaitJitterMs int64 `json:"tpm_wait_jitter_ms"`
}

func encodeRoutingStickySettings(s RoutingStickySettings) json.RawMessage {
	raw, err := json.Marshal(routingStickyDoc{
		EnabledDefault:  s.EnabledDefault,
		TTLMs:           durationToMs(s.TTL),
		TPMWaitMs:       durationToMs(s.TPMWait),
		TPMWaitJitterMs: durationToMs(s.TPMWaitJitter),
	})
	if err != nil {
		panic(fmt.Sprintf("appsettings: encode routing sticky settings: %v", err))
	}
	return raw
}

// DecodeRoutingStickySettings 解码并校验会话粘性配置（时长为 int 毫秒；拒绝未知字段）。
// ttl_ms 必须 > 0；tpm_wait_ms / tpm_wait_jitter_ms 允许 0（0=关闭短等/无抖动）。
func DecodeRoutingStickySettings(raw []byte) (RoutingStickySettings, error) {
	var doc routingStickyDoc
	if err := strictUnmarshal(raw, &doc); err != nil {
		return RoutingStickySettings{}, err
	}
	if doc.TTLMs <= 0 {
		return RoutingStickySettings{}, errors.New("ttl_ms must be > 0")
	}
	if doc.TPMWaitMs < 0 || doc.TPMWaitJitterMs < 0 {
		return RoutingStickySettings{}, errors.New("tpm_wait_ms/tpm_wait_jitter_ms must not be negative")
	}
	return RoutingStickySettings{
		EnabledDefault: doc.EnabledDefault,
		TTL:            msToDuration(doc.TTLMs),
		TPMWait:        msToDuration(doc.TPMWaitMs),
		TPMWaitJitter:  msToDuration(doc.TPMWaitJitterMs),
	}, nil
}

func routingStickyDefinition() Definition {
	return Definition{
		Key:      GatewayRoutingStickyKey,
		Category: "gateway",
		Label:    "会话粘性路由(sticky)",
		Description: "同会话请求钉住上次成功渠道以保上游 prompt cache（OpenAI prompt_cache_key / " +
			"Claude Code 会话头）。enabled_default 是线路未单独配置时的默认开关；ttl_ms 是绑定绝对过期" +
			"（命中不刷新，到期回落线路策略排序）；tpm_wait_ms/抖动是队首 TPM/并发满时的短等（0=不等）。",
		HotReload: true,
		Default:   encodeRoutingStickySettings(DefaultRoutingStickySettings()),
		Validate: func(raw json.RawMessage) error {
			_, err := DecodeRoutingStickySettings(raw)
			return err
		},
	}
}

// GatewayRoutingSticky 读取当前生效的会话粘性配置（解码失败回默认）。
func GatewayRoutingSticky(ctx context.Context, store *SettingsStore) RoutingStickySettings {
	s, err := DecodeRoutingStickySettings(store.Raw(ctx, GatewayRoutingStickyKey))
	if err != nil {
		return DefaultRoutingStickySettings()
	}
	return s
}

func defaultChannelTimeoutDefinition() Definition {
	return Definition{
		Key:      GatewayDefaultChannelTimeoutKey,
		Category: "gateway",
		Label:    "默认渠道超时",
		Description: "用户请求经网关调用上游时,渠道未配置 timeout_ms 的兜底超时。单位毫秒。" +
			"渠道行上的 timeout_ms 优先于此默认值。" +
			"不影响「渠道巡检」探测超时(admin_backend.channel_test.probe_timeout_ms)——检测专用、独立配置。",
		HotReload: true,
		Default:   encodeMsSetting(DefaultChannelTimeoutSetting),
		Validate: func(raw json.RawMessage) error {
			_, err := DecodePositiveMsSetting(raw)
			return err
		},
	}
}

// GatewayDefaultChannelTimeout 读取当前生效的默认渠道超时(解码失败回默认)。
func GatewayDefaultChannelTimeout(ctx context.Context, store *SettingsStore) time.Duration {
	d, err := DecodePositiveMsSetting(store.Raw(ctx, GatewayDefaultChannelTimeoutKey))
	if err != nil {
		return DefaultChannelTimeoutSetting
	}
	return d
}
