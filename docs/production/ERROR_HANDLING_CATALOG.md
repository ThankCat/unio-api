# 错误处理与错误返回目录（Error Handling Catalog）

> 本文档系统梳理 Unio API 全项目的错误体系：内部稳定错误码（`failure.Code`）、上游 adapter 错误分类（`UpstreamErrorCategory`）、网关对客户的错误映射（`/v1/*`）、以及 Admin/Console 管理端错误。目标是把「有哪些错误、各代表什么、如何被处理」一次讲清，并给出让错误处理更合理的改进建议。
>
> 覆盖范围：`internal/platform/failure`（错误码体系）、`internal/core/adapter`（上游分类）、`internal/app/gatewayapi/*`（客户可见映射）、`internal/app/adminapi`（管理端映射）。

---

## 0. 全局设计原则

Unio 的错误处理围绕四条铁律：

1. **稳定错误码是唯一契约**。所有内部错误都携带一个 `failure.Code`（如 `routing_model_not_found`），业务分支只依据 code / category 决策，不解析文本 message。
2. **上游原始 body 永不外泄**。adapter 把 provider 的 HTTP status / 错误 body / 网络错误解析成稳定的 `UpstreamErrorCategory`，gateway 只消费分类；provider 原文最多截断进 `ResponseSnippet` 仅供渠道排障留痕，绝不进客户响应或请求记录。
3. **上游凭据问题绝不渲染成客户 401**。上游 auth/permission 是平台 channel 的凭据问题，统一归 `502 api_error`，避免客户误以为自己的 API key 失效。
4. **5xx 一律通用文案**。只有 4xx 的安全摘要可回显给调用方；5xx 一律返回通用 message，不透传内部实现细节。

错误在系统里的两个正交维度：

| 维度 | 类型 | 回答的问题 | 定义位置 |
| --- | --- | --- | --- |
| **模块分类** | `failure.Category` | 「哪个模块出错」 | `internal/platform/failure/code.go` |
| **上游分类** | `UpstreamErrorCategory` | 「上游为什么失败」 | `internal/core/adapter/upstream_error.go` |

`failure.Category` 由错误码前缀自动推导：`config_invalid` → `config`，`routing_model_not_found` → `routing`（见 `Code.Category()`，按第一个 `_` 前缀切分）。

---

## 1. 内部错误码体系（`failure.Code`）

### 1.1 核心结构

```go
type Failure struct {
    Code    Code      // 稳定错误码（契约）
    Message string    // 安全摘要（可回显 4xx）
    Cause   error     // 底层 cause（Unwrap 支持 errors.Is/As）
    Fields  []Field   // 结构化上下文（如 field=model）
}
```

- `failure.New(code, opts...)` / `failure.Wrap(code, cause, opts...)` 构造。
- `failure.CodeOf(err)` / `CategoryOf(err)` / `FieldsOf(err)` 沿 error 链提取。
- `Error()` 只返回 Message（无则返回 code 字符串）——**永不含 cause 细节**，因此可安全回显 4xx。

### 1.2 错误码全集（按 Category 分组）

下面按模块分类列出全部错误码及其含义。**「客户可见」列**指该码是否可能出现在 `/v1/*` 客户响应路径。

#### config —— 启动配置（进程启动期，客户不可见）

| Code | 含义 |
| --- | --- |
| `config_missing` | 必须配置缺失 |
| `config_invalid` | 配置值格式/范围/类型非法 |
| `config_unsupported` | 格式合法但非支持的枚举值 |

#### http —— HTTP 请求解析/写出

| Code | 含义 | 客户可见 |
| --- | --- | --- |
| `http_unsupported_content_type` | Content-Type 非 JSON | ✅ 415 |
| `http_request_body_too_large` | 请求体超限 | ✅ 413 |
| `http_empty_json_body` | JSON body 为空 | ✅ 400 |
| `http_trailing_json_token` | body 含多余 token | ✅ 400 |
| `http_invalid_json_body` | JSON 格式非法 | ✅ 400 |
| `http_streaming_unsupported` | ResponseWriter 不支持 flush | ✅ 500 |
| `http_response_write_failed` | 响应写出失败 | 连接层 |
| `http_client_disconnected` | 客户端已断开 | 连接层 |

