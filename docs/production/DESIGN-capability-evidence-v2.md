# 设计方案（待审核）：能力证据体系 v2（TASK-H 完整落地）

> **SUPERSEDED（2026-06-23）**：证据 v2 与校正消费链将随
> [DEC-024](DECISIONS.md#dec-024-移除能力自动校正与能力闸门改为能力字典--人工声明) **删除**（`used_capabilities`、
> `delivery_mode`、`evidence` 包、calibration worker 等）。目标态见
> [DESIGN-capability-manual-declaration.md](DESIGN-capability-manual-declaration.md)。下文仅作历史参考。

| 属性 | 值 |
| --- | --- |
| 状态 | **superseded**（曾 implemented；→ DEC-024 删除范围见 §5） |
| 创建日期 | 2026-06-23 |
| 关联现场 | 校正建议中除 `prompt_cache` 外几乎全是「弱证据 · 证据 0」；Codex（OpenAI Responses）流量下 `tools.*` / `stream` 无法自动强证据 |
| 前置设计 | [DESIGN-capability-autocalibration.md](DESIGN-capability-autocalibration.md)（DEC-020）、[CAPABILITY_KEYS.md](../protocol/CAPABILITY_KEYS.md) §5.1 |
| 关联 GAP | [GAP-12-013](TODO_REGISTER.md#gap-12-013) 残留 **TASK-H** |
| 关联 DEC | DEC-015（能力架构）、DEC-020（被动校正） |
| 首验范围 | **OpenAI 协议族**（Responses + Chat Completions），渠道对 OpenAI 完整、用 Codex 带能力请求 E2E 验收 |

---

## 1. 背景与问题陈述

### 1.1 现象（2026-06 现场）

Admin「校正建议」典型行：

```text
GPT-5.5 · stream · full · 弱证据 · 成功 162 · 证据 0 · 比例 0%
GPT-5.5 · tools.custom · … · 弱证据 · 成功 N · 证据 0
GPT-5.5 · prompt_cache · … · 强证据 · 成功 N · 证据 M · 比例 >80%
```

退化告警在 `MIN_SUCCESS=1` 时误报：`tools.custom` 已声明但 `finish_class` 非 `tool_use` → `capability_upstream_degradation`。

### 1.2 根因（代码事实，2026-06-23 审计）

证据链在 **Layer 1→2→3 断链**，不是「上游不支持」：

```text
Adapter 解析响应
  └─ ResponseFacts.UsedCapabilities     ✅ Responses adapter 已从 output item 解析（function_call / custom_tool_call / …）
           │
           ▼
Settlement / requestlog
  └─ MarkAttemptSucceeded             ❌ 未传入 used_capabilities（列存在，恒为 '{}'）
           │
           ▼
Calibration ScanSucceeded SQL       ❌ 只读 finish_class + usage（cache/reasoning），不读 used_capabilities
           │
           ▼
calibration.AttemptHasEvidence        ❌ tools.* 只看 finish_class == tool_use
                                      ❌ Responses 直传 finish_class 恒为 stop（completed → FinishStop）
```

因此 **仅 `prompt_cache`**（走 `usage_records.cache_read_input_tokens`）能在 Codex 流量下产生强证据。

`adapter/facts.go` 已注释：`UsedCapabilities` 才是 tools.* 精确来源；校正侧尚未消费。

### 1.3 目标陈述

把 TASK-H 从「最小补丁」升级为 **四层闭环**，使：

1. OpenAI Responses / Chat 真实使用的能力 → **非零 evidence** → 校正建议/ auto 补可信；
2. `stream` / `tools.builtin.*` 等在响应可证明时升为 **Strong**；
3. 退化告警只针对 **真实 Strong 键**，不再因 `MIN_SUCCESS=1` + 断链误报；
4. 验收标准：**跑 Codex 带能力请求 → worker 扫描 → Admin/DB 看到预期 evidence_count**。

---

## 2. 目标与非目标

### 2.1 目标

| # | 目标 |
|---|------|
| G1 | **统一证据注册表**（`core/capability/evidence`）：每个 key 有 Tier + 判定规则（纯函数、可单测） |
| G2 | **Gateway 生产**：OpenAI Responses + Chat adapter 填满 `ResponseFacts.UsedCapabilities`（含 stream 族） |
| G3 | **审计落库**：`request_attempts.used_capabilities` 在 settlement 成功路径写入；recovery 路径不丢 |
| G4 | **校正消费**：扫描 SQL + `AttemptHasEvidence` 改读 `used_capabilities` + usage + delivery 元数据 |
| G5 | **Admin 可读**：建议列表展示 evidence 来源摘要（used / cache / reasoning / stream） |
| G6 | **Codex E2E 验收脚本**：文档化 env、步骤、SQL 断言（见 §10） |

### 2.2 非目标

| 项 | 说明 |
| --- | --- |
| Anthropic Messages 首验 | Phase 5 跟进；首验仅 OpenAI |
| 新增 capability key | 注册表 v1 不变 |
| enforce 切换 | 仍 deferred GAP-12-009；本设计为 enforce 前把证据对齐 |
| 自动**删除**能力 | 仍 add-only；退化只告警 |
| 多渠道自动 Layer 3 override | 仍单列后续（DESIGN-autocalibration TASK-H 原条目） |

---

## 3. 目标架构

```text
┌──────────────────────────────────────────────────────────────────┐
│ L1  Evidence Production（gateway 热路径）                          │
│  openai/responses/usage.go  ─┐                                   │
│  openai/chatcompletions/      ├──► ResponseFacts.UsedCapabilities │
│  (+ Delivery: stream/batch)   ─┘                                   │
└────────────────────────────┬─────────────────────────────────────┘
                             ▼
┌──────────────────────────────────────────────────────────────────┐
│ L2  Evidence Persistence                                         │
│  lifecycle/settlement.go → MarkAttemptSucceeded.used_capabilities│
│  settlement_recovery → 同步写入 recovery job + 重放                │
│  可选列 delivery_mode TEXT（stream|batch） migration 000043        │
└────────────────────────────┬─────────────────────────────────────┘
                             ▼
┌──────────────────────────────────────────────────────────────────┐
│ L3  Evidence Registry（core/capability/evidence）                  │
│  AttemptSnapshot + HasStrongEvidence(key) + TierOf(key)          │
└────────────────────────────┬─────────────────────────────────────┘
                             ▼
┌──────────────────────────────────────────────────────────────────┐
│ L4  Consumption                                                  │
│  calibration calibrator/store · DetectDegradations · Admin · metrics│
└──────────────────────────────────────────────────────────────────┘
```

**纪律**（继承 DEC-020）：

- 被动、增量、add-only、manual 优先、per-model `capability_autocalibrate` 档位不变；
- Strong 才 auto 补（且单渠道 + 非 limited 维度 + 比例阈值）；
- Medium / Weak 只 suggest 或人工。

---

## 4. 证据分级与 key 注册表（目标态）

### 4.1 Tier 定义

| Tier | 校正 auto 补 | 退化告警 | 说明 |
|------|-------------|---------|------|
| **Strong** | ✅（满足阈值 + auto + 单渠道） | ✅ | 响应侧可证明 |
| **Medium** | ❌ | ❌ | 强暗示，仅 suggest（可配更高 ratio） |
| **Weak** | ❌ | ❌ | 仅「required + succeeded」 |
| **Manual** | ❌ | ❌ | 无审计落点，仅手工声明 |

### 4.2 OpenAI 首验 key 证据规则（完整表）

| key | Tier | HasStrongEvidence 条件 |
|-----|------|--------------------------|
| `tools.function` | Strong | `used_capabilities` 含 key；**回退**：Chat `finish_class=tool_use` 且 required 含 function |
| `tools.custom` | Strong | `used_capabilities` 含 key（**禁止**用 finish_class 笼统归因） |
| `tools.parallel` | Strong | `used_capabilities` 含 key |
| `tools.choice_required` | Strong | `used_capabilities` 含任一 client tool key 且 required 含 choice_required |
| `tools.builtin.web_search` | Strong | `used_capabilities` 含 key（Responses `web_search_call` output item） |
| `tools.builtin.file_search` | Strong | `used_capabilities` 含 key |
| `tools.builtin.code_interpreter` | Strong | `used_capabilities` 含 key |
| `tools.builtin.computer_use` | Strong | `used_capabilities` 含 key |
| `tools.builtin.image_generation` | Strong | `used_capabilities` 含 key |
| `tools.builtin.mcp` | Strong | `used_capabilities` 含 key |
| `prompt_cache` | Strong | `usage_records.cache_read_input_tokens > 0` |
| `reasoning.effort` | Strong* | `reasoning_output_tokens > 0`（*auto 仍不补 limited 档位，只 suggest） |
| `reasoning.budget` | Strong* | 同上 |
| `stream` | Medium | `used_capabilities` 含 `stream`（**永不 auto、不参与退化告警**：分发方式而非模型能力，Codex 近乎恒流式无区分度，见 Q6） |
| `stream.tools` | Medium | `used_capabilities` 含 key（同 stream：suggest only，不参与退化告警） |
| `stream.usage` | Medium | `used_capabilities` 含 key（流式终态收到 usage；同 stream：suggest only，不参与退化告警） |
| `reasoning.summary` | Medium | output 解析进 `used_capabilities` |
| `responses.encrypted_content` | Medium | output 含 encrypted reasoning / compaction item → `used` |
| `response_format.json_object` | Medium | 响应结构合规完成 |
| `response_format.json_schema` | Medium | 同上 |
| `logprobs` | Medium | 响应含 logprobs |
| `image.input` / `audio.input` / `file.input` | Medium | 请求含模态且 adapter 未 drop 该次请求 |
| `text.input` / `text.output` | Weak | 成功即可（基线，一般不参与校正产出） |
| `service_tier` / `server_state.*` / `responses.compact.*` | Manual / Weak | 首验不阻塞；compact 走 adapter 能力位 |

> **替换 v1 规则**：废弃「tools.* 仅 finish_class=tool_use」作为 Responses 主路径；Chat 保留 finish_class 作回退。
>
> **builtin 现状（已核对代码 2026-06-23）**：`responses/usage.go` 的 `responsesOutputToolCapability` **已完整映射**全部 builtin output item（`web_search_call` / `file_search_call` / `code_interpreter_call` / `computer_call` / `image_generation_call` / `mcp_call`），不止三个。因此这些 builtin key 在 **Phase 1**（settlement 落库 `used_capabilities`）后即可达 Strong，**无需等 Phase 3**；P3-2 仅剩 `encrypted_content` / `reasoning.summary` 的 Medium 解析（`wireOutputItem` 目前只解析 `type`）。
>
> **builtin 升 Strong 的依据（对 v1 决定的修订）**：v1（DEC-020、`calibration.go` `toolCallEvidenceKeys` 注释）故意把 builtin 留作弱证据，理由是「`finish_class=tool_use` 只证明模型*发起*了工具调用，不证明上游*真执行*」。v2 改判 Strong 的前提是**证据来源换了**：Responses 终态 output 里出现 `*_call` item（如 `web_search_call`）是上游服务端执行后回填的终态结果项，而非请求侧声明——这恰好补上了 v1 缺的「执行证明」。故仅当**证据来自 `used_capabilities`（output item 命中）**时 builtin 才升 Strong；Chat 的 `finish_class` 回退路径**不得**用于 builtin（builtin 在 Chat 下仍弱证据）。
>
> **stream 取舍（已定稿，见 Q6）**：stream 族（`stream` / `stream.tools` / `stream.usage`）定为 **Medium**——只 suggest、**永不 auto、不参与退化告警**。理由：流式是分发方式而非模型能力差异，Codex 近乎恒流式会让该键失去区分度，并可能在偶发 buffer 时误报退化。Phase 1–4 仅靠 `used` 写入 stream 族；`delivery_mode`（Phase 3 / 000043）仅作 Admin 审计与二级佐证，**不作为校正证据来源**。

---

## 5. Layer 1：OpenAI Adapter 埋点规格

### 5.1 Responses（Codex 主路径）

**已有**（`responses/usage.go`）：

- `usedCapabilitiesFromOutput`：`function_call` → `tools.function`，`custom_tool_call` → `tools.custom`，builtin 映射表，≥2 client tool → `tools.parallel`。

**需补**：

| 信号 | 写入 used_capabilities |
|------|------------------------|
| 流式成功终态 | `stream`；若 output 含 tool item → `stream.tools`；若终态有 usage → `stream.usage` |
| `usage.Source == SourceUpstreamStream` | 同上（与终态 output 合并） |
| output `type=reasoning` 且含 summary | `reasoning.summary` |
| output encrypted reasoning / compaction | `responses.encrypted_content` |
| `output_tokens_details.reasoning_tokens > 0` | 不写入 used（走 usage 列）；校正 HasEvidence 读 usage |

**不改**：`responsesFinishClass` 仍 completed→stop（finish_class 不再承担 tools 证据）。

### 5.2 Chat Completions

**需新增** `UsedCapabilities` 解析：

| 信号 | 写入 |
|------|------|
| `finish_reason` ∈ tool_calls / function_call | 解析 `tool_calls` 区分 function vs custom |
| ≥2 tool_calls | `tools.parallel` |
| 流式终态 | `stream` / `stream.tools` / `stream.usage`（同 Responses 纪律） |
| usage cached / reasoning | 仍走 usage_records，不进 used |

### 5.3 统一 helper

建议 `internal/core/capability/evidence/produce.go`（或 `adapter/evidence_merge.go`）：

- `MergeUsedCapabilities(base []string, stream bool, hasStreamUsage bool, …) []string`
- 升序去重；仅 registered key。

---

## 6. Layer 2：审计落库

### 6.1 requestlog 扩展

`MarkAttemptSucceededParams` 增加：

```go
UsedCapabilities []string // 来自 facts.UsedCapabilities
DeliveryMode     string // "stream" | "batch"（可选，见 migration）
```

`lifecycle/settlement.go` 在 `MarkAttemptSucceeded` 传入 `facts.UsedCapabilities`。

### 6.2 Migration（建议 000043）

```sql
ALTER TABLE request_attempts
    ADD COLUMN delivery_mode TEXT NOT NULL DEFAULT 'batch'
        CHECK (delivery_mode IN ('stream', 'batch'));
```

`used_capabilities` 已在 000042 就位。

### 6.3 Settlement recovery

`settlement_recovery_jobs.used_capabilities`（000042 已有列）：

- CreatePendingJob 写入 facts.UsedCapabilities；
- 重放 MarkAttemptSucceeded 时带回，避免流式 risk 路径丢证据。

### 6.4 一致性

| 字段 | 含义 |
|------|------|
| `required_capabilities` | 请求侧推断（已有） |
| `used_capabilities` | 响应侧证明（本设计补齐写入） |
| Admin 调试 | 可对比二者差集 |

---

## 7. Layer 3：证据注册表（`core/capability/evidence`）

### 7.1 API（建议）

```go
type AttemptSnapshot struct {
    Key              capability.Key
    Required         capability.Set   // 或 []Key
    Used             []capability.Key
    FinishClass      string
    DeliveryMode     string // Phase 3（000043）才有值；Phase 1–2 恒为空，HasStrongEvidence 不得依赖
    CacheReadTokens  int64
    ReasoningTokens  int64
    UpstreamProtocol string // openai | anthropic（request_attempts 已有列，无需迁移）
}

func HasStrongEvidence(s AttemptSnapshot) bool
func TierOf(key capability.Key) Tier
func IsStrongEvidenceKey(key capability.Key) bool  // 替代 calibration 内硬编码 map
func KeyHasLimitedDimension(key capability.Key) bool
```

> `DeliveryMode` 字段 Phase 1 即可在 struct 中预留，但**判定逻辑与 SELECT 在 Phase 1 不得引用它**（列 Phase 3 才落库）。stream 族证据 Phase 1 仅靠 `Used` 命中。

### 7.2 替换点

| 现位置 | 改动 |
|--------|------|
| `calibration/calibration.go` `AttemptHasEvidence` | 委托 `evidence.HasStrongEvidence`；**签名由 `(key, finishClass, cache, reasoning)` 改为 snapshot 入参**，须同步改 `calibrator.go` 调用点（现 `calibrator.go:179`）与 `calibration_test.go` 的 `TestAttemptHasEvidence` |
| `calibration/calibration.go` `strongEvidenceKeys` | 迁入 `evidence` 包 |
| `calibration/store.go` `AttemptEvidence` | 增加 `UsedCapabilities []capability.Key`, `UpstreamProtocol`（**`DeliveryMode` 留到 Phase 3 再加**） |
| `sql/queries/capability_calibration_reads.sql` | **Phase 1**：SELECT 增加 `used_capabilities`, `upstream_protocol`（两列已就位）；**Phase 3**：再加 `delivery_mode`（000043 落库后） |

### 7.3 Calibrator scanDeltas

对每个 required key：

```go
if evidence.HasStrongEvidence(evidence.AttemptSnapshot{...}) {
    d.evidence++
}
```

---

## 8. Layer 4：消费方与 Admin

### 8.1 BuildPlan / DetectDegradations

逻辑不变，仅 **evidence 计数来源** 变准确；`isStrongEvidenceKey` 与 builtin tools 在 `used` 命中时升为 Strong。

### 8.2 Admin UI（建议）

校正建议行增加「证据来源」tooltip/json：

```json
{
  "success_count": 30,
  "evidence_count": 27,
  "evidence_ratio": 0.9,
  "evidence_sources": {
    "used_capabilities": {"tools.custom": 25, "stream": 30},
    "cache_hits": 10,
    "reasoning_tokens": 0
  }
}
```

### 8.3 Metrics（建议）

- `unio_capability_evidence_hit_total{key,tier,protocol}`
- `unio_capability_evidence_miss_total{key,reason}`

---

## 9. 实施阶段与任务拆分

### Phase 0 — 文档冻结（本文件）

- [x] Owner 审核本 DESIGN + 更新 [CAPABILITY_KEYS.md](../protocol/CAPABILITY_KEYS.md) §5.1
- [x] GAP-12-013 TASK-H 条目指向本文件

### Phase 1 — 主链路（OpenAI Responses，P0）✅ 已实施

| ID | 状态 | 任务 | 文件 |
|----|----|------|------|
| P1-1 | ✅ | 新建 `core/capability/evidence` 包 + 单测（OpenAI tools/cache/reasoning/stream） | `evidence/evidence.go`, `evidence/produce.go`, `evidence/evidence_test.go` |
| P1-2 | ✅ | settlement 写入 `used_capabilities`（+ `delivery_mode`） | `requestlog/service.go`, `store.go`, `lifecycle/settlement.go` |
| P1-3 | ✅ | calibration SQL/store/calibrator 读 `used_capabilities` + `upstream_protocol` + `delivery_mode` | `capability_calibration_reads.sql`, `calibration/store.go`, `calibrator.go` |
| P1-4 | ✅ | Responses `responsesFacts` 补 stream 族 used（Medium 证据） | `responses/usage.go`, `responses/usage_capabilities_test.go` |
| P1-5 | ✅ | 用 `evidence.HasStrongEvidence`（snapshot 入参）替换 `AttemptHasEvidence`；硬编码 map 下沉 evidence 包 | `calibration/calibration.go`, `calibrator.go`, `calibration_test.go` |
| P1-6 | ✅ | sqlc generate + `go test ./internal/core/capability/...` 全绿 | — |

**Phase 1 完成判据**：Codex 单次带 custom tool 的成功 Responses 请求，`request_attempts.used_capabilities` 非空且含 `tools.custom`（单测已覆盖 adapter→facts；真实流量待客户启用复跑）。

### Phase 2 — Chat Completions（P1）✅ 已实施

| ID | 状态 | 任务 |
|----|----|------|
| P2-1 | ✅ | Chat `responseFacts` 解析 tool_calls → UsedCapabilities（function/custom/parallel） |
| P2-2 | ✅ | Chat 流式按 tool_call index 还原类型 + stream 族 used（`used_capabilities_test.go`） |
| P2-3 | ✅ | 单测覆盖（非流式 / 流式增量 / unknown type 跳过） |

### Phase 3 — 扩展 key + delivery_mode 列（P1）✅ 已实施

| ID | 状态 | 任务 |
|----|----|------|
| P3-1 | ✅ | migration 000043 `request_attempts.delivery_mode`（stream/batch，仅审计） |
| P3-2 | ✅ | 新增 `encrypted_content` / `reasoning.summary` 的 Medium 解析；builtin output item 映射 `usage.go` 本已完整覆盖 |
| P3-3 | ✅ | settlement recovery 写 used（INSERT + 重放 facts 带回，delivery_mode 由 UsageSource 复算） |

### Phase 4 — Admin + 指标（P2）✅ 已实施（指标降级为结构化日志）

| ID | 状态 | 任务 |
|----|----|------|
| P4-1 | ✅ | suggestion rationale 扩展 `tier` + `evidence_source`（per-decision，随 JSONB 直达 Admin） |
| P4-2 | ⚠️ 调整 | 批处理 worker 无 HTTP /metrics，Prometheus 价值低且需大量管线 → 改为完成日志 `suggested_strong/weak` + dry-run 每条 `tier/evidence_source` 结构化日志 |
| P4-3 | ✅ | `worker-server calibrate-capabilities --dry-run` 输出 `tier` / `evidence_source` / `evidence_ratio` |

### Phase 5 — Anthropic 对等（P2，OpenAI 验收后）⏳ 未实施

Messages adapter UsedCapabilities + evidence 规则 Anthropic 分支（按设计仅 OpenAI 首验，Anthropic 跟进）。

---

## 10. Codex E2E 验收手册（OpenAI 渠道完整时）

### 10.1 环境变量（验证用，非生产默认）

```env
# worker
CAPABILITY_AUTOCALIBRATE_ENABLED=true
CAPABILITY_AUTOCALIBRATE_INTERVAL=5m
CAPABILITY_AUTOCALIBRATE_LOOKBACK=168h
CAPABILITY_AUTOCALIBRATE_MIN_SUCCESS=3
CAPABILITY_AUTOCALIBRATE_MIN_EVIDENCE_RATIO=0.8

# gateway：保持 observe
CAPABILITY_ENFORCE_OPENAI_RESPONSES=false
CAPABILITY_ENFORCE_OPENAI_CHAT=false
```

验证模型（示例）：

- `models.capability_autocalibrate = suggest`（观察建议）或 `auto`（测自动补，需单渠道）
- 模型尚未声明目标 key（或 dismiss 旧建议后重跑）

### 10.2 步骤

1. **重启 gateway**（加载 adapter 改动后）+ **worker**。
2. **Codex** 对目标 `model_id` 发起带能力请求（至少覆盖）：
   - streaming Responses；
   - custom tools（apply_patch 等）→ 期望 `tools.custom`；
   - 可选：parallel tools、web_search、prompt cache 二次请求。
3. 确认 gateway settlement 成功（非 recovery 路径优先）。
4. **SQL 断言**（PostgreSQL）：

```sql
-- 最近成功 attempt 应有 used_capabilities
SELECT id, finish_class, required_capabilities, used_capabilities, delivery_mode
FROM request_attempts
WHERE status = 'succeeded'
ORDER BY id DESC
LIMIT 5;

-- 某 key 的 rollup 应有 evidence > 0
SELECT model_id, channel_id, capability_key, success_count, evidence_count
FROM model_capability_observations
WHERE capability_key IN ('tools.custom', 'stream', 'prompt_cache')
ORDER BY last_seen_at DESC;
```

5. **手动跑 worker**（不等待 cron）：

```bash
cd unio-gateway
set -a && source .env && set +a
go run ./cmd/worker-server calibrate-capabilities --dry-run
# 确认 Plan 中 tools.custom / stream 的 EvidenceCount > 0 且 EvidenceKind=strong
go run ./cmd/worker-server calibrate-capabilities
```

6. **Admin → 能力中心 → 校正建议**：待采纳行应显示 **强证据** + 非零 evidence 比例（对 Strong key）。

### 10.3 预期结果矩阵

| 能力 | 请求特征 | used_capabilities（预期） | evidence_count |
|------|----------|---------------------------|----------------|
| `tools.custom` | 响应 output 含 custom_tool_call | 含 `tools.custom` | >0 |
| `tools.parallel` | 同响应 ≥2 client tool calls | 含 `tools.parallel` | >0 |
| `stream` | 流式 Responses | 含 `stream` | >0 |
| `prompt_cache` | 二次请求命中 cache | （used 可无） | >0 via cache tokens |
| `reasoning.effort` | reasoning_tokens > 0 | — | >0 via usage |
| `tools.builtin.web_search` | output 含 web_search_call | 含 builtin key | >0 |

### 10.4 失败排查

| 症状 | 检查 |
|------|------|
| used_capabilities 恒 `{}` | Phase 1 settlement 是否写入；migration 000042 是否 applied |
| tools 有 used 但 evidence 0 | calibrator 是否仍只用 finish_class |
| stream 成功 162 证据 0 | Responses 是否在 used 中写入 stream（Phase 1-4） |
| 退化告警误报 | MIN_SUCCESS 调回 ≥20；证据接好后 ratio 应正常 |

---

## 11. 测试策略

| 层级 | 覆盖 |
|------|------|
| Unit | `evidence` 包：每个 Strong key ≥2 例（有/无证据） |
| Adapter | Responses/Chat fixture JSON → 期望 `UsedCapabilities` |
| Calibration | 扩展 `calibration_test.go`：used_capabilities 驱动 strong |
| Integration | PG：insert attempt mock → scan → evidence_count |
| Contract | `CAPABILITY_KEYS.md` ↔ `evidence/registry.go` 一致性测试 |
| E2E | §10 Codex 手册 |

---

## 12. 风险与缓解

| 风险 | 缓解 |
|------|------|
| Responses 多轮 tool，单次 attempt 只覆盖部分 used | 按 attempt 粒度聚合（正确行为） |
| finish_class 回退与 used 双计 | used 优先；仅 used 空时 Chat 才回退 finish_class |
| Medium 误 auto | Medium 永不 auto |
| 历史 attempt 无 used | 不 backfill 亦可用；新流量正确即可；可选 backfill Chat tool_use |
| MIN_SUCCESS=1 误报退化 | 验收 env 可临时 3；生产 20 |

---

## 13. 文档与 DEC 变更清单

| 文档 | 变更 |
|------|------|
| [CAPABILITY_KEYS.md](../protocol/CAPABILITY_KEYS.md) §5.1 | 替换为 Tier 表 + 指向本 DESIGN |
| [DESIGN-capability-autocalibration.md](DESIGN-capability-autocalibration.md) §5 | 标注「v1 证据表已被 v2 取代」 |
| [TODO_REGISTER.md](TODO_REGISTER.md) GAP-12-013 | TASK-H → 本 DESIGN Phase 1–5 |
| [phase-12-capability-architecture/STATUS.md](../chapters/phase-12-capability-architecture/STATUS.md) | 增加 TASK-H v2 链接 |
| DECISIONS.md | 实施完成后追加 DEC-022（能力证据 v2）实施日志（DEC-021 已被「看板经营驾驶舱」占用） |

---

## 14. 开放问题（审核时定稿）

| # | 问题 | 定稿 |
|---|------|----------|
| Q1 | 是否新增 `delivery_mode` 列 | ✅ 是（000043，Phase 3）；仅作 Admin 审计与二级佐证，**不作校正证据来源**（一级 stream 证据走 `used` 含 `stream`） |
| Q6 | `stream` 该不该是 Strong（参与 auto 补 + 退化告警） | ✅ **定稿：降为 Medium，永不 auto、不参与退化告警**。流式是分发方式而非模型能力差异，Codex 近乎恒流式会让该键失去区分度并可能在偶发 buffer 时误报退化 |
| Q2 | builtin tools 有 used 即 Strong | ✅ 是——但**仅当证据来自 `used_capabilities`（Responses output item 命中，证明服务端已执行）**；Chat `finish_class` 回退路径不得用于 builtin。见 §4.2 「builtin 升 Strong 的依据」 |
| Q3 | 验收 MIN_SUCCESS | 验证用 3，生产 20 |
| Q4 | 是否 backfill 历史 Chat `tool_use` | ❌ 首版不做 |
| Q5 | Anthropic 与 OpenAI 同批还是 Phase 5 | Phase 5（用户首验 OpenAI） |

---

## 15. 版本记录

| 版本 | 日期 | 变更 |
| --- | --- | --- |
| draft | 2026-06-23 | 首稿：四层架构、OpenAI 首验、Codex E2E 手册、TASK-H 完整任务拆分 |
| accepted | 2026-06-23 | 代码核对修订：① 修正 builtin 映射事实（`usage.go` 已全量映射，P3-2 仅剩 encrypted_content/reasoning.summary）；② Q6 定稿 stream 族降 Medium（永不 auto、不参与退化告警）；③ Q2 限定 builtin 升 Strong 仅凭 `used_capabilities` 且补论证；④ 明确 `delivery_mode` Phase 3 才落库、Phase 1 SELECT/判定不得引用；⑤ `AttemptHasEvidence` 改 snapshot 入参须同步改调用点与测试 |
| implemented | 2026-06-23 | Phase 1–4 落地：新增 `core/capability/evidence` 注册表（Tier + `HasStrongEvidence`）；settlement/recovery 写 `used_capabilities`；calibration 读 used+protocol+delivery 并委托 evidence；Responses+Chat adapter 产出 used（含 stream 族 Medium、reasoning.summary/encrypted_content）；migration 000043 `delivery_mode`；rationale 带 `tier`/`evidence_source`；dry-run 输出 tier。一并修复前置 groundwork 遗留的 3 处测试编译破损（`CreateRequestAttemptRow`→`RequestAttempt` 转换、admin fakeStore 缺 autocalibrate 方法）。`go build ./...` / `go vet ./...` / 单测全绿。DEC-022。Phase 5（Anthropic）+ 真实非 dry / HTTP E2E 待续 |
