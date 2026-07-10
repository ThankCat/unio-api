package failure

import "strings"

// Code 是 Unio 内部稳定错误码。
type Code string

// Category 表示错误所属的大类，用于日志、指标和后续分层处理。
type Category string

const (
	// CategoryUnknown 表示无法从错误码推导出稳定分类。
	CategoryUnknown Category = "unknown"

	// CategoryConfig 表示启动配置或环境变量错误。
	CategoryConfig Category = "config"

	// CategoryHTTP 表示 HTTP 请求解析、协议或响应写出错误。
	CategoryHTTP Category = "http"

	// CategoryDependency 表示 PostgreSQL、Redis 等外部依赖连接错误。
	CategoryDependency Category = "dependency"

	// CategoryAuth 表示请求认证错误。
	CategoryAuth Category = "auth"

	// CategoryAPIKey 表示 API Key 管理错误。
	CategoryAPIKey Category = "apikey"

	// CategoryRateLimit 表示限流错误。
	CategoryRateLimit Category = "ratelimit"

	// CategoryRouting 表示模型路由错误。
	CategoryRouting Category = "routing"

	// CategoryCredential 表示凭据解析错误。
	CategoryCredential Category = "credential"

	// CategoryModelCatalog 表示模型目录查询错误。
	CategoryModelCatalog Category = "modelcatalog"

	// CategoryCapability 表示模型/渠道能力数据访问与校验错误。
	CategoryCapability Category = "capability"

	// CategoryAdapter 表示上游 adapter 调用或协议转换错误。
	CategoryAdapter Category = "adapter"

	// CategorySSE 表示 SSE 解析错误。
	CategorySSE Category = "sse"

	// CategoryBilling 表示账单金额计算错误。
	CategoryBilling Category = "billing"

	// CategoryLedger 表示余额账本错误。
	CategoryLedger Category = "ledger"

	// CategoryGateway 表示 gateway 编排错误。
	CategoryGateway Category = "gateway"

	// CategoryRequestLog 表示请求日志事实写入错误。
	CategoryRequestLog Category = "requestlog"

	// CategoryBootstrap 表示启动装配或 preflight 检查错误。
	CategoryBootstrap Category = "bootstrap"

	// CategoryObservability 表示可观测性基础设施（tracing/metrics 导出器）初始化错误。
	CategoryObservability Category = "observability"

	// CategoryAdminAuth 表示 admin 管理端认证错误。
	CategoryAdminAuth Category = "adminauth"

	// CategoryAdmin 表示 admin 管理端业务（provider/channel 等 CRUD）错误。
	CategoryAdmin Category = "admin"
)

// Category 从错误码前缀推导错误分类，例如 config_invalid => config。
func (c Code) Category() Category {
	raw := string(c)
	index := strings.IndexByte(raw, '_')
	if index <= 0 {
		return CategoryUnknown
	}

	return Category(raw[:index])
}

const (
	// CodeConfigMissing 表示必须配置缺失。
	CodeConfigMissing Code = "config_missing"

	// CodeConfigInvalid 表示配置值格式、范围或类型非法。
	CodeConfigInvalid Code = "config_invalid"

	// CodeConfigUnsupported 表示配置值格式合法，但不是当前系统支持的枚举值。
	CodeConfigUnsupported Code = "config_unsupported"
)

const (
	// CodeHTTPUnsupportedContentType 表示请求 Content-Type 不是 JSON。
	CodeHTTPUnsupportedContentType Code = "http_unsupported_content_type"

	// CodeHTTPRequestBodyTooLarge 表示请求体超过允许大小。
	CodeHTTPRequestBodyTooLarge Code = "http_request_body_too_large"

	// CodeHTTPEmptyJSONBody 表示 JSON 请求体为空。
	CodeHTTPEmptyJSONBody Code = "http_empty_json_body"

	// CodeHTTPTrailingJSONToken 表示 JSON 请求体包含额外 token。
	CodeHTTPTrailingJSONToken Code = "http_trailing_json_token"

	// CodeHTTPInvalidJSONBody 表示 JSON 请求体格式非法。
	CodeHTTPInvalidJSONBody Code = "http_invalid_json_body"

	// CodeHTTPStreamingUnsupported 表示当前 ResponseWriter 不支持流式写出。
	CodeHTTPStreamingUnsupported Code = "http_streaming_unsupported"

	// CodeHTTPResponseWriteFailed 表示 HTTP 响应写出失败。
	CodeHTTPResponseWriteFailed Code = "http_response_write_failed"

	// CodeHTTPClientDisconnected 表示客户端连接已断开。
	CodeHTTPClientDisconnected Code = "http_client_disconnected"
)

