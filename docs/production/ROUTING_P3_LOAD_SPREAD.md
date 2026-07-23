# P3：线路内渠道负载均衡与热渠道 TPM 治理

> 状态：**设计决议 / 待实现**（2026-07-19 修订）
> 前置：会话 sticky（P0）+ 队首 TPM/并发短等（P1）已落地。  
> 相关审计：[GATEWAY_LIFECYCLE_AUDIT.md](./GATEWAY_LIFECYCLE_AUDIT.md)
>
> **P4 替代提示（2026-07-22）**：本文档以下语义已由 [ROUTING_P4_GLOBAL_BREAKER_PROVIDER_PLAN.md](./ROUTING_P4_GLOBAL_BREAKER_PROVIDER_PLAN.md) 替代，实现以 P4 为准：
> 1. 进程内 `ChannelCircuitBreaker` + Admin 多实例 worst-wins 合并 → Redis Channel/Endpoint 全局熔断；
> 2. 「完整上游耗时 EWMA 参与健康因子」→ 仅由流式有效 FirstToken 样本产生的唯一 TTFT EWMA（流式/非流式调度共用，完整总耗时只作副指标）；
> 3. `health_score/health_factor` 与 `healthy/degraded/unhealthy` 主观分桶 → 错误率、流式 TTFT、容量、最终权重等客观事实；
> 4. `channels.base_url` → `provider_endpoints.base_url + channels.provider_endpoint_id`；
> 5. 统一 no-available 503 → 按最终排除原因聚合的 429/503/`model_not_found`。
> P3 的线路显式渠道池、`balanced/fixed`、成本不参与排序、sticky、毛利硬门禁等边界继续有效。

本次修订废弃原「`cheapest` 最便宜同成本档分流」方案，改为**线路边界内的显式渠道池负载均衡**。负载均衡不扫描全局渠道，也不按成本档决定候选范围；后续 DEC-055 只在已通过全部门禁的线路池候选间加入受限成本因子。

本阶段同时完成商业上线门禁：**毛利保护、balanced 调度口径、线路一致的模型可见性和实时运维诊断不得延期到后续阶段**。任一门禁未通过，P3 仍保持“待实现”状态。

---

## 1. 问题边界

| 能力 | 解决什么 | 不解决什么 |
|------|----------|------------|
| **P0 sticky** | 同会话保持同一渠道，减少上游 prompt cache 断裂 | 线路池容量不足 |
| **P1 队首短等** | 本地并发/TPM 瞬时满时先等待再换渠道 | 60s 级 TPM 窗口长期打满 |
| **P3 本文件** | 在当前线路绑定的渠道之间按容量和健康度分配请求 | 全局渠道调度、按成本自动升舱、客户自选渠道 |

### 1.1 线路池是唯一调度边界

线路必须由运营人员手动绑定渠道。运行时候选集合定义为：

```text
当前线路的 route_channels
  ∩ 当前模型的 channel_models
  ∩ ingress protocol
  ∩ enabled / credential / pricing / capability / breaker 可用性
```

因此：

- 未绑定到当前线路的渠道，永远不能被该线路请求选中；
- 当前线路的候选耗尽后，也不能回落到全局其他渠道或其他线路；
- 线路池中的渠道不一定都能服务每一个模型，最终候选仍需经过模型、协议和计价过滤；
- 后续新增渠道不会自动进入任何已有线路，必须显式加入线路池。

这里的“取消候选池”指取消 `pool_kind=all` 的**自动全量候选池**，不是删除 `route_channels`。`route_channels` 是新方案的核心配置。

### 1.2 当前代码基线

当前实现仍保留旧策略：

1. [`route.go`](../../internal/service/admin/route/route.go) 支持 `cheapest/stable/fixed/random`，并允许 `pool_kind=all`；
2. [`router.go`](../../internal/core/routing/router.go) 将 `PoolKind` 传入候选 SQL，由 `all` 分支绕过 `route_channels`；
3. [`candidates.go`](../../internal/service/gateway/lifecycle/candidates.go) 仍会按渠道成本执行 `cheapest` 排序；
4. `fixed` 当前通过 service 层校验为恰好一条显式渠道。

这些旧语义都需要在实现阶段直接移除；当前数据库允许重建，不保留旧线路数据兼容层。

---

## 2. 决议

### 2.1 线路配置

所有线路统一使用显式渠道池：

| 配置项 | 规则 |
|--------|------|
| 渠道池 | 必须手动选择至少一条渠道 |
| `balanced` | 至少一条渠道；多渠道时在池内负载均衡 |
| `fixed` | 必须且只能选择一条渠道 |
| 自动全量池 | 不支持 |
| `pool_kind` | 目标态删除，不再作为运行时分支 |

`fixed` 继续保持单渠道语义。允许 fixed 选择多条渠道后再按 priority 选第一条，会把“固定哪条渠道”变成隐式行为，因此不采用。

### 2.2 `balanced` 策略

本节原设计要求 `balanced` 完全忽略渠道成本，现由 DEC-055 修订：仍不按成本分档或改变候选范围，也不要求
线路池内渠道成本相同；在毛利硬校验通过后，真实渠道成本相对客户售价的保守比例会作为受限因子乘入最终权重。
成本不能绕过容量、breaker、权限、revision、限流或 sticky 边界。

