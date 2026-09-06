package appsettings

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/ThankCat/unio-gateway/internal/core/codexidentity"
)

// 本文件登记 gateway 热路径运行时配置。breaker、rate/concurrency defaults 与
// routing balance 由 Redis committed runtime control 驱动；其余配置继续通过 settings applier
// 热更新。429 cooldown 是 Redis 全局事实，不属于已删除的 timeout/5xx 进程内失败软冷却。
//
// 单位约定(用户决策):时长一律 int 毫秒,字段/key 带 _ms 后缀,
// 不用 "10m" 之类的 duration 字符串;比例用 (0,1] 浮点;计数用普通整数。

// gateway 配置在 app_settings 中的 key。
const (
	GatewayCircuitBreakerKey           = "gateway.circuit_breaker"
	GatewayRequestRateLimitDefaultsKey = "gateway.request_rate_limit_defaults"
	GatewayStreamIdleTimeoutKey        = "gateway.stream_idle_timeout_ms"
	GatewayChannelCooldownKey          = "gateway.channel_ratelimit_cooldown"
	GatewayCredential401ThresholdKey   = "gateway.credential_401_threshold"
	GatewayDefaultResponseTimeoutKey   = "gateway.default_response_timeout_ms"
	GatewayDefaultFirstTokenTimeoutKey = "gateway.default_first_token_timeout_ms"
	GatewayConcurrencyDefaultsKey      = "gateway.concurrency_defaults"
	GatewayRoutingStickyKey            = "gateway.routing_sticky"
	GatewayRoutingBalanceKey           = "gateway.routing_balance"
	GatewayCapacityWaitTimeoutKey      = "gateway.capacity_wait_timeout_ms"
	// GatewayAccountUsagePauseThresholdKey 是池型账号用量自动暂停的水位阈值（百分比整数）。
	GatewayAccountUsagePauseThresholdKey = "gateway.account_usage_pause_threshold_percent"
	// GatewayAccountPoolPreferSoonestResetKey 是池内 use-it-or-lose-it 排序开关。
	GatewayAccountPoolPreferSoonestResetKey = "gateway.account_pool_prefer_soonest_reset"
	// GatewayCodexClientVersionKey 是 Admin 覆写的 Codex 客户端版本号（空串 = 不覆写，跟随自动同步）。
	GatewayCodexClientVersionKey = "gateway.codex_client_version"
	// GatewayCodexClientVersionAutoSyncKey 控制是否采用 worker 自动同步到的官方最新稳定版。
	GatewayCodexClientVersionAutoSyncKey = "gateway.codex_client_version_auto_sync"
	// GatewayCodexClientVersionSyncedKey 是 worker 独占写入的官方最新稳定版快照（Admin 只读展示）。
	GatewayCodexClientVersionSyncedKey = "gateway.codex_client_version_synced"
	// GatewayStreamKeepaliveIntervalKey 是向客户端写 SSE 注释保活帧的间隔（0 = 关闭）。
	GatewayStreamKeepaliveIntervalKey = "gateway.stream_keepalive_interval_ms"
	// GatewayOpenAITTFTModeKey 是 TTFT 统计口径：semantic（首个进展）/ visible（首个可见内容）。
	GatewayOpenAITTFTModeKey = "gateway.openai_ttft_mode"
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

// CircuitBreakerSettings 是 Provider/Channel 全局熔断与 permit 生命周期配置。
// OpenDuration 仅供 Phase E 删除前的旧进程内 breaker 兼容使用，JSON 不再包含该字段。
type CircuitBreakerSettings struct {
	Enabled                           bool
	Window                            time.Duration
	MinRequests                       int
	FailureRatio                      float64
	ConsecutiveFailures               int
	ConsecutiveWindow                 time.Duration
	HalfOpenSuccesses                 int
	AttemptPermitTTL                  time.Duration
	AttemptPermitRenewInterval        time.Duration
	AttemptPermitTerminalTTL          time.Duration
	OriginRevisionOperationTTL        time.Duration
	StatusRevisionOperationTTL        time.Duration
	OpenDurations                     []time.Duration
	ProviderAmbiguousDistinctChannels int
	ProviderAmbiguousDistinctModels   int

	OpenDuration time.Duration
}

// DefaultCircuitBreakerSettings 返回全局熔断与 permit 生命周期的默认值。
func DefaultCircuitBreakerSettings() CircuitBreakerSettings {
	return CircuitBreakerSettings{
		Enabled:                           true,
		Window:                            30 * time.Second,
		MinRequests:                       20,
		FailureRatio:                      0.5,
		ConsecutiveFailures:               3,
		ConsecutiveWindow:                 10 * time.Second,
		HalfOpenSuccesses:                 2,
		AttemptPermitTTL:                  30 * time.Second,
		AttemptPermitRenewInterval:        10 * time.Second,
		AttemptPermitTerminalTTL:          5 * time.Minute,
		OriginRevisionOperationTTL:        24 * time.Hour,
		StatusRevisionOperationTTL:        24 * time.Hour,
		OpenDurations:                     []time.Duration{15 * time.Second, 30 * time.Second, time.Minute, 2 * time.Minute, 5 * time.Minute},
		ProviderAmbiguousDistinctChannels: 2,
		ProviderAmbiguousDistinctModels:   2,
		OpenDuration:                      15 * time.Second,
	}
}

type circuitBreakerDoc struct {
	Enabled                           bool    `json:"enabled"`
	WindowMs                          int64   `json:"window_ms"`
	MinRequests                       int     `json:"min_requests"`
	FailureRatio                      float64 `json:"failure_ratio"`
	ConsecutiveFailures               int     `json:"consecutive_failures"`
	ConsecutiveWindowMs               int64   `json:"consecutive_window_ms"`
	HalfOpenSuccesses                 int     `json:"half_open_successes"`
	AttemptPermitTTLMs                int64   `json:"attempt_permit_ttl_ms"`
	AttemptPermitRenewIntervalMs      int64   `json:"attempt_permit_renew_interval_ms"`
	AttemptPermitTerminalTTLMs        int64   `json:"attempt_permit_terminal_ttl_ms"`
	OriginRevisionOperationTTLMs      int64   `json:"origin_revision_operation_ttl_ms"`
	StatusRevisionOperationTTLMs      int64   `json:"status_revision_operation_ttl_ms"`
	OpenDurationsMs                   []int64 `json:"open_durations_ms"`
	ProviderAmbiguousDistinctChannels int     `json:"provider_ambiguous_distinct_channels"`
	ProviderAmbiguousDistinctModels   int     `json:"provider_ambiguous_distinct_models"`
}

func encodeCircuitBreakerSettings(s CircuitBreakerSettings) json.RawMessage {
	openDurations := make([]int64, 0, len(s.OpenDurations))
	for _, d := range s.OpenDurations {
		openDurations = append(openDurations, durationToMs(d))
	}
	raw, err := json.Marshal(circuitBreakerDoc{
		Enabled:                           s.Enabled,
		WindowMs:                          durationToMs(s.Window),
		MinRequests:                       s.MinRequests,
		FailureRatio:                      s.FailureRatio,
		ConsecutiveFailures:               s.ConsecutiveFailures,
		ConsecutiveWindowMs:               durationToMs(s.ConsecutiveWindow),
		HalfOpenSuccesses:                 s.HalfOpenSuccesses,
		AttemptPermitTTLMs:                durationToMs(s.AttemptPermitTTL),
		AttemptPermitRenewIntervalMs:      durationToMs(s.AttemptPermitRenewInterval),
		AttemptPermitTerminalTTLMs:        durationToMs(s.AttemptPermitTerminalTTL),
		OriginRevisionOperationTTLMs:      durationToMs(s.OriginRevisionOperationTTL),
		StatusRevisionOperationTTLMs:      durationToMs(s.StatusRevisionOperationTTL),
		OpenDurationsMs:                   openDurations,
		ProviderAmbiguousDistinctChannels: s.ProviderAmbiguousDistinctChannels,
		ProviderAmbiguousDistinctModels:   s.ProviderAmbiguousDistinctModels,
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
		Enabled:                           doc.Enabled,
		Window:                            msToDuration(doc.WindowMs),
		MinRequests:                       doc.MinRequests,
		FailureRatio:                      doc.FailureRatio,
		ConsecutiveFailures:               doc.ConsecutiveFailures,
		ConsecutiveWindow:                 msToDuration(doc.ConsecutiveWindowMs),
		HalfOpenSuccesses:                 doc.HalfOpenSuccesses,
		AttemptPermitTTL:                  msToDuration(doc.AttemptPermitTTLMs),
		AttemptPermitRenewInterval:        msToDuration(doc.AttemptPermitRenewIntervalMs),
		AttemptPermitTerminalTTL:          msToDuration(doc.AttemptPermitTerminalTTLMs),
		OriginRevisionOperationTTL:        msToDuration(doc.OriginRevisionOperationTTLMs),
		StatusRevisionOperationTTL:        msToDuration(doc.StatusRevisionOperationTTLMs),
		ProviderAmbiguousDistinctChannels: doc.ProviderAmbiguousDistinctChannels,
		ProviderAmbiguousDistinctModels:   doc.ProviderAmbiguousDistinctModels,
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
	if doc.OriginRevisionOperationTTLMs <= 0 || doc.StatusRevisionOperationTTLMs <= 0 {
		return CircuitBreakerSettings{}, errors.New("origin revision operation ttl must be > 0")
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
	if s.ProviderAmbiguousDistinctChannels < 2 || s.ProviderAmbiguousDistinctModels < 2 {
		return CircuitBreakerSettings{}, errors.New("provider ambiguous distinct thresholds must be >= 2")
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

// ---- 请求入口限流默认 ----

// RateLimitDefaultsSettings 是请求入口的 RPM/RPD 默认。
// 为 0 表示该维度默认不限；单个用户可在 users 行覆盖。
// 这里没有 TPM：Unio 不限制 TPM，token 吞吐只做观测（§8）。
type RateLimitDefaultsSettings struct {
	RPM int64
	RPD int64
}

// DefaultRateLimitDefaultsSettings 默认两维均不限；用户级显式限额仍可覆盖。
func DefaultRateLimitDefaultsSettings() RateLimitDefaultsSettings {
	return RateLimitDefaultsSettings{RPM: 0, RPD: 0}
}

type rateLimitDefaultsDoc struct {
	RPM int64 `json:"rpm"`
	RPD int64 `json:"rpd"`
}

func encodeRateLimitDefaultsSettings(s RateLimitDefaultsSettings) json.RawMessage {
	raw, err := json.Marshal(rateLimitDefaultsDoc(s))
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
	s := RateLimitDefaultsSettings(doc)
	if s.RPM < 0 || s.RPD < 0 {
		return RateLimitDefaultsSettings{}, errors.New("rpm/rpd must be zero or positive")
	}
	return s, nil
}

func requestRateLimitDefaultsDefinition() Definition {
	return Definition{
		Key:      GatewayRequestRateLimitDefaultsKey,
		Category: "gateway",
		Label:    "请求默认限流(RPM/RPD)",
		Description: "用户未单独配置时按用户生效的默认上限，0=该维度不限。" +
			"Redis revisioned control 是执行权威；Redis 或 BreakerStore 故障固定拒绝准入，不提供绕过开关。",
		HotReload: true,
		Default:   encodeRateLimitDefaultsSettings(DefaultRateLimitDefaultsSettings()),
		Validate: func(raw json.RawMessage) error {
			_, err := DecodeRateLimitDefaultsSettings(raw)
			return err
		},
	}
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

// ---- 标量项:上游超时 / 保活 / TTFT 口径 / 凭据 401 阈值 ----
//
// 上游超时只有两项：响应超时与首字预算。两项都按「全局默认 → 渠道行 → 号池账号」三层继承，每层 NULL 继承
// 上一层、显式 0 表示不限制、正数覆写；全局默认均为 0（2026-09-05 对齐 Sub2API，不按协议族分叉）。
// 首字预算由「上游进展」解除，reasoning 长思考不再被掐断；永不结束的流由上游数据间隔看门狗兜底：
//   - 上游数据间隔（idle）：从响应头起生效、任何上游数据重置，默认 180s，0 = 关闭；
//   - 下游保活注释帧：默认 10s，0 = 关闭；
//   - TTFT 口径：默认 semantic（首个进展），可切 visible（首个可见内容）。

// DefaultStreamIdleTimeoutSetting 与 Sub2API stream_data_interval_timeout 默认一致。
const DefaultStreamIdleTimeoutSetting = 180 * time.Second

// DefaultResponseTimeoutSetting 是系统默认响应超时：0 = 不限制。
const DefaultResponseTimeoutSetting = 0 * time.Second

// DefaultFirstTokenTimeoutSetting 是系统默认首字预算：0 = 不限制（Sub2API 默认）。
const DefaultFirstTokenTimeoutSetting = 0 * time.Second

// DefaultStreamKeepaliveIntervalSetting 与 Sub2API stream_keepalive_interval 默认一致。
const DefaultStreamKeepaliveIntervalSetting = 10 * time.Second

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

// DecodeNonNegativeMsSetting 解码 int 毫秒标量值,允许 0(表示关闭该等待/超时),拒绝负值。
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

func streamIdleTimeoutDefinition() Definition {
	return Definition{
		Key:      GatewayStreamIdleTimeoutKey,
		Category: "gateway",
		Label:    "流式上游数据间隔超时",
		Description: "流式上游从响应头到达起,相邻两次数据(协议事件、SSE 注释/空行)之间的最大静默时长,兜底半开/挂死连接。" +
			"单位毫秒,0=关闭。模型思考期间上游会持续推 reasoning 事件,不会被它误杀;" +
			"它不影响首字预算(心跳能重置它,但不能解除首字计时)。默认 180000(与 Sub2API 一致)。",
		HotReload: true,
		Default:   encodeMsSetting(DefaultStreamIdleTimeoutSetting),
		Validate: func(raw json.RawMessage) error {
			_, err := DecodeNonNegativeMsSetting(raw)
			return err
		},
	}
}

// GatewayStreamIdleTimeout 读取当前生效的流式数据间隔超时(解码失败回默认;0 = 关闭)。
func GatewayStreamIdleTimeout(ctx context.Context, store *SettingsStore) time.Duration {
	d, err := DecodeNonNegativeMsSetting(store.Raw(ctx, GatewayStreamIdleTimeoutKey))
	if err != nil {
		return DefaultStreamIdleTimeoutSetting
	}
	return d
}

func streamKeepaliveIntervalDefinition() Definition {
	return Definition{
		Key:      GatewayStreamKeepaliveIntervalKey,
		Category: "gateway",
		Label:    "流式下游保活间隔",
		Description: "向客户端写 SSE 注释帧(\": \\n\\n\")的间隔:超过该时长没有任何下游写出就补一帧,防止代理/客户端空闲断开。" +
			"首字之前也发;保活帧不算客户端已收到输出,不影响首字前换渠道。单位毫秒,0=关闭。默认 10000(与 Sub2API 一致)。",
		HotReload: true,
		Default:   encodeMsSetting(DefaultStreamKeepaliveIntervalSetting),
		Validate: func(raw json.RawMessage) error {
			_, err := DecodeNonNegativeMsSetting(raw)
			return err
		},
	}
}

// GatewayStreamKeepaliveInterval 读取当前生效的下游保活间隔(解码失败回默认;0 = 关闭)。
func GatewayStreamKeepaliveInterval(ctx context.Context, store *SettingsStore) time.Duration {
	d, err := DecodeNonNegativeMsSetting(store.Raw(ctx, GatewayStreamKeepaliveIntervalKey))
	if err != nil {
		return DefaultStreamKeepaliveIntervalSetting
	}
	return d
}

// DefaultOpenAITTFTMode 与 Sub2API openai_ttft_mode 默认一致。
const DefaultOpenAITTFTMode = "semantic"

// DecodeOpenAITTFTMode 解码 TTFT 口径,只接受 semantic / visible。
func DecodeOpenAITTFTMode(raw []byte) (string, error) {
	var mode string
	if err := json.Unmarshal(raw, &mode); err != nil {
		return "", fmt.Errorf("value must be a JSON string: %w", err)
	}
	switch strings.TrimSpace(mode) {
	case "semantic", "visible":
		return strings.TrimSpace(mode), nil
	default:
		return "", errors.New("ttft mode must be one of: semantic, visible")
	}
}

func openAITTFTModeDefinition() Definition {
	return Definition{
		Key:      GatewayOpenAITTFTModeKey,
		Category: "gateway",
		Label:    "TTFT 统计口径",
		Description: "首字(TTFT)记在哪个事件上:semantic=跳过协议前导后的首个结构性事件(reasoning 模型思考阶段的 " +
			"reasoning item 即算,反映\"模型开始工作\");visible=首个携带客户可用内容(文本/推理摘要/工具参数)的事件。" +
			"只影响上游 TTFT 样本、渠道评分与 Gateway TTFT,不影响计费(结算只看是否已交付可见内容)。默认 semantic(与 Sub2API 一致)。",
		HotReload: true,
		Default:   json.RawMessage(`"` + DefaultOpenAITTFTMode + `"`),
		Validate: func(raw json.RawMessage) error {
			_, err := DecodeOpenAITTFTMode(raw)
			return err
		},
	}
}

// GatewayOpenAITTFTMode 读取当前生效的 TTFT 口径(解码失败回默认 semantic)。
func GatewayOpenAITTFTMode(ctx context.Context, store *SettingsStore) string {
	mode, err := DecodeOpenAITTFTMode(store.Raw(ctx, GatewayOpenAITTFTModeKey))
	if err != nil {
		return DefaultOpenAITTFTMode
	}
	return mode
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

// ---- 订阅账号用量自动暂停阈值（号池改造第五节） ----

// DefaultAccountUsagePauseThresholdPercent 与 subscription/health 的代码默认一致（90%）。
const DefaultAccountUsagePauseThresholdPercent = 90

func accountUsagePauseThresholdDefinition() Definition {
	return Definition{
		Key:      GatewayAccountUsagePauseThresholdKey,
		Category: "gateway",
		Label:    "账号用量暂停阈值（%）",
		Description: "池型渠道账号的 5h/7d 任一用量窗口达到该百分比后，提前移出调度（到窗口重置自动恢复）。" +
			"取值 1~100；100 等于只在完全打满时暂停。热更新，下一次用量观测即生效。",
		HotReload: true,
		Default:   json.RawMessage(fmt.Sprintf("%d", DefaultAccountUsagePauseThresholdPercent)),
		Validate: func(raw json.RawMessage) error {
			n, err := DecodePositiveIntSetting(raw)
			if err != nil {
				return err
			}
			if n > 100 {
				return fmt.Errorf("threshold must be within 1..100, got %d", n)
			}
			return nil
		},
	}
}

func accountPoolPreferSoonestResetDefinition() Definition {
	return Definition{
		Key:      GatewayAccountPoolPreferSoonestResetKey,
		Category: "gateway",
		Label:    "号池优先使用最早重置的账号",
		Description: "开启后，池内选号在优先级之后先选「5h 用量窗口最早重置」的账号（use-it-or-lose-it：" +
			"订阅额度过期作废，快过期的窗口先用掉），再看负载率与最久未用。默认关闭（保持负载均衡优先）。",
		HotReload: true,
		Default:   json.RawMessage("false"),
		Validate: func(raw json.RawMessage) error {
			var v bool
			return json.Unmarshal(raw, &v)
		},
	}
}

// GatewayAccountPoolPreferSoonestReset 读取池内「最早重置优先」排序开关（解码失败回 false）。
func GatewayAccountPoolPreferSoonestReset(ctx context.Context, store *SettingsStore) bool {
	var v bool
	if err := json.Unmarshal(store.Raw(ctx, GatewayAccountPoolPreferSoonestResetKey), &v); err != nil {
		return false
	}
	return v
}

// GatewayAccountUsagePauseThreshold 读取当前生效的账号用量暂停阈值（解码失败回默认 90）。
func GatewayAccountUsagePauseThreshold(ctx context.Context, store *SettingsStore) float64 {
	n, err := DecodePositiveIntSetting(store.Raw(ctx, GatewayAccountUsagePauseThresholdKey))
	if err != nil || n > 100 {
		return DefaultAccountUsagePauseThresholdPercent
	}
	return float64(n)
}

// ---- 在途并发全局默认（DEC-029） ----

// ConcurrencyDefaultsSettings 是两级在途并发上限的全局默认（0=该级不限）。
// KeyLimit 作用于用户（ingress 中间件，多余并发立即 429）；
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
		Description: "「同时进行中」请求数上限（含整段流式传输），0=不限。key_limit 作用于用户" +
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

// ---- balanced 容量调度 ----

// RoutingBalanceSettings 是 objective_v1 五项评分配置（§7/§14.6）：
// 成本 / 渠道并发容量 / TTFT / 错误率 / Priority 五项百分比权重之和必须为 100。
// TTFT 与错误率各自使用滚动窗口 + 线性惩罚参数；无样本时对应指标分为 100。
type RoutingBalanceSettings struct {
	CostWeightPct        int
	ConcurrencyWeightPct int
	TTFTWeightPct        int
	ErrorRateWeightPct   int
	PriorityWeightPct    int

	TTFTWindow               time.Duration
	TTFTPenaltyUnit          time.Duration
	TTFTPenaltyPointsPerUnit float64

	ErrorWindow                  time.Duration
	ErrorPenaltyPointsPerPercent float64
}

func DefaultRoutingBalanceSettings() RoutingBalanceSettings {
	return RoutingBalanceSettings{
		CostWeightPct:                25,
		ConcurrencyWeightPct:         20,
		TTFTWeightPct:                25,
		ErrorRateWeightPct:           20,
		PriorityWeightPct:            10,
		TTFTWindow:                   30 * time.Minute,
		TTFTPenaltyUnit:              time.Second,
		TTFTPenaltyPointsPerUnit:     2.5,
		ErrorWindow:                  30 * time.Minute,
		ErrorPenaltyPointsPerPercent: 2.5,
	}
}

type routingBalanceDoc struct {
	CostWeightPct                int     `json:"cost_weight_pct"`
	ConcurrencyWeightPct         int     `json:"concurrency_weight_pct"`
	TTFTWeightPct                int     `json:"ttft_weight_pct"`
	ErrorRateWeightPct           int     `json:"error_rate_weight_pct"`
	PriorityWeightPct            int     `json:"priority_weight_pct"`
	TTFTWindowMs                 int64   `json:"ttft_window_ms"`
	TTFTPenaltyUnitMs            int64   `json:"ttft_penalty_unit_ms"`
	TTFTPenaltyPointsPerUnit     float64 `json:"ttft_penalty_points_per_unit"`
	ErrorWindowMs                int64   `json:"error_window_ms"`
	ErrorPenaltyPointsPerPercent float64 `json:"error_penalty_points_per_percent"`
}

func encodeRoutingBalanceSettings(s RoutingBalanceSettings) json.RawMessage {
	raw, err := json.Marshal(routingBalanceDoc{
		CostWeightPct:                s.CostWeightPct,
		ConcurrencyWeightPct:         s.ConcurrencyWeightPct,
		TTFTWeightPct:                s.TTFTWeightPct,
		ErrorRateWeightPct:           s.ErrorRateWeightPct,
		PriorityWeightPct:            s.PriorityWeightPct,
		TTFTWindowMs:                 durationToMs(s.TTFTWindow),
		TTFTPenaltyUnitMs:            durationToMs(s.TTFTPenaltyUnit),
		TTFTPenaltyPointsPerUnit:     s.TTFTPenaltyPointsPerUnit,
		ErrorWindowMs:                durationToMs(s.ErrorWindow),
		ErrorPenaltyPointsPerPercent: s.ErrorPenaltyPointsPerPercent,
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
	settings := RoutingBalanceSettings{
		CostWeightPct:                doc.CostWeightPct,
		ConcurrencyWeightPct:         doc.ConcurrencyWeightPct,
		TTFTWeightPct:                doc.TTFTWeightPct,
		ErrorRateWeightPct:           doc.ErrorRateWeightPct,
		PriorityWeightPct:            doc.PriorityWeightPct,
		TTFTWindow:                   msToDuration(doc.TTFTWindowMs),
		TTFTPenaltyUnit:              msToDuration(doc.TTFTPenaltyUnitMs),
		TTFTPenaltyPointsPerUnit:     doc.TTFTPenaltyPointsPerUnit,
		ErrorWindow:                  msToDuration(doc.ErrorWindowMs),
		ErrorPenaltyPointsPerPercent: doc.ErrorPenaltyPointsPerPercent,
	}
	return validateRoutingBalanceSettings(settings)
}

func validateRoutingBalanceSettings(settings RoutingBalanceSettings) (RoutingBalanceSettings, error) {
	weights := []int{
		settings.CostWeightPct,
		settings.ConcurrencyWeightPct,
		settings.TTFTWeightPct,
		settings.ErrorRateWeightPct,
		settings.PriorityWeightPct,
	}
	sum := 0
	for _, weight := range weights {
		if weight < 0 || weight > 100 {
			return RoutingBalanceSettings{}, errors.New("routing balance weights must be within [0, 100]")
		}
		sum += weight
	}
	if sum != 100 {
		return RoutingBalanceSettings{}, errors.New("routing balance weights must sum to 100")
	}
	if settings.TTFTWindow <= 0 {
		return RoutingBalanceSettings{}, errors.New("ttft_window_ms must be > 0")
	}
	if settings.TTFTPenaltyUnit <= 0 {
		return RoutingBalanceSettings{}, errors.New("ttft_penalty_unit_ms must be > 0")
	}
	if settings.TTFTPenaltyPointsPerUnit <= 0 || settings.TTFTPenaltyPointsPerUnit > 100 {
		return RoutingBalanceSettings{}, errors.New("ttft_penalty_points_per_unit must be within (0, 100]")
	}
	if settings.ErrorWindow <= 0 {
		return RoutingBalanceSettings{}, errors.New("error_window_ms must be > 0")
	}
	if settings.ErrorPenaltyPointsPerPercent <= 0 || settings.ErrorPenaltyPointsPerPercent > 100 {
		return RoutingBalanceSettings{}, errors.New("error_penalty_points_per_percent must be within (0, 100]")
	}
	return settings, nil
}

func routingBalanceDefinition() Definition {
	return Definition{
		Key:      GatewayRoutingBalanceKey,
		Category: "gateway",
		Label:    "渠道负载均衡",
		Description: "在该模型的可用渠道之间按成本、渠道并发容量、TTFT、错误率与 Priority 五项客观分确定性排序。" +
			"五项百分比权重之和必须为 100；TTFT 与错误率无样本时对应指标分为 100。",
		HotReload: true,
		Default:   encodeRoutingBalanceSettings(DefaultRoutingBalanceSettings()),
		Validate: func(raw json.RawMessage) error {
			_, err := DecodeRoutingBalanceSettings(raw)
			return err
		},
	}
}

// ---- 会话粘性路由全局默认（大 uncache 缺口 P0） ----

// RoutingStickySettings 是跨协议会话 sticky 的全局默认配置。
// 渠道行 sticky_enabled 可覆盖 EnabledDefault（NULL=继承此默认）；读取命中本身不续期，只有原绑定
// 渠道完整成功后才按 CAS 把 TTL 滑动续期为完整时长。它与上游 prompt cache TTL 解耦。容量等待不属于 sticky，见
// GatewayCapacityWaitTimeoutKey（§9.4 全池共享短等）。
type RoutingStickySettings struct {
	EnabledDefault bool
	TTL            time.Duration
}

// DefaultRoutingStickySettings 默认开启 sticky：TTL 30min。
func DefaultRoutingStickySettings() RoutingStickySettings {
	return RoutingStickySettings{
		EnabledDefault: true,
		TTL:            30 * time.Minute,
	}
}

type routingStickyDoc struct {
	EnabledDefault bool  `json:"enabled_default"`
	TTLMs          int64 `json:"ttl_ms"`
}

func encodeRoutingStickySettings(s RoutingStickySettings) json.RawMessage {
	raw, err := json.Marshal(routingStickyDoc{
		EnabledDefault: s.EnabledDefault,
		TTLMs:          durationToMs(s.TTL),
	})
	if err != nil {
		panic(fmt.Sprintf("appsettings: encode routing sticky settings: %v", err))
	}
	return raw
}

// DecodeRoutingStickySettings 解码并校验会话粘性配置（时长为 int 毫秒；拒绝未知字段）。ttl_ms 必须 > 0。
func DecodeRoutingStickySettings(raw []byte) (RoutingStickySettings, error) {
	var doc routingStickyDoc
	if err := strictUnmarshal(raw, &doc); err != nil {
		return RoutingStickySettings{}, err
	}
	if doc.TTLMs <= 0 {
		return RoutingStickySettings{}, errors.New("ttl_ms must be > 0")
	}
	return RoutingStickySettings{
		EnabledDefault: doc.EnabledDefault,
		TTL:            msToDuration(doc.TTLMs),
	}, nil
}

func routingStickyDefinition() Definition {
	return Definition{
		Key:      GatewayRoutingStickyKey,
		Category: "gateway",
		Label:    "会话粘性路由(sticky)",
		Description: "同会话请求钉住上次成功渠道以保上游 prompt cache（OpenAI prompt_cache_key / " +
			"Claude Code 会话头）。enabled_default 是渠道未单独配置时的默认开关；读取命中本身不续期，" +
			"只有原绑定渠道完整成功后把 ttl_ms 重新延长；到期后回落负载均衡排序。",
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

// DefaultCapacityWaitTimeoutSetting 是全池并发短等的默认预算（§9.4/D9）。
const DefaultCapacityWaitTimeoutSetting = time.Second

func capacityWaitTimeoutDefinition() Definition {
	return Definition{
		Key:      GatewayCapacityWaitTimeoutKey,
		Category: "gateway",
		Label:    "全池并发短等预算",
		Description: "候选池内所有渠道同时并发满时,整个请求最多共享等待多久再重扫一次(单位毫秒)。" +
			"等待是全池共享且每请求仅一次,不随渠道数量线性增长;等待期间不占用任何渠道并发。" +
			"仅并发满触发;熔断、权限、上游 429 冷却一律不等待,直接换渠道或返回。",
		HotReload: true,
		Default:   encodeMsSetting(DefaultCapacityWaitTimeoutSetting),
		Validate: func(raw json.RawMessage) error {
			_, err := DecodeNonNegativeMsSetting(raw)
			return err
		},
	}
}

// GatewayCapacityWaitTimeout 读取当前生效的全池短等预算(解码失败回默认)。0 表示关闭短等。
func GatewayCapacityWaitTimeout(ctx context.Context, store *SettingsStore) time.Duration {
	d, err := DecodeNonNegativeMsSetting(store.Raw(ctx, GatewayCapacityWaitTimeoutKey))
	if err != nil {
		return DefaultCapacityWaitTimeoutSetting
	}
	return d
}

func defaultResponseTimeoutDefinition() Definition {
	return Definition{
		Key:      GatewayDefaultResponseTimeoutKey,
		Category: "gateway",
		Label:    "默认响应超时",
		Description: "渠道与账号都未配置 response_timeout_ms 时的全局默认(单位毫秒),0=不限制。" +
			"非流式覆盖连接、响应头、完整响应体与解析;流式只覆盖「拿到上游响应头」。" +
			"三层继承:全局默认 → 渠道行 → 号池账号,每层留空继承上一层、显式 0 不限制、正数覆写。" +
			"不影响「渠道巡检」探测超时(admin_backend.channel_test.probe_timeout_ms)——检测专用、独立配置。默认 0。",
		HotReload: true,
		Default:   encodeMsSetting(DefaultResponseTimeoutSetting),
		Validate: func(raw json.RawMessage) error {
			_, err := DecodeNonNegativeMsSetting(raw)
			return err
		},
	}
}

// GatewayDefaultResponseTimeout 读取当前生效的默认响应超时(解码失败回默认;0 = 不限制)。
func GatewayDefaultResponseTimeout(ctx context.Context, store *SettingsStore) time.Duration {
	d, err := DecodeNonNegativeMsSetting(store.Raw(ctx, GatewayDefaultResponseTimeoutKey))
	if err != nil {
		return DefaultResponseTimeoutSetting
	}
	return d
}

func defaultFirstTokenTimeoutDefinition() Definition {
	return Definition{
		Key:      GatewayDefaultFirstTokenTimeoutKey,
		Category: "gateway",
		Label:    "默认首字超时",
		Description: "流式请求从发起上游调用到「首个上游进展」的最大等待(单位毫秒),0=不限制。" +
			"进展指跳过协议前导(response.created/in_progress)后的首个结构性事件,reasoning 模型思考阶段的 " +
			"reasoning item 事件即算,因此长思考不会被本项掐断;进展之后由「流式上游数据间隔超时」守护。" +
			"它与响应超时同一起点;HTTP 响应头、SSE 空行、注释和纯心跳都不解除首字计时。" +
			"三层继承:全局默认 → 渠道行 → 号池账号,每层留空继承上一层、显式 0 不限制、正数覆写;非流式不使用本项。默认 0。",
		HotReload: true,
		Default:   encodeMsSetting(DefaultFirstTokenTimeoutSetting),
		Validate: func(raw json.RawMessage) error {
			_, err := DecodeNonNegativeMsSetting(raw)
			return err
		},
	}
}

// GatewayDefaultFirstTokenTimeout 读取当前生效的默认首字超时(解码失败回默认;0 = 不限制)。
func GatewayDefaultFirstTokenTimeout(ctx context.Context, store *SettingsStore) time.Duration {
	d, err := DecodeNonNegativeMsSetting(store.Raw(ctx, GatewayDefaultFirstTokenTimeoutKey))
	if err != nil {
		return DefaultFirstTokenTimeoutSetting
	}
	return d
}

// ---- Codex 客户端身份版本（号池出站身份，对齐 Sub2API 的版本跟随） ----
//
// 上游 /backend-api/codex 在容量紧张时按客户端身份分优先级降载，陈旧版本先被丢弃，所以出站声明的
// 版本必须跟着官方发布走。生效优先级：Admin 覆写 → worker 自动同步值（开启时）→ 编译期基线；
// 任一值低于基线回落基线。三个头（originator / User-Agent / version）由 codexidentity 同源渲染。

// CodexClientVersionSynced 是 worker 写入的官方最新稳定版快照。
type CodexClientVersionSynced struct {
	Version  string    `json:"version"`
	SyncedAt time.Time `json:"synced_at"`
	Source   string    `json:"source"`
}

// DecodeCodexClientVersionOverride 解码 Admin 覆写的版本号：空串合法（不覆写），否则必须是官方形态。
func DecodeCodexClientVersionOverride(raw []byte) (string, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || trimmed[0] != '"' {
		return "", errors.New("value must be a JSON string")
	}
	var v string
	if err := json.Unmarshal(trimmed, &v); err != nil {
		return "", fmt.Errorf("value must be a JSON string: %w", err)
	}
	if strings.TrimSpace(v) == "" {
		return "", nil
	}
	normalized := codexidentity.NormalizeVersion(v)
	if normalized == "" {
		return "", errors.New("version must look like 0.152.1 or 0.153.0-alpha.5")
	}
	return normalized, nil
}

// DecodeCodexClientVersionSynced 解码自动同步快照；null 表示尚未同步过。
func DecodeCodexClientVersionSynced(raw []byte) (CodexClientVersionSynced, error) {
	if len(bytes.TrimSpace(raw)) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return CodexClientVersionSynced{}, nil
	}
	var doc CodexClientVersionSynced
	if err := strictUnmarshal(raw, &doc); err != nil {
		return CodexClientVersionSynced{}, err
	}
	if doc.Version != "" && codexidentity.NormalizeVersion(doc.Version) == "" {
		return CodexClientVersionSynced{}, errors.New("synced version must look like 0.152.1")
	}
	return doc, nil
}

// EncodeCodexClientVersionSynced 编码自动同步快照（worker 写入用）。
func EncodeCodexClientVersionSynced(s CodexClientVersionSynced) json.RawMessage {
	raw, err := json.Marshal(s)
	if err != nil {
		panic(fmt.Sprintf("appsettings: encode codex client version synced: %v", err))
	}
	return raw
}

func codexClientVersionDefinition() Definition {
	return Definition{
		Key:      GatewayCodexClientVersionKey,
		Category: "gateway",
		Label:    "Codex 客户端版本覆写",
		Description: "号池出站向 Codex 后端声明的客户端版本（写进 User-Agent、version 头与模型清单查询参数）。" +
			"留空 = 跟随自动同步的官方最新稳定版；填写后固定为该版本。上游会优先降载陈旧版本，" +
			"低于基线 " + codexidentity.BaselineVersion + " 的值一律按基线出站。形态如 0.152.1 或 0.153.0-alpha.5。",
		HotReload: true,
		Default:   json.RawMessage(`""`),
		Validate: func(raw json.RawMessage) error {
			_, err := DecodeCodexClientVersionOverride(raw)
			return err
		},
	}
}

func codexClientVersionAutoSyncDefinition() Definition {
	return Definition{
		Key:      GatewayCodexClientVersionAutoSyncKey,
		Category: "gateway",
		Label:    "Codex 客户端版本自动同步",
		Description: "开启后 worker 每 6 小时从 GitHub openai/codex 的最新正式发布同步版本号（预发布不算），" +
			"未填覆写时即以同步值出站，不必为跟版本而发版。关闭后只用覆写值，覆写也为空时用基线。",
		HotReload: true,
		Default:   json.RawMessage("true"),
		Validate: func(raw json.RawMessage) error {
			var v bool
			return json.Unmarshal(raw, &v)
		},
	}
}

func codexClientVersionSyncedDefinition() Definition {
	return Definition{
		Key:      GatewayCodexClientVersionSyncedKey,
		Category: "gateway",
		Label:    "Codex 客户端版本（自动同步值）",
		Description: "worker 最近一次从 GitHub openai/codex 同步到的官方最新稳定版及时间，只读。" +
			"null 表示尚未同步成功过。",
		HotReload: true,
		ReadOnly:  true,
		Default:   json.RawMessage("null"),
		Validate: func(raw json.RawMessage) error {
			_, err := DecodeCodexClientVersionSynced(raw)
			return err
		},
	}
}

// GatewayCodexClientVersion 返回当前生效的 Codex 客户端版本（覆写 → 同步值 → 基线，施加下限）。
// 解码失败的项按缺省处理，保证任何时候都能得到一个合法版本。
func GatewayCodexClientVersion(ctx context.Context, store *SettingsStore) string {
	override, err := DecodeCodexClientVersionOverride(store.Raw(ctx, GatewayCodexClientVersionKey))
	if err != nil {
		override = ""
	}
	autoSync := true
	if raw := store.Raw(ctx, GatewayCodexClientVersionAutoSyncKey); len(raw) > 0 {
		if err := json.Unmarshal(raw, &autoSync); err != nil {
			autoSync = true
		}
	}
	synced, err := DecodeCodexClientVersionSynced(store.Raw(ctx, GatewayCodexClientVersionSyncedKey))
	if err != nil {
		synced = CodexClientVersionSynced{}
	}
	return codexidentity.EffectiveVersion(override, autoSync, synced.Version)
}

// 售价折扣不再是全局设置：它随每条 model_prices 行走（sale_discount），
// 与同行的绝对售价共同决定该模型的客户售价。设置项、解码器与 Router 侧的热改入口
// 一并删除，定价的唯一来源是价格行本身。