#### dependency —— 外部依赖（启动/运行期基础设施）

| Code | 含义 |
| --- | --- |
| `dependency_postgres_unavailable` | PostgreSQL 连接池创建失败或 ping 不通 |
| `dependency_redis_unavailable` | Redis 连接失败或 ping 不通 |

#### auth —— 客户 API Key 认证（ingress）

| Code | 含义 | 映射 |
| --- | --- | --- |
| `auth_missing_api_key` | 缺少 API Key | 401 |
| `auth_invalid_api_key` | Key 不存在/无法匹配 | 401 |
| `auth_api_key_revoked` | Key 已吊销 | 401 |
| `auth_api_key_disabled` | Key 已禁用 | 401 |
| `auth_api_key_expired` | Key 已过期 | 401 |
| `auth_api_key_spend_limit_reached` | Key 达生命周期累计费用上限（M7） | 429 |
| `auth_store_failed` | 认证查询/更新存储失败 | 500 |

#### apikey —— API Key 管理（Console/Admin）

| Code | 含义 |
| --- | --- |
| `apikey_invalid_user_id` | 创建时 user_id 非法 |
| `apikey_invalid_name` | 创建时 name 非法 |
| `apikey_invalid_route` | 未提供合法线路（线路必填，无默认回落） |
| `apikey_generate_failed` | 随机密钥生成失败 |
| `apikey_store_failed` | 管理存储访问失败 |

#### ratelimit —— 限流

| Code | 含义 | 映射 |
| --- | --- | --- |
| `ratelimit_invalid_subject` | 限流 subject 非法（配置） | — |
| `ratelimit_invalid_limit` | 限流次数非法（配置） | — |
| `ratelimit_invalid_window` | 限流窗口非法（配置） | — |
| `ratelimit_store_failed` | 限流计数存储失败（fail-closed） | 500 |
| `rate_limit_exceeded` | Key 级 RPM/TPM/RPD 命中 | ✅ 429 |
| `channel_rate_limited` | 渠道级限流命中（单候选跳过；全部命中则整体 429） | ✅ 429 |

> 注意：`rate_limit_exceeded` 与 `channel_rate_limited` 缺少 `ratelimit_` 前缀，Category 推导分别为 `rate` 与 `channel`，**不归入 `ratelimit` 类**（见 §5 问题 6）。

#### routing —— 模型路由

| Code | 含义 | 映射 |
| --- | --- | --- |
| `routing_model_not_found` | 请求模型不存在 | ✅ 404 |
| `routing_model_not_available` | 项目无权使用该模型 | ✅ 404 |
| `routing_no_available_channel` | 模型存在但无可用 channel | ✅ 503 |
| `routing_route_not_configured` | Key 与项目均未绑定可用线路 | 500* |
| `routing_store_failed` | routing 查询存储失败 | 500 |
| `routing_credential_resolve_failed` | 构建候选时凭据解析失败 | 500 |
| `routing_protocol_invalid` | 请求未携带受支持的 ingress 协议族 | 500* |

> \* `routing_route_not_configured` / `routing_protocol_invalid` 目前不在 handler 显式分支中，落入 default 500（见 §5 问题 3）。

#### credential —— 上游凭据加解密

| Code | 含义 |
| --- | --- |
| `credential_ref_missing` | credential_ref 为空 |
| `credential_not_found` | credential_ref 找不到凭据 |
| `credential_master_key_invalid` | CREDENTIAL_MASTER_KEY 格式/长度非法 |
| `credential_encrypt_failed` | 凭据加密失败 |
| `credential_decrypt_failed` | 凭据解密失败（含密文被篡改） |
| `credential_ciphertext_invalid` | 入库密文长度/格式非法 |

#### modelcatalog / capability —— 模型目录与能力