硬过滤完成后，按下列已确认口径生成候选顺序：

1. 先执行硬过滤：模型绑定、协议、渠道/服务商状态、凭据、当前有效价格、能力和熔断状态；
2. 读取 channel-level 全局并发和 TPM 使用量；同一渠道被多条线路共享时，所有线路读取同一份容量事实；
3. 分别计算并发剩余比例和 TPM 剩余比例，容量分取二者最小值，即最紧张资源决定可用容量；
4. 结合近窗口错误率和延迟得到健康因子；健康因子限定在 `(0,1]` 并保留正下限，健康越差只做降权，breaker open 等状态仍由硬过滤摘除；
5. `weight = capacity_score × health_factor`，对正权重候选执行加权随机，并以不放回方式生成完整 fallback 顺序；
6. 请求失败时，fallback 只从当前线路池的剩余候选中继续尝试。

剩余比例统一限定在 `[0,1]`。显式不限的资源按 `1` 处理；继承限制先解析成实际限制再计算；单项容量信号未知时使用另一项，全部容量信号未知时退化为线路池内均匀随机。错误率或延迟无样本时健康因子按中性值处理。

容量信号只用于候选顺序，不能在排序阶段预占配额。真正尝试前仍由现有 channel-level admit/ratelimit 原子预占，防止多实例、多线路并发读取后重复超卖。

存在正权重候选时，容量分为 `0` 的候选不参与首选随机，但保留为最后 fallback。所有候选容量分都为 `0` 时，选择压力最低的候选执行现有队首短等；超时后继续在线路池内 fallback，最终耗尽则返回无可用渠道。

`balanced` 允许只绑定一条渠道，便于线路逐步扩容；此时运行行为自然退化为单候选，但语义上仍允许后续加入更多渠道。

### 2.3 `fixed` 策略

`fixed` 线路只有一个显式渠道候选：

- 不做负载均衡；
- 不做跨渠道 fallback；
- 渠道因状态、熔断、凭据、模型或协议不满足而不可用时，线路直接返回无可用渠道；
- 不允许自动切换到全局其他渠道。

### 2.4 与 sticky 共存

sticky 的作用域同样受线路池限制：

- sticky 命中渠道仍在当前线路池、且通过当前可用性过滤时，置顶并优先保持，避免主动破坏上游 prompt cache；
- sticky 绑定渠道已经被移出线路池、归档、熔断或不再支持当前模型/协议时，视为硬摘除，清理 sticky 后重新执行 `balanced`；
- sticky 不得把一个不属于当前线路的渠道重新带入候选；
- `fixed` 线路不因 sticky 改变其唯一渠道。

候选准备顺序为：

```text
当前线路 route_channels 硬过滤
  → 模型 / 协议 / 价格 / 凭据 / 能力 / 熔断过滤
  → balanced 容量与健康度排序，或 fixed 保留唯一候选
  → 失败软冷却 demote
  → sticky 置顶
```

sticky 置顶仍然优先于 `balanced` 的普通调度，但不能突破线路池边界。

### 2.5 利润保护

`balanced` 即使按 DEC-055 加入受限成本因子，同一线路仍可能命中不同成本的渠道，因此“不亏”必须由配置约束
和运行时毛利硬门禁保证，不能依赖随机路由顺序。

当前计价模型下：

```text
客户售价 = model_price × route.price_ratio
渠道成本 = 绝对成本覆盖
        或 model_price × channel_cost_multiplier × recharge_factor
```

仅比较“线路倍率 > 渠道倍率”并不足够，还必须覆盖：

- uncached input、output、cache read/write、reasoning output 等计价分项；
- 渠道模型级绝对成本覆盖；
- 渠道默认/模型级成本倍率；
- 充值倍率；
- 当前线路绑定渠道和其支持模型的组合。

线路创建、更新和启用时，必须校验当前线路池内所有可路由模型的毛利不为负。渠道成本、充值倍率或模型基准价变更后，也必须在变更生效前重新校验受影响线路。

不亏优先于可用性：配置变更会产生负毛利时整笔事务拒绝；运行时最后一道校验发现负毛利候选时直接硬摘除并告警。若当前线路全部候选均因负毛利被摘除，则返回无可用渠道，不允许为了成功率继续请求。

### 2.6 模型可见性与冗余

`/v1/models` 使用线路池可路由模型的**并集**：当前线路池中至少存在一条满足模型、协议、状态、计价、能力和毛利要求的渠道，模型即可见。

某模型只有一条可用渠道时仍然展示，但 admin 必须显示“无冗余”告警；两条及以上可用渠道才视为具备线路内 fallback 能力。

### 2.7 渠道与服务商归档护栏

归档渠道或服务商会删除 `route_channels` 关系时，必须先计算受影响线路。若操作会让任一启用线路变成空池，则禁止直接归档；运营人员必须在同一管理操作中指定替代渠道，或者先停用受影响线路。

替换渠道、更新线路池和归档动作必须处于同一事务边界，避免中间状态导致启用线路短暂空池。归档后不自动恢复线路绑定，恢复渠道仍需运营人员显式加入线路。

---

## 3. 运行时落点

线路候选 SQL 必须始终使用 `route_channels` 过滤，不再接受 `PoolKind` 参数：

```sql
EXISTS (
    SELECT 1
    FROM route_channels rc
    WHERE rc.route_id = :route_id
      AND rc.channel_id = c.id
)
```

