# Phase 13 Admin 模块与接口（讨论草稿）

> 状态：**临时讨论草稿**，未固化进 PLAN.md / STATUS.md。审查通过后再正式拆任务、合并进章节文档。
> 本文件是本轮讨论的完整结论：范围边界 + 9 个模块（M1~M9）+ 接口约定 + RESTful 资源地图 + 关键设计决策 + 推进顺序 + GAP 映射 + deferred + 待定决策。
> 可随时删除或合并进正式章节文档。

## 本轮讨论结论速览

1. 阶段 13 只做 **admin-server**；console-server / 支付充值 / GAP-12-006 全部 deferred。
2. 余额只做 **admin 手工调额**（走 ledger）。
3. **M1 认证砍到最小**：单管理员 + 静态 `ADMIN_API_TOKEN` 中间件，**不做 JWT / RBAC / 建表 / 登录会话 / CLI / 审计**；保留 `AdminPrincipal` context 缝便于日后升级。
4. RBAC 用自写粗粒度门禁的思路（**当前阶段连门禁都不做**），不引入 Casbin。
5. 新增 **M9 工作台看板**（运营首页，只读聚合 KPI）。
6. 路由遵循 REST：路径无动词、状态变更用 PATCH、动作建模成 job/子资源。
7. **交付方式 = 垂直切片、前后端协同**：不一次性把整套 API 设计死；模块图/资源地图当"北极星契约"，每片端点在做对应 UI 时定稿。
8. **前端 `unio-admin` 单独建仓库**（与 `unio-gateway` 平级），不进 `unio-gateway`；契约靠 `/admin/v1` OpenAPI / 导出类型同步。

## 范围边界（已确认）

- 阶段 13 = **只做 admin-server**（平台运营闭环），服务 `/admin/v1/*`。
- 余额变动**只做 admin 手工调额**（走 `core/ledger`），支付/自助充值后续阶段。
- **不在阶段 13**：整个 console-server（`/console/v1/*`）、GAP-12-006 `/console/v1/models`、支付/充值、invoices / members / alerts / webhooks。
- enforce 实际切换（`CAPABILITY_ENFORCE_*` 翻 true）是阶段末尾**运营动作**，不是编码任务，依赖真实模型 + 能力位铺好 + observe 观察期。

## 交付方式与前端协同（已确认）

- **垂直切片**：按"屏/工作流"端到端推进，不一次性铺满 API。每片只定该 UI 需要的最小端点，建后端 → 建前端 → 联调 → 下一片。
- **本文档定位**：模块图与 RESTful 资源地图是**北极星契约/全景参考**，不是一次性施工单；具体端点形态、`draft 态 vs 聚合事务端点`、上线门禁严格度等，在做对应切片时才定稿。
- **前端 `unio-admin` 独立仓库**（与 `unio-gateway` 平级），不放进 `unio-gateway`（沿用 AGENTS.md "前端不进后端仓库"决策）。
- **契约同步**：`unio-gateway` 维护 `/admin/v1` OpenAPI / 导出 TS 类型，`unio-admin` 消费；切片粒度小以压住跨仓库漂移。
- **协作角色**：后端（`unio-gateway`）+ 前端（`unio-admin`）一起做。

## 模块总览

| 模块 | 名称 | 主要表 | 关联 GAP | 风险级别 |
| --- | --- | --- | --- | --- |
| M1 | Admin 认证（单管理员，静态 token） | 无（仅 env ADMIN_API_TOKEN） | 新增 | 安全 |
| M2 | Credential 管理 | channels.credential_ref (+ 凭据存储方案) | GAP-6-001 | 安全 |
| M3 | Provider / Channel / Model / 绑定 | providers, channels, channel_models, models | GAP-6-003, GAP-12-007, GAP-11-006 | 路由/资金间接 |
| M4 | 定价管理（售价 + 成本价） | prices, channel_cost_prices | 依赖 TASK-7.22 | 资金 |
| M5 | 能力管理 | model_capabilities, channel_capability_overrides, model_capability_sync_jobs | GAP-12-007, GAP-12-009, GAP-12-002 | 商业承诺 |
| M6 | Request / Usage / Billing 只读查询台 | request_records, request_attempts, usage_records, usage_line_items, price_snapshots, cost_snapshots, ledger_entries, ledger_billing_exceptions | 依赖 TASK-7.x / 8.02 | 信息脱敏 |
| M7 | 客户 / 项目 / 预算 / 手工调额 | users, projects, project_model_policies, user_balances, ledger_entries | GAP-6-005, GAP-3-002 | 资金 |
| M8 | 系统 / 任务 / 健康（横切） | settlement_recovery_jobs, model_capability_sync_jobs, channels(health) | GAP-12-011 | 运营 |
| M9 | 工作台看板（运营首页，只读聚合 KPI） | request_records, usage_records, ledger_entries, channels(health) | 新增 | 运营 |

