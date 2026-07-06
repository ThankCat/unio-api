# 实体归档生命周期（服务商 / 渠道 / 线路）落地方案，2026-07

> 本文只做**方案与落地清单**，不动代码。凡标 **【待确认】** 的地方请先拍板，我据此再实现。
>
> - 撰写基准：对照当前工作区代码与数据库约束逐项勘探，文件/表名来自真实代码。
> - 阅读约定：先看 §1（一句话）、§2（为什么不硬删）、§4（连锁语义）、§5（护栏），决策项集中在 §9。

---

## 1. 一句话

**给服务商 / 渠道 / 线路增加第三种状态 `archived`（归档）：归档 = 只改状态、不删任何数据、完全可逆；归档实体从「路由候选」与「默认列表」中隐藏，但历史账单/请求记录仍可完整回溯。** 用它替代「想删删不掉」的困境——用过的实体永远保留可审计，但运营界面保持干净。

---

## 2. 为什么不做硬删除（背景与约束）

计费型网关的铁律：**承载过资金/请求的实体不可物理删除**，否则历史账单指向不存在的对象 → 对账断链、审计失败、纠纷无法复现。

现状（已勘探确认）：

- 三张配置表 `providers` / `channels` / `routes` 的 `status` 目前只有 `enabled` / `disabled`（CHECK 约束）。
- 硬删已实现且**故意在有历史时拒绝**（降级为 conflict「disable instead」）：
  - `provider.Delete` → `DeleteProvider`（被 channels / 账务引用则 23503）
  - `channel.Delete` → `DeleteChannelCascade`（级联清 `channel_models`/`channel_prices`，但被 `route_channels`/`request_attempts`/`request_records`/`cost_snapshots`/`settlement_recovery_jobs` 引用则 23503）
  - `channelmodel.Delete` / `model.DeleteModelCascade`（本轮已修配置行级联）
- 路由候选查询 `FindRouteCandidates`（`sql/queries/channel_models.sql`）已要求 `p.status='enabled' AND c.status='enabled' AND cm.status='enabled'` + 命中线路池 + `c.credential_valid`。

**关键结论**：归档不需要动路由逻辑——路由已经只认 `enabled`，`archived` 天然被排除。

---

## 3. 数据模型：状态轴设计

### 3.1 共用 `status` 字段，扩为三态枚举

`status ∈ { enabled, disabled, archived }`（**不**新开 `archived_at` 布尔轴）。

理由：
- 归档是**递进的终态生命周期**：`enabled`（在跑）→ `disabled`（临时停用、仍显示）→ `archived`（长期收起、默认隐藏），三者互斥。
- 路由/统计已按 `status='enabled'` 过滤，`archived` 自动被排除，改动面最小。
- 单字段单 CHECK，避免「归档又启用」这种矛盾组合。

与既有「凭据有效性轴」的关系（见 `PLAN-channel-credential-gate-2026-07.md`）：`credential_valid` 是**正交**的系统判定轴，与 `status` 无关；归档不改 `credential_valid`。

### 3.2 迁移（下一个编号 `000066`）——含 `archived_at`（已定 D-1）

三张表各加 `archived_at timestamptz`（可空），并扩 status CHECK；同时加**一致性不变量**：`archived_at` 有值 ⟺ status='archived'。

```sql
-- 000066_add_archived_status.up.sql（示意，providers；channels/routes 同理）
ALTER TABLE providers ADD COLUMN archived_at timestamptz;
ALTER TABLE providers DROP CONSTRAINT providers_status_check;
ALTER TABLE providers ADD  CONSTRAINT providers_status_check
    CHECK (status = ANY (ARRAY['enabled','disabled','archived']));
-- 不变量：归档态必有归档时间，非归档态必无——让归档/恢复逻辑机械可验、杜绝脏状态。
ALTER TABLE providers ADD CONSTRAINT ck_providers_archived_at
    CHECK ((status = 'archived') = (archived_at IS NOT NULL));
```

- **写入口径**：归档时 `status='archived', archived_at=now()`；恢复时 `status='disabled', archived_at=NULL`（见 §4A 恢复逻辑）。有了上面的 CHECK，任何漏清 `archived_at` 的写入都会被 DB 直接拒绝。
- **down 迁移**：先把所有 `archived` 行改回 `disabled` 且 `archived_at=NULL`，再删 CHECK 与列（否则残留 archived 违反旧约束）。
- `archived_at` 用途：列表按归档时间排序、审计展示、以及 §11 P2 保留期清理的时间基准。