生命周期层只接收当前线路已经过滤后的候选：

```text
PlanChat
  → 当前线路解析
  → route_channels ∩ 模型/协议/计价候选
  → PrepareCandidates(mode=balanced|fixed)
  → 当前线路池内 fallback plan
```

任何 fallback、短等结束后的重选和失败重试，都必须复用同一个线路候选集合，不能重新查询全局渠道。

---

## 4. 配置与 API

### 4.1 线路 API

目标态线路请求只保留：

```json
{
  "name": "balanced-openai",
  "mode": "balanced",
  "channel_ids": [11, 12, 15]
}
```

`pool_kind` 和 `all` 不再出现在 admin DTO、前端表单或 routing 请求参数中。线路详情始终返回显式绑定的渠道列表。

### 4.2 balanced 运行参数

容量权重可以通过系统设置热更新，具体 key 名称在实现时进入 `gateway_settings` 注册表：

| 字段 | 默认 | 说明 |
|------|------|------|
| `enabled` | `true` | `balanced` 容量调度总开关；关闭时仍限制在线路池内 |
| `weight_by_remaining` | `true` | 是否按剩余并发/TPM 加权；关闭时池内均匀分流 |

关闭容量权重不代表恢复 `cheapest`，也不代表允许使用全局候选；只是在当前线路池内退化为均匀或稳定顺序。

---

## 5. 可观测与实时后台

实时可观测属于本次 P3 的商业上线门禁。目标不是只提供 Prometheus 指标，而是让运营人员在线路详情直接看到当前渠道容量、健康度、调度权重和最近路由决策。

### 5.1 当前基线与目标

当前后台已有：

- 基于 request/attempt 数据库记录的线路成功率、fallback、延迟等历史聚合；
- admin-server 从多个 gateway 实例拉取进程内 breaker 快照，并按 worst-wins 合并。

当前缺少：线路页自动刷新、Redis 全局并发/TPM 快照、balanced 分数组成、候选过滤原因和请求级路由决策。P3 必须补齐这些能力。

### 5.2 指标与结构化日志

至少记录以下信号：

| 信号 | 用途 |
|------|------|
| `routing_balance_total{mode,result}` | balanced/fixed 调度结果和降级原因 |
| `routing_balance_candidate_count` | 当前线路经过过滤后的候选数 |
| `routing_balance_pool_size` | 当前线路显式绑定的渠道数 |
| `routing_balance_selected_total{route,channel}` | 分析线路池内实际选择分布 |
| `routing_balance_fallback_total{route,reason}` | 线路内 fallback 次数与原因 |
| `routing_balance_load_skew` | 观察线路池内负载偏斜 |
| `routing_balance_capacity_read_total{result}` | Redis 容量读取成功、失败和退化 |
| `routing_margin_guard_total{result}` | 负毛利配置拒绝和运行时摘除 |
| `routing_sticky_total{result}` | sticky 命中、失效和清理情况 |
| `routing_trace_write_total{result}` | 路由决策追踪写入成功、失败和采样跳过 |

结构化日志至少包含 `request_id/route_id/mode/channel_id/pool_size/candidate_count/selected_order/fallback_reason`。日志和指标不得包含 prompt、credential、完整 base URL 或 API Key。

指标标签禁止使用 request ID、模型原始请求体等无界值；route/channel 标签仅使用受控 ID 或低基数标识。

### 5.3 实时运行时快照

新增 admin 聚合接口：

```text
GET /admin/v1/routes/{id}/ops/runtime?model_id=...&protocol=...
```

数据来源与合并规则：

```text
PostgreSQL: 线路池、模型绑定、价格与毛利状态
Redis:      channel-level 全局并发/TPM 使用量
Gateway:    各实例 breaker、错误率、延迟和健康快照
Attempts:   最近 1m/5m 选择比例、fallback 和拒绝统计
```

- Redis 容量是跨线路、跨 gateway 的全局事实；
- gateway 健康快照继续复用现有多实例 fan-out，breaker 状态按 worst-wins 合并，并保留各实例明细；
- capacity score、health factor 和最终 weight 必须由与实际调度共用的 scorer 产出，admin 不得复制一套可能漂移的计算公式；
- 线路详情提供模型/协议选择器；未选择时显示池级状态，选择后显示该模型/协议的硬过滤结果和实际权重；
- 每个数据源携带 `observed_at`，任一关键运行时数据超过 10 秒未更新时标记 `stale`，不得继续显示为正常实时值。

响应至少包含：

```text
route_id / mode / observed_at / stale
pool_size / candidate_count / no_redundancy
channel_id / status / eligible / excluded_reason
concurrency_used / limit / remaining_ratio
tpm_used / limit / remaining_ratio
breaker_state / error_rate / latency / health_factor
capacity_score / final_weight / current_order
selected_1m / selected_5m / fallback_1m / margin_status
instance_snapshots
```

运行时快照是只读观测，不得预占容量、推进 breaker 状态机或改变 sticky。

### 5.4 请求级路由决策追踪

本阶段新增持久化 `routing_decision_traces`（名称实现时可调整），关联 `request_record_id`。每条 trace 记录：