---

## M1 — Admin 认证（TASK-13.01，单管理员极简版）

单运营者（平台所有者本人）阶段，认证砍到最小：所有 admin 操作的入口门，但不做登录/会话/权限。

- 认证：静态 env `ADMIN_API_TOKEN`（强随机），`admin_auth` 中间件常量时间比对，守护全部 `/admin/v1/*`。
- 身份缝：中间件认证后往 context 塞恒定 `AdminPrincipal`，handler 一律从 context 取身份；以后换登录/多管理员/RBAC 只改中间件，不动 handler。
- **不做**：JWT、RBAC/role、admin_users / admin_sessions / admin_audit_logs 表、密码 / argon2id、create-admin CLI、登录/刷新/登出流程。
- 本模块**不涉及任何 migration / sqlc**；第一批建表推迟到 M2/M3。
- 硬隔离：customer API key 不能访问 `/admin/v1/*`（不同进程 + 独立 token）。

端点：`GET /healthz`（免鉴权）、`GET /admin/v1/ping`（鉴权探针）；真正资源端点从 M2/M3 起。

> 升级触发：要给 unio-web 接登录页、出现第二个管理员、或后台对更多人开放时，再升级为"密码登录 → 会话 token + RBAC"，并补 admin 操作审计。

## M2 — Credential 管理（TASK-13.02）

让 channel 凭据能安全录入、轮换、审计；adapter 只拿到 `channel.Runtime.Credential`。

- 现状：`channels.credential_ref` 存 AES-GCM 密文，请求期用 `CREDENTIAL_MASTER_KEY` 实时解密。
- 本模块能力：后台录入明文 → 加密入库（明文不长期落库 / 不进日志）、轮换、读取审计。
- **决策点（GAP-6-001）**：v1 沿用 master key + 密文列，还是引入外部 KMS / secret manager（resolver 做成可插拔接口）。规划 13.02 时单独定。
- 与 M3 channel CRUD 强耦合：channel 要能跑就得先有 credential，二者同批落地。

## M3 — Provider / Channel / Model / 绑定 管理（TASK-13.03A）

把路由所需业务数据变成后台可管理对象；写库即对下一请求生效（routing 实时读库，无需重启）。

- **Provider CRUD**：基础业务实体。
- **Channel CRUD**：protocol、adapter_key、base_url、priority、enabled、health、credential_ref。
  - 写入校验：protocol + adapter_key 复合键必须存在于 adapter registry（关 **GAP-6-003**）。
- **Channel ↔ Model 绑定**（`channel_models`）：upstream_model、enabled。
- **Model CRUD / provisioning**：建出真实 model 行（owned_by、lab/canonical_id、enabled、max_output_tokens 等）。
  - 是关 **GAP-12-007**（adapter 画像物化进真实 model 行）和 **GAP-11-006**（Codex 默认模型名映射）的前提。
- 注意：adapter 本身是代码能力（静态 registry），后台只能从已注册 adapter 中选，不热加载 adapter 代码。

## M4 — 定价管理（TASK-13.03B）

- **客户售价** `prices` CRUD：pricing_unit、currency、input/output/cached/reasoning 明确金额、effective_from/to。
- **provider 成本价** `channel_cost_prices` CRUD。
- **生效窗口不重叠校验**（依赖 TASK-7.22）。
- 只填明确金额，不做倍率/折扣（DEC 已定）；金额不用 float。
- 不动历史快照：`price_snapshots` / `cost_snapshots` 是账务事实，只读。

