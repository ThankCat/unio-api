# Origin 并入 Provider 落地方案

> **状态：** 决断 **D1–D11 已齐** — 同目录 `DECISIONS.md` 为摘要，**本文件 §4 为实施权威全文**  
> **范围：** `unio-gateway` + `unio-admin` + `unio-blueprint`（Gateway/Admin 术语与 ADR）  
> **前提：** 开发环境可清库 / 清 Redis；不要求兼容线上历史熔断键与旧 Origin 行  
> **产品约束（已决）：** 一个 Provider **恰好一个** `origin`（上游根地址）；官方与中转拆成两个 Provider，不追求同 Provider 多 root  
> **实施节奏（D8）：** 一次做完；无「先 1:1 再物理合并」过渡  
> **代码现状已核对：** §3 / §5 / §6 / §7 的清单来自实际代码审计（含文件行号），不是推测

---

## 0. 已冻结决断速查（实施前必读）

| ID | 一句话 | 实现时勿漏 |
| --- | --- | --- |
| 生命周期 | 继续 **归档**；不做硬删+账本大改 | UI/API 仍走 archive；删 Cascade |
| **D1** | 归档腾坑改 `origin` 后缀；分层清 Redis | `UNIQUE(origin)`；在途资源留待收口 / TTL |
| **D1b** | Channel 在池中 / Provider 下有未归档 Channel 或非终态 operation → **409** | 不静默拆线 / abort；删两侧 Cascade/WithReplacement |
| **D2** | 删除 Duplicate / CopyChannelsToOrigin | Gateway + Admin + 合同测试全清 |
| **D3** | `Channel=enabled` ⇒ `Provider=enabled` | disabled Provider 下可配置 Channel，但只能保存为 disabled |
| **D3b** | 停用 Provider 前须先停用全部 enabled Channel | 有 enabled Channel → **409**；不自动级联 |
| **D4** | 清库直建 `provider_routing_operations` | **不**并入 `runtime_control_operations`；**readiness 查询同步改** |
| **D5** | 有 enabled Channel 时改 `origin` 须显式确认 | `confirm_enabled_channels=true` + `expected_origin_revision`；不用 token |
| **D6** | 字段/API 用 **`origin`**，不用 `base_url` | `origin`=地址字符串，不是实体 |
| **D7** | 删 `/admin/v1/provider-origins*`；能力进 `/providers` | **不**留代理；provider reset breaker 是**新建**端点 |
| **D8** | 一次切换（schema+Redis+API+UI+文档） | 开发期清库清 Redis |
| **D9** | Provider 上保留**两条** revision：`origin_revision` + `status_revision` | Lua 双轨结构原样保留，只改名/改作用域 |
| **D10** | 丢弃 `provider_origins.name` | Provider.name 为唯一展示名；DTO/UI 去 `provider_origin_name` |
| **D11** | **删除** combined routing 组合端点 | 只留「改 origin」「改 status」两条；连 combined Lua 一起删 |

---

## 1. 目标

把三层模型：

```text
Provider → Provider Origin（base_url + 公共熔断/围栏）→ Channel
```

收成两层：

```text
Provider（`origin` + 服务商级公共熔断/围栏）→ Channel
```

- **源站实体（`provider_origins`）退役**；上游根地址落在 Provider 字段 **`origin`**（D6）
- **源站级熔断** 改名为并实现为 **服务商（Provider）级熔断**（语义仍是「一个 API Root = 一个公共故障域」，只是 Root 唯一挂在 Provider 上）
- Channel 只挂 `provider_id`，不再挂 `provider_origin_id`
- Admin 不再有独立 Origin 资源（D7）
- **Origin 的两条 revision 上移到 Provider，仍是两条**（D9）

**非目标（本变更不做）：**

- 不削弱 Channel 熔断 / 429 cooldown / 凭据闸门 / AttemptPermit
- 不做「Provider 下多 origin」兼容路径
- 不重做生命周期：继续 **归档（status=archived）**，不上独立 soft-delete / 硬删+账本自包含大改  
- 不做「先强制 1:1 + UI 藏 Origin，再物理合并」（D8 已否决）
- **不**把两条 revision 合成一条（D9 已否决）
- 开发库仍可清库重建（本变更 schema 终态依赖可丢本地数据）

### 生命周期约定（已决 = D1 + D1b）

归档 = 数据保留 + 退出路由。顺序如下（**禁止**先糊 Provider、再顺带渠道）。

**不变量**
- `route_channels` **不得**含已归档 Channel；**可以**含 `disabled` Channel  
- Channel=`enabled` 时 Provider 必须=`enabled`；停用 Provider 前须先停用其下全部 enabled Channel  
- 不得出现「Provider=`archived` 且其下仍有非归档 Channel」

**Archive Channel（显式、不静默拆线）**
1. 若仍在**任意** `route_channels` 中 → **409**（提示先从线路移除该渠道）  
2. 池中已无引用后 → `archived` + 改名 `__archived_<id>`  
3. 归档提交后立即阻止新请求；已经取得 permit 或已经开始 transport 的请求继续完成响应、usage 与结算  
4. **清 Redis（必做但不得破坏在途收口）**：立即删除 Channel breaker、cooldown、admission control、permission 及 recheck queue 成员；permit、并发租约和 RPM/RPD/TPM 桶保留给 `Finish` / `Abort` 收口，异常残留由 TTL 回收  
5. 在途请求的 breaker / TTFT 结果因 Channel 已归档而 stale/no-op，但资源收口必须先完成  
6. **禁止**归档接口内自动 `DELETE route_channels`  

> **现状差异（必须改，代码已核对）**：今天 `ArchiveChannelCascade` 内含 `DELETE FROM route_channels WHERE channel_id = ...`（`sql/queries/admin/channel.sql:596-597`），且 409 护栏只由 `ListEnabledRoutesEmptiedByChannel`（`channel.sql:646-656`）在「该渠道是某启用线路**最后一个**成员」时触发。D1b 要求收紧为「**在任意 route_channels 里就 409**」，并删除自动拆线。见 §4 D1b。

**Archive Provider（同样显式、不级联）**
1. 若仍有未归档 Channel → **409**（提示先归档渠道）  
2. 若存在非终态 `provider_routing_operations`（`preparing` / `prepared` / `db_committed`）→ **409**；归档不自动 abort 或接管该操作  
3. 无未归档渠道且无非终态操作后 → Provider `archived` + `origin := origin || '__archived_' || id`（腾出 UNIQUE）  
4. **清 Redis（必做）**：删除该 Provider 的 fence/control、`breaker:v2:provider:{id}`、evidence 与已终态 routing-op Redis 记录；不删除任何在途 permit 或 Channel 资源  
5. Restore Provider **不**级联恢复渠道；只移除末尾与当前 ID 完全匹配的 `__archived_<id>`，裸 URL 已被占用则 **409**，不自动改写地址；恢复成功后 `InitProviderControl`  