---

## 4. 连锁语义（最关键）

区分两类关系：**「从属」往下级联，「关联」只解绑不级联，「依赖」拦截。**

```
服务商 ──拥有──▶ 渠道          （从属：归档往下级联）
渠道  ──关联──▶ 线路候选池      （关联：仅从池移除，不动线路本身）
线路  ◀──绑定── api_key        （依赖：归档前必须无绑定 key，否则拦截）
```

已勘探确认：`route_channels` **不被任何账务/历史表外键引用** → 其行是纯配置，删除安全（无 23503 风险）；`routes` 被 `api_keys.route_id` 依赖。

### 4.1 归档服务商

单事务内：
1. 该 provider 名下**所有 `status<>'archived'` 的渠道**一并置为 `archived`（从属级联）。
2. 这些渠道**从所有线路候选池移除**（`DELETE FROM route_channels WHERE channel_id IN (...)`）。
3. provider 置 `archived`。
4. **不动**线路本身、**不动** api_key。
5. **预警返回**：因此候选池变空的线路列表 + 绑定这些空线路的 api_key 列表（不阻断，仅告知）。

### 4.2 归档渠道

单事务内：
1. 渠道置 `archived`。
2. 从所有线路候选池移除（删它的 `route_channels` 行）。
3. **不动**服务商、**不动**线路本身（线路可能仍挂着别家渠道）。
4. **预警返回**：因此变空的线路。

### 4.3 归档线路（硬护栏：必须无绑定 Key；配 API Key 迁移，见 §4B）

**规则（已定 D-2）**：归档线路要求该线路下**没有**绑定的 api_key。

- **线路下有 Key → 拦截**（返回 conflict + 受影响 key 列表），并在响应里给出「可迁移」信号，前端据此直接提供**内联迁移入口**（见 §4B 入口②）。
- **线路下无 Key** → 置 `archived, archived_at=now()`；`route_channels` **保留**（线路已隐藏，留着便于恢复）；不动渠道、不动服务商。

两条使用路径（都合法）：
1. **先迁移，后归档**：管理员先用独立的「API Key 迁移」把 Key 换绑到别的线路（§4B 入口①），此时线路已无 Key，再点归档即直接成功——**不触发拦截**。
2. **归档时迁移**：直接点归档 → 被拦截 → 弹窗内选目标线路 → 「迁移并归档」在**单事务内**先把该线路全部 Key 迁走、再归档线路。

### 4.5 归档连锁总表

| 归档对象 | 自身 | 下级渠道 | route_channels | 线路本身 | api_key |
|---|---|---|---|---|---|
| 服务商 | →archived | **一并 archived** | 移除相关渠道行 | 不动（预警空池） | 不动（预警） |
| 渠道 | →archived | — | 移除自己行 | 不动（预警空池） | — |
| 线路 | →archived | — | 保留 | — | **有绑定则拦截 → 迁移** |

---

## 4A. 恢复逻辑（un-archive，完整定义）

**通用口径**：恢复 = `archived → disabled` 且 `archived_at → NULL`（受 §3.2 CHECK 强约束，二者必须同一条 UPDATE 内完成）。**永不直接恢复到 `enabled`**——恢复出来的实体默认停着，管理员再手动启用，杜绝「一恢复就接流量/计费」。**向下级联归档、但不向上级联恢复**（安全非对称）。

**名字（已定，极简）**：恢复**不改名**，沿用归档时的名字（渠道/线路即带 `__archived_<id>` 后缀的名字）。想要回干净名字由管理员恢复后手动改（正常唯一校验）。故恢复逻辑**无任何名字冲突分支**。

### 4A.1 恢复服务商
- provider：`archived → disabled`，`archived_at=NULL`。
- 其下渠道：**保持 archived**（不自动恢复），需逐个恢复。
- 无阻断护栏（归档的服务商永远可恢复）。恢复后 provider 处于 disabled，仍不参与路由，直到手动启用。

### 4A.2 恢复渠道
- **硬护栏**：若其所属 **provider 仍是 archived → 拦截**（"请先恢复所属服务商"）。理由：不允许在归档的父级下存在「半活」子级，避免悬挂/困惑状态；且 provider 归档时路由本就不通。
- channel：`archived → disabled`，`archived_at=NULL`。
- `route_channels`：归档时已删（D-3）→ **不自动恢复**。恢复后渠道**不在任何线路池里**，需手动重新加入线路并启用才参与路由（UI 明确提示）。
- `credential_valid`：**不动**（正交轴）。原为失效则仍失效，需一次检测通过才翻回。

