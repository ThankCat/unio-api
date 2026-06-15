# 阶段 14 计划 - models.dev 目录解耦（Model Catalog Decoupling）

> 状态：in_progress（核心实现已落地：迁移 + sqlc + 同步改写 + 采纳/刷新/提醒 API + 前端，
> `go build/vet/test` 全绿、前端 `tsc/lint/vite build` 全绿、HTTP 端到端验证通过）。
> 当前进度与剩余项见 [STATUS.md](STATUS.md)，验收标准见 [ACCEPTANCE.md](ACCEPTANCE.md)。
>
> 决策来源：延续并修正 [DEC-015](../../production/DECISIONS.md#dec-015-能力架构三层模型与-modelsdev-定位)
> 的实现方式——「models.dev 仅作种子源、不是运行时事实源」这条原则不变，
> 但把它从「直接灌进运行时 `models` 表」改为「独立目录表 + 显式采纳 + 关联追更」。

## 1. 背景与动机

当前 models.dev 同步会把整份模型名录**直接写进运行时 `models` 表**（`source=seed_models_dev`、`status=disabled`），
导致：

- **运行时表被几百条停用行污染**，管理台模型列表混乱、难找。
- **`model_id` 被锁成 `provider/model` 格式不可改**——因为目录条目被当成了正式模型，
  而正式模型的 `model_id` 是对外稳定标识、设计上不可变（`UpdateModel` 不改、前端编辑框禁用）。
- 同步要在共享表上做 `manual` 守护 / 冲突 / 删除标记等一套合并逻辑，复杂度都来自「目录数据和运营数据混在一张表」。

这与 DEC-015 自己定的原则相矛盾：models.dev 应该是「参考目录（菜单）」，
而 `models` 表应该只放「我真正要对外服务的模型」。

## 2. 现状（代码事实，整改前）

| 维度 | 现状 |
| --- | --- |
| 同步落库 | `internal/core/modelcatalog`（`feed.go` 解析 → `merge.go` `PlanSync` → `store.go` 写 `models`）。`UpsertSeedModelByCanonicalID`/`MarkSeedModelRemovedUpstream`/`ListCanonicalModels` 直接读写 `models` 表。 |
| 表 | `models`（000007）含 catalog 专属列：`canonical_id`(UNIQUE)、`lab`、`context_window_tokens`、`max_output_tokens`、`input/output_price`、`release_date`、`source`(`seed_models_dev`/`manual`/`import`)、`removed_upstream_at`。 |
| 能力 | `model_capabilities`（000023）：`(model_id, capability_key)`，`support_level`、`limits`、`source`(`models_dev`/`manual`/`adapter_seed`)。同步首次入库时按 `coarseCapabilities()` 写 `source=models_dev` 粗能力位。 |
| 同步审计 | `model_capability_sync_jobs`（000025）。 |
| 触发 | 手动：`POST /admin/v1/capability/sync-jobs`（dry-run/apply）；定时：`ModelCatalogSyncWorker`（`MODEL_CATALOG_SYNC_ENABLED` 默认关）。 |
| 创建模型 | `POST /admin/v1/models`（`CreateModel`：只写 `model_id/display_name/owned_by/status/lab/max_output_tokens`，`source=manual`，不带元数据、不带能力）。 |
| 前端 | 能力中心 `CapabilityPage` 的「同步」Tab 触发同步；`ModelsPage` 列模型（含 seed 停用行）；`ModelFormDialog` 新建/编辑（编辑时 `model_id` 只读）；`ModelCapabilitiesDialog` 逐模型管能力。 |
| 运行时消费 | gateway `/v1/models`（`ListAvailableModelsForProject`）、routing、capability gate 都读 `models` + `model_capabilities`。 |

## 3. 问题（要解决的）

1. 运行时表被目录污染。
2. 采纳来的模型 `model_id` 不可改、被迫用 `provider/model`。
3. 同步合并逻辑因「共享表」而复杂。
4. 采纳后无法感知上游目录后续变化（无追更）。

## 4. 目标 / 非目标

### 目标
- models.dev 数据落到**独立目录表**，运行时**永不读取**它。
- 同步只刷新目录表，不再触碰 `models`。
- 提供「**从 models.dev 目录挑选模板 → 预填（`model_id`/元数据/能力清单可改）→ 创建**」的流程。
- **采纳模板时，能力提示一并带入新模型**，且采纳界面可增删改能力（用户要求重点）。
- **本地模型与目录条目关联**，提供**更新检测 + 提醒 + 一键刷新**（本期做，详见 §6.3 / §8 / §9）。
- `models` 表整改后**只含运营亲手创建/采纳的模型**，无 seed 污染。
- 含前端改动。

### 非目标
- 不动 gateway 运行时契约（`/v1/*`、routing、capability gate 行为不变）。
- 不动账务事实 schema（ledger / cost_snapshot / price_snapshot）。
- 不改能力闸门 observe/enforce 策略（仍默认 observe）。
- 不做跨 provider 能力 sub-routing（DEC-015 既有非目标）。

## 5. 总体设计（目标态）

```text
                 ┌──────────────────────────┐
   models.dev ─▶ │  sync（仅刷新目录）        │
                 └──────────────┬───────────┘
                                ▼
        ┌────────────────────────────────────────┐
        │ model_catalog + model_catalog_capabilities│  ← 纯参考目录，运行时不读
        └──────────────┬─────────────────────────┘
            浏览/搜索    │  ▲ 关联（model_catalog_links）+ 指纹对比 → 更新检测/提醒
            + 选模板     │  │
                       ▼  │
   「从目录采纳」：预填(model_id/元数据/能力 可改) → 编辑 → 原子创建 + 建立关联
                       ▼
        ┌────────────────────────────────────────┐
        │ models + model_capabilities（运营事实）  │  ← 运行时只读这里（不变）
        └────────────────────────────────────────┘
```

关键边界：**目录表是写入侧的「素材库」，运行时事实仍只在 `models`/`model_capabilities`**。
采纳是一次「快照拷贝」并建立**关联**；目录之后变化不自动回灌，但通过指纹对比**检测到差异 → 提醒 →（可选）一键刷新覆盖**。

## 6. 数据模型变更（草案，DDL 为示意）

### 6.1 新增 `model_catalog`（参考目录）
```sql
CREATE TABLE model_catalog (
    canonical_id TEXT PRIMARY KEY,                 -- models.dev lab/model，如 openai/gpt-4o
    lab TEXT NOT NULL,
    display_name TEXT NOT NULL,
    context_window_tokens BIGINT,
    max_output_tokens BIGINT,
    input_price_usd_per_million_tokens  NUMERIC(20,10),
    output_price_usd_per_million_tokens NUMERIC(20,10),
    release_date DATE,
    removed_upstream_at TIMESTAMPTZ,               -- 上游下架标记（不删本地目录行）
    fingerprint TEXT NOT NULL,                     -- 本条目内容指纹（元数据+能力提示规范化后 hash），用于追更对比
    synced_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX idx_model_catalog_lab ON model_catalog (lab);
-- 搜索本期用 display_name / canonical_id ILIKE；量大再考虑 pg_trgm。
```
> `fingerprint` 是**单条目**指纹（不同于现有 `feed.go` 的整 feed 指纹），用于精确判断「我采纳的快照 vs 目录最新」是否有差异。

### 6.2 新增 `model_catalog_capabilities`（目录能力提示）
```sql
CREATE TABLE model_catalog_capabilities (
    canonical_id TEXT NOT NULL REFERENCES model_catalog(canonical_id) ON DELETE CASCADE,
    capability_key TEXT NOT NULL CHECK (capability_key <> ''),
    support_level TEXT NOT NULL CHECK (support_level IN ('full','limited','unsupported')),
    limits JSONB,
    PRIMARY KEY (canonical_id, capability_key)
);
```
> 来源即现有 `feed.go::coarseCapabilities()` 的映射结果，落到目录而不再首入库写 `models`。无 `source` 字段。

### 6.3 新增 `model_catalog_links`（采纳关联 + 更新提醒状态）
```sql
CREATE TABLE model_catalog_links (
    model_id BIGINT PRIMARY KEY REFERENCES models(id) ON DELETE CASCADE,   -- 一个模型至多关联一条目录
    canonical_id TEXT NOT NULL REFERENCES model_catalog(canonical_id),     -- 采纳来源；非唯一 → 一条目录可派生多个模型（Q2）
    adopted_fingerprint TEXT NOT NULL,            -- 采纳/上次刷新时目录条目的 fingerprint（快照基线）
    reminder_muted BOOLEAN NOT NULL DEFAULT false,-- 永久忽略更新
    reminder_snooze_until TIMESTAMPTZ,            -- 稍后提醒：此时间之前不提醒
    dismissed_fingerprint TEXT,                   -- 忽略本次更新：忽略的是哪个 fingerprint（目录再变会重新提醒）
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX idx_model_catalog_links_canonical ON model_catalog_links (canonical_id);
```

**更新检测与提醒判定（纯查询推导）：**
- `update_available`（有差异）= 关联存在 且（`model_catalog.fingerprint <> link.adopted_fingerprint` 或 `model_catalog.removed_upstream_at IS NOT NULL`）。
- `should_remind`（要弹提醒）= `update_available` 且 `NOT reminder_muted`
  且 `dismissed_fingerprint IS DISTINCT FROM model_catalog.fingerprint`
  且 `(reminder_snooze_until IS NULL OR now() >= reminder_snooze_until)`。
- 三个「忽略」动作：
  - **忽略本次更新** → `dismissed_fingerprint = 当前目录 fingerprint`（目录再变到新指纹 → 重新提醒）。
  - **永久忽略更新** → `reminder_muted = true`（可取消静音 unmute）。
  - **稍后提醒** → `reminder_snooze_until = 选定时间`。
- **从目录刷新本模型**（采纳更新）→ 用目录最新值覆盖模型元数据 + 能力，`adopted_fingerprint = 当前目录 fingerprint`，并清空 `dismissed_fingerprint` / `reminder_snooze_until`。`model_id` 不变。

### 6.4 `models` 表整改
- **删除 `canonical_id` 列**（及其唯一索引）：采纳来源指纹迁到 `model_catalog_links`（非唯一，支持一模板多模型）。
- 删除 `removed_upstream_at`（属目录概念）。
- **删除 `lab` 列（Q8）**：运营侧与 `owned_by` 重复，统一用 `owned_by`（OpenAI `/v1/models` 契约字段）。`lab` 概念只保留在目录表 `model_catalog`（按厂商分组/筛选）；采纳时 `owned_by` 默认取目录 `lab`。
- `source` CHECK 收敛为 `('manual','catalog')`：`manual`=空白手建；`catalog`=从 models.dev 模板采纳（采纳后仍完全可编辑）。退役 `seed_models_dev`/`import`。
- 保留 `context_window_tokens` / `max_output_tokens` / `input/output_price` / `release_date`：作为采纳时的**快照**，可编辑、可被「刷新」覆盖；**空白手建也可选填（Q5）**，统一资料维度（纯展示、不参与计费）。
- `model_id` 仍**创建后不可变**（对外稳定标识）；采纳时是「新建」，此刻可自由填写。
  **校验保持不变（不允许 `/`）**——因为 Q1 默认值取「去前缀」的模型名（见 §15），不会带斜杠。

### 6.5 `model_capabilities`
- **删除 `source` 列**（Q4）：新设计下同步不再写运行时能力表，`source` 已无意义。连带退掉 `UpsertModelCapability` 的 source 入参与前端来源徽章。
- 其余结构不变。采纳带入的能力写这里；采纳界面可增删改。

### 6.6 退役的查询 / 逻辑
- `sql/queries/models.sql`：移除 `UpsertSeedModelByCanonicalID`、`MarkSeedModelRemovedUpstream`、`ListCanonicalModels`（迁到目录表的等价查询）。
- `modelcatalog/merge.go`：`PlanSync` 的 `manual` 守护 / conflict / 共享表 removal 大幅简化（目录是专表，直接全量 upsert + 标记下架 + 算单条指纹）。
- `DeleteModelCascade`：`model_catalog_links` 由 `ON DELETE CASCADE` 自动清理，无需显式删。

## 7. 同步逻辑改造（`internal/core/modelcatalog`）

- `feed.go`（拉取+解析 models.json/api.json + `coarseCapabilities`）：**复用**，几乎不动；新增按条目算 `fingerprint`（元数据 + 排序后能力提示规范化 hash）。
- `merge.go`：简化为目录维度的 upsert/removal（不再有 manual 冲突概念）。
- `store.go`：`SyncStore` 改写为写 `model_catalog` + `model_catalog_capabilities` + 单条 `fingerprint`（按 `canonical_id` 全量 upsert；feed 不含的标 `removed_upstream_at`）。
- `syncer.go` + `model_capability_sync_jobs`：审计保留；`Result`/dry-run 摘要字段对齐目录口径（feed/upsert/removed/cap-hints）。
- worker（`ModelCatalogSyncWorker`）+ `MODEL_CATALOG_SYNC_*` 配置：保留，目标改为刷新目录。

## 8. 接口契约（admin API，`/admin/v1`）

### 目录浏览
- `GET /admin/v1/model-catalog?q=&lab=&page=&page_size=` — 分页/搜索目录条目（返回 `canonical_id`、`lab`、`display_name`、`context`、`prices`、`release_date`、`removed_upstream_at`、`capability_count`、`adopted_count` 已被采纳次数）。
- `GET /admin/v1/model-catalog/{canonicalID}` — 单条详情 + 能力提示数组（供预填）。

### 采纳创建（原子）
- `POST /admin/v1/models/from-catalog` — body（采纳界面可编辑后的最终值）：
  ```jsonc
  {
    "canonical_id": "openai/gpt-4o",        // 采纳来源（写入 model_catalog_links）
    "model_id": "gpt-4o",                    // 默认=去前缀的模型名，可改；客户调用名
    "display_name": "GPT-4o",
    "owned_by": "openai",                    // 默认取目录 lab，可改（Q8：models 不再有 lab）
    "status": "disabled",
    "context_window_tokens": 128000,         // 价格/上下文均为快照展示，不参与计费
    "max_output_tokens": 16384,
    "input_price_usd_per_million_tokens": "...",   // 仅基线快照；不据此建/预填客户售价（Q7）
    "output_price_usd_per_million_tokens": "...",
    "release_date": "2024-05-13",
    "capabilities": [                         // 模板带来、可增删改（无 source）
      {"capability_key":"text.input","support_level":"full"},
      {"capability_key":"tools.function","support_level":"full"}
    ]
  }
  ```
  服务端在**单事务**内：插入 `models`（`source=catalog`）+ 批量写 `model_capabilities` + 建 `model_catalog_links`
  （`adopted_fingerprint` = 该目录条目当前 `fingerprint`）。保证「能力一起带过来」+ 关联建立。
  **不创建 `prices`（客户售价）行**（Q7）：售价仍由运营在售价管理处单独设定，目录基线仅作参考展示。

### 追更（更新检测 / 提醒 / 刷新）
- 模型 DTO（`GET /admin/v1/models`、`/models/{id}`）增加 `catalog` 子对象：
  ```jsonc
  "catalog": {
    "canonical_id": "openai/gpt-4o",
    "update_available": true,        // 目录指纹 != 采纳指纹，或上游已下架
    "removed_upstream": false,
    "should_remind": true,           // 综合 mute/snooze/dismiss 后是否应提醒
    "reminder": { "muted": false, "snooze_until": null, "dismissed_fingerprint": null }
  }
  ```
  （非采纳模型该字段为 null。）
- `GET /admin/v1/models?has_update=true` — 仅列「应提醒」的模型（或返回总数，供前端红点）。
- `POST /admin/v1/models/{id}/catalog-refresh` — 用目录最新值覆盖该模型元数据 + 能力，更新 `adopted_fingerprint`，清 dismiss/snooze。`model_id` 不变。（可带 body 选择「仅元数据 / 仅能力 / 全部」，本期默认全部；前端先展示 diff。）
- `POST /admin/v1/models/{id}/catalog-reminder` — body `{ "action": "dismiss" | "mute" | "unmute" | "snooze", "snooze_until": "<ISO8601>" }`：
  - `dismiss` → `dismissed_fingerprint = 当前目录 fingerprint`。
  - `mute` / `unmute` → 置/清 `reminder_muted`。
  - `snooze` → 置 `reminder_snooze_until`。

### 保留
- `POST /admin/v1/models`（空白手建）+ `CreateModel`：**扩展支持可选元数据**（Q5）——`context_window_tokens` / `input/output_price` / `release_date`（`max_output_tokens` 已有）；不再接收 `lab`（Q8）。
- `POST /admin/v1/capability/sync-jobs`、`GET /admin/v1/capability/sync-jobs`（语义改为刷新目录）。
- 既有 `GET/PUT/DELETE /admin/v1/models/{id}/capabilities/{key}` 等能力管理不变（去 source）。

## 9. 前端改动（`unio-admin`）

- **独立「模型目录」页（Q6）**：浏览/搜索 models.dev 目录（搜索 + 分页 + 按 lab 过滤），每条显示是否已被采纳、能力提示概览，提供「采纳为模型」入口。
- **采纳流程**（目录页或 `ModelsPage` 进入 → `ModelFormDialog` 采纳态）：
  - 预填：`model_id` 默认=**去前缀的模型名**（`openai/gpt-4o` → `gpt-4o`）、可改；元数据可改；**能力清单展示且可增删改**（预览态，不立即落库）。
  - 提交走 `POST /admin/v1/models/from-catalog`。
  - 保留「空白新建」路径：表单**去掉 `lab` 输入**（Q8，统一用 `owned_by`），**新增可选元数据输入**（上下文长度 / 价格基线 / 发布日期，Q5）。
- **售价设置处**：展示采纳模型的**目录价格基线作「参考价」**（如「models.dev 参考价：input \$2.5 / output \$10」），但**不自动预填/创建售价**（Q7）。
- **更新提醒 UI（Q3）**：
  - `ModelsPage` / 模型目录页对 `should_remind=true` 的模型显示「有更新」徽章；侧栏/顶部可显红点总数（`?has_update=true`）。
  - 点开 → **差异面板**（当前快照 vs 目录最新：元数据 + 能力 diff）+ 操作：
    **从目录刷新（采纳更新）** / **忽略本次更新** / **永久忽略更新** / **稍后提醒（选时间）**。
- **能力中心「同步」Tab**：文案/结果卡片改为「刷新 models.dev 目录」口径（feed 条数 / upsert / 下架）。
- **新增 API 封装** `src/lib/api/modelCatalog.ts`：`listCatalog` / `getCatalogEntry` / `createModelFromCatalog` / `refreshFromCatalog` / `setCatalogReminder`。
- `ModelCapabilitiesDialog` / `capability.ts`：去掉 source 字段与来源徽章。
- `ModelsPage`：不再混入 seed 停用行。

## 10. 运行时（不变）

gateway `/v1/*`、`ListAvailableModelsForProject`、routing、capability gate 仍只读 `models` + `model_capabilities`；
`model_catalog` / `model_catalog_capabilities` / `model_catalog_links` 运行时**不读**。整改的爆炸半径限定在「同步 + admin 管理 + 前端」。

## 11. 数据迁移（开发期，可随意重建）

- 你已确认开发阶段 DB 可 down/up，**无需保数据迁移脚本**。
- 新增迁移：`model_catalog`、`model_catalog_capabilities`、`model_catalog_links`；`models` 删 `canonical_id`/`removed_upstream_at`/`lab`、`source` 收敛；`model_capabilities` 删 `source`。
- 推荐追加新 migration（`000027+`）保持历史线性；开发期直接 `down→up` 重置即可。现存 seed 行重置后自然消失。

## 12. 任务拆分（实现期再逐项打勾）

- [x] T1 迁移：建 `model_catalog`(+`fingerprint`) / `model_catalog_capabilities` / `model_catalog_links`（000027/000028）；`models` 删 `canonical_id`+`removed_upstream_at`+`lab`、`source` 收敛（000029）；`model_capabilities` 删 `source`（000030）。已验证 up/down 可逆。
- [x] T2 sqlc：目录 upsert/list/search/get/标记下架 + 单条指纹；链接表 CRUD + 提醒状态更新；`models` query 调整（采纳事务、刷新、`catalog` 子对象聚合、`has_update`）。
- [x] T3 `modelcatalog`：`feed.go` 加单条指纹；`store.go`/`merge.go`/`syncer.go` 改写为写目录；退役共享表合并逻辑。
- [x] T4 service：`internal/service/admin/modelcatalog` 目录查询服务；「从目录采纳」事务服务（建模型+能力+链接）；「刷新」服务（覆盖+更新基线）；提醒状态服务。
- [x] T5 adminapi：`GET /model-catalog(+/*)`、`POST /models/from-catalog`、`POST /models/{id}/catalog-refresh`、`POST /models/{id}/catalog-reminder`、模型 DTO 加 `catalog` 子对象、`?has_update`；bootstrap 装配。
- [x] T6 前端：模型目录页、采纳预填+能力编辑、更新徽章+差异面板+四个动作、`modelCatalog.ts`、能力中心文案、去 source 徽章；表单去 `lab` 输入（Q8）+ 手建可选元数据输入（Q5）。
- [x] T7 测试：同步写目录+指纹（DB 集成）、单条指纹纯函数、提醒判定查询、目录搜索分页；既有测试全部修复（53 包全绿）。采纳/刷新/提醒走 HTTP 端到端验证（见 STATUS 剩余项：补 service DB 集成测试）。
- [~] T8 文档：补 `STATUS.md`/`ACCEPTANCE.md`（已建）、更新 chapters `README.md` 索引（已改）；`PROJECT_STATUS.md`/`DECISIONS.md` 待收口时补。

## 13. 测试策略

- 单元：`merge` 目录 upsert/removal + 单条指纹纯函数；采纳 DTO 校验（`model_id` 模式、能力 key 合法、limits 仅 limited）；提醒判定（`should_remind` 在 mute/snooze/dismiss/指纹相等等组合下的真值矩阵）。
- DB 集成：同步落目录（upsert + 能力提示 + 下架 + 指纹 + sync_job 审计）；`from-catalog` 单事务建模型+能力+链接，失败整体回滚；`catalog-refresh` 覆盖元数据/能力且更新 `adopted_fingerprint`、清 dismiss/snooze。
- 回归：gateway/routing/gate 读 `models` 行为不变（目录三表不参与）。
- 前端：build + lint；采纳能力可编辑落库正确；提醒四动作行为正确。

## 14. 风险与取舍

1. **快照 + 追更**：采纳后是快照（运营数据不被悄悄改），但通过关联 + 指纹检测「有更新」并提醒，可一键刷新覆盖——兼顾安全与不脱节。提醒可忽略本次/永久/稍后。
2. **中等偏上重构**：动到迁移（3 新表 + 改 2 表）+ 同步包 + admin API（采纳/刷新/提醒）+ 前端（目录页 + 提醒面板），但运行时不动，可分段交付。
3. **指纹口径**：需稳定的单条目规范化指纹（字段顺序/空值/数值格式统一），否则会误报「有更新」。测试需覆盖。
4. **目录体量**：models.dev 数百条，选择器/目录页需搜索 + 分页；本期 ILIKE 足够。

## 15. 决策记录与待确认

> 已敲定（2026-06-15）：

- **Q1 `model_id` 默认 = 去前缀**：采纳时默认填**去掉 lab 前缀的模型名**（`openai/gpt-4o` → `gpt-4o`），可在采纳界面改。
  因此 `model_id` 校验**保持不变**（不允许 `/`），无需放开。
  注：去前缀后可能与已有模型重名（`model_id` 全局唯一），采纳时若冲突由用户改名消歧（如 `gpt-4o-azure`）。
- **Q2 一条目录可采纳成多个模型 = 允许**：`model_catalog_links.canonical_id` **非唯一**；同一条目录可派生多个 `models`（不同 `model_id`、不同渠道/定价）。
- **Q3 追更 = 本期做**：本地模型通过 `model_catalog_links` 关联目录；按**单条目指纹**检测「有更新」并提醒；
  提醒可：**忽略本次更新 / 永久忽略更新 / 稍后提醒（选时间）**；并提供**从目录刷新本模型**（覆盖元数据+能力、`model_id` 不变）。详见 §6.3 / §8 / §9。
- **Q4 能力去 `source`**：删除 `model_capabilities.source` 字段及相关入参/前端徽章；采纳界面可增删改能力。
- **Q6 独立「模型目录」浏览页**：单独页面浏览/搜索 models.dev 目录（不止创建流程弹窗）。
- **Q5 空白手建也支持填元数据 = 是**：`POST /admin/v1/models` + `CreateModel` 扩展支持可选的
  `context_window_tokens` / `input_price` / `output_price` / `release_date`（`max_output_tokens` 已支持）。
  全部可选、纯快照展示（不参与计费），让手建模型与采纳模型资料维度一致。
- **Q7 不预填客户售价 = 不做**：采纳/刷新**不自动创建 `prices`（客户售价）行**，也不按基线加价。
  仅在设置售价处把**目录价格基线当「参考价」展示**（基线≈上游成本，自动当售价会导致零毛利/误导）。
- **Q8 `models` 只留 `owned_by`、去掉 `lab`；目录表保留 `lab`**：`model_catalog.lab` 保留（目录按厂商分组/筛选）；
  `models` 删除 `lab` 列，运营侧统一用 `owned_by`（OpenAI `/v1/models` 契约字段）。采纳时 `owned_by` 默认取目录 `lab`。

## 16. 参考
- [DEC-015](../../production/DECISIONS.md#dec-015-能力架构三层模型与-modelsdev-定位)
- [phase-12 Capability Architecture PLAN](../phase-12-capability-architecture/PLAN.md)
- [phase-13 Admin PLAN](../phase-13-admin/PLAN.md)
- 代码锚点：`internal/core/modelcatalog/{feed,merge,store,syncer}.go`、`sql/queries/models.sql`、`internal/app/adminapi/{router,models}.go`、`unio-admin/src/pages/{ModelsPage,CapabilityPage}.tsx`、`unio-admin/src/components/models/ModelFormDialog.tsx`