**删除的危险路径（本变更必须做）**
- 删除 `ArchiveProviderCascade`、`ArchiveProviderWithReplacement`（`sql/queries/admin/provider.sql:75-146`）  
- 删除 `ArchiveChannelCascade` 的拆线 CTE、`ArchiveChannelWithReplacement`（`sql/queries/admin/channel.sql:592-644`）  
- 删除 Admin UI `ArchiveWithReplacementDialog`（Provider 与 Channel 两处入口）  
- Admin UI 改为分步：改线路 → 归档渠道 → 归档服务商

---

## 2. 目标领域模型

| 概念 | 职责 |
| --- | --- |
| **Provider** | 归属/记帐主体 + **唯一** `origin` + `origin_revision` + `status` + `status_revision` + **Provider breaker / fence** |
| **Channel** | 凭据、协议、adapter、模型映射、成本、Channel 限流与 Channel 熔断；必属一个 Provider |
| ~~Provider Origin~~ | **删除**（表 `provider_origins` 不存在；`name` 一并丢弃，D10） |

**有效状态（统一口径，替代双层合成）**

- 旧：`EffectiveOriginStatus(providerStatus, originStatus)`（`internal/core/runtimecontrol/origin_publisher.go:172-181`）
- 新：**只有一个 status 源 = `providers.status`**。Redis `effective_status` 直接等于 `providers.status`；不再有两层合成函数
- 路由 SQL 只留 `p.status = 'enabled'`（今天是 `p.status='enabled' AND pe.status='enabled'` 两个分开过滤，见 §6）
- **验收（对照 R1）**：PG 行 status、Redis `effective_status`、Admin runtime sync 比较必须都用这同一个来源

命名对照（代码/Redis/文档必须替换；D6 / D9 / D10）：

| 旧 | 新 |
| --- | --- |
| `provider_origins` 表 / Origin 实体 | **删除** |
| Origin.`base_url` | Provider.`origin` |
| Origin.`base_url_revision` | Provider.`origin_revision` |
| Origin.`status` / `status_revision` | Provider.`status` / **新增** Provider.`status_revision` |
| Origin.`name` | **删除**（D10） |
| `provider_origin_id`（Channel / attempt FK） | **删除**；事实表用 `provider_id` + revision / 快照 `origin` |
| `breaker:v2:origin:{id}` | `breaker:v2:provider:{id}` |
| `origin-evidence:*` | `provider-evidence:*` |
| `origin-routing:v1:op:{token}` | `provider-routing:v1:op:{token}` |
| `origin_routing_operations` | `provider_routing_operations`（D4） |
| Admin `/provider-origins*` | **删除**；能力并入 `/providers`（D7） |
| UI「源站」独立层 / `provider_origin_name` | 服务商详情内嵌编辑 `origin`；无源站名（D10） |
| 文案 Base URL | **Origin**（上游根地址） |

---

## 3. 先前排查风险 → 本方案如何处理

| ID | 原风险 | 合并后 | 本变更动作（已决） |
| --- | --- | --- | --- |
| R1 | Admin 用 Redis `effective_status` 比 Origin **行** `status`，假 `stale` | 双层消失 | **验收项**：统一到 `providers.status` 单一来源（§2） |
| R2 | UNIQUE 含 archived 占坑 | 归档改写 `origin` | **D1**：后缀 `__archived_<id>` + 清 Redis |
| R3 | CopyChannelsToOrigin / Duplicate 半成品 | 功能删除 | **D2** |
| R4 | 建渠道不拒 disabled/archived Origin | 校验 Provider | **D3**：archived Provider 禁止配置；disabled Provider 下只允许保存 disabled Channel；启用 Channel 必须先启用 Provider |
| R5 | Origin batch >256 卡住启停 | 扇出消失 | 自然消除；**同时删死设定** `origin_status_batch_max`（见 R11） |
| R6 | Provider disabled 时 combined routing 被拒 | combined 路径整条删除 | **D11 已决：删除**，脚枪随端点一并消失 |
| R7 | 热改 URL 不检查挂载 Channel | 护栏 | **D5**：enabled Channel 存在时要求显式确认，并核对 `expected_origin_revision` |
| R8 | 硬删后 Redis 孤儿 key | 今天**完全没有** purge helper（`provider.go:267-268` 注释承认惰性残留） | Archive/Delete 路径**新增**显式 DEL；开发期可整库 flush |
| R9 | Reset breaker 不校验实体存在 | 迁到 Provider Reset | Reset 前读 PG，不存在 **404**（今天只有 channel 侧有此护栏） |
| **R10** | **Gateway readiness 硬编码 `origin_routing_operations`** | 表改名后 readiness 直接坏 | `sql/queries/shared/app_settings.sql:72` 与 `:118` 必须同步改（喂 `runtime_operations_reconciled` / `runtime_maintenance_smoke_allowed`） |
| **R11** | **`circuit_breaker` 设置文档含 5 个 origin 键** | `origin_status_batch_max` 成死设定 | 见 §5「app_settings」；含 `gateway_settings.go:192-194` 范围校验与前端 `RuntimeSettingsPanel.tsx` |
| **R12** | **`DeleteProvider` 硬删 CTE 会清 origins + origin routing ops** | 本变更不做硬删项目，但这条既存查询必须改 | `sql/queries/admin/provider.sql:51-73` |

---

## 4. 已冻结决断（实施权威全文）

> 摘要见 `DECISIONS.md`。若摘要与本节冲突，**以本节为准**并回写摘要。

### D1 — 归档顺序 + `origin` 腾坑（**已决**）

**分步 API（全程 409 护栏，无静默级联）** — 含 **D1b**
1. **归档 Channel**：仍在**任意**线路池 → **409**；前端先调线路 API 移除 → 再归档 Channel → 清理非在途 Redis 状态；permit、并发租约和计数桶留待收口 / TTL  
2. **归档 Provider**：仍有未归档 Channel 或非终态 Provider routing operation → **409**；前端先处理完成 → 再归档 Provider（`origin` 后缀）→ **清 Provider Redis**  
3. 池中允许 `disabled`，**禁止** archived Channel 留在池中  
4. **删除**四条级联/替换实现与对应 UI：  
   - `ArchiveProviderCascade`、`ArchiveProviderWithReplacement`（`admin/provider.sql:75-146`）  
   - `ArchiveChannelCascade` 的拆线 CTE、`ArchiveChannelWithReplacement`（`admin/channel.sql:592-644`）  