| Code | 含义 |
| --- | --- |
| `modelcatalog_store_failed` | 模型目录查询存储失败 |
| `capability_store_failed` | 能力数据查询/写入失败 |
| `capability_invalid_key` | 能力 key 不在已发布注册表内 |
| `capability_invalid_support_level` | 支持级别非法/当前层不允许 |
| `capability_invalid_source` | 能力声明/同步任务来源非法 |
| `capability_not_found` | 请求的能力数据不存在 |

#### adapter —— 上游 adapter 调用/协议转换

| Code | 含义 |
| --- | --- |
| `adapter_invalid_registration` | adapter 注册信息非法 |
| `adapter_duplicate_key` | adapter key 重复注册 |
| `adapter_channel_invalid` | channel runtime 参数非法（如 base_url 空） |
| `adapter_encode_request_failed` | 编码上游请求失败 |
| `adapter_create_request_failed` | 创建上游 HTTP 请求失败 |
| `adapter_send_request_failed` | 发送上游请求失败（网络层，未拿到响应） |
| `adapter_upstream_status` | 上游返回非 2xx |
| `adapter_decode_response_failed` | 解析上游响应失败 |
| `adapter_invalid_response` | 上游响应语义不满足契约（如空 choices、缺 usage） |
| `adapter_emit_failed` | 向下游回调发送 chunk 失败 |
| `adapter_read_stream_failed` | 读取上游 stream 失败 |
| `adapter_response_too_large` | 非流式响应体超字节上限（防 OOM） |
| `adapter_stream_idle_timeout` | 流式 idle 窗口内无字节推进（半开/挂死连接） |
| `adapter_tokenize_failed` | provider-specific tokenizer 执行失败 |
| `adapter_request_unsupported` | 请求含 provider 无法保持语义的字段，调用前拒绝 → 客户 400 |

#### sse —— SSE 解析

| Code | 含义 |
| --- | --- |
| `sse_line_too_long` | SSE 单行超限 |
| `sse_event_too_large` | SSE 单 event 超限 |
| `sse_malformed_stream` | SSE 非正常中断或底层读取失败 |

#### billing / ledger —— 计费与账本

| Code | 含义 | 映射 |
| --- | --- | --- |
| `billing_invalid_usage` | usage token 不满足计费约束 | 500 |
| `billing_invalid_price` | 售价/成本单价快照缺必需单价或非法 | 500 |
| `billing_unsupported_pricing_unit` | 不支持的计价单位 | 500 |
| `billing_unsupported_formula` | 不支持的计费公式 | 500 |
| `ledger_insufficient_balance` | 余额不足 | ✅ 402 / 422 |
| `ledger_invalid_amount` | 账本金额参数非法 | 400（Admin） |
| `ledger_idempotency_conflict` | 幂等键被不同参数复用 | 409（Admin） |
| `ledger_store_failed` | ledger 事务/存储失败 | 500 |
| `ledger_reservation_not_found` | 无可结算的冻结记录 | 500 |

> `ledger_insufficient_balance` 是**唯一会被三个协议 handler 显式改写文案**的账本码：OpenAI → `insufficient_quota`（402），Anthropic → `invalid_request_error`（402），Admin → 422。用 402 与限流 429 区分，避免客户端把余额不足当限速重试。

#### gateway —— 编排

| Code | 含义 |
| --- | --- |
| `gateway_adapter_not_registered` | routing 选中的 adapter 未注册 |
| `gateway_input_token_estimate_failed` | 无法完成候选级保守输入 token 估算 |
| `gateway_stream_usage_missing` | stream 正常结束但无 final usage |
| `gateway_chat_settlement_failed` | 成功响应后结算失败 |
| `gateway_chat_authorization_failed` | 调用上游前冻结余额失败 |
| `gateway_chat_settlement_idempotency_conflict` | 重复 settlement 事实与首次不一致 |
| `gateway_request_orphan_reclaimed` | 崩溃遗留孤儿请求被清扫 worker 释放并收口 failed |

#### requestlog / bootstrap / observability