- route、mode、pool size、requested model、protocol 和 operation；
- sticky 输入、是否命中、是否失效以及失效原因；
- 每个线路池渠道的硬过滤结果和排除原因；
- 并发/TPM 剩余比例、capacity score、health factor、最终 weight；
- 最终候选顺序、首选渠道和 fallback chain；
- 容量读取是否退化、毛利 guard 是否触发；
- 决策时间和算法版本。

追踪不得保存 prompt、请求体、credential、完整 base URL 或上游响应正文。

保存策略：

- fallback、无可用渠道、负毛利摘除、容量读取失败、全部容量为零、sticky 失效等异常决策 100% 保存；
- 普通成功请求按可热更新采样率保存，默认 `5%`，使用 request ID 哈希做稳定采样；
- 默认保留 7 天，保留期可配置，清理任务按批次删除；
- trace 写入失败不得改变客户请求结果，但必须记录 `routing_trace_write_total{result="failed"}` 并告警。

新增查询接口：

```text
GET /admin/v1/routes/{id}/ops/decisions
GET /admin/v1/requests/{request_id}/routing-decision
```

### 5.5 Admin 实时交互

线路详情新增“运行时”视图和“最近路由决策”列表：

- 页面可见时每 3 秒轮询运行时快照和最近决策；页面进入后台时暂停轮询，恢复可见后立即刷新；
- 提供手动刷新和最后更新时间；
- 超过 10 秒未获得新快照时显示醒目的“数据陈旧”，不把旧容量伪装成实时状态；
- 显示池大小、有效候选数、无冗余、全部满载、负毛利和容量源异常等线路级状态；
- 渠道行显示并发、TPM、breaker、错误率、延迟、capacity score、health factor、weight、1m/5m 选择比例和排除原因；
- 点击最近决策可查看完整候选评分、sticky 和 fallback 过程，并跳转到对应 request 详情。

第一版采用 React Query 3 秒轮询，不引入 SSE/WebSocket。若以后要求亚秒级刷新，再单独评估推送协议；这不影响本阶段“3 秒刷新、10 秒陈旧”的验收口径。

### 5.6 可观测性必须回答的问题

后台、指标、日志和 routing decision trace 合起来必须能回答：

1. 请求是否只在当前线路绑定渠道中选择；
2. 某条渠道为什么被选中、降权或硬摘除；
3. 当前权重来自并发、TPM、健康还是 sticky；
4. sticky 失效是否因为渠道被移出线路池；
5. fallback 是否发生在线路池内部，以及每次失败原因；
6. 是否存在负毛利候选或价格变更被拒绝；
7. Redis 或某个 gateway 实例的运行时数据是否陈旧或不可用；
8. 当前线路是否具备模型级冗余。

---

## 6. 数据库重建

当前数据库允许重建，本阶段不实现旧 `all/cheapest/stable/random` 数据迁移，也不保留兼容读取或双写逻辑。直接修改目标 schema 并重新初始化数据库：

1. `routes.mode` CHECK 只允许 `balanced/fixed`；
2. 删除 `routes.pool_kind` 及相关 CHECK；
3. 保留 `route_channels` 作为线路唯一渠道来源；
4. 新增 `routing_decision_traces`（最终名称可按实现约定）及 request/route/time 查询索引和保留期清理索引；
5. 删除 SQL、sqlc、service、admin API 和前端中的 `PoolKind/pool_kind/all`；
6. 重建后重新创建线路，并为每条线路显式写入 `route_channels`；
7. 数据库 ID 会变化，重建时同步清理 Redis 中 sticky、限流和并发等旧 ID 状态；
8. 从空库执行完整 schema 初始化和 seed，验证不存在旧 mode 或隐式全量池。

重建后的任何新建或更新线路都必须显式提供 `channel_ids`，不能通过缺省值获得渠道。

---

## 7. 验收

1. 线路绑定 A、B，未绑定 C：该线路所有请求和 fallback 永远不会命中 C；
2. `balanced` 在线路池内至少两条可用渠道时，首轮流量不会长期 100% 集中到同一条渠道；
3. 一条线路池渠道并发/TPM 接近上限时，新请求明显偏向池内其他可用渠道；
4. 容量信号不可用时，仍只在线路池内退化，不回落到全局渠道；
5. sticky 命中时最终渠道保持稳定；sticky 渠道移出线路池后会清理并重新选择；
6. `fixed` 线路只能选择一条渠道，渠道失败时不尝试其他渠道；
7. 空渠道池的线路创建/更新被拒绝，启用线路不会出现空池断供；
8. 线路、模型和渠道成本变化后，负毛利组合被阻止或明确告警，不得静默进入 balanced；
9. `/v1/models` 等模型可见性接口与 API Key 当前线路一致，不向客户展示线路无法实际路由的模型；
10. Admin 线路页每 3 秒刷新运行时容量、健康、权重和最近决策，超过 10 秒的数据明确标记陈旧；
11. fallback、无可用渠道、负毛利、容量读取失败和 sticky 失效都有可审计的 routing decision trace；
12. 后端、admin API、前端和数据库重建测试覆盖新 schema，代码和接口中不存在旧策略或 `pool_kind`。

---

## 8. 明确不做