## M5 — 能力管理（TASK-13.03C，收阶段 12 尾巴）

- **models.dev 同步可视化 + 手动触发**（`model_capability_sync_jobs`）。
- **模型层人工 capability 覆盖**（`model_capabilities`，source=manual 不被同步覆盖）。
- **channel 层 override**（`channel_capability_overrides`，**只能做减法**：限制/关闭，不能凭空声明）。
- **adapter 画像物化**：调 `MaterializeAdapterSeed` 把 DeepSeek 等画像写进真实 model 行（关 **GAP-12-007**）。
- **observe→enforce 控制**：观察期 metric/审计复核（`unio_gateway_capability_missing_total`），校准后翻 `CAPABILITY_ENFORCE_*`（关 **GAP-12-009 / GAP-12-002**），切换决策记入 DECISIONS 实施日志。

## M6 — Request / Usage / Billing 只读查询台（TASK-13.04）

按权限查询请求与账务事实，全部只读。

- `request_records` / `request_attempts` 状态机事实。
- `usage_records` / `usage_line_items` 用量。
- `price_snapshots` / `cost_snapshots` 计费快照。
- `ledger_entries` / `ledger_billing_exceptions`（如 `authorization_underfunded` 平台核销）。
- 过滤：user / project / api_key / model / provider / channel / status / 时间范围。
- 安全：safe_user_message 与 internal_error_detail 分离；默认脱敏 credential / API key / 上游原始错误 / prompt / 响应正文。

## M7 — 客户 / 项目 / 预算 / 手工调额（TASK-13.05）

- **user 维度**：余额查询；**手工调额**——必须走 `core/ledger`（调额原因 + 审计 + ledger entry），**禁止直接改 `user_balances` row**。
- **project 维度**：用量统计、启停、模型可见性（`project_model_policies` allow/deny）、预算/用量上限、专属或禁用 channel 策略（关 **GAP-6-005**）。
- **API key 后台视图**：列出 / 吊销 / 禁用 + 审计（关联 GAP-3-002）。
- 当前为个人账户模式；org/team 模式预留不实现。

## M8 — 系统 / 任务 / 健康（横切）

- 任务视图：`settlement_recovery_jobs`、`model_capability_sync_jobs`（含 sync 运营残留 GAP-12-011）。
- channel health 视图 / 熔断状态（结合阶段 8 metrics）。

## M9 — 工作台看板（运营首页，只读聚合）

运营者登录后的首页，聚合现有事实给出概览；纯只读、不引入新业务事实。

- KPI 概览：区间内请求数 / 成功率 / 错误率、token 用量、花费与收入、活跃 channel 数 + 健康分布、用户余额总额、待结算异常数（`ledger_billing_exceptions`）。
- 趋势序列：requests / tokens / spend 按 hour|day 的时间序列，供前端画折线。
- 数据来源：`request_records` / `usage_records` / `ledger_entries` / `channels(health)` / `ledger_billing_exceptions`；可结合阶段 8 Prometheus metrics（但历史聚合更适合落库查询）。
- 性能：大表实时聚合要留意，后续可借 `usage_rollup_worker`（已预留）或物化视图优化。
- 依赖 M6 的事实读取面，靠后或与 M6 并行；纯只读、风险低。

---

## 接口约定（所有模块通用）

- 鉴权：`Authorization: Bearer <ADMIN_API_TOKEN>`（M1 静态 token）。
- 风格：REST 资源 + 标准动词；**路径只放名词、不出现动词**；状态变更用 `PATCH`；动作（同步/物化等）建模成"任务/子资源"。
- 启停 ≠ 删除：启停用 `PATCH {enabled:false}`，`DELETE` 只留给真正软删（如 API key 吊销置 `revoked_at`）。
- 成功响应：单资源直接返回对象；列表 `{ "data": [...], "page": { "limit", "offset"/"next_cursor", "total?" } }`。
- 错误响应：admin 自有 envelope，**直接暴露内部 `failure.Code`**（不带上游 body / SQL / 凭据）：

