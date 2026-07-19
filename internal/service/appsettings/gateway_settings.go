package appsettings

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// 本文件登记 6 组 gateway 热路径运行时配置(迁移自 env,DEC:db_only + 全热改):
// 熔断器 / 限流全局默认 / 流式 idle 超时 / 渠道 429 冷却 / 凭据 401 阈值 / 默认渠道超时。
// 值形状与校验对齐原 config.go 的 env 校验。
//
// 单位约定(用户决策):时长一律 int 毫秒,字段/key 带 _ms 后缀(对齐 channels.timeout_ms 惯例),
// 不用 "10m" 之类的 duration 字符串;比例用 (0,1] 浮点;计数用普通整数。

// gateway 配置在 app_settings 中的 key。
const (
	GatewayCircuitBreakerKey         = "gateway.circuit_breaker"
	GatewayRateLimitDefaultsKey      = "gateway.rate_limit_defaults"
	GatewayStreamIdleTimeoutKey      = "gateway.stream_idle_timeout_ms"
	GatewayChannelCooldownKey        = "gateway.channel_ratelimit_cooldown"
	GatewayCredential401ThresholdKey = "gateway.credential_401_threshold"
	GatewayDefaultChannelTimeoutKey  = "gateway.default_channel_timeout_ms"
	GatewayFailureCooldownKey        = "gateway.channel_failure_cooldown_ms"
	GatewayConcurrencyDefaultsKey    = "gateway.concurrency_defaults"
	GatewayRoutingStickyKey          = "gateway.routing_sticky"
	GatewayRoutingBalanceKey         = "gateway.routing_balance"
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

// CircuitBreakerSettings 是渠道熔断器的运行时配置(enabled 亦可热改:关闭时熔断器放行且不记状态)。
type CircuitBreakerSettings struct {
	Enabled      bool
	Window       time.Duration
	MinRequests  int
	FailureRatio float64
	OpenDuration time.Duration
}

// DefaultCircuitBreakerSettings 与原 CIRCUIT_BREAKER_* env 默认一致。
func DefaultCircuitBreakerSettings() CircuitBreakerSettings {
	return CircuitBreakerSettings{
		Enabled:      true,
		Window:       30 * time.Second,
		MinRequests:  20,
		FailureRatio: 0.5,
		OpenDuration: 30 * time.Second,
	}
}

type circuitBreakerDoc struct {
	Enabled        bool    `json:"enabled"`
	WindowMs       int64   `json:"window_ms"`
	MinRequests    int     `json:"min_requests"`
	FailureRatio   float64 `json:"failure_ratio"`
	OpenDurationMs int64   `json:"open_duration_ms"`
}

func encodeCircuitBreakerSettings(s CircuitBreakerSettings) json.RawMessage {
	raw, err := json.Marshal(circuitBreakerDoc{
		Enabled:        s.Enabled,
		WindowMs:       durationToMs(s.Window),
		MinRequests:    s.MinRequests,
		FailureRatio:   s.FailureRatio,
		OpenDurationMs: durationToMs(s.OpenDuration),
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
		Enabled:      doc.Enabled,
		Window:       msToDuration(doc.WindowMs),
		MinRequests:  doc.MinRequests,
		FailureRatio: doc.FailureRatio,
		OpenDuration: msToDuration(doc.OpenDurationMs),
	}
	if doc.WindowMs <= 0 {
		return CircuitBreakerSettings{}, errors.New("window_ms must be > 0")
	}
	if s.MinRequests <= 0 {
		return CircuitBreakerSettings{}, errors.New("min_requests must be > 0")
	}
	if s.FailureRatio <= 0 || s.FailureRatio > 1 {
		return CircuitBreakerSettings{}, errors.New("failure_ratio must be within (0, 1]")
	}
	if doc.OpenDurationMs <= 0 {
		return CircuitBreakerSettings{}, errors.New("open_duration_ms must be > 0")
	}
	return s, nil
}

func circuitBreakerDefinition() Definition {
	return Definition{
		Key:      GatewayCircuitBreakerKey,
		Category: "gateway",
		Label:    "渠道熔断器",
		Description: "按渠道统计固定窗口错误率,超阈值熔断(open_duration_ms 后半开探测)。" +
			"enabled=false 时放行全部且不记状态。window_ms/open_duration_ms 单位毫秒;failure_ratio∈(0,1]。",
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

// ---- 限流全局默认 ----

// RateLimitFailurePolicy 的合法取值。
const (
	RateLimitFailClosed = "fail_closed"
	RateLimitFailOpen   = "fail_open"
)

// RateLimitDefaultsSettings 是两层限流(线路+用户 / 渠道)的全局默认上限与故障策略。
// RPM/TPM/RPD 为 0 表示该维度默认不限;具体主体可在 api_keys/channels 行覆盖。
type RateLimitDefaultsSettings struct {
	RPM           int64
	TPM           int64
	RPD           int64
	FailurePolicy string
}

// FailOpen 报告计数后端故障时是否放行。
func (s RateLimitDefaultsSettings) FailOpen() bool {
	return s.FailurePolicy == RateLimitFailOpen
}

// DefaultRateLimitDefaultsSettings 与原 RATE_LIMIT_* env 默认一致。
func DefaultRateLimitDefaultsSettings() RateLimitDefaultsSettings {
	return RateLimitDefaultsSettings{RPM: 60, TPM: 0, RPD: 0, FailurePolicy: RateLimitFailClosed}
}

type rateLimitDefaultsDoc struct {
	RPM           int64  `json:"rpm"`
	TPM           int64  `json:"tpm"`
	RPD           int64  `json:"rpd"`
	FailurePolicy string `json:"failure_policy"`
}

func encodeRateLimitDefaultsSettings(s RateLimitDefaultsSettings) json.RawMessage {
	raw, err := json.Marshal(rateLimitDefaultsDoc(s))
	if err != nil {
		panic(fmt.Sprintf("appsettings: encode rate limit defaults: %v", err))
	}
	return raw
}

// DecodeRateLimitDefaultsSettings 解码并校验限流全局默认配置(拒绝未知字段)。
func DecodeRateLimitDefaultsSettings(raw []byte) (RateLimitDefaultsSettings, error) {
	var doc rateLimitDefaultsDoc
	if err := strictUnmarshal(raw, &doc); err != nil {
		return RateLimitDefaultsSettings{}, err
	}
	s := RateLimitDefaultsSettings(doc)
	if s.RPM < 0 || s.TPM < 0 || s.RPD < 0 {
		return RateLimitDefaultsSettings{}, errors.New("rpm/tpm/rpd must be zero or positive")
	}
	if s.FailurePolicy != RateLimitFailClosed && s.FailurePolicy != RateLimitFailOpen {
		return RateLimitDefaultsSettings{}, fmt.Errorf("invalid failure_policy %q (want fail_closed|fail_open)", s.FailurePolicy)
	}
	return s, nil
}

func rateLimitDefaultsDefinition() Definition {
	return Definition{
		Key:      GatewayRateLimitDefaultsKey,
		Category: "gateway",
		Label:    "限流全局默认(RPM/TPM/RPD)",
		Description: "未在 API Key/渠道上单独配置时生效的全局默认上限,0=该维度不限。" +
			"failure_policy 决定 Redis 计数故障时 fail_closed(拒绝)或 fail_open(放行)。",
		HotReload: true,
		Default:   encodeRateLimitDefaultsSettings(DefaultRateLimitDefaultsSettings()),
		Validate: func(raw json.RawMessage) error {
			_, err := DecodeRateLimitDefaultsSettings(raw)
			return err
		},
	}
}

// GatewayRateLimitDefaults 读取当前生效的限流全局默认(解码失败回默认)。
func GatewayRateLimitDefaults(ctx context.Context, store *SettingsStore) RateLimitDefaultsSettings {
	s, err := DecodeRateLimitDefaultsSettings(store.Raw(ctx, GatewayRateLimitDefaultsKey))
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

// ---- 渠道失败软冷却（DEC-029） ----

// DefaultFailureCooldownSetting 是 timeout/5xx 失败后的默认软冷却时长（对齐 LiteLLM 默认 5s 冷却）。
const DefaultFailureCooldownSetting = 5 * time.Second

// DecodeNonNegativeMsSetting 解码 int 毫秒标量值，要求 >= 0（0=关闭该功能），返回 time.Duration。
func DecodeNonNegativeMsSetting(raw []byte) (time.Duration, error) {
	var ms int64
	if err := json.Unmarshal(raw, &ms); err != nil {
		return 0, fmt.Errorf("value must be an integer of milliseconds: %w", err)
	}
	if ms < 0 {
		return 0, errors.New("milliseconds must not be negative")
	}
	return msToDuration(ms), nil
}

func failureCooldownDefinition() Definition {
	return Definition{
		Key:      GatewayFailureCooldownKey,
		Category: "gateway",
		Label:    "渠道失败软冷却",
		Description: "渠道发生 timeout/5xx 类上游故障后，把它软冷却这么长时间：期间新请求的候选排序把该渠道" +
			"demote 到末尾（健康渠道优先），让紧随其后的客户端重试快速绕开慢渠道。只重排不剔除——" +
			"该模型只有一条可用渠道时行为不变（唯一渠道保护）。单位毫秒，0=关闭。",
		HotReload: true,
		Default:   encodeMsSetting(DefaultFailureCooldownSetting),
		Validate: func(raw json.RawMessage) error {
			_, err := DecodeNonNegativeMsSetting(raw)
			return err
		},
	}
}

// GatewayFailureCooldown 读取当前生效的渠道失败软冷却时长（解码失败回默认；0=关闭）。
func GatewayFailureCooldown(ctx context.Context, store *SettingsStore) time.Duration {
	d, err := DecodeNonNegativeMsSetting(store.Raw(ctx, GatewayFailureCooldownKey))
	if err != nil {
		return DefaultFailureCooldownSetting
	}
	return d
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

// RoutingBalanceSettings 控制 balanced 在线路池内是否读取容量，以及是否按剩余容量加权。
type RoutingBalanceSettings struct {
	Enabled           bool
	WeightByRemaining bool
}

func DefaultRoutingBalanceSettings() RoutingBalanceSettings {
	return RoutingBalanceSettings{Enabled: true, WeightByRemaining: true}
}

type routingBalanceDoc struct {
	Enabled           bool `json:"enabled"`
	WeightByRemaining bool `json:"weight_by_remaining"`
}

func encodeRoutingBalanceSettings(s RoutingBalanceSettings) json.RawMessage {
	raw, err := json.Marshal(routingBalanceDoc(s))
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
	return RoutingBalanceSettings(doc), nil
}

func routingBalanceDefinition() Definition {
	return Definition{
		Key:         GatewayRoutingBalanceKey,
		Category:    "gateway",
		Label:       "线路负载均衡",
		Description: "balanced 仅在线路显式渠道池内调度。enabled=false 时池内均匀分流；weight_by_remaining=false 时不读取容量、仅按健康度加权。",
		HotReload:   true,
		Default:     encodeRoutingBalanceSettings(DefaultRoutingBalanceSettings()),
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