5. `ListEnabledRoutesEmptiedByChannel`（只查「最后一个成员」）→ 改为「**存在任一 route_channels 引用即冲突**」的护栏查询  

**归档 Redis 清理清单（实现时对照 breakerstore keys）**

| 时机 | 必须删除（合并后命名） |
| --- | --- |
| Archive Channel · 立即 | `breaker:v2:channel:{id}`、channel cooldown、`admission:v1:channel:{id}` control、`permission:v1:channel-model:{ch}:*`，并从 permission recheck queue 移除对应成员 |
| Archive Channel · 延后 | **不得立即删除** permit、`breaker:v2:channel:{id}:conc`、`admission:v1:ch-{rpm,rpd,tpm}:{id}:*`；由 `Finish` / `Abort` 收口或 TTL 回收 |
| Archive Provider | 无非终态 operation 后删除 `breaker:v2:provider:{id}`、`provider-evidence:{id}:{category}:{channels,models}`（6 键）、provider fence/control 与已终态 `provider-routing:v1:op:*`；不删除在途 permit |

**其它**
- 全局仍 `UNIQUE(origin)`  
- **CHECK 无需豁免**：`origin || '__archived_' || id` 仍以 `https?://` 开头，现有 scheme CHECK 天然通过（原计划写的「归档行豁免 CHECK」是多余的）  
- Restore Provider 不级联渠道；只剥离当前 ID 的精确归档后缀；裸 URL 冲突则拒绝；恢复后重新 Init control  
- 验收：归档 409 护栏生效；立即清理键归档后不存在；在途资源可正常收口并最终消失；原 URL 可建新 Provider；无级联归档代码路径

### D2 — Channel Duplicate / 复制渠道（**已决：整条删除**）

- 1 Provider = 1 origin 后，不再需要「复制到另一源站 / Duplicate Channel」运营路径  
- **本变更删除：**  
  - Gateway：`POST /admin/v1/channels/{id}/duplicate`（`adminapi/channel/register.go:61`）+ `channel_duplicate.go` 全文（含 `copyChildren`）  
  - Admin UI：`DuplicateChannelDialog.tsx`、`CopyChannelsToOriginDialog.tsx` 及 `ChannelRowActions.tsx:180-184`、`ProviderOriginsSection.tsx:190` 入口；共享的 `defaultDuplicateChannelName` 一并删  
  - 相关测试  
- **不做**原子性修补（功能直接下线）  
- **注意（事实修正）**：仓库**没有** OpenAPI/Swagger spec，前端类型是手写的（`src/lib/api/*.ts`）。合同断言在 `unio-admin/tests/p4-api-contract.test.ts`；duplicate 目前**未被合同测试覆盖**，无需删断言，但删功能后要确认该文件不引用  
- 验收：代码与 UI 无 duplicate/copy-to-origin 路径

### D3 — Provider 状态对 Channel 配置 / 启用的约束（**已决：显式状态不变量**）

**不变量：`Channel.status = enabled` 蕴含 `Provider.status = enabled`。**

- Provider=`enabled`：允许创建、编辑和启用 Channel  
- Provider=`disabled`：允许创建和编辑 Channel，但目标状态只能是 `disabled`；禁止把 Channel 启用  
- Provider=`archived`：禁止创建、普通编辑或启用 Channel  
- 启用 Channel 时必须在服务端重新读取所属 Provider，非 `enabled` → **409**  
- **现状**：`internal/service/admin/channel/channel.go:456-466` 只验存在性与归属（`resolveOriginForProvider:905-916`），**完全不看 status**；前端只在 `ChannelFormDialog.tsx` 客户端过滤 archived。本变更把所有入口的校验落到服务端

### D3b — 停用 / 启用 Provider（**已决：不级联，先满足不变量**）

- 停用 Provider 前检查下属 Channel；存在任一 enabled Channel → **409**，提示先逐个停用  
- 停用 Provider **不**自动修改 Channel.status，不提供级联停用  
- 启用 Provider 不自动启用 Channel；运维再显式启用所需 Channel  
- Channel 停用时保留其 `route_channels` 成员关系；重新启用后可在仍启用的 Route 中恢复候选资格  
- Admin 直接展示 Provider / Channel 各自行状态，不再依赖“Channel 行 enabled、实际被 Provider 遮蔽”的有效状态补丁

### D4 — `origin_routing_operations` 表处置（**已决：清库直建终态表**）

- 删除 `provider_origins` / `origin_routing_operations` 旧形态  
- 清库后直接建成终态表 **`provider_routing_operations`**：挂 `provider_id`（去掉 `origin_id` 列与其 partial UNIQUE）；`kind` 取值对齐单目标 Provider 围栏，只留 `origin` 与 `status` 两种（删掉 `origin_create` / `provider_status_batch` / combined 的 `base_url_status`，后者见 D11）  
- **不**并入 `runtime_control_operations`（渠道限额 + 全局 app_settings 围栏继续单独那张表）  
- **必须同步改 readiness（R10）**：`sql/queries/shared/app_settings.sql:72`、`:118` 两处 `NOT EXISTS (... FROM origin_routing_operations ...)`  
- 不必保留「先建 origin 操作表再 rename」的迁移史

### D5 — 修改 Provider.`origin` 的护栏（**已决：revision + 显式确认字段**）

- 创建 Provider 时可提交 `origin`；创建后普通 Provider 编辑接口不得修改 `origin` 或 `status`  
- 地址只能走 `PATCH /providers/{id}/origin`，状态只能走 `POST /providers/{id}/status`，分别执行独立 revision 围栏  
- 修改地址必须提交 `expected_origin_revision`；服务端锁定并重新检查当前 revision，不匹配则 **409**  
- 若存在 enabled Channel，还必须提交 `confirm_enabled_channels=true`；未确认则 **409**，Admin 展示影响后由用户确认并重试  
- `confirm_enabled_channels` 是显式布尔确认字段，**不是 token**，不单独签发、存储或过期  
- 无 enabled Channel 时不要求确认字段，但仍要求 `expected_origin_revision`  
- 改 URL 仍走 Provider 围栏（只抬 `origin_revision`、写 `provider_routing_operations`、同步 Redis）  
- 安全停机换地址的标准顺序是：停用 Channel → 停用 Provider → 改 `origin` → 启用 Provider → 按需启用 Channel

