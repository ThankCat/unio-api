# 设计方案：能力人工声明 + 能力字典表（移除自动校正与能力闸门）

| 属性 | 值 |
| --- | --- |
| 状态 | **accepted / 待实施**（2026-06-23 定稿） |
| 创建日期 | 2026-06-23 |
| 关联决策 | [DEC-024](DECISIONS.md#dec-024-移除能力自动校正与能力闸门改为能力字典--人工声明) |
| 取代 | [DEC-015](DECISIONS.md#dec-015-能力架构三层模型与-modelsdev-定位)（能力闸门部分 superseded）、[DEC-020](DECISIONS.md#dec-020-被动证据式模型能力自动校正)、[DEC-022](DECISIONS.md#dec-022-能力证据体系-v2used_capabilities-闭环--证据注册表)、[DESIGN-capability-autocalibration.md](DESIGN-capability-autocalibration.md)、[DESIGN-capability-evidence-v2.md](DESIGN-capability-evidence-v2.md) |
| 延续 | [DEC-023](DECISIONS.md#dec-023-移除能力架构-layer-3渠道能力收紧)（无渠道收紧）、阶段 14 [model catalog 解耦](../chapters/phase-14-model-catalog-decoupling/PLAN.md) |
| 能力 key 来源 | **从 `keys.go` 迁到 DB 能力字典表 `capability_keys`**；[CAPABILITY_KEYS.md](../protocol/CAPABILITY_KEYS.md) 降级为人类参考 |

---

## 1. 背景与动机

### 1.1 现场问题

1. **自动校正噪声**：能力自动校正（DEC-020 / DEC-022 + 证据 v2）在 Codex 真实流量下长期出现 Admin「校正建议」**成功 N · 证据 0**，运营难以理解、难以信任；校正档位 / 建议采纳 / rollup / watermark 整条链路认知成本高。
2. **闸门空转**：能力闸门（DEC-015）`enforce` 全程**未开启**（`CapabilityEnforcement` 零值 = observe 模式），生产里只产出 `capability gate observe: model capability unavailable` 的 WARN 日志与 metric，**不拒绝任何请求、也不参与选路**。即闸门当前对线上**无功能性作用**，只有噪声与维护成本。
3. **诉求**：运营希望「以官网 / models.dev 为准，人工声明能力即可」；并希望**新增一个能力时只改数据、不改代码**。

### 1.2 产品方向（定稿）

1. **彻底移除**能力自动校正及证据链（worker、CLI、DB 表、`used_capabilities` / `delivery_mode`、evidence 包、前端入口）。
2. **彻底移除能力闸门**（observe + enforce + `required_capabilities` 推断 + `capability_signals` + 闸门 metric + 审计列）。capability 子系统退化为**纯文档 / 目录元数据**，无任何运行时行为。
3. **新增能力字典表 `capability_keys`**：成为合法能力 key 的**唯一真源**（取代 `keys.go` 常量注册表）；带**中文描述**字段供运维区分。新增能力 = 往字典表插一行，**永不改代码**。
4. **模型能力 `model_capabilities` 保留为纯声明 / 展示**：`support_level` / `limits` 在**创建模型 / 采纳目录时**人工填写，仅用于 Admin 能力矩阵展示与运维记录，不被任何运行时逻辑读取。
5. **合并新建模型入口**：「自定义」与「从模型目录同步」合并为带下拉的「新建」按钮。

---

## 2. 目标与非目标

### 2.1 目标

| # | 目标 |
| --- | --- |
| G1 | 删除自动校正全栈，消除「证据 0」运营噪声 |
| G2 | 删除能力闸门全栈，消除空转 WARN 与维护成本；capability = 纯数据 |
| G3 | 新增 `capability_keys` 字典表为 key 唯一真源（含中文描述），新增能力零代码 |
| G4 | `model_capabilities` 收敛为「创建 / 采纳时人工声明 + 展示」 |
| G5 | Admin 新建模型 UX：下拉「自定义 \| 从模型目录同步」 |
| G6 | 文档与 DEC 对齐，标明 superseded 关系 |

### 2.2 非目标（本阶段不做）

| 项 | 说明 |
| --- | --- |
| 能力的运行时行为数据化 | 请求是否「用到」某能力的解析本属协议代码；删闸门后**不再需要**，故不做 |
| 恢复自动校正 / 闸门 | 删除为**破坏性**；未来若需要预检或被动学习，须新开设计 |
| 改 gateway 公开契约 | `/v1/*`、routing 选路、pricing 不变（仅去掉路由里的能力观测旁路） |
| 改 models.dev 同步语义 | 仍只写 `model_catalog*`，采纳时才写 `model_capabilities`（阶段 14 已定） |
| 能力模板（带档位的套餐） | 上一版设计的 `capability_templates` **不做**；档位在创建 / 采纳时直接填 |

> 说明：本设计相对前一版的最大变化——放弃「带 `support_level`/`limits` 的能力模板表」，改为「纯 key 字典表 + 创建时填档位」，并**额外删除能力闸门**。原因见 §1.1 与决策记录 DEC-024。

---

## 3. 目标态：capability 退化为「数据三件套」

```text
┌──────────────────────────────────────────────────────────────┐
│ ① capability_keys（新 DB 字典表，key 唯一真源）                  │
│    key · domain · display_name · description(中文) · sort      │
│    · 取代 keys.go 常量注册表                                    │
│    · Admin 下拉 / 能力矩阵列均来自这里                          │
│    · 校验 model_capabilities 写入的 key 合法性                  │
│    · 新增能力 = 插一行，零代码                                  │
└───────────────────────────────┬──────────────────────────────┘
                                │ 校验 key 合法（写入边界）
                ┌───────────────┴───────────────┐
                ▼                               ▼
┌──────────────────────────┐      ┌──────────────────────────┐
│ ② model_catalog_         │      │ ③ model_capabilities      │
│    capabilities          │ 采纳 │   （声明 / 展示）           │
│   （目录粗能力提示）       │─────▶│   model ↔ key + level +   │
│   阶段 14 已有            │      │   limits                  │
└──────────────────────────┘      │   · 创建 / 采纳时人工填    │
                                   │   · Admin 矩阵展示        │
                                   │   · /v1/models cap-tags   │
                                   │     + capability 过滤      │
                                   └────────────▲──────────────┘
                                                │ 手工 CRUD
                                       ModelCapabilitiesDialog
```

**关键变化**：删除闸门后，`model_capabilities` 不再被 capability **闸门**读取，但**仍有读取者**——面向客户的 `/v1/models` 接口（见 §3.4）。它纯粹是「声明这个模型支持什么」的记录，服务于 Admin 能力矩阵、运维沟通与客户预检。

### 3.1 ① `capability_keys`（新字典表 = key 唯一真源）

- **取代 `keys.go`**：合法能力 key 的判定从「代码常量 map + `IsRegisteredKey`」改为「查 DB 字典表（带内存缓存）」。
- 开发期 DB 可自由重构（用户确认）；字典随迁移 seed 初始 33 个 key + 中文描述。
- 用途：Admin 能力下拉、能力矩阵表头、校验 `model_capabilities` 写入的 key。
- `keys.go` **整体删除（活法二）**：合法 key 真源全部移到字典表；仍需命名 key 的「幸存生产者」（adapter `capability_profile.go` ×2、目录 `coarseCapabilities`）改用**字符串字面量**（`"text.input"`），不再依赖 Go 常量（见 §5.3）。
- `SupportLevel` 等仍被 `model_capabilities` 校验用的枚举迁到独立小文件 `support_level.go` 保留。

### 3.2 ② `model_catalog_capabilities`（阶段 14，保留）

- models.dev 同步产出的粗能力提示，仅用于采纳时预填 `model_capabilities` 与目录展示。
- 与本设计无冲突，原样保留。

### 3.3 ③ `model_capabilities`（保留，纯声明 / 展示）

- 表结构不变：`(model_id, capability_key)` PK，`support_level`，`limits`，`updated_by`。
- 写入来源（删除校正 + 闸门后）：

| 来源 | 路径 |
| --- | --- |
| 手工 | Admin `PUT/DELETE /models/{id}/capabilities/{key}` |
| 目录采纳 / 刷新 | `POST /models/from-catalog`、`POST /models/{id}/catalog-refresh`（阶段 14） |
| Adapter seed | `POST /capability/adapter-seed-jobs`（保留，运营手动触发） |
| ~~自动校正~~ | **删除** |

- **读取者**（删除闸门后仍存在，均**不经闸门**）：
  - Admin 能力矩阵 / 模型详情展示；
  - **面向客户的 `/v1/models`**（见 §3.4）。

### 3.4 `/v1/models` cap-tags 与 capability 过滤（重要：必须保留）

`internal/app/gatewayapi/openai/models/handler.go` 暴露 OpenAI 兼容的 `/v1/models`，其中 Unio 扩展字段
`capabilities` 是每个模型的 **cap-tags**（`support_level <> 'unsupported'` 的 `capability_key`，去重升序），
并支持 `?capability=a,b,c` 做 **AND 过滤**供客户预检。

- 数据路径：`ListAvailableModelsForProject`（`models.sql.go` 的独立 SQL）**直接读 `model_capabilities`**，
  **不经过 capability 闸门 / `Evaluate`**。故删除闸门**完全不影响**此接口。
- 这是「用户端展示模型能力」的一等读取者，是保留 `model_capabilities` 的首要理由。
- 删闸门后此路径**原样保留**；`?capability=` 过滤为 lenient 语义（未识别 key 自然匹配不到，不报错）。
- 与字典表的关系：cap-tags 来自 `model_capabilities`，其 key 经 FK 约束于 `capability_keys`，故对外暴露的
  能力标签天然是合法 key。

---

## 4. 能力字典表 `capability_keys`

### 4.1 Schema（计划迁移，开发期可调）

```sql
CREATE TABLE capability_keys (
    key          TEXT PRIMARY KEY,
    domain       TEXT NOT NULL DEFAULT '',     -- 分组: text/image/audio/tools/reasoning/...
    display_name TEXT NOT NULL DEFAULT '',     -- 简短名（可中文）
    description  TEXT NOT NULL DEFAULT '',     -- 中文描述，供运维区分（可写明 OpenAI/Anthropic 语境）
    sort_order   INTEGER NOT NULL DEFAULT 0,   -- Admin 展示排序
    deprecated   BOOLEAN NOT NULL DEFAULT false,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);
```

`model_capabilities.capability_key` 增加外键 `REFERENCES capability_keys(key) ON UPDATE CASCADE ON DELETE RESTRICT`（保证不能声明字典里没有的 key，也不能删掉仍被模型引用的 key；软退役用 `deprecated`）。

### 4.2 中文描述（运维区分用）

`description` 用中文写清「这个能力是什么、在哪个协议 / 厂商语境下出现」，例如：

| key | display_name | description（示例） |
| --- | --- | --- |
| `tools.builtin.web_search` | 内置联网搜索 | OpenAI Responses 服务端内置 `web_search` 工具；DeepSeek 无对应内置工具 |
| `responses.encrypted_content` | 推理项加密透传 | OpenAI Responses 推理项 `encrypted_content` 跨轮携带 |
| `reasoning.effort` | 推理强度档位 | OpenAI `reasoning_effort` / Responses `reasoning.effort`；Anthropic 用 thinking budget 另计 |
| `reasoning.budget` | 思考预算 | Anthropic thinking budget（与 OpenAI effort 区分） |

> 描述只服务于人（运维 / Admin），不参与任何判定逻辑。

### 4.3 Seed

迁移内 seed 当前 33 个 key（沿用 CAPABILITY_KEYS.md 列表）+ 中文描述 + domain 分组 + sort_order。后续新增能力直接 `INSERT`（或 Admin 增删，见 §6）。

---

## 5. 移除范围

### 5.1 删除的 DB 对象（计划迁移 000045+，可拆步）

| 对象 | 原迁移 | 归属 | 说明 |
| --- | --- | --- | --- |
| `model_capability_suggestions` | 000039 | 自动校正 | 校正建议 |
| `model_capability_observations` | 000038 | 自动校正 | rollup 观测 |
| `capability_calibration_state` | 000040/000041 | 自动校正 | watermark + 租约 |
| `models.capability_autocalibrate` | 000037 | 自动校正 | per-model 档位 |
| `request_attempts.used_capabilities` | 000042 | 证据 v2 | 证据落库 |
| `request_attempts.delivery_mode` | 000043 | 证据 v2 | 审计列 |
| `settlement_recovery_jobs.used_capabilities` | 000042 | 证据 v2 | recovery 重放 |
| `request_attempts.required_capabilities` | 000036? | **闸门** | 闸门审计列（随闸门删除） |
| `request_records.required_capabilities`（如有） | — | **闸门** | 同上 |

**保留**：`model_capabilities`、`model_catalog*`、`model_capability_sync_jobs`。
**新增**：`capability_keys`（§4）。

### 5.2 删除的代码 —— 自动校正 + 证据 v2

| 区域 | 删除 |
| --- | --- |
| `internal/core/capability/calibration/` | 整包 |
| `internal/core/capability/evidence/` | 整包 |
| `internal/app/workers/capability_calibration_worker.go` | 删除 |
| `cmd/worker-server` | `calibrate-capabilities` 子命令 |
| `internal/bootstrap/worker_server.go` | calibrator 装配 |
| `config` | `CapabilityAutocalibrateConfig` + `CAPABILITY_AUTOCALIBRATE_*` |
| `ResponseFacts.UsedCapabilities` | 字段及 adapter/settlement 生产链 |
| `output_capabilities.go`、`bridge_facts.go` 等 | 证据 v2 专用文件 |
| `scripts/calibrate-capabilities.sh` | 删除 |
| Admin API / 前端 | suggestions / autocalibrate 路由、handler、`CapabilityPage` 建议区、`ModelCapabilitiesDialog` 档位下拉、`api/capability.ts` 相关客户端 |

### 5.3 删除的代码 —— 能力闸门（本轮新增）

| 区域 | 删除 |
| --- | --- |
| `internal/core/capability/gate.go` | `Evaluate`、`RequestLimits`、`GateResult*`、`Evaluation` |
| `internal/core/capability/inference.go` | `Infer` / `InferLimits` / `RequestSignals` |
| `internal/core/capability/keys.go` | **整体删除（活法二）**；`Key` 常量、`registeredKeys`、`IsRegisteredKey`、`RegisteredKeys` 全删。`SupportLevel` / `IsValidSupportLevel` 迁到 `support_level.go`（`model_capabilities` 仍校验档位）。校验 key 合法性改查 `capability_keys` 字典 |
| `internal/core/capability/set.go` | 若仅服务 `Infer`/`Evaluate` 则删除 |
| `capability_profile.go` ×2、`feed.go coarseCapabilities`（**保留功能**） | 不删，但把 `capability.KeyXxx` 常量引用改为**字符串字面量**（`"text.input"` 等），并改用 `string`/本地 `Declaration` 类型；同时移除 `limits` 注释里「闸门 limitViolated 唯一消费者」表述（limits 删闸门后仅展示） |
| `internal/service/gateway/capabilitygate/` | 整包 |
| `internal/app/gatewayapi/{openai,anthropic}/.../capability_signals.go`（×3 + 测试） | 删除 |
| `internal/core/routing/router.go` | `CapabilityChecker` / `CapabilityEnforcement` 接口与字段、`observeCapability` / `enforceCapability`、`CapabilityCheckInput` / `CapabilityObservation`、`ChatRouteRequest.RequiredCapabilities` / `RequestLimits`、`ErrModelCapabilityUnavailable`、`MissingCapabilities`、`failure.CodeRoutingModelCapabilityUnavailable` |
| 三协议 service 层 | 去掉构造 `RequiredCapabilities` / `RequestLimits` 与 enforce 错误渲染分支（`create_response.go`、`chat_completion.go`、`messages.go` 等） |
| `internal/service/admin/capability/enforcement.go`（+ 测试） | 删除；Admin enforce 开关 API + 前端 UI |
| `internal/bootstrap/gateway_server.go` | `SetCapabilityChecker` / `SetCapabilityEnforcement` 装配 |
| `internal/core/requestlog/*` | capability observation 持久化（`required_capabilities` 审计列写入） |
| `internal/service/admin/query/request.go`、`adminapi/requests.go` | 请求详情里的 `required_capabilities` 展示 |
| `metrics` | `IncCapabilityCheck` / `IncCapabilityRequired` / `IncCapabilityMissing` |

**保留**（capability 仅剩这些）：

- `capability_keys` 字典（新）+ 其只读 API + Admin 字典维护页（可选）
- `model_capabilities` Store / CRUD / `ModelCapabilitiesDialog`（key 下拉改读字典）
- **`/v1/models` cap-tags + `?capability=` 过滤**（`models/handler.go`、`catalog.go ListAvailableModels`、`ListAvailableModelsForProject` SQL）—— 面向客户，独立读 `model_capabilities`，不经闸门，原样保留
- `model_catalog*` 同步与采纳（阶段 14）
- adapter `dropUnsupported`（出站 Drop，DEC-012 真实行为，与闸门无关）
- `SupportLevel` 枚举与校验

> **活法二的代价与缓解**：删 `keys.go` 常量后，幸存生产者用字符串字面量，**失去编译期拼写检查**。缓解：
> 加一个**一致性测试**——遍历 `capability_profile.go` / `coarseCapabilities` 产出的 key 字符串，断言每个都
> 存在于 `capability_keys` 字典 seed（或运行期查字典）；并保留 `capability_profile_test.go` 对 profile↔drop 的守护。

---

## 6. Admin API 与前端

### 6.1 能力字典 API

| Method | Path | 说明 |
| --- | --- | --- |
| GET | `/admin/v1/capability/keys` | 列表（key + domain + display_name + description + deprecated），供下拉 / 矩阵 / 字典页 |
| POST | `/admin/v1/capability/keys` | 新增能力 key（可选；也可只靠迁移 seed + SQL） |
| PUT | `/admin/v1/capability/keys/{key}` | 改 display_name / description / domain / sort / deprecated |
| DELETE | `/admin/v1/capability/keys/{key}` | 删除（仅当无 `model_capabilities` 引用，否则 RESTRICT 拒绝；一般用 `deprecated`） |

> 写操作可按需开放：若运维习惯改库 seed，POST/PUT/DELETE 可后置。最小实现只需 GET。

### 6.2 模型能力编辑（**批量,一次保存**）

**现状痛点**：当前仅 per-key 接口——`PUT /admin/v1/models/{id}/capabilities/{key}` / `DELETE .../{key}`，
改 N 个能力 = N 次请求 + N 次保存,运维很累。

**目标**：批量编辑 + 一次保存。

**新增批量接口**（声明式整表覆盖,语义最简单）：

| Method | Path | 语义 |
| --- | --- | --- |
| PUT | `/admin/v1/models/{id}/capabilities` | body 为该模型**期望的完整能力集** `[{key, support_level, limits}]`；服务端在一个事务里 upsert 列出的、删除未列出的（声明式 replace-all） |

- 保留 per-key `PUT/DELETE .../{key}` 作兼容（可选）；前端主路径走批量。
- 校验：批量内每个 `key` 经字典校验、`support_level` 经枚举校验、`limited` 才允许 `limits`；任一不合法**整批拒绝**（事务回滚）。

**前端 `ModelCapabilitiesDialog` 改造**：

- 一次性列出字典全部 key（按 `domain` 分组，带 `display_name` + `description`）。
- 每行一个 `support_level` 选择：`未声明 / full / limited / unsupported`（「未声明」= 不写该行）。
- 支持**多选 + 批量设档**：勾选多行 → 一键设为 `full` / `unsupported` 等。
- 底部单个「保存」→ 收集全表状态 → 调一次批量 `PUT`。
- key 下拉/列表来源从 `keys.go` 改为 `GET /capability/keys`。
- `support_level=limited` 时 `limits`（如 `reasoning.effort` 的 `{"max_effort":"high"}`）**删闸门后不再被消费**，仅展示记录，UI 标注「仅记录，不参与运行时判定」。

### 6.3 新建模型下拉（原 Phase 3）

`ModelsPage`「新建」改为 Dropdown：

| 菜单项 | 行为 |
| --- | --- |
| 自定义 | 打开 `ModelFormDialog` |
| 从模型目录同步 | 跳转 `/model-catalog` 或打开 `AdoptFromCatalogDialog` |

### 6.4 删除的 UI

- `CapabilityPage` 校正建议区；`ModelCapabilitiesDialog` 校正档位下拉；`api/capability.ts` suggestions/autocalibrate
- Admin 能力 enforce 开关页 / 组件
- 请求详情里的 `required_capabilities` / capability observe 展示

---

## 7. 运营流程（删闸门后）

```text
1. models.dev 同步（可选）→ 刷新 model_catalog
2. 新建模型：
     a. 从目录采纳 → 带 coarse 能力 → 按官网微调档位 → 保存
     b. 自定义新建 → 手填能力（key 下拉来自字典，带中文描述）→ 保存
3. 日常：ModelCapabilitiesDialog 手工改；catalog-refresh 追更目录指纹
4. 新增一个能力：往 capability_keys 插一行（或 Admin 新增）→ 立即可在模型上声明 / 展示
```

**不再存在**：required 推断、observe WARN、enforce 拒绝、校正建议。能力声明纯粹是文档化记录。

---

## 8. 实施分期

| Phase | 内容 | 验收 |
| --- | --- | --- |
| **P1** | 删自动校正 + 证据链（迁移、代码、前端） | `go test ./...`、settlement 测试绿；migrate up/down |
| **P2** | 删能力闸门全栈（routing / signals / gate / inference / keys.go / enforce / metric / 审计列） | build/vet/test 绿；gateway 三协议回归（请求行为不变，因 enforce 本就关） |
| **P3** | 新增 `capability_keys` 字典表 + seed + 只读 API；`ModelCapabilitiesDialog` 改读字典 | 字典列表可用；模型能力 CRUD 正常 |
| **P4** | 新建模型下拉（自定义 / 目录）+ 删除残留 UI | ModelsPage UX |

P1、P2 可分别独立合并；P3 依赖 P2（keys.go 删除后 key 来源切字典）。

> 注：P2 删 `keys.go` 与 P3 建字典表存在「key 真源切换」窗口——实现时可在同一 PR 内先建表 seed、再切 `model_capabilities` 校验到字典、最后删 `keys.go`，避免中间态无 key 校验。

---

## 9. 与既有文档关系

| 文档 | 状态 |
| --- | --- |
| `DESIGN-capability-autocalibration.md` | **superseded** → 本文 |
| `DESIGN-capability-evidence-v2.md` | **superseded** → 本文 §5 |
| `DEC-020` / `DEC-022` | **superseded** → DEC-024 |
| `DEC-015` | **部分 superseded**：能力闸门（observe/enforce）删除；「模型层能力声明」概念以纯展示形式保留 |
| `DEC-023` | **有效**（无渠道收紧，与删闸门方向一致） |
| 阶段 14 catalog PLAN | **有效** |
| `CAPABILITY_KEYS.md` | **降级**为人类参考；权威 key 列表移到 `capability_keys` 字典表 seed |

---

## 10. 风险与缓解

| 风险 | 缓解 |
| --- | --- |
| 删闸门触及 routing / 三协议 service | enforce 本就关闭 → 删除后**生产请求行为零变化**；全量跑 routing / 三协议 / settlement 测试 |
| **误伤 `/v1/models` cap-tags** | 该接口走独立 SQL 直读 `model_capabilities`、不经闸门；删闸门时**不动** `models/handler.go` / `catalog.go` / `ListAvailableModelsForProject`；保留其单测 |
| 删 `keys.go` 失去编译期 key 拼写检查（活法二） | 幸存生产者改字符串后，加一致性测试断言其 key ∈ `capability_keys` 字典；保留 `capability_profile_test.go` |
| 失去能力可观测（metric / WARN） | 本就是空转噪声；如需「客户请求了哪些能力」可后续在 ingress 轻量打点，不依赖闸门 |
| 破坏性迁移不可回滚数据 | down 仅恢复 schema；已删 suggestions/observations/used 数据不可恢复（可接受，开发期） |
| 将来想要预检 / 选路能力 | 需新开设计重建；本设计已在 DEC-024 记录此权衡 |

---

## 11. 已定决策与剩余开放问题

**已定（2026-06-23 用户确认）**：

- **keys.go 处理 = 活法二**：整体删除常量与注册表，幸存生产者改字符串字面量。
- **`model_capabilities` 保留**：首要理由是面向客户的 `/v1/models` cap-tags 展示 + capability 过滤（§3.4），其次 Admin 矩阵。

**剩余开放（实施前可定，均有安全默认）**：

| # | 问题 | 建议默认 |
| --- | --- | --- |
| Q1 | `capability_keys` 是否开放 Admin 写（POST/PUT/DELETE） | 先只做 GET + 迁移 seed；写操作按需补 |
| Q2 | `model_capabilities` 是否保留 `limits` 列 | 保留（纯展示记录；标注不参与运行时） |
| Q3 | `Key` 类型是否保留 | 删 `keys.go` 后倾向直接用 `string`；如签名需要可留独立 `type Key string`，实现时定 |
| Q4 | `description` 是否需要按厂商分列 | 不需要，单 `description` 中文字段内写明语境即可 |

---

## 12. 版本记录

| 日期 | 变更 |
| --- | --- |
| 2026-06-23 | 初稿：移除自动校正 + 能力模板表方案 |
| 2026-06-23 | 重大修订：**额外移除能力闸门**；能力模板表改为 **`capability_keys` 字典表（key 唯一真源 + 中文描述）**；`keys.go` 删除；`model_capabilities` 收敛为纯声明 / 展示 |
| 2026-06-23 | 定 keys.go=活法二（删常量、生产者改字符串）；**纠正**「无运行时读取者」——新增 §3.4：`/v1/models` cap-tags + capability 过滤直读 `model_capabilities`、不经闸门、必须保留；补对应风险与缓解 |
| 2026-06-23 | §6.2 模型能力编辑改为**批量声明式**：新增 `PUT /models/{id}/capabilities`（整表 replace-all，一事务），`ModelCapabilitiesDialog` 改多选 + 批量设档 + 一次保存 |