const (
	// CodeDependencyPostgresUnavailable 表示 PostgreSQL 无法创建连接池或 ping 不通。
	CodeDependencyPostgresUnavailable Code = "dependency_postgres_unavailable"

	// CodeDependencyRedisUnavailable 表示 Redis 无法连接或 ping 不通。
	CodeDependencyRedisUnavailable Code = "dependency_redis_unavailable"
)

const (
	// CodeAuthMissingAPIKey 表示请求缺少 API Key。
	CodeAuthMissingAPIKey Code = "auth_missing_api_key"

	// CodeAuthInvalidAPIKey 表示 API Key 不存在或无法匹配。
	CodeAuthInvalidAPIKey Code = "auth_invalid_api_key"

	// CodeAuthAPIKeyRevoked 表示 API Key 已吊销。
	CodeAuthAPIKeyRevoked Code = "auth_api_key_revoked"

	// CodeAuthAPIKeyDisabled 表示 API Key 已禁用。
	CodeAuthAPIKeyDisabled Code = "auth_api_key_disabled"

	// CodeAuthAPIKeyExpired 表示 API Key 已过期。
	CodeAuthAPIKeyExpired Code = "auth_api_key_expired"

	// CodeAuthAPIKeySpendLimitReached 表示 API Key 已达生命周期累计费用上限（M7）。
	CodeAuthAPIKeySpendLimitReached Code = "auth_api_key_spend_limit_reached"

	// CodeAuthStoreFailed 表示 API Key 认证查询或更新存储失败。
	CodeAuthStoreFailed Code = "auth_store_failed"
)

const (
	// CodeAPIKeyInvalidUserID 表示创建 API Key 时 user_id 非法。
	CodeAPIKeyInvalidUserID Code = "apikey_invalid_user_id"

	// CodeAPIKeyInvalidName 表示创建 API Key 时 name 非法。
	CodeAPIKeyInvalidName Code = "apikey_invalid_name"

	// CodeAPIKeyInvalidRoute 表示创建 API Key 时未提供合法线路（线路必填，无默认回落）。
	CodeAPIKeyInvalidRoute Code = "apikey_invalid_route"

	// CodeAPIKeyGenerateFailed 表示 API Key 随机密钥生成失败。
	CodeAPIKeyGenerateFailed Code = "apikey_generate_failed"

	// CodeAPIKeyStoreFailed 表示 API Key 管理存储访问失败。
	CodeAPIKeyStoreFailed Code = "apikey_store_failed"
)

const (
	// CodeRateLimitInvalidSubject 表示限流 subject 非法。
	CodeRateLimitInvalidSubject Code = "ratelimit_invalid_subject"

	// CodeRateLimitInvalidLimit 表示限流次数非法。
	CodeRateLimitInvalidLimit Code = "ratelimit_invalid_limit"

	// CodeRateLimitInvalidWindow 表示限流窗口非法。
	CodeRateLimitInvalidWindow Code = "ratelimit_invalid_window"

	// CodeRateLimitStoreFailed 表示限流计数存储失败。
	CodeRateLimitStoreFailed Code = "ratelimit_store_failed"

	// CodeRateLimitExceeded 表示 API Key 级 RPM/TPM/RPD 限流命中，请求被拒（映射 HTTP 429）。
	CodeRateLimitExceeded Code = "rate_limit_exceeded"

	// CodeGatewayChannelRateLimited 表示渠道级 RPM/TPM/RPD 限流命中：单候选层面用于跳过该候选 fallback；
	// 全部候选都被限流时作为整体失败码上抛（映射 HTTP 429）。
	CodeGatewayChannelRateLimited Code = "channel_rate_limited"

	// CodeGatewayChannelConcurrencyLimited 表示渠道在途并发上限命中（DEC-029）：单候选层面用于跳过该候选
	// fallback；全部候选都被并发限制时作为整体失败码上抛（映射 HTTP 429）。
	CodeGatewayChannelConcurrencyLimited Code = "channel_concurrency_limited"
)