- 全局渠道负载均衡；
- `pool_kind=all` 自动候选池；
- `cheapest`、`stable`、`random` 线路策略；
- 最便宜成本档、成本相等分桶或按成本自动升舱；
- sticky 命中后主动拆会话摊负载；
- 跨线路 fallback；
- 客户 Key/请求级指定具体渠道；
- 用负载均衡替代熔断、429 冷却或渠道凭据治理；
- 改变 DEC-026 客户售价公式。

---

## 9. 实现拆分

1. **数据与约束**：直接重建目标 schema，mode 收紧为 `balanced/fixed`，删除 `pool_kind`；
2. **route service/admin API**：只接受 `balanced/fixed`，所有线路强制 `channel_ids`；
3. **routing SQL**：删除全量池分支，所有候选必须通过 `route_channels`；
4. **lifecycle**：将成本排序替换为线路池内 balanced 容量/健康度排序，保留 fixed 和 sticky 语义；
5. **容量读取**：复用 admit/ratelimit 只读接口，排序阶段禁止预占；
6. **利润保护**：补充线路池与模型/渠道成本组合校验，以及成本变化后的影响线路检查；
7. **admin 前端**：删除池类型和旧策略选择，只保留模式选择与渠道多选；
8. **模型可见性**：让 `/v1/models` 按当前线路池收敛可见模型；
9. **实时可观测**：扩展 gateway runtime 快照，新增 Admin 线路运行时接口、routing decision trace、3 秒轮询和 10 秒陈旧状态；
10. **指标与测试**：补齐线路边界、balanced 分布、fixed 无 fallback、sticky 清理、数据库重建、毛利保护和实时可观测测试。

上述 10 项全部属于本次 P3 的交付范围，不设“先上线路由、后补商业门禁”的拆分。

---

## 10. 商业上线门禁

### 10.1 毛利保护必须是硬约束

实现必须提供可复用的线路池毛利校验器，并在以下入口执行：

- 创建/更新线路；
- 启用线路；
- 修改线路倍率；
- 修改模型基准价；
- 修改渠道绝对成本、渠道成本倍率或充值倍率。

校验范围是「线路 × 当前线路池渠道 × 该渠道支持的模型 × 所有计价分项」。校验失败时返回可定位到线路、渠道、模型和计价分项的错误，不能只返回一个笼统的“价格无效”。

价格变更必须与影响线路的校验处于同一事务边界，或采用等价的版本化校验，避免价格已经生效但线路仍按旧毛利判断。运行时还要保留最后一道保护：发现已失效的负毛利候选时禁止调度，并产生告警。

### 10.2 balanced 必须使用全局渠道负载

一个渠道可能同时被多个线路绑定，因此容量状态不能按线路分别统计。`balanced` 使用渠道级共享负载口径：

```text
channel global in-flight / TPM usage
  → 当前线路池内候选的容量分数
  → 当前线路池内加权选择
```

线路只决定“哪些渠道可以参与”，不复制或隔离渠道的真实并发/TPM 消耗。容量信号读取、预占和释放必须沿用同一套 channel-level ratelimit/admit 事实，排序阶段不能重复预占。

本次实现必须固定并测试：

- 并发剩余量和 TPM 剩余量的组合口径；
- 健康度对容量分数的影响；
- 容量读取失败时的退化行为；
- 多线路同时竞争同一渠道时不会发生重复超卖。

### 10.3 模型可见性必须跟随线路

对绑定线路的 API Key，模型列表只能返回当前线路池实际可路由的模型：

```text
model visible
⇔ 当前线路池中至少存在一条满足模型、协议、状态、计价、能力和毛利要求的渠道
```

请求 routing 和 `/v1/models` 必须共用相同的线路池过滤事实，不能一个走显式池、另一个走全局模型列表。模型列表、请求失败和 admin 诊断的可用性判断需要有一致的 route-aware 测试。

### 10.4 运维诊断必须可操作

admin 必须能直接看到：

- 线路绑定的渠道及当前启用状态；
- 每个模型在该线路上的可路由/不可路由状态；
- 不可路由原因：未绑定、未绑定模型、协议不匹配、未定价、凭据无效、熔断或负毛利；
- 线路池内各渠道的容量、健康度和近期选择比例；
- 当前渠道变更会影响哪些线路；
- 最近路由决策中每个候选的过滤原因、容量分、健康因子、最终权重、sticky 和 fallback 过程；
- 数据更新时间和陈旧状态。

可以提供“选择当前符合条件的渠道”作为管理操作的批量辅助，但提交结果必须落成明确的 `route_channels` 记录，未来新增渠道不得自动加入已有线路。

线路运行时视图必须按 3 秒轮询、10 秒判定陈旧的口径工作；多 gateway breaker/健康状态必须聚合，容量必须读取 Redis channel-level 全局事实。请求级 routing decision trace 不得延期。

### 10.5 门禁验收

以下任一项失败，都不能将文档状态改为已完成：

1. 任意计价分项出现负毛利时，线路或价格变更无法生效；
2. 同一渠道被多个线路使用时，所有线路看到的是同一份全局容量状态；
3. `/v1/models` 返回的模型在当前线路上实际可请求成功；
4. admin 能定位线路模型不可用和负毛利的具体原因；
5. 所有 fallback、sticky 和重试都没有突破线路池边界；
6. 空库重建后没有旧 mode、`pool_kind`、空的启用线路或隐式全量池；
7. Admin 在线路请求发生后 10 秒内展示新的运行时容量、权重和选择统计；
8. fallback、无可用渠道、负毛利、容量读取失败和 sticky 失效均有完整 routing decision trace；
9. runtime 数据中断超过 10 秒时，Admin 明确显示陈旧而不是旧的正常状态。

