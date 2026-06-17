# 整改方案（待审核）：上下文压缩与请求体上限

| 属性 | 值 |
| --- | --- |
| 状态 | **draft / 待审核** |
| 创建日期 | 2026-06-16 |
| 关联现场 | Codex + Unio 本地网关长会话；`POST /v1/responses` 与 `POST /v1/responses/compact` |
| 关联 GAP（现状） | [GAP-11-007](TODO_REGISTER.md#gap-11-007)、[DEC-018](DECISIONS.md#dec-018-上游-responses-直传--第三方桥接分流dec-014-补充) |
| 拟新增 GAP | GAP-11-013（请求体上限）、GAP-11-014（compact 双路径）——**审核通过后再写入 TODO_REGISTER** |

---

## 1. 背景与问题陈述

### 1.1 业务场景

Codex CLI 将 `base_url` 指向 Unio Gateway（`POST /v1/responses`），在长会话中：

1. 每轮携带**完整历史** `input[]`（无状态设计，符合 [GAP-11-001](TODO_REGISTER.md#gap-11-001)）。
2. 接近上下文预算时，Codex 调用 `POST /v1/responses/input_tokens` 预检，再调用 `POST /v1/responses/compact` 压缩历史。
3. 压缩成功后，用返回的短 `output[]` 作为下一轮 `input` 继续对话。

### 1.2 已观测问题

#### A. `413 Request Entity Too Large`（网关入口拒绝）

| 现象          | 说明                                                                                                        |
| ----------- | --------------------------------------------------------------------------------------------------------- |
| 日志特征        | `POST /v1/responses` 或 `/compact` 在 **10–30ms** 内返回 `413`，无上游 `request_id`                                |
| 根因          | `httpx.DecodeJSON` 使用硬编码 `DefaultMaxJSONBodyBytes = 1 << 20`（**1MB**），见 `internal/platform/httpx/json.go` |
| 与 token 的关系 | 长会话 JSON body（大量 `input`、tool 结果、reasoning 载体）可轻易超过 1MB，**与模型 context window 是否还有余量无关**                   |
| 连锁影响        | compact 请求同样需读取完整 `input[]`；**compact 在入口被 413 拒绝 → 历史无法缩短 → 后续请求继续膨胀**                                   |

#### B. 上下文压缩能力单一、与主路径分流不对称

| 现象     | 说明                                                                                                                                                      |
| ------ | ------------------------------------------------------------------------------------------------------------------------------------------------------- |
| 现状     | `compact_response.go` 仅实现 **Synthetic（chat 摘要降级）**，强制 `executeNonStreamChat(allowDirect=false)`                                                         |
| 与主路径差异 | `POST /v1/responses` 已按 [DEC-018](DECISIONS.md#dec-018-上游-responses-直传--第三方桥接分流dec-014-补充) 分流：**原生 Responses 直传** vs **第三方 chat 桥接**；compact **未复用该分流** |
| 第三方上游  | DeepSeek 等无 `/responses/compact`；当前摘要降级可用，但**不等价** OpenAI 加密 compaction（[GAP-11-007](TODO_REGISTER.md#gap-11-007)）                                      |
| 原生上游   | OpenAI / Codex 官方渠道具备原生 compact；Unio **未透传**，无法获得官方 `compaction` item 语义                                                                                |


---

## 2. 整改目标与非目标

### 2.1 目标

1. **消除 Codex 长会话因 1MB 硬限制导致的入口 413**，使 compact 与主请求可正常进入业务层。
2. **将 compact 拆为两条清晰路径**：原生上游 **透传** vs 第三方 **Synthetic 自定义**，与 `CreateResponse` 分流哲学一致。
3. **保持无状态商业承诺**：不引入服务端会话存储；不默认开启 `context_management.compact_threshold` 自动压缩（可作为后续可选增强）。
4. **账务与 lifecycle 不变量**：两条 compact 路径均走完整 routing / authorization / settlement（与现 compact 一致）。

### 2.2 非目标（本阶段不做）

| 项 | 说明 |
| --- | --- |
| 服务端 `context_management.compact_threshold` 自动压缩 | 参考 LiteLLM / OpenAI server-side compaction；单独后续任务 |
| BM25 / middle-out 截断（不调用摘要模型） | 参考 OpenRouter / Helicone；可作为 Synthetic 的补充策略，本方案不默认实现 |
| Headroom 类 tool 输出专用压缩 | 独立产品层；不在 Unio gateway 核心范围 |
| 修正 `channel_prices` 成本价 | 运营配置，非网关代码 |
| 有状态 Responses（retrieve / `previous_response_id` 服务端续接） | [GAP-11-009](TODO_REGISTER.md#gap-11-009) 永久边界 |

---

## 3. 业界做法摘要（对照用）

| 项目             | 请求体上限                                                             | Compact / 上下文压缩                                                              |
| -------------- | ----------------------------------------------------------------- | ---------------------------------------------------------------------------- |
| **new-api**    | `MAX_REQUEST_BODY_MB` 可配置（默认约 128MB）；gzip/br 解压后 `MaxBytesReader` | OpenAI/Codex：**透传** `/responses/compact`；其他渠道无本地摘要                           |
| **one-api**    | 应用层无统一上限，依赖 Nginx                                                 | **无** compact 实现                                                             |
| **LiteLLM**    | 代理层 + 大 payload 感知                                                | OpenAI 透传 + **universal 服务端摘要** + `litellm.compress()` BM25 + proxy callback |
| **Bifrost**    | 大 payload 日志截断                                                    | OpenAI **透传** compact；agent runtime 另有 session 摘要                            |
| **OpenRouter** | —                                                                 | 可选 `context-compression` 插件：**middle-out 删中间消息**（非摘要）                        |
| **Helicone**   | —                                                                 | Header 触发 truncate / middle-out / fallback；Anthropic context_editing         |
| **Unio（现状）**   | **1MB 硬编码**                                                       | 仅 Synthetic chat 摘要；无透传                                                      |

**结论**：多上游网关普遍采用 **「能透传则透传，不能则网关自实现」**；请求体上限与 compact **解耦**，但 Codex 场景下两者必须同时可用。

---

## 4. 整改项 A：请求体上限（413）

### 4.1 设计原则

- 上限是 **网关安全与稳定性** 配置，不是业务计费逻辑。
- **解压后大小**计入限制（对齐 new-api：防 zip bomb / 超大 gzip body）。
- 超限返回现有 OpenAI-compatible 错误语义：`413` + `request body too large`（已实现于 responses/chat handler）。
- 与 **HTTP_READ_TIMEOUT**、渠道 `timeout_ms` 分层：body 限制防 OOM；超时防 hung upstream。

### 4.2 建议实现

| 项       | 建议                                                                                                                              |
| ------- | ------------------------------------------------------------------------------------------------------------------------------- |
| 配置项     | `HTTP_MAX_JSON_BODY_MB`（或 `GATEWAY_MAX_JSON_BODY_MB`），默认 **32** 或 **128**（对齐 new-api 统一默认 128MB 的方向；本地开发可 8–32）                 |
| 代码落点    | `internal/platform/config` + `httpx.DecodeJSON` 改为读取配置（保留 `DefaultMaxJSONBodyBytes` 作 fallback）                                 |
| 作用范围    | 所有经 `httpx.DecodeJSON` 的 gateway ingress（chat、responses、compact、input_tokens、admin 若共用）                                         |
| gzip 请求 | 若未来 gateway 接收 `Content-Encoding: gzip`，在 middleware 解压后再 `MaxBytesReader`（**本整改可只做 JSON 明文 + 可配置上限；gzip 解压中间件列为可选 follow-up**） |
| 前置代理    | 文档注明：Nginx `client_max_body_size` 需 ≥ 网关配置，否则仍 413                                                                              |

### 4.3 建议默认值（待审核拍板）

| 环境 | 建议默认 | 理由 |
| --- | --- | --- |
| 生产 | 64MB 或 128MB | Codex 长会话 + tool 大 payload；对齐 new-api |
| 本地开发 | 32MB | 足够 compact 调试，降低误配 OOM 风险 |

### 4.4 验收标准（A）

- [ ] Codex 长会话复现时，`/v1/responses/compact` 不再因 **< 几 MB** 的 body 在入口 413（在配置上限内）。
- [ ] 构造 `> HTTP_MAX_JSON_BODY_MB` 的请求仍稳定 413，错误体为 OpenAI-compatible。
- [ ] `go test` 覆盖：默认配置、显式 env、超限路径。
- [ ] `.env` / launch.json 文档或示例更新。

---

## 5. 整改项 B：Compact 双路径（Native 透传 vs Synthetic 自定义）

### 5.1 设计原则

与 `CreateResponse`（`create_response.go`）对称：

```text
POST /v1/responses/compact
  → ingress 校验（不变）
  → CompactOrchestrator 选路
       ├─ NativeCompact   ：上游支持原生 compact → 透传 /v1/responses/compact
       └─ SyntheticCompact：上游不支持 → 可插拔合成策略（默认 = 现有 chat 摘要）
```

**分叉依据**：渠道 `adapter_key` + 能力位（非硬编码 provider 名称）。

### 5.2 路径定义

#### 路径 1：NativeCompact（远程透传）

| 项 | 说明 |
| --- | --- |
| 触发条件 | 选中渠道的 adapter 声明 `responses.compact.native`（或等价能力 key）；且 compact 请求经 capability gate 放行 |
| 行为 | 请求体原样转发上游 `POST /v1/responses/compact`；响应原样返回（仅 `model` 回显改写，与直传 responses 一致） |
| 上游要求 | **必须**有原生 compact API（OpenAI、Azure OpenAI 若支持、Codex 中转等） |
| 响应形态 | 可能含 `type: compaction` + `encrypted_content` |
| 计费 | 按上游 compact 用量；若上游有独立 compact 定价，走 `channel_prices` / 快照（与 new-api compact 模型后缀策略对齐为 **可选 follow-up**） |
| 失败回落 | 上游 404/unsupported → **可配置**是否回落 Synthetic（默认建议：回落并打 audit 日志，避免 Codex 断链） |

#### 路径 2：SyntheticCompact（第三方自定义）

| 项 | 说明 |
| --- | --- |
| 触发条件 | 不满足 NativeCompact（DeepSeek、多数第三方） |
| 默认实现 | **保持现状**：`input[]` + `instructions` → `executeNonStreamChat` → `{"output":[message(output_text)]}` |
| 扩展点 | `CompactionStrategy` 接口，按 `adapter_key` 注册可选实现（未来某渠道自建 compact API 时可挂自定义 adaptor） |
| 响应形态 | 单条 assistant message；第一版仍不签发 `compaction` 密文 item（[GAP-11-007](TODO_REGISTER.md#gap-11-007) 永久限制可改为「Synthetic 路径限制」） |
| 计费 | 一次普通 completion，operation 仍为 `responses`（与现测试一致） |

### 5.3 建议代码结构（审核用，非实施）

```text
internal/service/gateway/openai/responses/
  compact_orchestrator.go      # 选路 + 统一 CompactHistory 入口
  compact_native.go              # NativeCompact：adapter 直传
  compact_synthetic.go           # SyntheticCompact：迁自 compact_response.go
  compact_response.go            # 删除或仅保留类型/常量（实施时决定）

internal/core/adapter/openai/responses/
  compact.go                     # 上游 compact HTTP 调用（若尚无）

protocol/CAPABILITY_KEYS.md      # 新增 responses.compact.native / responses.compact.synthetic
```

Ingress（`endpoints_handler.go`）**不感知**路径差异。

### 5.4 与主路径、桥接层的关系

| 场景 | 主路径 `POST /responses` | `POST /compact` |
| --- | --- | --- |
| `adapter_key=openai`（responses 直传槽） | 直传 `/responses` | **NativeCompact 透传** |
| `adapter_key=deepseek` 等 chat-only | chat 桥接 | **SyntheticCompact 摘要** |
| 下一轮 input 含 `type:compaction` | 直传：原样过 upstream；桥接：当前 **不还原**（`responses_chat_map.go` GAP-11-001） | Native 路径客户应原样回传 compaction item；Synthetic 路径仍忽略 compaction item |

**待审核决策（见 §7）**：Synthetic 路径是否在 input 侧支持将历史 `compaction` item 还原为 summary message（仅影响第三方续聊，不影响 Native 透传）。

### 5.5 能力矩阵更新（拟）

| 能力 | Native 路径 | Synthetic 路径 |
| --- | --- | --- |
| `POST /v1/responses/compact` | ✅ 等同上游 | ⚠️ 摘要降级 |
| OpenAI 加密 compaction 语义 | ✅（上游决定） | ❌ |
| `context_management.compact_threshold` | 跟随上游（若上游支持） | ❌（本整改不实现） |

### 5.6 验收标准（B）

- [ ] OpenAI/Codex 官方渠道：compact 请求到达上游，响应含上游原生形态（黑盒或 mock 验证 URL/body 未改写）。
- [ ] DeepSeek 渠道：行为与现 `compact_response.go` 一致（回归测试绿）。
- [ ] 路由选路与 `adapter_key` / 能力位一致；错误渠道不误透传。
- [ ] 两条路径均产生 `request_records` + `usage_records` + settlement。
- [ ] [CAPABILITY_MATRIX.md](../chapters/phase-11-openai-responses-api/CAPABILITY_MATRIX.md) 按路径拆分 compact 声明。

---

## 6. 任务拆解（待审核）

审核通过后建议写入 [phase-11 PLAN](../chapters/phase-11-openai-responses-api/PLAN.md) 或独立增量章节。

| ID | 优先级 | 内容 | 依赖 |
| --- | --- | --- | --- |
| **TASK-11.18** | P1 | 可配置 `HTTP_MAX_JSON_BODY_MB` + `httpx.DecodeJSON` 接入 config | 无 |
| **TASK-11.19** | P1 | 文档：Nginx/前置代理 body 限制与 env 示例 | TASK-11.18 |
| **TASK-11.20** | P1 | `CompactOrchestrator` + 能力位 `responses.compact.native` / `responses.compact.synthetic` | 阶段 12 capability 可挂 key |
| **TASK-11.21** | P1 | `NativeCompact`：responses adapter 透传 `/responses/compact` + lifecycle | TASK-11.20 |
| **TASK-11.22** | P1 | `SyntheticCompact`：迁现有 `compact_response.go`，接口化 `CompactionStrategy` | TASK-11.20 |
| **TASK-11.23** | P2 | Native 失败回落 Synthetic（可配置）+ audit 日志 | TASK-11.21 |
| **TASK-11.24** | P2 | 黑盒：大 body compact 不 413；OpenAI mock compact 透传；DeepSeek 摘要回归 | TASK-11.18–22 |
| **TASK-11.25** | P2 | （可选）gateway gzip 解压 + 解压后 MaxBytesReader | TASK-11.18 |

---

## 7. 开放问题（请审核勾选/批注）

| # | 问题 | 建议默认 | 你的决定 |
| --- | --- | --- | --- |
| Q1 | `HTTP_MAX_JSON_BODY_MB` 生产默认值 | **64MB** | 待填 |
| Q2 | Native compact 上游 404 时是否自动回落 Synthetic | **是**（打 warn 日志） | 待填 |
| Q3 | Synthetic 路径是否解析入站 `type:compaction` item | **否**（本阶段）；Codex 走 Native 时用官方 item | 待填 |
| Q4 | compact 能力 key 命名 | `responses.compact.native` + `responses.compact.synthetic` | 待填 |
| Q5 | 是否本阶段实现 `context_management.compact_threshold` | **否**，单独 follow-up | 待填 |
| Q6 | 整改阶段归属 | 作为 **阶段 11 增量**（TASK-11.18+），不新开 phase | 待填 |
| Q7 | GAP-11-007 文档表述 | 拆为「Synthetic 永久限制」；Native 路径单独声明 | 待填 |

---

## 8. 风险与回滚

| 风险 | 缓解 |
| --- | --- |
| 提高 body 上限导致内存压力 | 默认 64MB 而非无限；监控 gateway heap；前置代理同步限制 |
| Native 透传误配到不支持的上游 | capability gate + 可选回落 Synthetic |
| 双路径计费不一致 | 快照仍按实际 token；Native/Synthetic 共用 settlement，成本价按渠道配置 |
| 与 DEC-018 直传分流行为不一致 | 文档与 CAPABILITY_MATRIX 显式写清 compact 分流表 |

回滚：env 恢复 1MB 等效值；compact 选路可通过能力位关闭 Native（仅 Synthetic）。

---

## 9. 审核检查清单

- [ ] 问题描述与现场是否准确
- [ ] 目标 / 非目标边界是否同意
- [ ] 默认值（body 上限、回落策略）是否拍板
- [ ] 任务拆分粒度是否合适
- [ ] 是否同意作为阶段 11 增量而非新 phase
- [ ] 审核通过后：更新 TODO_REGISTER（GAP-11-013/014）、PLAN、STATUS、DECISIONS（若需 DEC-019）

---

## 10. 参考链接

- 现实现：`internal/platform/httpx/json.go`、`internal/service/gateway/openai/responses/compact_response.go`
- [official-other-endpoints.md](../protocol/openai/responses/official-other-endpoints.md)（compact / input_tokens）
- [RESPONSES_CHAT_BRIDGE.md](../chapters/phase-11-openai-responses-api/RESPONSES_CHAT_BRIDGE.md)
- 外部：new-api `MAX_REQUEST_BODY_MB`、`relay-router.go` `/responses/compact`；LiteLLM compaction / `litellm.compress()` 文档