const (
	// CodeRoutingModelNotFound 表示请求模型不存在。
	CodeRoutingModelNotFound Code = "routing_model_not_found"

	// CodeRoutingModelNotAvailable 表示 project 无权使用请求模型。
	CodeRoutingModelNotAvailable Code = "routing_model_not_available"

	// CodeRoutingNoAvailableChannel 表示模型存在但没有可用 channel。
	CodeRoutingNoAvailableChannel Code = "routing_no_available_channel"

	// CodeRoutingRouteNotConfigured 表示 API Key 与项目均未绑定可用线路。
	CodeRoutingRouteNotConfigured Code = "routing_route_not_configured"

	// CodeRoutingStoreFailed 表示 routing 查询存储失败。
	CodeRoutingStoreFailed Code = "routing_store_failed"

	// CodeRoutingCredentialResolveFailed 表示 routing 构建候选时凭据解析失败。
	CodeRoutingCredentialResolveFailed Code = "routing_credential_resolve_failed"

	// CodeRoutingProtocolInvalid 表示 routing 请求没有携带受支持的 ingress 协议族。
	CodeRoutingProtocolInvalid Code = "routing_protocol_invalid"
)

const (
	// CodeCredentialRefMissing 表示 credential_ref 为空。
	CodeCredentialRefMissing Code = "credential_ref_missing"

	// CodeCredentialNotFound 表示 credential_ref 找不到对应凭据。
	CodeCredentialNotFound Code = "credential_not_found"

	// CodeCredentialMasterKeyInvalid 表示 CREDENTIAL_MASTER_KEY 格式或长度非法。
	CodeCredentialMasterKeyInvalid Code = "credential_master_key_invalid"

	// CodeCredentialEncryptFailed 表示上游凭据加密失败。
	CodeCredentialEncryptFailed Code = "credential_encrypt_failed"

	// CodeCredentialDecryptFailed 表示上游凭据解密失败（含密文被篡改）。
	CodeCredentialDecryptFailed Code = "credential_decrypt_failed"

	// CodeCredentialCiphertextInvalid 表示入库密文长度或格式非法。
	CodeCredentialCiphertextInvalid Code = "credential_ciphertext_invalid"
)

const (
	// CodeModelCatalogStoreFailed 表示模型目录查询存储失败。
	CodeModelCatalogStoreFailed Code = "modelcatalog_store_failed"
)

const (
	// CodeCapabilityStoreFailed 表示能力数据查询或写入存储失败。
	CodeCapabilityStoreFailed Code = "capability_store_failed"

	// CodeCapabilityInvalidKey 表示能力 key 不在已发布的稳定注册表内。
	CodeCapabilityInvalidKey Code = "capability_invalid_key"

	// CodeCapabilityInvalidSupportLevel 表示能力支持级别非法或在当前层不被允许。
	CodeCapabilityInvalidSupportLevel Code = "capability_invalid_support_level"

	// CodeCapabilityInvalidSource 表示能力声明或同步任务来源非法。
	CodeCapabilityInvalidSource Code = "capability_invalid_source"

	// CodeCapabilityNotFound 表示请求的能力数据不存在。
	CodeCapabilityNotFound Code = "capability_not_found"
)