| Code | 含义 |
| --- | --- |
| `requestlog_id_generate_failed` | 生成 request_id 失败 |
| `requestlog_store_failed` | 请求日志写入失败 |
| `requestlog_invalid_state_transition` | request/attempt 状态转移不合法 |
| `bootstrap_store_failed` | 启动前检查读取存储失败 |
| `bootstrap_provider_adapter_capability_missing` | 启用 provider 的 adapter 缺进程要求能力 |
| `observability_tracer_init_failed` | tracer/exporter 初始化失败 |

#### adminauth / admin —— 管理端

| Code | 含义 | 映射 |
| --- | --- | --- |
| `adminauth_missing_token` | admin 请求缺 token | 401 |
| `adminauth_invalid_token` | admin token 不匹配 | 401 |
| `admin_invalid_argument` | 管理请求参数非法 | 400 |
| `admin_not_found` | 目标资源不存在 | 404 |
| `admin_conflict` | 违反唯一约束（slug/名重复） | 409 |
| `admin_adapter_binding_unsupported` | (protocol, adapter_key) 未注册 | 422 |
| `admin_pricing_window_overlap` | 价格生效窗口与现有启用窗口重叠 | 422 |
| `admin_store_failed` | 管理存储访问失败 | 500 |

---

## 2. 上游 Adapter 错误分类（`UpstreamErrorCategory`）

adapter 把 provider 错误解析成 8 个稳定分类，gateway 只据此做 retry/fallback/熔断/cooldown/凭据闸门 决策。

### 2.1 分类全集与下游决策

| Category | 触发条件 | 可 retry (fallback) | 算渠道故障（熔断） | 触发 429 cooldown | 触发凭据闸门 | 客户 HTTP |
| --- | --- | --- | --- | --- | --- | --- |
| `auth` | 上游 401 | ✅ | ❌（改由凭据闸门专管） | ❌ | ✅ | 502 |
| `permission` | 上游 403 | ❌ | ✅ | ❌ | ❌ | 502 |
| `rate_limit` | 上游 429 | ✅ | ✅ | ✅ | ❌ | 429 |
| `bad_request` | 其他 4xx | ❌ | ❌ | ❌ | ❌ | 400 |
| `timeout` | 408 / DeadlineExceeded / net timeout / stream idle | ✅ | ✅ | ❌ | ❌ | 504 |
| `canceled` | context.Canceled（客户端断开） | ❌ | ❌ | ❌ | ❌ | 502* |
| `server_error` | 5xx / 连接级失败 / 流截断 / SSE 内联终态错误 | ✅ | ✅ | ❌ | ❌ | 502 |
| `unknown` | HTTP<400 却进错误路径 / 链上无 UpstreamError | ❌ | ❌ | ❌ | ❌ | 502* |

\* `canceled` / `unknown` 无专属 handler 分支，落入 default 502。

### 2.2 关键设计点

- **401 可 retry 但不熔断**：`(provider, key)` 联合唯一，fallback 到同模型另一 channel 大概率是另一把 key，会成功；401 改由持久化**凭据闸门**（`credential_gate.go`，连续 N 次默认 3 次后翻 `credential_valid=false`）摘除，避免与熔断两套机制重叠。
- **403 不进凭据闸门**：只做熔断瞬时摘除，不自动 ban。
- **`canceled` 的精确识别**：流式 header 超时用 `cancelCause(DeadlineExceeded)` 实现，底层 transport 可能暴露成裸 `context canceled`；`newUpstreamSendErrorWithContextCause` 检查 ctxCause 区分「服务端 header 超时（timeout，可 fallback）」与「真实客户端取消（canceled，不 fallback）」。
- **retryable 集合**：`{rate_limit, timeout, server_error, auth}`（`retry.go`）。
- **渠道故障集合**：`{timeout, server_error, rate_limit, permission}`（`breaker.go` 的 `IsChannelFaultError`）。

---

## 3. 网关对客户的错误映射（`/v1/*`）

三个协议 handler（OpenAI chat completions、OpenAI responses、Anthropic messages）各有一个 `map*ServiceError`，映射逻辑**高度一致**。

### 3.1 响应信封