P3 的完成定义不是“请求能在几条渠道之间随机分配”，而是上述产品边界、账务边界、容量边界和运维边界同时成立。

---

## 11. 测试计划

### 11.1 测试原则

- 负载均衡随机源必须可注入，单元测试使用固定 seed，不使用偶发通过的概率断言；
- 时间窗口、价格生效时间、熔断和 sticky TTL 使用可控时钟；
- DB 测试使用独立事务或独立测试库，Redis 并发测试使用独立 key 前缀并在结束后清理；
- 路由边界、账务边界和容量边界都要有负向测试，证明“不会选中”与“不会写入”，不能只验证成功路径；
- OpenAI Chat Completions、OpenAI Responses、Anthropic Messages 三个入口必须复用同一组线路边界断言；
- 单元和生命周期测试可以使用 mock，但发布 E2E 必须请求真实上游并取得真实响应、usage 和上游请求标识；
- E2E 不直接修改生产渠道行、生产价格或生产 API Key，而是把真实渠道的 `base_url/credential` 安全复制到独立命名的测试渠道，所有测试配置和记录可按前缀完整清理；
- 真实凭据只能通过环境变量或受控配置读取，禁止写入测试代码、文档、快照、日志和失败输出。

### 11.2 单元测试

#### 线路规则

1. 只接受 `balanced/fixed`，拒绝 `cheapest/stable/random` 和未知 mode；
2. `balanced` 接受一条或多条渠道，拒绝空池；
3. `fixed` 只接受恰好一条渠道；
4. 渠道 ID 去重、非正数和不存在渠道返回可读错误；
5. route DTO 和领域模型中不再出现 `PoolKind`。

#### balanced 评分与顺序

采用表驱动测试覆盖：

| 场景 | 预期 |
|------|------|
| 并发/TPM 都有剩余 | `capacity_score` 取两者较小值 |
| 并发接近满、TPM 充足 | 并发成为瓶颈 |
| TPM 接近满、并发充足 | TPM 成为瓶颈 |
| 单项信号未知 | 使用已知项 |
| 两项信号都未知 | 线路池内均匀随机 |
| 显式不限 | 对应剩余比例为 `1` |
| 错误率或延迟变差 | 权重单调下降 |
| 无健康样本 | 健康因子为中性值 |
| 存在正权重候选 | 零容量候选不作首选、保留最后 fallback |
| 全部容量为零 | 选择压力最低候选进入短等 |
| 不同成本、相同负载 | 成本不改变权重和候选顺序分布 |

固定 seed 下验证加权不放回顺序无重复、包含全部候选。另做大样本统计测试验证等权渠道分布在合理容差内、容量较高渠道命中比例显著更高；统计测试固定 seed 和样本数，避免 flaky。

#### sticky、fixed 与 fallback

1. 有效 sticky 始终置顶，即使不是 balanced 普通首选；
2. sticky 渠道不在线路池或被硬摘除时不进入候选，并触发绑定清理；
3. sticky 渠道容量满时先走现有短等，超时后只在线路池内 fallback；
4. `fixed` 只有一个候选，失败后不产生第二次上游 attempt；
5. 未绑定渠道即使负载和健康度最佳也永远不会进入 plan。

#### 毛利校验

逐项覆盖 uncached input、output、reasoning output、cache read、cache write 5m/30m/1h：

1. 倍率路径：`model_price × channel_cost_multiplier × recharge_factor`；
2. 绝对成本覆盖优先于倍率路径；
3. 售价等于成本允许，售价小于成本拒绝；
4. 可空分项按当前计费回退规则比较，不能因 NULL 绕过校验；
5. 长上下文跨阈值后的售价和成本仍满足不亏；
6. 错误返回包含 `route_id/channel_id/model_id/component`；
7. 运行时负毛利候选被硬摘除，而不是降权或放到 fallback 末尾。

### 11.3 数据库与 sqlc 集成测试

从空数据库执行完整 schema 初始化，并验证：

1. `routes` 不存在 `pool_kind` 列，mode CHECK 只允许 `balanced/fixed`；
2. `route_channels` 是候选范围的唯一来源，未绑定渠道无法被 `FindRouteCandidates` 返回；
3. 同线路、同模型下正确叠加协议、状态、凭据、价格和用户模型策略过滤；
4. 线路、模型价格、绝对成本、成本倍率和充值倍率变更的毛利校验与写入处于同一事务，失败后数据完整回滚；
5. 直接归档渠道或服务商会让启用线路空池时返回 conflict，渠道、线路池和归档状态都不发生部分写入；
6. 指定替代渠道的归档操作原子完成，事务提交后启用线路仍有候选；
7. route-aware 模型查询采用并集口径，并返回每个模型的可用候选数；
8. sqlc 重新生成后不存在 `PoolKind` 字段和旧 mode 注释；
9. 数据库重建后清理 Redis 旧 ID 状态的运维脚本或启动流程可重复执行。

### 11.4 Service 与 Admin API 测试