const (
	// CodeAdapterInvalidRegistration 表示 adapter 注册信息非法。
	CodeAdapterInvalidRegistration Code = "adapter_invalid_registration"

	// CodeAdapterDuplicateKey 表示 adapter key 重复注册。
	CodeAdapterDuplicateKey Code = "adapter_duplicate_key"

	// CodeAdapterChannelInvalid 表示 channel runtime 参数非法。
	CodeAdapterChannelInvalid Code = "adapter_channel_invalid"

	// CodeAdapterEncodeRequestFailed 表示 adapter 编码上游请求失败。
	CodeAdapterEncodeRequestFailed Code = "adapter_encode_request_failed"

	// CodeAdapterCreateRequestFailed 表示 adapter 创建上游 HTTP 请求失败。
	CodeAdapterCreateRequestFailed Code = "adapter_create_request_failed"

	// CodeAdapterSendRequestFailed 表示 adapter 发送上游请求失败。
	CodeAdapterSendRequestFailed Code = "adapter_send_request_failed"

	// CodeAdapterUpstreamStatus 表示上游返回非 2xx HTTP 状态。
	CodeAdapterUpstreamStatus Code = "adapter_upstream_status"

	// CodeAdapterDecodeResponseFailed 表示 adapter 解析上游响应失败。
	CodeAdapterDecodeResponseFailed Code = "adapter_decode_response_failed"

	// CodeAdapterInvalidResponse 表示上游响应语义不满足 adapter 契约。
	CodeAdapterInvalidResponse Code = "adapter_invalid_response"

	// CodeAdapterEmitFailed 表示 adapter 向下游回调发送 chunk 失败。
	CodeAdapterEmitFailed Code = "adapter_emit_failed"

	// CodeAdapterReadStreamFailed 表示 adapter 读取上游 stream 失败。
	CodeAdapterReadStreamFailed Code = "adapter_read_stream_failed"

	// CodeAdapterResponseTooLarge 表示上游非流式响应体超过网关配置的字节上限（防 OOM）。
	CodeAdapterResponseTooLarge Code = "adapter_response_too_large"

	// CodeAdapterStreamIdleTimeout 表示流式上游在 idle 超时窗口内未推进任何字节（疑似半开/挂死连接）。
	CodeAdapterStreamIdleTimeout Code = "adapter_stream_idle_timeout"

	// CodeAdapterTokenizeFailed 表示 adapter 执行 provider-specific tokenizer 失败。
	CodeAdapterTokenizeFailed Code = "adapter_tokenize_failed"

	// CodeAdapterRequestUnsupported 表示请求携带了当前 provider 无法保持语义的字段，
	// adapter 在调用上游前明确拒绝；HTTP 层映射为协议原生 400。
	CodeAdapterRequestUnsupported Code = "adapter_request_unsupported"
)

const (
	// CodeSSELineTooLong 表示 SSE 单行超过配置上限。
	CodeSSELineTooLong Code = "sse_line_too_long"

	// CodeSSEEventTooLarge 表示 SSE 单个 event 超过配置上限。
	CodeSSEEventTooLarge Code = "sse_event_too_large"

	// CodeSSEMalformedStream 表示 SSE stream 非正常中断或底层读取失败。
	CodeSSEMalformedStream Code = "sse_malformed_stream"
)

const (
	// CodeBillingInvalidUsage 表示 usage token 数量不满足计费约束。
	CodeBillingInvalidUsage Code = "billing_invalid_usage"

	// CodeBillingInvalidPrice 表示客户售价或 provider 成本单价快照缺少必需单价或单价非法。
	CodeBillingInvalidPrice Code = "billing_invalid_price"

	// CodeBillingUnsupportedPricingUnit 表示不支持当前计价单位。
	CodeBillingUnsupportedPricingUnit Code = "billing_unsupported_pricing_unit"

	// CodeBillingUnsupportedFormula 表示不支持当前计费公式。
	CodeBillingUnsupportedFormula Code = "billing_unsupported_formula"
)

const (
	// CodeLedgerInsufficientBalance 表示余额不足。
	CodeLedgerInsufficientBalance Code = "ledger_insufficient_balance"

	// CodeLedgerInvalidAmount 表示账本金额参数非法。
	CodeLedgerInvalidAmount Code = "ledger_invalid_amount"

	// CodeLedgerIdempotencyConflict 表示幂等键被不同账本参数复用。
	CodeLedgerIdempotencyConflict Code = "ledger_idempotency_conflict"

	// CodeLedgerStoreFailed 表示 ledger 事务或存储操作失败。
	CodeLedgerStoreFailed Code = "ledger_store_failed"

	// CodeLedgerReservationNotFound 表示请求没有可结算的余额冻结记录。
	CodeLedgerReservationNotFound Code = "ledger_reservation_not_found"
)