- **OpenAI chat / responses**：`{"error": {"message", "type", "param", "code"}}`（`httpx.WriteOpenAIError`）。
- **Anthropic messages**：`{"type":"error", "error":{"type","message"}}`（`anthropic.NewErrorResponse`）。

### 3.2 统一映射表

| 内部错误 | OpenAI status/code | Anthropic status/type | Responses status/code |
| --- | --- | --- | --- |
| `ledger_insufficient_balance` | 402 `insufficient_quota` | 402 `invalid_request_error` | 402 `insufficient_quota` |
| `rate_limit_exceeded` / `channel_rate_limited` | 429 `rate_limit_exceeded` | 429 `rate_limit_error` | 429 `rate_limit_exceeded` |
| `adapter_request_unsupported` | 400 `unsupported_parameter` | 400 `invalid_request_error` | 400 `unsupported_parameter` |
| `routing_model_not_found` / `not_available` | 404 `model_not_found` | 404 `not_found_error` | 404 `model_not_found` |
| `routing_no_available_channel` | 503 `model_unavailable` | 503 `api_error` | 503 `model_unavailable` |
| 上游 `rate_limit` | 429 `rate_limit_exceeded` | 429 `rate_limit_error` | 429 `rate_limit_exceeded` |
| 上游 `timeout` | 504 `upstream_timeout` | 504 `api_error` | 504 `upstream_timeout` |
| 上游 `bad_request` | 400 `invalid_request` | 400 `invalid_request_error` | 400 `invalid_request` |
| 上游 `auth/permission/server/unknown` | 502 `upstream_error` | 502 `api_error` | 502 `upstream_error` |
| 兜底 | 500 `internal_error` | 500 `api_error` | 500 fallback |

### 3.3 流式（SSE）已开始后的错误

一旦 SSE 首帧写出，就不能再回退普通 JSON error（HTTP status 已定）：

- **首帧前失败** → 正常 JSON error（可达性最佳），且可能触发 fallback。
- **首帧后失败** → 只能写 data-only error chunk（OpenAI）/ `event:error`（Anthropic、Responses），复用相同映射渲染安全 type/code，无法改 HTTP status。

### 3.4 Responses 协议专属拒绝码

- `unsupported_background`（400）：`background:true` 被拒（无异步能力）。
- `unsupported_endpoint_stateless`（501）：有状态 endpoint（`GET/DELETE /v1/responses/{id}`、`/input_items`、`/cancel`）不支持。

---

## 4. Admin / Console 管理端错误

### 4.1 响应信封

- **成功**：`{"data": ...}`（`writeData`）。
- **错误**：`{"error": {"message","type","param","code"}}`（复用 `httpx.WriteError`，type 固定 `api_error`）。

### 4.2 映射策略（`adminErrorStatus`）

| Code | HTTP |
| --- | --- |
| `admin_invalid_argument` | 400 |
| `admin_adapter_binding_unsupported` | 422 |
| `admin_pricing_window_overlap` | 422 |
| `admin_not_found` | 404 |
| `admin_conflict` | 409 |
| `ledger_invalid_amount` | 400 |
| `ledger_insufficient_balance` | 422 |
| `ledger_idempotency_conflict` | 409 |
| `capability_invalid_*` | 400 |
| `capability_not_found` | 404 |
| Category==http | 400 |
| 其余 | 500 |

- **安全边界**：只有非 500 才回显 `err.Error()`（安全摘要）；500 一律 `"internal error"` + code `internal_error`。
- code 为空时兜底 `internal_error`。

---

## 5. 已发现的不一致与改进建议

按优先级排列。这些是「让错误处理更合理」的具体落点。

### P1 — 建议尽快处理

**问题 1：非流式 body-too-large / decode 失败缺少 upstream category。**
非流式路径（`chat.go`）里 `adapter_response_too_large` / `adapter_decode_response_failed` / `adapter_read_stream_failed` 不是 `UpstreamError`，无 category → `UpstreamCategoryOf` 返回 `(unknown,false)` → 不 retry、不算渠道故障、客户落 500。而流式同类问题归 `server_error` 可首字节前 fallback。**同样是「上游返回超大/损坏响应」，流式能 fallback、非流式不能，行为不一致**，且客户拿到不友好的 500。
> 建议：给非流式这几个错误也包装成 `server_error` 分类的 `UpstreamError`，使其可 fallback，并映射成 502 而非 500。