1. route create/update/get/list 的请求和响应只包含 `mode` 与显式 `channel_ids/channels`；
2. 空池、fixed 多渠道、旧 mode、负毛利和不存在渠道分别返回稳定的 4xx 错误码与字段；
3. 修改模型价、渠道成本、成本倍率、充值倍率时，能列出并校验所有受影响线路；
4. 渠道/服务商归档接口返回受影响线路，支持“替换并归档”或提示先停用线路；
5. 线路详情诊断覆盖未绑定模型、协议不匹配、未定价、凭据无效、熔断、负毛利和无冗余；
6. 批量辅助选择只写入当次明确选择的 `route_channels`，之后新建渠道不会自动加入；
7. 指标 DTO 和日志字段包含 route、pool、candidate、selected channel 与跳过原因。

### 11.5 Gateway 生命周期测试

本节是确定性的生命周期集成测试，可以使用 A、B、C 三条 mock upstream 渠道，其中线路只绑定 A、B；它不能替代 11.10 的真实上游 E2E：

1. 正常请求只命中 A/B，C 在所有首选、fallback、sticky 和重试场景中都不会出现；
2. A 返回 5xx、429、超时或凭据失效时，只 fallback 到 B；
3. A/B 都失败时返回无可用渠道，不请求 C；
4. A 负载升高后新 sticky miss 流量偏向 B；已有有效 sticky 仍保持 A；
5. A 从线路池移除后 sticky 清理，新请求选择 B；
6. `fixed=A` 时 A 失败只产生一次 attempt；
7. 负毛利 A 被硬摘除后只使用 B；A/B 都负毛利时不上游调用；
8. 非流式、流式、客户端取消、部分流和 fallback 后结算均正确释放容量；
9. OpenAI Chat Completions、OpenAI Responses、Anthropic Messages 三个入口行为一致。

### 11.6 Redis 并发与负载测试

使用真实 Redis 或等价的原子脚本集成环境，不能只靠内存 fake：

1. 两条不同线路同时绑定同一渠道，并发请求共享同一个 channel concurrency key；
2. 总在途数永远不超过渠道限制，不因 route ID 不同而各自获得一份额度；
3. TPM 预占、结算修正和失败释放在多线路并发下保持守恒；
4. 成功、上游失败、超时、客户端取消和进程收尾路径都恰好释放一次；
5. 容量快照并发读取不产生预占，真正 admit 使用原子操作解决竞态；
6. Redis 不可用时按既定 fail-open/fail-closed 规则退化，但线路边界不变；
7. 对 ratelimit、lifecycle 和 routing 关键包执行 race test。

负载测试至少包含等容量 50/50、不同容量、单渠道过热、多线路共享渠道四组场景，记录命中分布、拒绝率、短等次数、fallback 率、P95/P99 和是否超限。分布验收使用区间而不是要求精确比例。

### 11.7 模型可见性测试

1. 线路池 A 支持 M1、B 支持 M2 时，模型列表返回 M1/M2 并集；
2. M1 只有一条候选时仍返回，并在 admin 标记“无冗余”；
3. 唯一候选停用、失去价格、凭据失效或变成负毛利后，M1 不再对该线路可见；
4. 同一用户的不同 API Key 绑定不同线路时，`/v1/models` 返回不同结果；
5. 模型列表可见后，使用同一 API Key 发起请求至少存在一条可执行候选；
6. 模型列表与 routing 共用查询事实的契约测试，防止以后再次漂移。

### 11.8 Admin 前端测试

`unio-admin` 当前没有自动化测试脚本，本阶段引入组件测试和浏览器 E2E：

1. 线路表单只展示 `balanced/fixed`，不展示池类型和旧策略；
2. balanced 可选择一条或多条渠道，fixed 切换后只保留一条，空池不能提交；
3. 负毛利错误能定位并展示具体渠道、模型和计价分项；
4. 线路详情正确展示模型可用性、无冗余、容量、健康和不可用原因；
5. 归档会造成空池时展示受影响线路，并要求替换渠道或先停用；
6. 批量选择提交后刷新页面，展示的渠道集合与后端 `route_channels` 一致；
7. 页面可见时每 3 秒刷新运行时和最近决策，进入后台后暂停，恢复可见后立即刷新；
8. 快照超过 10 秒时显示“数据陈旧”，新快照到达后自动恢复；
9. 最近决策可以展开候选分数、过滤原因、sticky 和 fallback，并跳转 request 详情；
10. 桌面和移动视口下表单、实时表格、诊断列表和错误信息不遮挡、不溢出。

组件测试建议使用 Vitest + Testing Library；关键管理流程使用 Playwright，对真实本地 admin API 执行创建线路、修改池、诊断和归档护栏流程。

### 11.9 实时可观测性测试

#### 后端与聚合

1. Redis 容量快照返回 channel-level 全局并发/TPM，读取不产生预占；
2. 两个 gateway 实例返回不同 breaker 状态时按 worst-wins 合并，同时保留实例明细；
3. runtime endpoint 的 capacity score、health factor 和 weight 与真实调度 scorer 对同一输入完全一致；
4. model/protocol 过滤后 candidate count、excluded reason 和 current order 正确；
5. 任一关键数据源超过 10 秒时 `stale=true`，恢复新快照后变回 false；
6. runtime endpoint 不推进 breaker、不刷新 sticky、不修改 Redis 容量；
7. 最近 1m/5m 选择、fallback 和拒绝统计与 request/attempt 事实一致。