### 4A.3 恢复线路
- route：`archived → disabled`，`archived_at=NULL`。
- `route_channels`：归档时保留 → 恢复后池子还在。但其中个别渠道可能此后被单独归档（其 route_channels 行已随之删除），故池 = 当时残留的成员；路由层本就过滤 archived 渠道，无副作用。
- api_key：归档前该线路已无 Key（硬护栏保证）→ 恢复后仍无 Key，需管理员手动绑定或用迁移功能迁入。
- 无阻断护栏（归档的线路永远可恢复）。

### 4A.4 恢复态一致性小结
```
enabled ──archive──▶ archived ──restore──▶ disabled ──(手动)──▶ enabled
disabled ─archive──▶ archived ──restore──▶ disabled
```
- 恢复渠道需父服务商非 archived；其余无阻断。
- 恢复一律落 disabled；`archived_at` 必随状态清空（DB CHECK 兜底）。

---

## 4B. API Key 线路迁移（换绑）功能

**目的**：把 api_key 的绑定线路从 A 换到 B（`api_keys.route_id` A→B）。既是独立运营能力，也是「线路归档」的前置手段。

**不影响历史**：`api_keys.route_id` 是**当前绑定**；历史请求在 `request_records.route_id` 已快照当时线路，改绑不회改写历史账单。

**校验**：目标线路必须存在且 `status='enabled'`（不能把 Key 迁到 disabled/archived 线路，否则 Key 立即不可用）；源与目标不能相同。

**两个入口**：
1. **入口①（独立迁移）**：线路详情 / API Key 管理页提供「迁移 Key 到其他线路」——支持**单个 Key 迁移**与**整条线路一键迁移**（该线路全部 Key → 目标线路）。与归档解耦，随时可用。
2. **入口②（归档时内联迁移）**：归档线路被拦截后，弹窗内选目标线路 → 「迁移并归档」在**单事务内**：先把源线路全部 Key 迁到目标线路、再归档源线路。

**接口（示意）**：
- `POST /admin/v1/api-keys/{id}/migrate-route`　body `{ "target_route_id": N }`（单个）
- `POST /admin/v1/routes/{id}/migrate-keys`　body `{ "target_route_id": N }`（整条线路批量；入口①的批量版）
- `POST /admin/v1/routes/{id}/archive`　body `{ "migrate_keys_to": N | null }`
  - `migrate_keys_to=null` 且线路有 Key → 409 拦截（返回受影响 key 列表 + `can_migrate=true`）。
  - `migrate_keys_to=N` → 事务内迁移全部 Key 到 N 再归档（入口②）。
  - 线路本就无 Key → 直接归档。

**幂等/并发**：迁移与归档同事务，避免「迁一半又来新请求绑定源线路」的竞态；批量迁移按 `route_id=源` 全量 UPDATE。

---

## 5. 护栏清单（归档 vs 硬删的差异）

- 归档**不受外键限制**：不删行，账务/历史外键全程完好 → **任何实体任何时候都能归档**（哪怕跑过一万次请求）。这正是它优于硬删之处。
- 唯一硬护栏：**归档线路前必须无绑定 api_key**（否则 key 无线路可用，请求全挂）。
- 软护栏（不阻断，仅预警）：归档导致某线路候选池为空 / 某 key 的线路为空 → 返回警告文案，让管理员知情。
- 归档是**幂等**：对已 `archived` 的实体再归档为 no-op（返回成功，受影响 0）。

---

## 6. 后端改动清单

### 6.1 SQL 查询（sqlc）

- **新增归档/恢复语句**（各表）：`ArchiveProviderCascade`（CTE：子渠道置 archived + 删 route_channels + provider 置 archived，`:execrows`）、`ArchiveChannel`（渠道置 archived + 删自身 route_channels）、`ArchiveRoute`（置 archived）、以及对应 `RestoreX`（→ disabled）。
- **预警查询**：`ListRoutesEmptiedByChannels`（给定 channel 集合，返回将变空的线路）、`CountApiKeysByRoute`（线路绑定 key 数）。
- **列表默认过滤**：以下 `:many` 列表默认排除 `archived`，并接受可选 `include_archived` 参数（默认 false）：
  - `providers.sql` → `ListProviders` / `ListProvidersPage`；`providers_ops.sql` → `ProvidersOpsTable` / `ProvidersOpsTableCount`
  - `channels.sql` → `ListChannelsByProvider` / `ListChannelsPage`；`channels_ops.sql` → `ChannelsOpsTable` / `ChannelsOpsTableCount`（及 ops 统计聚合是否计入 archived，见 §8）
  - `routes.sql` → `ListRoutes`；`routes_ops.sql` → `RoutesOpsTable` / `RoutesOpsTableCount`