const (
	// CodeGatewayAdapterNotRegistered 表示 routing 选中的 adapter 未注册。
	CodeGatewayAdapterNotRegistered Code = "gateway_adapter_not_registered"

	// CodeGatewayInputTokenEstimateFailed 表示 gateway 无法完成候选级保守输入 token 估算。
	CodeGatewayInputTokenEstimateFailed Code = "gateway_input_token_estimate_failed"

	// CodeGatewayStreamUsageMissing 表示 stream 正常结束但没有 final usage。
	CodeGatewayStreamUsageMissing Code = "gateway_stream_usage_missing"

	// CodeGatewayChatSettlementFailed 表示 chat 成功响应后的结算失败。
	CodeGatewayChatSettlementFailed Code = "gateway_chat_settlement_failed"

	// CodeGatewayChatAuthorizationFailed 表示 chat 调用上游前冻结余额失败。
	CodeGatewayChatAuthorizationFailed Code = "gateway_chat_authorization_failed"

	// CodeGatewayChatSettlementIdempotencyConflict 表示重复 settlement 的事实和第一次成功结算不一致。
	CodeGatewayChatSettlementIdempotencyConflict Code = "gateway_chat_settlement_idempotency_conflict"

	// CodeGatewayRequestOrphanReclaimed 表示进程崩溃遗留的孤儿请求被清扫 worker 释放冻结并收口为 failed。
	CodeGatewayRequestOrphanReclaimed Code = "gateway_request_orphan_reclaimed"
)

const (
	// CodeRequestLogIDGenerateFailed 表示生成 request_records.request_id 失败。
	CodeRequestLogIDGenerateFailed Code = "requestlog_id_generate_failed"

	// CodeRequestLogStoreFailed 表示 request log 存储写入失败。
	CodeRequestLogStoreFailed Code = "requestlog_store_failed"

	// CodeRequestLogInvalidStateTransition 表示 request/attempt 状态转移不合法。
	CodeRequestLogInvalidStateTransition Code = "requestlog_invalid_state_transition"
)

const (
	// CodeBootstrapStoreFailed 表示启动前检查读取存储失败。
	CodeBootstrapStoreFailed Code = "bootstrap_store_failed"

	// CodeBootstrapProviderAdapterCapabilityMissing 表示启用 provider 配置的 adapter 缺少当前进程要求的能力。
	CodeBootstrapProviderAdapterCapabilityMissing Code = "bootstrap_provider_adapter_capability_missing"
)

const (
	// CodeObservabilityTracerInitFailed 表示 OpenTelemetry tracer/exporter 初始化失败。
	CodeObservabilityTracerInitFailed Code = "observability_tracer_init_failed"
)

const (
	// CodeAdminAuthMissingToken 表示 admin 请求缺少认证 token。
	CodeAdminAuthMissingToken Code = "adminauth_missing_token"

	// CodeAdminAuthInvalidToken 表示 admin 认证 token 与配置不匹配。
	CodeAdminAuthInvalidToken Code = "adminauth_invalid_token"
)

const (
	// CodeAdminInvalidArgument 表示 admin 管理请求参数非法（格式、枚举、范围）。
	CodeAdminInvalidArgument Code = "admin_invalid_argument"

	// CodeAdminNotFound 表示 admin 管理请求的目标资源不存在。
	CodeAdminNotFound Code = "admin_not_found"

	// CodeAdminConflict 表示 admin 写入违反唯一约束（如 slug、provider 内 channel 名重复）。
	CodeAdminConflict Code = "admin_conflict"

	// CodeAdminAdapterBindingUnsupported 表示 channel 的 (protocol, adapter_key) 复合键未在当前进程 adapter registry 注册。
	CodeAdminAdapterBindingUnsupported Code = "admin_adapter_binding_unsupported"

	// CodeAdminPricingWindowOverlap 表示新建/调整的价格生效窗口与同一 channel/model 现有启用窗口重叠。
	CodeAdminPricingWindowOverlap Code = "admin_pricing_window_overlap"

	// CodeAdminStoreFailed 表示 admin 管理存储访问失败。
	CodeAdminStoreFailed Code = "admin_store_failed"
)
