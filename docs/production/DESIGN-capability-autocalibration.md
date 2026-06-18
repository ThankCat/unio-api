# 设计方案（待审核）：模型能力自动校正（从真实流量学习）

| 属性 | 值 |
| --- | --- |
| 状态 | **accepted / 已审核**（Q1–Q7 全部采用建议默认，见 §12） |
| 创建日期 | 2026-06-18 |
| 审核日期 | 2026-06-18 |
| 关联现场 | observe 期 `capability gate observe: model capability unavailable` WARN 刷屏；模型 `model_capabilities` 声明不全，与 Codex 真实请求所用能力不一致 |
| 关联架构 | 能力架构 DEC-015；[CAPABILITY_KEYS.md](../protocol/CAPABILITY_KEYS.md)；observe/enforce 闸门（[GAP-12-003](TODO_REGISTER.md#gap-12-003)） |
| 已落 GAP | [GAP-12-013](TODO_REGISTER.md#gap-12-013)（能力自动校正） |
| 已落 DEC | [DEC-020](DECISIONS.md#dec-020-被动证据式模型能力自动校正)（被动证据式能力校正） |

---

## 1. 背景与问题陈述

### 1.1 现象

`gateway-server` 长期刷以下 WARN（observe 模式，**不拦请求**）：

```text
WARN capability gate observe: model capability unavailable
  model_db_id=1 capability_result=model_unavailable
  required_capabilities=[... tools.custom tools.parallel tools.builtin.web_search ...]
  missing_model_capabilities=[prompt_cache responses.encrypted_content tools.builtin.mcp tools.builtin.web_search tools.custom tools.parallel]
```

### 1.2 根因

- 运营创建模型时**手工声明的能力清单不全**（`model_capabilities` 只有一部分 key）。
- 而 Codex 真实请求会用到更多能力（apply_patch=`tools.custom`、并行工具、web_search、MCP、prompt cache、加密 reasoning）。
- 声明 < 实际使用 → observe 闸门逐条记 `missing` WARN。
- 客户也很难手工把第三方中转上游的能力对全（官方文档难查、中转能力 = 其背后真实模型的能力且可能被阉割）。

### 1.3 机会

`request_attempts` 已逐条持久化 **这次请求需要哪些能力（`required_capabilities`）+ 是否成功（`status`）+ 证据线索（`finish_class`）**；`usage_records` 已持久化 **cache 命中 / reasoning token**。即「真实流量已经把模型实际能力暴露出来了」，可被动学习，无需主动探针、零额外上游成本。

---

## 2. 目标与非目标

### 2.1 目标

1. **从真实成功流量被动学习**模型实际具备的能力，补齐 `model_capabilities`，消除 observe WARN，并为切 enforce 打基础。
2. **per-model 开关**：每个模型可独立设 `off` / `suggest` / `auto`。
3. **证据分级**：只有「响应真用到了该能力」（强证据）才允许自动补；弱证据只产生建议待人工采纳。
4. **安全可控**：add-only、manual 永远优先、可审计、可撤销、规模化无 OOM/写风暴。

### 2.2 非目标（本阶段不做）

| 项 | 说明 |
| --- | --- |
| 主动探针（向上游发测试请求） | 被动学习已够用且零成本；主动探针单列后续任务 |
| 自动**下调/删除**能力 | add-only；上游退化只出告警，由人工下调（防抖动误删） |
| 学习 `limited` 的细粒度 `limits`（如 reasoning.effort 允许档位集合） | 只能粗粒度 `full`；细粒度仍人工 |
| 跨模型/跨渠道的能力推断泛化 | 严格按 (model, channel) 证据，不外推 |

---

## 3. 总体设计

```text
真实流量
  └─ request_attempts(status=succeeded, required_capabilities[], finish_class)
       + usage_records(cache_read_input_tokens, reasoning_output_tokens)
            │ 后台 worker（cron，不动请求热路径）
            ▼
   增量扫描（watermark）→ 聚合进 rollup 表（按 model × channel × capability）
            │ 决策（读小 rollup，不碰原始大表）
            ▼
   对「成功用过 但 model_capabilities 未声明 且 无人工声明」的能力：
      ├─ 强证据 + 阈值达标 + 单渠道模型 + model.mode=auto → 自动 upsert(full, updated_by=auto_calibrate) + 记审计
      └─ 否则（弱证据/多渠道/有 limits/model.mode=suggest）→ 写 suggestion(status=pending) 等 admin 采纳
```

**核心纪律**：被动、增量、证据式、add-only、manual 优先、per-model 可控、全程可审计可撤销。

---

## 4. 数据模型变更

### 4.1 `models` 加 per-model 开关

```sql
ALTER TABLE models ADD COLUMN capability_autocalibrate TEXT NOT NULL DEFAULT 'suggest'
  CHECK (capability_autocalibrate IN ('off', 'suggest', 'auto'));
```

| 档 | 含义 |
| --- | --- |
| `off` | 不学习（手写能力即权威） |
| `suggest`（默认） | 只产生建议，等人工一键采纳 |
| `auto` | 强证据自动补；弱证据仍只建议 |

### 4.2 rollup 聚合表（规模化关键）

```sql
CREATE TABLE model_capability_observations (
  model_id        BIGINT NOT NULL REFERENCES models(id) ON DELETE CASCADE,
  channel_id      BIGINT NOT NULL REFERENCES channels(id) ON DELETE CASCADE,
  capability_key  TEXT   NOT NULL,
  success_count   BIGINT NOT NULL DEFAULT 0,   -- 成功且 required 含该 key 的尝试数
  evidence_count  BIGINT NOT NULL DEFAULT 0,   -- 其中带强证据的尝试数
  first_seen_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
  last_seen_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
  PRIMARY KEY (model_id, channel_id, capability_key)
);
```

按 **(model, channel)** 粒度学习，避免多渠道（分档卖档）把强渠道能力误套到弱渠道。

### 4.3 建议/决策表（admin 工作流 + 审计）

```sql
CREATE TABLE model_capability_suggestions (
  id              BIGSERIAL PRIMARY KEY,
  model_id        BIGINT NOT NULL REFERENCES models(id) ON DELETE CASCADE,
  capability_key  TEXT   NOT NULL,
  suggested_level TEXT   NOT NULL CHECK (suggested_level IN ('full','limited')),
  evidence_kind   TEXT   NOT NULL CHECK (evidence_kind IN ('strong','weak')),
  rationale       JSONB  NOT NULL,              -- {success_count, evidence_count, ratio, window, channel_ids, sample_attempt_ids}
  status          TEXT   NOT NULL CHECK (status IN ('pending','accepted','dismissed')),
  created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
  decided_at      TIMESTAMPTZ,
  decided_by      TEXT,
  UNIQUE (model_id, capability_key)
);
```

`auto` 模式自动 upsert 时同时写一条 `status=accepted, decided_by=auto_calibrate` 留痕。

### 4.4 watermark（增量游标）

```sql
CREATE TABLE capability_calibration_state (
  id                       SMALLINT PRIMARY KEY DEFAULT 1 CHECK (id = 1),
  last_processed_attempt_id BIGINT NOT NULL DEFAULT 0,
  updated_at               TIMESTAMPTZ NOT NULL DEFAULT now()
);
```

> 实现细节（可合并 4.2/4.3、watermark 是否单行表）列为开放问题 Q5。

---

## 5. 证据规则（基于真实审计列，v1）

| 能力 key | 强证据来源（成功尝试上） | 档位 |
| --- | --- | --- |
| `tools.function` / `tools.custom` / `tools.parallel` / `tools.choice_required` | `request_attempts.finish_class = 'tool_use'`（证明工具真被调用） | 强 |
| `prompt_cache` | `usage_records.cache_read_input_tokens > 0`（state='known'，证明缓存真命中） | 强 |
| `reasoning.effort` / `reasoning.budget` | `usage_records.reasoning_output_tokens > 0`（state='known'，证明真推理） | 强 |
| `image.input` / `audio.input` / `file.input` | 仅「带该模态且成功、未 bad_request」 | 弱 |
| `tools.builtin.web_search` / `file_search` / `code_interpreter` / `mcp` | 当前审计无法证明服务端真执行 | 弱 |
| `responses.encrypted_content` / `reasoning.summary` / `response_format.*` / `service_tier` / `logprobs` / `server_state.*` | 当前审计无落点 | 弱 |

> **重要限制（诚实声明）**：`finish_class=tool_use` 只证明「某个工具被调了」，无法精确区分 function vs custom；v1 把它当作「该尝试 required 中所有 `tools.*` key 的强证据」（粗粒度，足够消噪，enforce 前人工抽检）。弱证据能力**永不自动补**，只进建议；要变强证据需后续给 adapter 增加按 key 的命中埋点（单列任务）。

---

## 6. 阈值与判定

对每个 (model, channel, capability) 未声明项：

- `success_count ≥ MIN_SUCCESS`（默认 20，防偶发）
- 强证据键还需 `evidence_count / success_count ≥ MIN_EVIDENCE_RATIO`（默认 0.8）
- `last_seen_at` 在 lookback 窗口内（默认 7 天）

自动补（写 `model_capabilities` full）的**全部前置**：① 该 key 是强证据键且比例达标；② 模型当前**只有一个 enabled 渠道**（多渠道→只建议）；③ `model.capability_autocalibrate='auto'`；④ 该 (model,key) **无任何人工声明**（`updated_by≠auto_calibrate`）。
否则 → 写/更新 `suggestion(status=pending)`。

---

## 7. 后台任务设计（复用现有 worker 框架）

与 models.dev 同步同构（`internal/app/workers/model_catalog_sync_worker.go` 为范本）：

- `internal/core/capability/calibration`：纯函数 planner（输入 rollup + 模型/渠道/已声明能力 + 阈值 → 输出「自动补集合」+「建议集合」）+ store（增量聚合 / upsert / watermark），**可单测、无 IO 混入**。
- `internal/app/workers/capability_calibration_worker.go`：cron（interval 门控 + 失败退避 + 连续失败告警），单实例锁（复用 settlement recovery 锁模式）。
- `worker-server calibrate-capabilities --dry-run` 子命令：只打印将要补/建议的变更，不落库（上线前演练）。

**执行流**：读 watermark → 批量拉 `id > watermark AND status='succeeded'` 的 attempts（join `channel_models` 映射 Unio model、join `usage_records` 取证据）→ 增量更新 rollup → 推进 watermark → 决策（读 rollup）→ 产出自动补/建议（每轮 `MAX_CHANGES_PER_RUN` 封顶）。

---

## 8. admin 工作流

- 列表：`GET` 待采纳建议（model、capability、evidence_kind、rationale、success/evidence 计数）。
- 操作：`accept`（→ upsert `model_capabilities` full + 标 `decided_by`）/ `dismiss`（→ 不再重复打扰，见下）。
- 可见性：`model_capabilities` 中 `updated_by=auto_calibrate` 的行在 admin 标注「自动」，可一键撤销。
- **抑制重复**：`dismiss` 或人工把该 (model,key) 设为 `unsupported`（manual）后，worker 永不再补/再建议该项（manual 优先 + dismissed 记录）。

---

## 9. 配置项（默认全保守）

| env | 默认 | 说明 |
| --- | --- | --- |
| `CAPABILITY_AUTOCALIBRATE_ENABLED` | `false` | worker 总开关（opt-in） |
| `CAPABILITY_AUTOCALIBRATE_INTERVAL` | `6h` | 调度间隔 |
| `CAPABILITY_AUTOCALIBRATE_LOOKBACK` | `168h` | 只看近 7 天 |
| `CAPABILITY_AUTOCALIBRATE_MIN_SUCCESS` | `20` | 单 (model,channel,key) 最小成功数 |
| `CAPABILITY_AUTOCALIBRATE_MIN_EVIDENCE_RATIO` | `0.8` | 强证据比例阈值 |
| `CAPABILITY_AUTOCALIBRATE_MAX_CHANGES_PER_RUN` | `200` | 单轮变更封顶（防写风暴） |

per-model `capability_autocalibrate` 列覆盖「是否对该模型生效及档位」。

---

## 10. 风险与缓解（规模化全盘考虑）

### A. 性能 / 数据量
| 风险 | 缓解 |
| --- | --- |
| `request_attempts` 无限增长，全表扫越来越贵 | **增量扫**（watermark，只扫新行）+ 只看 lookback 窗口；需 `(status, id)` 或 `(status, created_at)` 索引 |
| `text[]` 展开 + 大表 GROUP BY | **rollup 增量聚合**，决策只读小 rollup，成本与历史总量解耦 |
| 热点写库 / 锁竞争 | 只在「新能力首次跨阈值」时写（极罕见）；幂等 upsert；`MAX_CHANGES_PER_RUN` 封顶 |
| cron 重入 / 多实例并发 | **DB 单例分布式租约**（迁移 000041：`capability_calibration_state.locked_by/locked_until`；抢到才跑、运行中续租、崩溃后据 TTL 自动释放）；watermark 推进幂等。✅ 已实现（`calibration.Lease`） |

### B. 正确性（最要命）
| 风险 | 缓解 |
| --- | --- |
| 量大后「200 但其实没用到」也会高频出现，纯计数会误补 | **比例阈值**（强证据/成功）而非绝对数；弱证据**永不自动补** |
| 单个奇葩客户污染共享模型 | rationale 记来源；可加「多 api_key 多样性」约束（开放问题 Q3） |
| `finish_class=tool_use` 无法区分 function/custom | 粗粒度当作 tools.* 强证据；enforce 前人工抽检；精确化留埋点任务 |
| 有 limits 的能力被粗暴标 full（放过不支持档位） | 有 limits 维度的键**默认只建议**，不自动 |

### C. 多渠道归属
| 风险 | 缓解 |
| --- | --- |
| 同模型多渠道（分档），强渠道学到的能力套到弱渠道超声明 | rollup 按 (model, channel)；**v1 只对单渠道模型 auto**，多渠道只建议；后续可自动下 Layer 3 渠道 override |

### D. 回滚 / 治理
| 风险 | 缓解 |
| --- | --- |
| 人工删了自动能力，下轮又被加回 | **manual 永远优先**（worker 不碰 `updated_by≠auto_calibrate` 行）；`dismiss`/设 `unsupported` = 永久抑制 |
| 上游静默退化，add-only 不会自动收 | 故意 add-only（防抖动误删）；改为**退化告警**供人工下调。✅ 已实现（`DetectDegradations`：已声明强证据键近期证据比例塌陷 < 0.2 → WARN `alert=capability_upstream_degradation`，绝不据此删能力） |
| 自动变更不可解释 | 每条记 rationale（计数/比例/窗口/样本 attempt id），可审计可撤销 |

### E. 运维 / 其它
- **零额外成本**：被动学习，不主动打探针、不烧 token。
- **隐私**：只聚合能力 key，不存 prompt 内容。
- **与 enforce 互斥**：enforce 开了会先拒、学不到 → 契合「observe 学习 → 声明稳定 → 切 enforce」节奏；切 enforce 前提示「模型 X 仍有 N 条待采纳建议」。
- **未注册 key**：跳过（`Infer` 只产注册 key，store 写入也校验）。

---

## 11. 任务拆解（待审核）

| ID | 优先级 | 内容 | 依赖 |
| --- | --- | --- | --- |
| TASK-A | P1 | 迁移：`models.capability_autocalibrate` 列 + observations/suggestions/state 表 + 索引；sqlc 查询 | 无 |
| TASK-B | P1 | `core/capability/calibration` planner（纯函数：rollup+模型+阈值→自动补/建议）+ store（增量聚合/watermark/upsert）+ 单测 | A |
| TASK-C | P1 | 证据提取：attempts.finish_class + usage_records(cache_read/reasoning) join 映射 | A |
| TASK-D | P1 | `workers/capability_calibration_worker.go` cron + DB 单例分布式租约（`calibration.Lease`，迁移 000041）+ 失败退避/告警 + 上游退化告警 + `worker-server calibrate-capabilities --dry-run` | B,C |
| TASK-E | P1 | config 接入（§9 env）+ per-model 档位读取 | A |
| TASK-F | P2 | admin：建议列表 / accept / dismiss / 撤销 auto 行 + 审计 | B |
| TASK-G | P2 | 文档：CAPABILITY_KEYS 证据说明、STATUS、DEC-020、GAP-12-013 | 全部 |
| TASK-H | P3 | （后续）按 key 精确命中埋点 / 多渠道自动 Layer 3 override | — |

---

## 12. 开放问题（已审核 — 全部采用建议默认）

| # | 问题 | 建议默认 | 你的决定 |
| --- | --- | --- | --- |
| Q1 | per-model 默认档 | `suggest` | ✅ 采用默认 `suggest` |
| Q2 | 强证据自动补的阈值（MIN_SUCCESS / 比例） | 20 / 0.8 | ✅ 采用默认 20 / 0.8 |
| Q3 | 是否要求「多 api_key 多样性」防单源污染 | 本阶段否，rationale 记来源即可 | ✅ 采用默认（本阶段否） |
| Q4 | 多渠道模型是否完全禁 auto（只建议） | 是（v1） | ✅ 采用默认（v1 多渠道只建议） |
| Q5 | observations/suggestions 是否合并成一张表 | 分两张（rollup 连续 / 决策离散） | ✅ 采用默认（分两张） |
| Q6 | 弱证据键是否也进建议（还是完全忽略） | 进建议（人工判断） | ✅ 采用默认（弱证据进建议） |
| Q7 | 阶段归属 | 阶段 12 能力架构增量（不新开 phase） | ✅ 采用默认（阶段 12 增量，GAP-12-013） |

---

## 13. 验收标准

- [x] 单测：planner 在「强证据达标 / 比例不足 / 多渠道 / 有 manual 声明 / off 档 / 窗口外 / 未达阈值」各路径产出正确（`calibration_test.go`，11 例）。
- [x] `--dry-run` 在本地真实库（gpt-5.5 单渠道）跑出合理结果：`prompt_cache=strong`（155 中 153 真 cache 命中）+ `tools.custom/tools.parallel/...` 建议，不落库（observations/suggestions/watermark 仍 0）。
- [x] `auto` 档：dry-run(auto) 验证 `prompt_cache` → 自动补（`updated_by=auto_calibrate`）。真实非 dry 运行 + observe WARN 消失的端到端复跑待客户启用时验证。
- [x] manual 声明的项永不被自动覆盖（planner「已声明 → 跳过」单测）；`dismiss` 后不再复现（planner「dismissed → 跳过」单测 + store 决策落库）。
- [x] 增量扫 watermark + `MAX_CHANGES_PER_RUN` 封顶已实现（dry-run 扫 170 行、max_attempt_id=260）；watermark 增量端到端待真实非 dry 运行验证。
- [x] admin 建议列表 / 采纳 / 忽略端点已实现并接线（`/capability/suggestions`、`/models/{id}/capability-suggestions/{key}/{accept,dismiss}`）；HTTP 端到端测试待补。

> 说明：单测 + adapter 级证据 + 真实库 dry-run 已验证；标「待验证」的为「真实非 dry 运行 / HTTP 端到端」，因 worker 默认关闭、需客户启用后复跑（见 GAP-12-013 / TASK-H）。

---

## 14. 参考

- 数据：`request_attempts`（`required_capabilities`/`status`/`finish_class`）、`usage_records`（`cache_read_input_tokens`/`reasoning_output_tokens`）、`model_capabilities`、`channel_models`。
- 代码：`internal/core/capability`（keys/inference/gate/store）、`internal/app/workers`（model_catalog_sync / settlement_recovery / runner）、`internal/service/gateway/capabilitygate`。
- 决策：DEC-015 能力架构；本方案见 [DEC-020](DECISIONS.md#dec-020-被动证据式模型能力自动校正)、[GAP-12-013](TODO_REGISTER.md#gap-12-013)。