### D6 — 命名：用 `origin` 指地址，不用 `base_url`（**已决**）

- **`provider_origins` 表删除**后，不再存在「Origin 实体」；`origin` 一词只表示 **Provider 上的上游根地址字符串**  
- Provider 字段：`origin text NOT NULL`、`origin_revision bigint`（**禁止**引入 `base_url` / `base_url_revision`）  
- UNIQUE / 归档腾坑：`UNIQUE(origin)`；归档时 `origin := origin || '__archived_' || id`  
- API/UI/文档：说 **Origin**（上游根地址），不说 Base URL  
- Redis/围栏/metrics/Candidate/settings 键中的 base_url 语义一律改为 origin  
- **明确：** 不是保留 Origin 表，只是地址字段叫 origin

### D7 — Admin API（**已决：直接删除独立资源**）

**删除**以下 9 条（代码已核对，`adminapi/providerorigin/providerorigins.go`）：

| HTTP | 路径 | 现 handler |
| --- | --- | --- |
| GET | `/provider-origins` | `list:173` |
| POST | `/provider-origins` | `create:220` |
| GET | `/provider-origins/{id}` | `get:206` |
| PATCH | `/provider-origins/{id}` | `update:239`（仅改 name → D10 后无意义） |
| POST | `/provider-origins/{id}/status` | `updateStatus:258` |
| POST | `/provider-origins/{id}/base-url` | `updateBaseURL:277` |
| POST | `/provider-origins/{id}/routing` | `updateRouting:296`（见 **D11**） |
| GET | `/provider-origins/{id}/ops/runtime` | `runtime:133` |
| DELETE | `/provider-origins/{id}/ops/circuit-breaker` | `resetBreaker:151` |

- 能力并入 `/providers`；Gateway 与 Admin 在各自仓库正常 commit / push，作为同一协调变更集完成并验证；**不**留 deprecated 代理层或兼容 shim  
- **注意（事实修正）**：`/providers/{id}/ops/circuit-breaker` 与 `/providers/{id}/ops/runtime` **今天不存在**，是**新建**端点，不是搬迁；新建时必须带 R9 的「先读 PG，不存在返回 404」护栏（今天 origin 侧没有，channel 侧 `breaker_ops.go:99-101` 有，照它写）  
- `GET /providers/ops` 内嵌的 `origins[]` 数组（`providers_ops.go:35,143-158`）压平为 provider 级 `origin` 字段  
- Provider DTO 的 `affected_origin_count`（`providers.go:37,42`）删除或改为渠道计数  
- 前端合同测试 `unio-admin/tests/p4-api-contract.test.ts:145-214` 断言全部 `/provider-origins*` 路径，需同批改写

### D8 — 实施节奏（**已决：一次做完**）

- 一次做完：schema + Redis key + API + UI + 文档  
- 开发期清库清 Redis  
- **不做**「先强制 1:1 + UI 藏 Origin，再物理合并」  
- `unio-gateway`、`unio-admin`、`unio-blueprint` 是三个独立仓库，不创建 PR；各自正常 commit / push  
- 交付顺序：Gateway 改造并验证 → Admin 改造并验证 → 全部代码行为确定后最后更新、校验并提交 Blueprint  
- Gateway 与 Admin 新旧契约不支持混用，部署时作为同一发布批次切换  
- **编译绿的现实约束**：P1 一动 schema 就会同时打断 routing SQL、service、lifecycle 与测试；Gateway 的 P1–P4 作为同一改造线推进，`go build ./...` 与 Go 测试在 P4 末尾要求；Admin 测试在 P5 末尾要求

### D9 — Provider 上保留几条 revision？（**已决：两条**）

**决定：`providers.origin_revision`（管地址）+ `providers.status_revision`（管启停），维持今天 Origin 的双轨语义。**

- 理由：双轨已深度固化在 Lua——`originfence_lua.go` 61 处、`lua.go` 16 处引用 `*_fence_generation` / `*_revision_state`；两条各有独立 fence generation、独立 pending 状态、分开的 stale disposition（`stale_revision` 对 `stale_status_revision`）。合成一条等于重写状态机  
- **`providers` 今天两条都没有**（`migrations/000007_providers.up.sql` 只有 id/slug/name/status/created_at/updated_at/archived_at），两条都是**新增列**  
- **原样保留、只改名/改作用域**：  
  - `base_url_fence_generation` → `origin_fence_generation`；`status_fence_generation` 保留语义  
  - `base_url_revision_state` → `origin_revision_state`；`status_revision_state` 保留  
  - disposition：`stale_revision`（地址）与 `stale_status_revision`（启停）**两个子原因都保留**  
  - Channel hash 上的 `provider_origin_id` / `base_url_revision` / `status_revision` 绑定字段 → `provider_id` / `origin_revision` / `status_revision`，rotate 触发条件不变  
  - combined（一次 +1 两条 revision）的脚本与类型**删除**，见 D11；两条 revision 各自单独的 prepare/commit/abort 保留  
- **Provider 启停仍只 bump `status_revision`**，不动 `origin_revision`（与今天 `provider_status_batch` 一致），但**不再 batch 扇出**（单目标）  
- 事实表两列快照因此各留一条：见 §5

### D10 — `provider_origins.name` 怎么处置？（**已决：直接丢弃**）

- Origin 的 `name` 与 `UNIQUE(provider_id, name)` **不迁移**、不改名、不保留备注字段  
- `Provider.name` 是唯一展示名  
- 连带删除：Channel DTO 与前端的 `provider_origin_name`（`ChannelDetailPage.tsx:137`）、Provider 列表「源站」列与 hover（`providers-os-columns.tsx:49`）、`provider-origins-columns.tsx` 整个文件  
- **本轮只更新文档，不动代码**（代码在获批开工后按 P1–P5 执行，Blueprint 最终事实在 P7 回写）

### D11 — combined routing 组合端点（**已决：删除**）

背景：`POST /provider-origins/{id}/routing`（`providerorigins.go:296` → `providerorigin.UpdateRouting:415`）是「一次原子改 base_url + status」的组合端点，后端已实现但**前端从未调用**（Admin UI 分开调 base-url 与 status）。

**决定：整条删除，不迁到 Provider。**