- **线路池编辑器候选**：列渠道给「加入线路」的查询排除 `archived`（若采纳 §4.4「删 route_channels」，此项天然满足；仍需保证下拉不列 archived）。
- **不改**：`FindRouteCandidates`（已 `status='enabled'`，archived 自动排除）。

### 6.2 Service 层

- `provider.Service`：`Archive(id)` / `Restore(id)`；沿用 `conflict/notFound` 错误族。
- `channel.Service`：`Archive(id)` / `Restore(id)`。
- `route.Service`：`Archive(id)`（含 api_key 绑定护栏）/ `Restore(id)`。
- `validateStatus` 扩展接受 `archived`（但**创建/普通更新入口不允许直接设 archived**——归档只能走专用接口，避免绕过级联/护栏）。
- 归档返回结构带**预警载荷**（空池线路、受影响 key）供前端提示。

### 6.3 Admin API（`internal/app/adminapi`）

- 归档/恢复端点：
  - `POST /admin/v1/providers/{id}/archive` · `.../restore`
  - `POST /admin/v1/channels/{id}/archive` · `.../restore`
  - `POST /admin/v1/routes/{id}/archive`（body 含 `migrate_keys_to`，见 §4B）· `.../restore`
- Key 迁移端点：`POST /admin/v1/api-keys/{id}/migrate-route`、`POST /admin/v1/routes/{id}/migrate-keys`（§4B）。
- **列表筛选（已定，改默认口径）**：列表端点接受 `status` 过滤参数，**默认只返回 `enabled`**；要看 `disabled` / `archived` / 全部，须显式传参。取代当前「默认不筛选（返回全部）」的行为。
- 硬删端点（**已定 D-4，保留 + 收紧**）：
  - 保留现有 `DELETE`（无历史的干净实体真删）。
  - **收紧为「先归档才能删」**：删除仅允许对 `status='archived'` 的实体发起；对 `enabled/disabled` 发起删除返回 400/409（"请先归档"）。
  - 归档实体删除时若仍被历史引用 → 沿用现有 23503→conflict（"有历史记录，不可删除，请保持归档"）。
- DTO 增 `status`（含 archived）、`archived_at`；归档响应含 `warnings`（空池线路 / 受影响 key）。

---

## 7. 前端改动清单（`unio-admin`）

- 三个列表页（服务商 / 渠道 / 线路）状态筛选（**已定，改默认**）：
  - 现状：筛选只有一个状态选项，默认不筛选（显示全部）。
  - 改为：**默认只显示「开启」（enabled）**；`disabled` / `archived` 需**手动切换筛选**才显示（筛选项：开启 / 停用 / 已归档 / 全部）。
  - 状态徽标增「已归档」样式。
- 行操作按钮（**已定 D-4**，按状态分流，删除只在归档态出现）：
  - `enabled` / `disabled`：显示「归档」；**不显示「删除」按钮**（避免误删）。
  - `archived`：显示「恢复」+「删除」（删除仅在无历史时可点；有历史则禁用并 tooltip「有历史记录，不可删除」）。
- 归档确认弹窗：展示**预警**（将清空的线路、受影响 key）。
- **线路归档弹窗（§4B 入口②）**：若线路有绑定 Key → 不直接归档，改为展示「此线路有 N 个 Key，需先迁移」+ 目标线路选择器 +「迁移并归档」按钮（调 `archive` 带 `migrate_keys_to`）。
- **API Key 迁移入口①**：线路详情 / Key 管理页提供「迁移到其他线路」（单个 + 整条线路一键迁移）。
- 线路详情「候选渠道」编辑器：候选下拉过滤 archived 渠道。
- `src/lib/api/*.ts`：加 archive/restore/migrate 调用 + `status` 筛选参数 + `archived_at` / `warnings` 类型。

---

## 8. 统计口径（需拍板的边界）

- **运营看板 / ops 聚合**（`ChannelsOps*` / `ProvidersOps*` / `RoutesOps*` / dashboard）：归档实体的**历史请求**是否计入区间统计？
  - 倾向：**历史照常计入**（钱确实花过），但**实体列表**默认不显示 archived 行。即「统计看事实、列表看在用」。