**问题 2：`canceled` 落入 default 502，语义不严谨。**
handler 的 `mapUpstream*Error` 没有 `canceled` 专属分支，客户端主动取消被渲染成「上游网关错误 502」。实践中连接多已断开影响有限，但审计与语义上不准确。
> 建议：为 `canceled` 增加分支（如映射 499 client_closed_request 或明确的取消语义），至少在日志层与真实上游 502 区分开。

**问题 3：部分 routing 错误码无显式分支，落入 500。**
`routing_route_not_configured` 与 `routing_protocol_invalid` 在客户 handler 里没有显式 case，落入 default 500。前者其实是「配置/请求」问题，更接近 4xx。
> 建议：`routing_route_not_configured` 映射 400/403（客户或平台配置缺失），`routing_protocol_invalid` 映射 400。

### P2 — 建议整理

**问题 4：三份 adapter `errors.go` 分类逻辑逐字重复。**
`upstreamCategoryForStatus` / `upstreamCategoryForSendError` / `classifyStreamReadCategory` 在 chat/responses/messages 三处重复，靠「同口径」注释维持一致，未来改规则（如新增 409/413 处理）有三处漂移风险。
> 建议：抽到 `core/adapter` 共享 helper，三处调用同一份。

**问题 5：Responses 内联终态错误固定 `server_error`，不看上游 error.code。**
上游 SSE `response.failed`/`error` 事件里的 `code`（可能是 rate_limit / invalid_request）只写进 message，一律归 `server_error`，可能把内联限流/非法请求当瞬时故障 fallback。符合「不解析 body」原则但略不精确。
> 建议：评估是否对内联终态错误的 `type` 字段做有限映射（仅识别 rate_limit / invalid_request 两类稳定语义）。

**问题 6：限流码前缀不统一，Category 推导偏离。**
`rate_limit_exceeded` → category `rate`，`channel_rate_limited` → category `channel`，都不归入 `ratelimit` 类。日志/指标按 category 聚合限流时会漏掉这两个最重要的运行期限流码。
> 建议：要么重命名为 `ratelimit_exceeded` / `ratelimit_channel_limited`，要么在指标聚合处显式纳入。（注意：重命名涉及客户可见 code 契约，需评估兼容性。）

### 观测建议（非 bug）

- send/stream 错误路径的 `UpstreamMetadata` 为空（StatusCode=0、无 snippet），渠道排障对「读流中途失败/连接级失败」缺原文留痕，只能靠 cause message。可评估在这些路径补充有限的诊断字段（不外泄给客户）。

---

## 6. 快速索引（关键文件）

| 关注点 | 文件 |
| --- | --- |
| 错误码全集 + Category | `internal/platform/failure/code.go` |
| Failure 结构 / 构造 / 提取 | `internal/platform/failure/failure.go` |
| 响应信封 / OpenAI error writer | `internal/platform/httpx/response.go` |
| 上游分类核心类型 | `internal/core/adapter/upstream_error.go` |
| 上游分类逻辑（各协议） | `internal/core/adapter/{openai/chatcompletions,openai/responses,anthropic/messages}/errors.go` |
| retry 判定 | `internal/service/gateway/lifecycle/retry.go` |
| 熔断 / IsChannelFaultError | `internal/service/gateway/lifecycle/breaker.go` |
| 429 cooldown | `internal/service/gateway/lifecycle/cooldown.go` |
| 凭据闸门 | `internal/service/gateway/lifecycle/credential_gate.go` |
| OpenAI chat 客户映射 | `internal/app/gatewayapi/openai/chatcompletions/handler.go` |
| OpenAI responses 客户映射 | `internal/app/gatewayapi/openai/responses/handler.go` |
| Anthropic messages 客户映射 | `internal/app/gatewayapi/anthropic/messages/handler.go` |
| Admin 错误映射 | `internal/app/adminapi/errors.go` |