#### Routing decision trace

1. 普通成功请求按 request ID 哈希稳定采样，同一 request 重试不会得到不同采样结果；
2. fallback、无可用渠道、负毛利、容量读取失败、全部容量为零和 sticky 失效 100% 写入；
3. trace 完整记录候选过滤、容量分、健康因子、weight、最终顺序和 fallback chain；
4. trace 不包含 credential、API Key、完整 base URL、prompt、请求体或响应正文；
5. trace 写入失败不改变客户请求结果，同时增加失败指标并产生日志；
6. 7 天默认保留和批量清理可使用可控时钟验证，清理不会删除 request/attempt 账务事实；
7. request 详情和线路最近决策接口只能通过 admin 鉴权访问。

#### 前端实时行为

使用 fake timer 和 Playwright 验证 3 秒轮询、页面隐藏暂停、恢复立即刷新、10 秒陈旧提示、手动刷新、模型/协议切换以及最近决策详情。测试同时断言轮询不会重复创建请求、不会因旧响应后到覆盖较新的 `observed_at`。

### 11.10 真实上游端到端场景

真实上游 E2E 是发布硬门禁，不能用纯 mock、固定响应 fixture 或仅验证本地路由计划代替。测试至少准备两条能够真实完成同一模型请求的上游渠道；需要验证未绑定隔离时再准备第三条真实渠道。

#### 数据隔离与凭据安全

1. 从运营已配置的真实渠道安全读取 `base_url/credential`，复制成独立命名的 E2E provider/channel；不修改原渠道的状态、价格、限流、线路绑定或凭据；
2. 为 E2E 单独创建 model、model price、channel model、channel cost、route、user、API Key 和余额，统一使用 `e2e-routing-p3-<run-id>` 前缀；
3. 凭据只在进程内传递，测试输出只允许打印测试 channel ID、provider slug 和脱敏 request ID；
4. 测试设置低于真实上游限制的本地并发/TPM 上限，限制请求数量和 max tokens，避免为了制造 429 主动消耗或打满真实上游额度；
5. 每次运行结束按 run ID 清理全部测试数据，失败时保留可执行的清理命令，但不得输出凭据。

#### 故障注入规则

真实成功路径必须直达真实上游。需要稳定验证 5xx、429、慢响应、超时或断流时，允许为 E2E 测试渠道配置透明故障代理：正常模式完整转发真实请求和响应；故障模式在转发前或响应途中注入指定错误。代理只用于可控故障，不得把本地固定成功响应伪装成真实上游成功。

至少执行以下场景：

1. 创建绑定真实渠道 A/B 的 balanced 线路，通过 `/v1/chat/completions`、`/v1/responses`、`/v1/messages` 中适用于对应渠道协议的入口取得真实上游响应和真实 usage；
2. 对同协议、同模型的真实 A/B 发送受控样本请求，验证 balanced 分布、request attempts 和最终渠道均只来自 A/B；
3. 将 A 的本地渠道容量压低或通过透明代理注入满载、5xx、429、慢响应、超时和 breaker open，验证只 fallback 到真实渠道 B，并由 B 返回真实成功响应；
4. 创建未绑定但真实可用且健康度最佳的 C，验证它从不出现在 request attempts；
5. 使用真实多轮请求建立 sticky 会话，验证正常保持、移池清理和重新绑定；
6. 创建指向真实渠道 A 的 fixed 线路，故障注入后验证无 fallback；
7. 尝试提交会导致负毛利的线路或价格变更，验证 API 拒绝、DB 回滚，随后真实请求的售价和成本快照不漂移；
8. 两条线路并发使用同一真实渠道，在低于上游真实限制的本地限制下验证全局容量不超限；
9. 归档测试渠道/服务商导致空池时被阻止，指定真实替代渠道后归档成功；
10. 对照真实上游响应、usage/request ID、request record、attempt chain、price/cost snapshot、指标和 admin 诊断，确认整条链路事实一致；
11. 请求完成后 10 秒内，Admin 运行时视图展示新的容量、权重和选择统计，最近决策可定位到同一 request ID；
12. 制造 fallback、无可用渠道、容量读取失败和 sticky 失效，验证异常 trace 100% 可见且内容不含敏感信息；
13. 测试结束按命名前缀清理全部数据，不触碰原真实渠道配置和非测试记录。

若某个协议没有至少两条能够服务同一模型的真实渠道，该协议的 balanced fallback E2E 不能标记通过；必须补齐真实渠道，不能用 mock 降级验收。

### 11.11 完成命令与发布门禁

实现完成后至少执行：

```bash
gofmt -w <changed-go-files>
go vet ./...
go build ./...
go test ./...
go test -race ./internal/core/routing/... ./internal/platform/ratelimit/... ./internal/service/gateway/lifecycle/...

cd ../unio-admin
bun run lint
bun run build
bun run test
bun run test:e2e
```

DB/Redis 集成测试使用独立环境变量运行，不允许因未设置依赖而在最终发布验收中全部 Skip。还必须完成一次空库重建、一次多线路共享渠道负载测试、一次实时 Admin 可观测验收和一次真实上游端到端全场景测试；纯 mock E2E 不计入发布门禁。

只有本节测试全部通过、商业上线门禁无豁免，才能把文档状态从“待实现”改为“已完成”。