```json
{ "error": { "code": "routing_model_not_found", "message": "model not found", "details": {} } }
```

- 分页：默认 `limit` + `offset`；大表（`request_records` / `usage_records`）预留 keyset cursor。
- 金额：字符串 decimal（**绝不 float**）+ 显式 `currency` / `pricing_unit`。
- 时间：RFC3339 UTC；id：int64。
- `PATCH` 语义：指针字段区分"不改"与"显式置零"。
- 永不返回：credential 明文/密文、API key 明文/hash、password、上游原始错误。

## RESTful 资源地图（13.01 只占头两行，其余 M2+）

```text
# M1（13.01）
GET    /healthz                                   # 免鉴权
GET    /admin/v1/ping                             # 鉴权探针

# M3 provider / channel / model / 绑定
GET    /admin/v1/providers                        POST   /admin/v1/providers
GET    /admin/v1/providers/{id}                   PATCH  /admin/v1/providers/{id}     DELETE /admin/v1/providers/{id}
GET    /admin/v1/channels?provider_id=&enabled=   POST   /admin/v1/channels
GET    /admin/v1/channels/{id}                    PATCH  /admin/v1/channels/{id}      DELETE /admin/v1/channels/{id}
GET    /admin/v1/channels/{id}/health             # 健康并入资源；列表 GET /channels 带 health 字段
GET    /admin/v1/channels/{id}/models             POST   /admin/v1/channels/{id}/models      # 绑定 upstream_model
PATCH  /admin/v1/channels/{id}/models/{modelId}   DELETE /admin/v1/channels/{id}/models/{modelId}
GET    /admin/v1/models?enabled=&lab=             POST   /admin/v1/models
GET    /admin/v1/models/{id}                      PATCH  /admin/v1/models/{id}        DELETE /admin/v1/models/{id}

# M2 credential（channel 下，write-only）
PUT    /admin/v1/channels/{id}/credential         DELETE /admin/v1/channels/{id}/credential   # GET channel 只回 has_credential/masked

# M4 定价
GET    /admin/v1/models/{id}/prices               POST   /admin/v1/models/{id}/prices         # 售价
GET    /admin/v1/channels/{id}/cost-prices        POST   /admin/v1/channels/{id}/cost-prices   # 成本价
PATCH  /admin/v1/prices/{id}                       # 关闭窗口=改 effective_to；不提供 DELETE

# M5 能力
GET    /admin/v1/models/{id}/capabilities
PUT    /admin/v1/models/{id}/capabilities/{key}   DELETE /admin/v1/models/{id}/capabilities/{key}
PUT    /admin/v1/channels/{id}/capability-overrides/{key}   DELETE /admin/v1/channels/{id}/capability-overrides/{key}  # 只能减
GET    /admin/v1/capability/sync-jobs             POST   /admin/v1/capability/sync-jobs        # 触发同步=建 job
POST   /admin/v1/capability/adapter-seed-jobs      # 物化 adapter 画像=建 job
GET    /admin/v1/capability/enforcement           PUT    /admin/v1/capability/enforcement      # observe/enforce 现状/切换

# M6 只读查询台
GET    /admin/v1/requests?user_id=&project_id=&model=&status=&from=&to=
GET    /admin/v1/requests/{requestId}              # 含 attempts
GET    /admin/v1/usage?...
GET    /admin/v1/ledger/entries?...
GET    /admin/v1/ledger/billing-exceptions?...

# M7 客户 / 项目 / 预算 / 余额
GET    /admin/v1/users                            GET    /admin/v1/users/{id}
GET    /admin/v1/users/{id}/balance                # 单例
GET    /admin/v1/users/{id}/balance-adjustments   POST   /admin/v1/users/{id}/balance-adjustments   # 走 ledger
GET    /admin/v1/projects?user_id=                GET    /admin/v1/projects/{id}    PATCH /admin/v1/projects/{id}   # 启停/预算
PUT    /admin/v1/projects/{id}/model-policies      # 模型可见性
GET    /admin/v1/projects/{id}/api-keys           POST   /admin/v1/projects/{id}/api-keys
DELETE /admin/v1/projects/{id}/api-keys/{keyId}    # 软删=吊销；明文只在 POST 返回一次

# M8 系统
GET    /admin/v1/system/settlement-recovery-jobs

# M9 工作台看板
GET    /admin/v1/dashboard/overview?from=&to=
GET    /admin/v1/dashboard/timeseries?metric=requests|tokens|spend&interval=hour|day&from=&to=
```