- Provider 侧只保留两条独立写路径：`PATCH /providers/{id}/origin`（改地址）与 `POST /providers/{id}/status`（改启停）  
- 需要「换地址并重新启用」时按 D5 正规流程分步：**停用 → 改地址 → 启用**。中途 Provider 处于 disabled、不接流量，中间态对调用方不可见，无需原子组合  
- 连带删除：  
  - `OriginFencer.updateRouting`、`FenceOps` 中的 combined 方法  
  - `luaPrepareOriginRoutingChange` / `Commit` / `Abort OriginRoutingChange` 三个 combined Lua 脚本与对应 Go 包装（`originfence.go`、`originfence_lua.go`）  
  - `OriginRoutingChange` 类型、`OriginFenceKind` 里的 combined kind  
  - `provider_routing_operations.kind` 枚举中不含 combined（`base_url_status`）  
- **收益**：R6 的「Provider disabled 时组合更新被拒」脚枪随端点消失，无需重新设计

---

## 5. 数据模型变更（终态）

> 迁移现状：`migrations/000001`–`000040` **当前连续**；`provider_origins` 在 `000008`，`origin_routing_operations` 在 `000009`。

### `providers` 增加（D6 + D9）

- `origin text NOT NULL`（上游根地址；normalize 规则沿用今天 `providerorigin.NormalizeBaseURL`）  
- `origin_revision bigint NOT NULL DEFAULT 1`（CHECK ≥ 1）  
- `status_revision bigint NOT NULL DEFAULT 1`（CHECK ≥ 1）— **今天 providers 没有此列**  
- CHECK：`origin <> ''`、`origin ~* '^https?://'`（归档后缀仍满足，无需豁免）  
- UNIQUE：`UNIQUE(origin)`（全局，含 archived；靠 D1 后缀腾坑）  
- **不**迁移 Origin 的 `name`（D10）

### `channels`

- **删除** `provider_origin_id`、索引 `idx_channels_provider_origin_id`、复合 FK `channels_provider_origin_fkey (provider_origin_id, provider_id)`  
- 保留 `provider_id`；上游地址一律读 Provider.`origin`  
- 原「改 `provider_origin_id` 触发 `config_revision + 1`」的注释与逻辑删除（地址变更现在走 Provider `origin_revision`）  
- Create/edit/enable 状态护栏见 D3  

### 删除表

- `provider_origins`（连带 `uq_provider_origins_id_provider`、`provider_origins_provider_id_name_key`、两个索引）  
- 旧 `origin_routing_operations`（由 D4 终态替代）  

### 事实表 / 审计表（**含原计划漏项**）

| 表 | 动作 |
| --- | --- |
| `request_records` | 删 `final_provider_origin_id` 及其 FK（已有 `final_provider_id`）；`request_records.sql` 里三处 `final_provider_origin_id = (SELECT c.provider_origin_id ...)` 子查询删除 |
| `request_attempts` | 删 `provider_origin_id` + 索引 + FK；`provider_origin_base_url_revision` → `provider_origin_revision`，`provider_origin_status_revision` 保留语义（两条都留，D9）；**`breaker_origin_disposition` → `breaker_provider_disposition`**（含其 CHECK 枚举） |
| **`channel_test_logs`**（原计划漏） | `tested_origin_base_url_revision` → `tested_origin_revision`；`tested_origin_status_revision` 保留；两个 CHECK 同步改。与 401 失效 / 探测 CAS 强耦合（`ApplyChannelProbeResult`、`ApplyRuntime401CredentialInvalidation`） |
| `routing_decision_traces` | JSON 内 `origin_id` / `candidate_origin_base_url_revision` / `runtime_origin_*` 键改名（`lifecycle/routing_trace.go:85-96`） |

### `app_settings`（**原计划漏项，R11**）

`circuit_breaker` 设置文档（`internal/service/appsettings/gateway_settings.go:104-114`）含 5 个 origin 键：

| 现键 | 动作 |
| --- | --- |
| `origin_base_url_revision_operation_ttl_ms` | → `origin_revision_operation_ttl_ms` |
| `origin_status_revision_operation_ttl_ms` | → `status_revision_operation_ttl_ms` |
| `origin_status_batch_max` | **删除**（1:1 后不再扇出，成死设定）；连带删 `gateway_settings.go:192-194` 的 `[1,1024]` 范围校验 |
| `origin_ambiguous_distinct_channels` | → `provider_ambiguous_distinct_channels` |
| `origin_ambiguous_distinct_models` | → `provider_ambiguous_distinct_models` |

同步：`breakerstore/control_payload_lua.go` 的解析键名、`blackbox/sdkfixture/fixture.go`、前端 `unio-admin/src/components/system/RuntimeSettingsPanel.tsx`。

### 硬删路径（R12）

`sql/queries/admin/provider.sql:51-73` 的 `DeleteProvider` CTE 会清 `origin_routing_operations` 并 `DELETE FROM provider_origins`。本变更不新增硬删能力，但**这条既存查询必须重写**（改清 `provider_routing_operations`，不再删 origin 行）。

### 迁移策略（开发期 · D8）

- 直接改 `migrations/*.up.sql` 终态，**不**写「线上 remapping」迁移  
- 具体动作：  
  1. `000007_providers` 加 `origin` / `origin_revision` / `status_revision` 及约束  
  2. **删除** `000008_provider_origins.{up,down}.sql`  
  3. **删除** `000009_origin_routing_operations.{up,down}.sql`，在同位置新建 `provider_routing_operations`（依赖 `providers`，放 `000008` 槽位合适）  
  4. `000010_channels` 去 `provider_origin_id` / 索引 / 复合 FK  
  5. `000011_request_records`、`000012_request_attempts`、`000019_channel_test_logs` 按上表改列  
  6. **重新编号为连续**（净减 1 个文件 → `000001`–`000039`），保持今天的连续性约定  
- 本地：`make infra-down` 清 volume → 重放全部 up → seed  
- Redis：flush 开发 DB 或整实例（避免旧 `origin:*` 键）

---

## 6. 运行时变更

### breakerstore（`internal/platform/breakerstore`，29 个文件）