- **健康分桶 / 检测 worker**：worker 检测应**跳过 archived 渠道**（不再对已归档渠道发探测、不消耗上游额度）。`channel_test_worker` 的选取条件从 `status='enabled'` 保持不变即可（archived 天然不被选）。

---

## 9. 决策项（均已拍板 2026-07）

- **D-1**（§3.2）：加 `archived_at` 时间列。✅ **已定：加**，并配 `(status='archived')=(archived_at IS NOT NULL)` 一致性 CHECK；恢复时随状态清空（见 §4A）。
- **D-2**（§4.3/§4B）：归档线路遇绑定 key。✅ **已定：拦截 + 提供 API Key 迁移**。两个迁移入口（独立迁移 / 归档时内联「迁移并归档」）；先迁移后归档则不触发拦截。
- **D-3**（§4A.2）：归档渠道时 route_channels。✅ **已定：删除**（池永远干净；恢复后手动重加）。
- **D-4**（§6.3/§7）：硬删。✅ **已定：保留，且收紧为「先归档才能删」**——未归档实体不显示删除按钮；归档实体支持硬删（仅无历史时）。
- **D-5**（§8）：ops/看板统计计入归档实体历史。✅ **已定：计入**（钱确实花过；列表隐藏但统计看事实）。
- **D-6**：范围。✅ **已定：服务商/渠道/线路三类一起做**。
- **D-7**（§11 P2）：保留期清理任务。ℹ️ **本期不做，列为后续**。说明见下。

### D-7 是什么（补充解释）

「保留期清理（retention purge）」= 给历史数据设一个保存期限（比如账单/请求记录保留 12~24 个月）。后台定时任务把**超过期限**的老历史（`request_records` / `cost_snapshots` / `ledger_*` 等）**导出备份后删除**。一旦某个归档实体关联的历史全部被清走，它就从「有历史、删不掉」变回「无历史、可硬删」，于是可被彻底清除或随历史一并清理。

作用：解决「运营三五年后，归档实体和历史数据无限累积」的**终极存储/合规问题**。因为它牵涉数据保留策略、合规、导出备份，属于重武器，**本期先不做**——本期的「归档 + 默认隐藏」已足够让界面保持干净。等规模真正上量（或有合规/存储压力）时再单独立项。

---

## 10. 验证计划

- 迁移 up/down 往返；`sqlc generate` 通过。
- 单测：归档级联（provider→channels→route_channels）、线路 key 护栏拦截、恢复落 disabled、幂等、列表默认过滤、`FindRouteCandidates` 对 archived 零命中。
- 集成：归档后该渠道/服务商不再被路由（真实候选查询为空）；恢复后需手动启用+重加池才恢复路由。
- 前端：`tsc` + `vite build`；列表默认隐藏归档、开关可见、归档/恢复弹窗预警正确。
- 后端：`gofmt`/`go vet`/`go build`/`go test ./...` 全绿。

---

## 11. 分阶段（建议）

1. **P0（本期主体）**：schema 三态 + `archived_at` + 一致性 CHECK；列表默认只显示 enabled + 手动筛选 disabled/archived；三类实体归档/恢复接口与级联/护栏（§4/§4A）；API Key 迁移功能与两个入口（§4B）；硬删收紧为「先归档」（§6.3）；前端筛选默认、行操作分流、迁移弹窗（§7）。
2. **P1（名字复用，已定：纳入 P0 一起做）**：归档时**释放名字**，规则按各表真实唯一约束分别处理（已核实）：
   - **渠道**（唯一 `(provider_id, name)`）：归档时 `name → 原名__archived_<id>`，释放该 provider 下的原名供新建复用。
   - **线路**（唯一 `name` 全局）：归档时 `name → 原名__archived_<id>`，释放全局线路名。
   - **服务商**（唯一 `slug` 标识）：**slug 归档时不变**（已定）——服务商被归档大概率是它本身出了问题，不会再用同 slug 新建，无需释放；provider 的 `name` 非唯一，也无需改。
   - **恢复口径（已定，极简）**：**恢复后沿用「归档时的名字」**（即带 `__archived_<id>` 后缀的名字），**不做**「改回原名 / 名字冲突处理」。因为渠道/线路/服务商都支持改名——若想要回干净名字，管理员恢复后手动改名即可（受正常唯一校验约束）。这样恢复逻辑无需任何名字冲突分支。
3. **P2（后续，非本期）**：保留期清理任务（D-7）。