## 关键接口设计决策

1. **credential 是 write-only 子资源**：`PUT /channels/{id}/credential` 录入/轮换，GET channel 永不回明文（只 `has_credential` 或 masked 尾 4 位）。
2. **价格不可删**：账务要复算，只能 `PATCH effective_to` 关闭窗口；新旧窗口重叠 → 422 + `billing_*` code。
3. **余额调整是 ledger 动作**：`POST /users/{id}/balance-adjustments {amount, reason}` 走 `core/ledger` 记一笔 entry，禁止 PATCH `user_balances`。
4. **API key 创建明文只回一次**（仿现有 `apikey.Generate`），之后只存 hash/prefix。
5. **capability key 校验**：写覆盖时 key 必须在 `docs/protocol/CAPABILITY_KEYS.md` 注册表内，否则 `capability_invalid_key`。
6. **enforce 热切（待定）**：`CAPABILITY_ENFORCE_*` 现为 env + 重启。要后台热切 observe→enforce，需把它从 env 挪成 DB runtime setting；不挪则 `enforcement` 端点只能"查看"，切换仍靠改 env 重启。

---

## 建议推进顺序

```text
M1 Admin 认证（静态 token）
  └─ M2 Credential ── M3A Provider/Channel/Model/绑定
                          ├─ M4 定价
                          ├─ M5 能力管理 ──（prod observe 期后）enforce 切换
                          └─ M7 项目策略/预算/手工调额
  └─ M6 只读查询台（可与上面并行，低风险）
       └─ M9 工作台看板（依赖 M6 事实数据，靠后或并行）
```

要点：
- M2 与 M3A 同批（channel 依赖 credential）。
- M3A 的 Model provisioning 是 M5 物化与 enforce 切换的前提。
- M6 只读、风险低，可早做或并行。
- enforce 切换是 M5 落地 + prod 观察后的运营动作。

## GAP 收口映射

| GAP | 收口模块 |
| --- | --- |
| GAP-6-001 credential resolver | M2 |
| GAP-6-003 channel 写入复合键校验 | M3A |
| GAP-6-005 project 禁用 / 专属 channel | M7 |
| GAP-12-007 adapter 画像物化 | M3A(provisioning) + M5 |
| GAP-12-009 / 12-002 enforce 切换 + 覆盖面复核 | M5（+ prod 观察期） |
| GAP-11-006 Codex 默认模型名映射 | M3A（需要时） |
| GAP-3-002 API key 管理 + 审计 | M7 |
| GAP-12-011 sync 运营残留 | M8 |

## 明确 deferred（不在阶段 13）

- console-server 全部（`/console/v1/*`）。
- GAP-12-006 `/console/v1/models`（依赖 console-server）。
- 支付 / 自助充值。
- invoices / members / alerts / webhooks。
- **JWT / 密码登录 / 会话管理**（单管理员阶段用静态 token；多管理员或接 web 登录页时再升级）。
- **RBAC（含 role）与 admin 操作审计日志**（单运营者无可门禁；随多管理员/对外开放再补）。
- 登录暴力破解节流 / 锁定。

## 待定决策

1. M2 credential 存储：master key + 密文列（现状延续）vs 外部 KMS/secret manager。
2. M5 enforce 切换的灰度顺序与观察期判据（DEC-015 已定先 Chat 再 Anthropic 再 Responses）。
3. admin 认证升级时机：何时从静态 token 升级为登录/会话（+ 是否同时补 RBAC 与操作审计）。
4. enforce 是否做后台热切：`CAPABILITY_ENFORCE_*` 从 env 挪成 DB runtime setting（可热切）vs 维持 env + 重启（`enforcement` 端点只读）。