- Key（`keys.go`）：`origin:{id}` → `provider:{id}`；`origin-evidence:*` → `provider-evidence:*`（每实体 6 键 = 3 category × {channels,models}）；`originfence.go:57-59` 的 `origin-routing:v1:op:{token}` → `provider-routing:*`  
- Lua：`originfence_lua.go`（helpers + 15 个脚本）、`lua.go`（gate/acquire、finish、renew、abort、reset、snapshotMany、permission）符号与 hash 字段名按 D9 改名；**实体 key 全部由 Go 传 KEYS[]**，Lua 内不拼前缀，改动集中在 hash 字段名与状态机命名  
- Go 符号：`ScopeOrigin` → `ScopeProvider`；`OriginEvidenceCategory`、`OriginRoutingChange`、`OriginStatusRevisionTransition`、`OriginRoutingRecovery*`、`RuntimeOriginControlProof`、`Init/RestoreMissingOriginControl`、`Prepare/Commit/Abort Origin*` 全套改名  
- Permit（`types.go:209-223`）：`OriginID` → `ProviderID`；`OriginBaseURLRevision` → `OriginRevision`；`OriginStatusRevision` 保留；两个 fence generation 与 `origin_control_enforced` 同步改名  
- **保留**（D9 明确不动）：Channel breaker、half-open 租约（origin 与 channel 各一套）、eligible 归因、跨 Channel/模型的歧义证据门槛、prepare 即推进 generation 的语义、两条 revision 的独立 stale 子原因  
- **原计划漏：integrity / fault-clear proof**  
  - `integrity_lua.go:342-365` 的 `luaRuntimeFaultClearCommit` 按 **每个 origin** 传 (base_revision, status_revision, effective_status) 三元组 + `origin_count`  
  - `bootstrap/runtime_control_recovery.go:109-131` 的 `captureRuntimeReconciliationProof` 按 provider×origin 组 proof，合并后集合从 O(origins) → O(providers)  
  - 这是 `P4_FULL_STATE_LOSS_E2E` 的门，必须在 P2 一并改
- **原计划漏：Redis purge helper 今天不存在**。只有 fence commit 时 Lua 内 DEL evidence、`Reset(ScopeOrigin)` DEL 6 evidence 键、PG 侧 24h 清终态 op。D1 要求的 archive purge 是**新增能力**

### routing / lifecycle

- 候选结构 `ChatRouteCandidate`（`internal/core/routing/router.go:74-138`）：`ProviderOriginID` 删除，`ProviderOriginBaseURLRevision` → `ProviderOriginRevision`，`ProviderOriginStatusRevision` 保留；`Channel.Runtime.BaseURL` → `Origin`  
- 路由 SQL（`sql/queries/gateway/channel_models.sql`）：**现状是** `JOIN provider_origins pe ON pe.id = c.provider_origin_id AND pe.provider_id = c.provider_id`（:77-81）加 `p.status='enabled' AND pe.status='enabled'` 两个分开过滤（:140-147），且**不**走 `EffectiveOriginStatus`。改为只 `JOIN providers p` + 只留 `p.status = 'enabled'`（§2 统一口径）  
- 上游 URL 拼接 `BuildUpstreamURL`（`internal/core/adapter/upstream_url.go:10-70`）的注释与参数名改 origin；各 adapter 调用点无逻辑变化  
- attempt 写入（`lifecycle/request_lifecycle.go:355-402`、`requestlog/store.go:232-257`）字段改名  
- 401 凭据闸门（`lifecycle/credential_gate.go:11-16`）与 403 permission hash（`keys.go:52-54`、`lua.go:1189-1218`）固化的 origin 两 revision → provider 两 revision（**原计划漏**）  
- Admin 路由作战台 `routeruntime/runtime.go:596-605` 的 `provider_origin_<status>` 归因分支删除（只剩 provider 分支）  
- metrics label：`origin_id` → `provider_id`（`metrics.go:379-436`，含 `origin_failure_total`、`upstream_ttft_seconds`、两组 revision fence gauge）；开发期接受看板断档  
- `EffectiveOriginStatus`（`origin_publisher.go:172-181`）**删除**；4 处调用改读 `providers.status`

### runtimecontrol（`internal/core/runtimecontrol`）

- `origin_publisher.go` / `origin_reconciler.go` → provider 围栏发布/恢复；`OriginFenceKind*`、`OriginRoutingEnvelope`、`CanonicalOriginRoutingOperation` 等改名  
- Provider 启停：删除 `provider_status_batch` 扇出与 `provider/fence.go:138-151` 的 `providerOriginTransitions`；改为单目标 fence（顺带消除 `>MaxBatch` 的 409 与 1793 KEYS 问题）  
- 锁序从「Provider → Origin IDs(ORDER BY id) → operation」简化为「Provider → operation」；`WithOriginLocks` helper 重做  
- reconciler 的 `JOIN provider_origins` 恢复全部 origin control → 按 provider 恢复  
- Provider disable 有 enabled Channel 时 **409**，不自动改 Channel 行 status（D3b）

### 明确不变

- Channel 级熔断、429 cooldown、credential gate、AttemptPermit 生命周期  
- 「只把真实 upstream transport 可归因结果写入 breaker」  
- 双 revision 的 fence generation / pending / stale 子原因结构（D9）

---

## 7. Admin API / UI

### API（D7 + D2 + D3 + D5 + D10）

- **删除** `/admin/v1/provider-origins*` 全部 9 条（清单见 §4 D7），无代理  
- Provider：创建时写 `origin`；普通编辑不接收 `origin` / `status`；`PATCH /providers/{id}/origin` 与 `POST /providers/{id}/status` 分别走 D5 / D9 围栏  
- Provider：**新建** `GET /providers/{id}/ops/runtime` 与 `DELETE /providers/{id}/ops/circuit-breaker`（带 404 存在性护栏，R9）  
- `GET /providers/ops`：`origins[]` 压平为 `origin` 字段；`affected_origin_count` 删除/改义  
- Channel：body 去掉 `provider_origin_id`；DTO 去 `provider_origin_name`（D10）；服务端校验 `provider_id` + D3  
- Channel breaker runtime DTO（`channel/breaker_ops.go:44-46`）的 origin 字段改名  
- **删除** Duplicate Channel API（D2）  
- **删除** Provider/Channel 两侧 Cascade / WithReplacement archive 路径（D1b）  
- **删除** combined routing 组合端点与其围栏实现（D11）；Provider 只有「改 origin」「改 status」两条写路径

### UI（unio-admin，无 i18n 文件，文案硬编码在组件内）

