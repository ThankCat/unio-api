package channel

import "time"

// SupplyForm 是渠道的供给形态：凭据从哪里来，容量与健康按哪一层核算。
type SupplyForm string

const (
	// SupplyFormCredential 是存量形态：渠道自身持一份 API Key，容量与健康都在渠道层。
	SupplyFormCredential SupplyForm = "credential"
	// SupplyFormPool 表示渠道下挂一池订阅账号：渠道不持凭据，凭据、并发、健康与用量都在账号上。
	// 协议、adapter、模型绑定、定价与超时仍在渠道层，故 routing 的候选单位不变。
	SupplyFormPool SupplyForm = "pool"
)

// IsPool 让调用方不必逐处比较字符串常量；未知取值一律按 credential 处理，
// 使新增形态在未适配的代码路径上退化为存量行为而不是崩溃。
func (f SupplyForm) IsPool() bool {
	return f == SupplyFormPool
}

// Runtime 表示一次 adapter 调用使用的运行时渠道参数。
type Runtime struct {
	ID     int64
	Name   string
	Origin string
	APIKey string

	// ResponseTimeout 限制非流式「完整响应体 + adapter 解析完成」或流式「拿到上游 HTTP 响应头」，
	// 从 upstream_started_at 起算。0 表示不限制。取值按「全局默认 → 渠道行 → 号池账号」三层继承：
	// 每层 NULL 继承上一层、显式 0 不限制、正数覆写；全局默认为 0（2026-09-05 对齐 Sub2API）。
	ResponseTimeout time.Duration

	// FirstTokenTimeout 只用于流式：从同一个 upstream_started_at 起算，限制「首个上游进展」（§11.2）。
	// 两个预算共享起点，不能等响应头到达后再重新给一份完整首字预算。0 表示不限制；继承规则同 ResponseTimeout。
	FirstTokenTimeout time.Duration

	// ProviderSlug 是业务 provider 标识（providers.slug），供 adapter 选择 stream translator；由 routing 注入。
	ProviderSlug string

	// ProxyURL 是渠道级出站代理（proxies 实体，enabled 才注入；空串直连）。
	// 出站 client 选择回退链：Account.ProxyURL → ProxyURL → 默认直连 client。
	ProxyURL string

	// Account 是池型渠道本次出站冻结的订阅账号身份（credential 型恒为零值）。
	// 由 lifecycle 在 permit 固化后填充：APIKey 换成账号 access token，本结构补齐
	// 上游账号标识与出口代理。adapter 对号池无感知——它只读 Runtime 上的事实。
	Account AccountIdentity
}

// FingerprintMode 是账号级指纹收敛档位（subscription_accounts.fingerprint_mode）。
type FingerprintMode string

const (
	// FingerprintModeOff 不收敛：客户端设备/会话 id 按「客户 × 账号」1:1 映射后出站，
	// 上游看到的设备数与真实客户数一致（默认，与 Sub2API 默认 off 一致）。
	FingerprintModeOff FingerprintMode = "off"
	// FingerprintModeDevice 收敛设备 id：账号内全部客户共用一个由账号种子派生的 installation_id，
	// 上游看到 1 台设备 + 各自独立的会话；会话/对话 id 不合并（与按对话缓存亲和不冲突）。
	FingerprintModeDevice FingerprintMode = "device"
)

// AccountIdentity 是一次出站所用订阅账号的最小身份集。
type AccountIdentity struct {
	// ID 是 subscription_accounts 主键；0 表示本次出站不经账号（credential 型渠道）。
	ID int64
	// UpstreamAccountID 是上游账号标识（Codex 的 chatgpt_account_id），随请求头出站。
	UpstreamAccountID string
	// ProxyURL 是账号绑定出口；空串直连。导入换码、令牌刷新、正式请求三条路径共用。
	ProxyURL string
	// FingerprintMode 是指纹收敛档位；空串按 off 处理。
	FingerprintMode FingerprintMode
	// FingerprintSeed 是系统管理的账号级随机种子（首次开启收敛时生成、永不改写），
	// device 模式据此派生固定的 installation_id；off 模式不使用。
	FingerprintSeed string
}

// ConvergesDevice 表示本次出站需要把设备 id 收敛为账号固定值。
func (a AccountIdentity) ConvergesDevice() bool {
	return a.FingerprintMode == FingerprintModeDevice && a.FingerprintSeed != ""
}