- 移除 `ProviderOriginsSection.tsx`（含导出的 `ProviderOriginFormDialog`）、`provider-origins-columns.tsx`、`ProviderDetailContent.tsx:47` 的「源站」Tab、`ProviderRowActions.tsx:105-109` 的「新建源站」  
- 移除 `src/lib/api/providerOrigins.ts` 整个 client  
- 移除 `DuplicateChannelDialog.tsx` / `CopyChannelsToOriginDialog.tsx` 与两处入口（D2）  
- 移除 Provider 与 Channel 两处 `ArchiveWithReplacementDialog` 入口（D1b）  
- Provider 表单/详情：内嵌 `origin` 编辑与 runtime 指示；改 URL 走 D5 确认流  
- Channel 表单：去掉 Origin 下拉；disabled Provider 下可创建 / 编辑但只能保存为 disabled；archived Provider 禁止配置；启用 Channel 必须先启用 Provider（服务端也校验，D3）  
- Provider 停用入口：存在 enabled Channel 时提示先停用渠道；不提供级联停用；渠道列表直接展示行状态（D3b）  
- 只读展示面改字段名：`providers-os-columns.tsx`、`channelsOps.ts`、`providersOps.ts`、`routesOps.ts`、`RouteRuntimeSection.tsx`、`RuntimeSettingsPanel.tsx`（含 R11 的设置键）、`RuntimeDiagnosticsPanel.tsx`  
- Archive 分步引导：线路 → 渠道 → 服务商（D1b）  
- 测试：`tests/p4-api-contract.test.ts:145-214,271+`、`tests/components/{ProviderOriginsSection,ChannelFormDialog,ProviderRowActions,ProviderFormDialog,RouteRuntimeSection}.test.tsx`  
- E2E：`e2e/providers-origins.spec.ts` 改写为 provider `origin` 流程；`e2e/p4-routing.spec.ts:348` 的 ProviderOrigin breaker 断言改 provider  

---

## 8. 文档（Blueprint）

**ADR 约定（已核对）**：`unio-blueprint/docs/decisions/README.md:48` 明确「若要改变已接受决策，**必须新建 ADR 并标记取代关系，不得改写原结论**」。因此：

| 文档 | 状态 | 动作 |
| --- | --- | --- |
| Gateway ADR-0001 统一领域术语 | active | **新建 superseding ADR** |
| Gateway ADR-0008 运行态代际围栏 | active | **新建 superseding ADR**（围栏单位改 Provider，双 revision 保留） |
| Gateway ADR-0010 上游熔断归因 | active | **新建 superseding ADR**（归因域改 Provider） |
| Gateway ADR-0006/0007/0009/0011 | active | 视改动幅度：仅术语校正可走「现状校正」段（ADR-0001:51-52 有先例）；触及 admission/breaker 契约则仍需新 ADR |
| Admin ADR-0002 Provider Origin 与供给管理 | **proposed** | 未 accepted，**可就地大幅改写**为 Provider `origin` 管理 |
| Gateway features | — | `resilience-circuit-breakers`、`runtime-control-recovery`、`routing-load-balancing`、`admission-control`、`provider-adaptation`、`data-lifecycle`、`error-semantics`、`public-api-contracts`、`features/README` |
| Admin features/pages | — | `operations-management`、`operations-observability`、`pages/provider-origin-channel-management`（draft）、`pages/README`、`roadmap`、`quality` |
| glossary / overview | — | gateway + admin 两份 glossary 与 overview、`docs/architecture/overview.md`、`products/website/pages/home.md` |
| decisions/README 索引 | — | gateway + admin 两份 |

**交接规则（`AGENTS.md:11-16`、`DEVELOPMENT.md:67-72`）**：只按最终代码/Schema/测试证明的行为更新 Blueprint，不把计划写成事实；Blueprint 更新并校验通过后删除本临时计划。

---

## 9. 实施阶段（任务包 · 对照决断）

> **D8：** 一次切换。Gateway P1–P4、Admin P5 分仓推进并在各自阶段末保持验证通过；Blueprint 在代码完成后最后回写。三仓正常 commit / push，不创建 PR。

### P0 — 冻结产品约定（文档）

- [x] 确认 D1–D11（全文见 §4）  
- [x] `DECISIONS.md` 摘要  
- [x] Blueprint 已建 proposed ADR「Provider、Channel 与 Route 供给生命周期」；最终事实仍在 P7 回写  
- [ ] 你确认「可以按 PLAN 开工」后进入 P1  

### P1 — Schema 终态（D1 / D4 / D6 / D9 / D10）

- [ ] `000007_providers` 加 `origin` + `origin_revision` + `status_revision` + CHECK + `UNIQUE(origin)`  
- [ ] 删 `000008_provider_origins`、删 `000009_origin_routing_operations`，新建 `provider_routing_operations`  
- [ ] `000010_channels` 去 `provider_origin_id` / 索引 / 复合 FK  
- [ ] `request_records` 去 `final_provider_origin_id`；`request_attempts` 改三列（含 `breaker_provider_disposition`）  
- [ ] **`channel_test_logs` 改两列 + 两 CHECK**  
- [ ] **readiness 查询改表名**（`shared/app_settings.sql:72,118`）  
- [ ] **`DeleteProvider` CTE 重写**（R12）  
- [ ] **迁移重新编号为连续** `000001`–`000039`  
- [ ] `sqlc generate`  
- [ ] 本地清库重放 migrations + seed + Redis flush  

### P2 — breakerstore + runtimecontrol（D1 Redis / D6 / D9）

- [ ] keys / 15 个 fence Lua / 7 个 store Lua / permit / snapshot / Finish 归因升 Provider，**双 revision 结构原样保留**  
- [ ] **integrity fault-clear proof 结构改造**（`integrity_lua.go` + `runtime_control_recovery.go`）  
- [ ] **新增** archive/delete Redis purge helper：立即清理不可路由状态，保留 permit / 并发租约 / 计数桶给在途收口或 TTL（对齐 §4 D1 清单）  
- [ ] 单目标 fence：删 batch 扇出、`providerOriginTransitions`、锁序简化  
- [ ] **`circuit_breaker` 设置键改名 + 删 `origin_status_batch_max`**（R11）  
- [ ] 单测：`store_test.go`(1509)、`originfence_test.go`(167)、`runtime_fault_test.go`(400)、`origin_publisher_test.go`(103)  
- [ ] Provider disable 有 enabled Channel 时 409；不扇出、不自动改 Channel 行（D3b）  

### P3 — routing / lifecycle / metrics（D3 / D6 / D9）

- [ ] 候选字段与 `FindRouteCandidates` 去 origin join，只留 `p.status='enabled'`  
- [ ] `EffectiveOriginStatus` 删除 + 4 处调用改单一来源  
- [ ] attempt / trace / 401 gate / 403 permission 快照字段  
- [ ] metrics label 与 `routeruntime` 归因分支  
- [ ] **p4fault harness 改造**（`harness_test.go:591-597,617` seed 与 Redis key）+ 7 个 e2e 文件  

### P4 — Admin service + API（D1b / D2 / D3 / D5 / D7 / D10）

- [ ] `providerorigin` 包（`providerorigin.go` + `fence.go`）逻辑并入 `provider`；删 9 条路由  
- [ ] **新建** provider runtime / reset breaker 端点（含 404 护栏）  
- [ ] `providers/ops` 压平 `origins[]`、处理 `affected_origin_count`  
- [ ] Channel 去 origin_id / origin_name；护栏 D3 落到服务端  
- [ ] Provider 普通编辑去 `origin` / `status`；专用端点按 D5 校验 expected revision 与显式 confirm  
- [ ] 归档 409 + 分层清 Redis；Provider 非终态 routing operation 直接拒绝；**删 Provider 与 Channel 两侧 Cascade/WithReplacement**；改写「任意池引用即 409」查询（D1/D1b）  
- [ ] 删除 Duplicate（D2）与 combined routing 端点及其围栏实现（D11）  
- [ ] handler 测试（`provider_endpoint_handlers_test.go` 等）  
- [ ] **此处要求 `go build ./...` + Go 测试全绿**  

### P5 — Admin UI + 合同/e2e（D2 / D3b / D5 / D7 / D10）

- [ ] 删 Origin IA / client / 两个复制 Dialog / 两处 Replacement Dialog  
- [ ] Provider 表单详情内嵌 `origin`；改 URL 确认流（D5）  
- [ ] Provider 停用 409 引导、Channel 状态约束（D3/D3b）；Archive 分步文案（D1b）  
- [ ] 只读展示面与 `RuntimeSettingsPanel` 字段/键改名  
- [ ] `p4-api-contract.test.ts` + 5 个组件测试 + 2 个 e2e spec  
- [ ] **此处要求 Admin 构建、合同测试与前端测试全绿**  

### P6 — 回归（对照 §10）

- [ ] `breakerstore` 全量单测  
- [ ] `P4_FAULT_E2E=1 go test ./internal/blackbox/p4fault`（基础套件）  
- [ ] 二级开关套件至少跑 `P4_FULL_STATE_LOSS_E2E`（验 proof 改造）与 `P4_LONG_STREAM_E2E`  
- [ ] 手工：建 Provider（含 `origin`）→ 建 Channel → 路由候选可见  
- [ ] 手工：改 `origin` 抬 `origin_revision`（D5）；启停只抬 `status_revision`；有 enabled Channel 时停用 Provider → 409，全部停用后成功且无假 stale（R1/D3b）  
- [ ] 手工：池中 Channel 归档 → 409；解除引用并归档后立即清理键消失，在途资源完成收口 / TTL；同 URL 可新建 Provider（D1）  
- [ ] 手工：readiness / 维护冒烟接口正常（R10）  

### P7 — Blueprint 回写与收尾

- [ ] 所有代码改造与验证完成后，新建 / 完成 superseding ADR（Gateway 0001/0008/0010）+ 就地改写 Admin ADR-0002  
- [ ] features / pages / glossary / overview / decisions README  
- [ ] 删临时 `docs/changes/...`  
- [ ] Gateway、Admin、Blueprint 三仓按上述顺序分别正常 commit / push，不创建 PR  

---

## 10. 验收标准（按决断勾选）

1. 库中无 `provider_origins`；`providers` 有 `origin` + `origin_revision` + `status_revision`，**无** `base_url`、**无** origin name（D6/D9/D10）  
2. Redis 无 `origin:` / `origin-evidence:` / `origin-routing:` 前缀键；Provider/Channel 熔断与 half-open 行为与文档一致  
3. 两条 revision 各自独立生效：改 `origin` 只抬 `origin_revision`，启停只抬 `status_revision`；`stale_revision` 与 `stale_status_revision` 两个子原因仍可分别产生（D9）  
4. Admin **无** `/provider-origins*`；无「源站」独立 IA；provider runtime/reset 端点存在且对不存在 id 返回 404（D7/R9）  
5. 状态单一来源：路由 SQL、Redis `effective_status`、Admin runtime sync 三者都以 `providers.status` 为准，无假 stale（R1）  
6. Channel API 无 `provider_origin_id` / `provider_origin_name`；disabled Provider 下只可创建 / 编辑 disabled Channel，archived Provider 禁止配置，启用 Channel 必须先启用 Provider（D3/D10）  
7. 无 Duplicate / CopyChannelsToOrigin 代码与 UI（D2）  
8. 无 Provider/Channel 两侧 Cascade/WithReplacement；渠道在任意 `route_channels` 中即 409；归档立即清理键消失，在途 permit / 租约 / 桶可正常收口并最终消失；原 URL 可重建（D1/D1b）  
9. Provider disable 遇 enabled Channel 返回 409，不级联改 Channel.status；Provider enable 不自动启用 Channel（D3b）  
10. 普通 Provider 编辑不能改 `origin` / `status`；改 `origin` 必须提交 `expected_origin_revision`，有 enabled Channel 时还须 `confirm_enabled_channels=true`（D5）  
11. 存在 `provider_routing_operations`，未并入 `runtime_control_operations`，`kind` 只含 `origin` / `status`，且 readiness 两处查询已指向新表（D4/D11/R10）  
12. `circuit_breaker` 设置无 `origin_status_batch_max`，其余键已按 provider 语义改名，前端设置面板同步（R11）  
13. `channel_test_logs` / `request_attempts` / `routing_decision_traces` 的 origin 列与 JSON 键已改名  
14. 迁移文件编号连续  
15. 官方与中转 = 两个 Provider 可同时存在（不同 `origin`）  
16. p4fault 基础套件 + `P4_FULL_STATE_LOSS_E2E` + breakerstore 单测通过  
17. Blueprint 术语与实现一致；Gateway active ADR 走 superseding 而非就地改结论  

---

## 11. 回滚

开发期：git revert + 清库清 Redis 回到旧 migrations。  
不做生产双版本混跑方案。

---

## 12. 下一步

- [x] §4 D1–D8 拍板并回写本 PLAN  
- [x] D9（两条 revision）、D10（丢弃 origin name）、D11（删 combined 端点）  
- [x] 代码审计核对，补齐 §5/§6/§7 漏项与 R10–R12  
- [ ] 你确认「可以按 PLAN 开工」→ 从 P0 剩余项（ADR draft）或直接 P1 开始  
