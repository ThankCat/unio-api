# P4：Provider 单故障域、Redis 全局熔断与客观路由观测改造计划

> 状态：**设计定稿 / 待实现**
>
> 日期：2026-07-19
>
> 前置基线：[ROUTING_P3_LOAD_SPREAD.md](./ROUTING_P3_LOAD_SPREAD.md)
>
> P3 实施记录：[ROUTING_P3_IMPLEMENTATION_LOG.md](./ROUTING_P3_IMPLEMENTATION_LOG.md)

P4 不改变 P3 已确定的线路边界、`balanced/fixed` 模式、毛利门禁、sticky 和线路内 fallback。P4 专门修正 P3 运行后暴露的四类问题：

1. 进程内渠道熔断在多 Gateway 下不是同一事实，低流量时又可能长期达不到比例熔断样本；
2. `channels.base_url` 让同一上游故障域分散在多条渠道上，无法可靠执行 Provider 级保护；
3. 当前 balanced 使用完整上游调用耗时参与权重，流式长输出会被误判为响应慢；
4. Admin 的“健康/降级/不健康”是主观分桶且不影响路由，容易与实时熔断、凭据状态和主动检测混淆。

本计划把熔断事实统一到 Redis，以 Provider 作为单 BaseURL 故障域；balanced 只使用容量、近期渠道错误率和首响延迟计算最终分流权重；Admin 删除主观健康配置和标签，只展示可核验的原始事实及最终权重。

---

## 1. 目的

### 1.1 商业目的

本次改造必须达到以下结果：

1. 同一渠道或 Provider 在任意 Gateway 上触发熔断后，所有 Gateway 立即遵守同一状态；
2. 低流量渠道连续故障时不依赖 20 个样本才能被保护；
3. 同一 BaseURL 的公共故障能够一次摘除整个 Provider，避免在同一故障域内依次消耗多个渠道；
4. 长流式响应不会因为总生成时间长而被降低分流权重；
5. 运营后台不再用主观“健康”标签代替错误率、首响、熔断和检测结果；
6. 客户端永远看不到 channel、Provider、BaseURL、候选数、熔断状态或内部 `failure.Code`；
7. 路由、限流、计费、审计和毛利边界继续保持 P3 已验收行为。

### 1.2 工程目的

1. Redis 是渠道和 Provider 熔断的唯一运行时事实源，不再保留进程内熔断状态；
2. 熔断状态迁移使用 Lua 原子执行，Gateway 实例时钟不参与状态判断；
3. Admin 直接读取 Redis 全局快照，不再 fan-out 各 Gateway 的进程内快照并做 worst-wins 合并；
4. Provider BaseURL 成为数据库业务事实，Channel 不再重复保存；
5. 首响延迟和总耗时在数据、接口和页面上明确分开；
6. routing decision trace 能解释最终权重，但不保存敏感 URL、凭据或上游正文。

### 1.3 不在本次范围

1. 不取消线路显式渠道池，不允许 fallback 到未绑定渠道或其他线路；
2. 不恢复 `cheapest/stable/random` 等旧策略；
3. 不让成本参与 balanced 排序；
4. 不取消主动渠道检测和 `credential_valid` 凭据闸门；
5. 不引入跨 Provider 自动扩池；
6. 不承诺所有上游故障都能返回成功结果；无法服务时仍返回协议安全的通用 `503`；
7. 不把 Redis 作为请求、attempt、用量、金额或账务最终事实源；
8. 不在本次引入 Redis Cluster，多 key Lua 以当前单 Redis 部署为前提；未来启用 Cluster 前必须重新设计 key slot。

---

## 2. 已确认决议

以下决议已由用户确认，实施时不再保留并行旧语义。

### 2.1 Provider 与 BaseURL

采用单故障域模型：

```text
一个 Provider = 一个 API Root = 一个上游故障域
```

- `providers` 持有唯一 `base_url`；
- `channels.base_url` 删除；
- Channel 继续持有协议、adapter、凭据、模型绑定、价格、限流和并发等账号级事实；
- 同一供应商若提供多个独立 API Root，必须创建多个 Provider；
- Provider 的不同协议只有在共享同一 API Root 时才能放在同一 Provider 下；
- Adapter 根据 `protocol + adapter_key + operation` 拼接最终路径。

### 2.2 Redis 全局熔断

- 删除 `ChannelCircuitBreaker` 的进程内状态实现；
- Channel 和 Provider 熔断全部使用 Redis；
- Redis 不可用时熔断模块 fail-open，不扩大线路池、不绕过认证/计费/毛利/凭据/状态等硬门禁；
- Redis 故障必须产生指标、结构化日志和 Admin 数据源异常；
- half-open 探测使用 Redis 全局租约，同一作用域同一时刻只允许一个探测。

### 2.3 Provider 级故障归因

Provider 级故障采用分级归因：

| 错误 | Channel 熔断 | Provider 熔断 | 说明 |
|------|--------------|----------------|------|
| DNS/TLS/连接拒绝/EOF/网络断开 | 是 | 是 | 与账号无关，属于公共 endpoint 故障 |
| timeout | 是 | 是 | 公共 endpoint 或服务慢故障 |
| HTTP 502/503/504 | 是 | 是 | 明确公共服务不可用信号 |
| HTTP 500 | 是 | 条件计入 | 需短窗内至少 2 条不同渠道重复出现，避免单模型误伤 Provider |
| HTTP 429 | 比例窗口计入，不进快速连续失败 | 否 | 由渠道 429 硬冷却即时保护 |
| HTTP 403 | 是 | 否 | 渠道账号或模型权限问题 |
| HTTP 401 | 否 | 否 | 连续 401 凭据闸门专管 |
| HTTP 400/404/422 | 否 | 否 | 请求、模型或能力问题 |
| 客户端取消 | 否 | 否 | 非上游责任 |
| 2xx 协议解析失败 | Channel 计入 | Provider 否 | 先按 adapter/protocol/channel 归因，不扩大公共故障域 |

单次 HTTP 500 不得立即熔断整个 Provider。

### 2.4 双触发熔断

Channel 和 Provider 使用同一状态机框架，触发条件包含快速触发和比例触发：

```text
快速触发：10 秒内连续 3 次可归因故障
比例触发：30 秒窗口内至少 20 次，故障率 >= 50%
```

规则：

1. 成功一次即清空连续失败计数；
2. 429 不增加快速连续失败，但可以进入 Channel 比例窗口；
3. Provider 的 500 只有满足跨渠道证据后才进入 Provider 计数；
4. half-open 同一时刻只放一个探测，连续成功 2 次才恢复 closed；
5. half-open 任一次失败立即重新 open；
6. 重复 open 使用 `15s -> 30s -> 60s -> 120s -> 300s` 退避；
7. closed 稳定一个完整窗口后重置退避级别；
8. open 是硬摘除，首响/错误权重不会把 open 候选重新带回。

### 2.5 首响作为分流性能信号

用于 balanced 权重的延迟改为首响延迟：

- 流式：从上游调用开始到第一个实际向客户端发送的 SSE chunk；
- 非流式：从上游调用开始到收到上游首个响应字节；
- 首响前失败：记录从调用开始到失败发生的时长，但该样本按失败处理，不进入成功首响 EWMA；
- 完整总耗时继续落库和展示，只作为性能副指标，不参与权重；
- 现有 `response_started_at` 作为持久审计事实，语义统一为“当前 attempt 观测到上游响应开始的时刻”；流式取第一个实际发送 chunk，非流式取首个上游响应字节；
- 非流式需要补充首字节观测，不能继续用 `Invoke` 返回时刻代替。

### 2.6 删除 Admin 主观健康分桶

删除：

- `admin_backend.channel_health_thresholds`；
- `healthy/degraded/unhealthy/no_data` 渠道分桶；
- Channel、Provider、Model、Dashboard 中由该分桶生成的“健康/降级/不健康”标签；
- 前端本地重复实现的 `healthBucketOf`；
- Admin API 中只服务于该分桶的 `health/health_bucket` 字段。

保留：

- attempt 成功率、失败次数、超时次数和最近错误；
- 首响延迟和完整总耗时；
- Channel/Provider 熔断状态及剩余 open 时间；
- 主动检测结果；
- `credential_valid`；
- 容量剩余、最终权重和实际分流占比。

后台不再给 balanced 的中间乘数起面向用户的新名字。运营只看客观输入和“当前权重”。

### 2.7 客户端错误边界

内部继续使用并保存：

- `routing_no_available_channel`；
- 完整排除原因；
- Channel/Provider breaker 状态；
- 候选池大小和 fallback 链。

外部统一映射为安全协议错误：

OpenAI Chat Completions / Responses：

```json
{
  "error": {
    "type": "api_error",
    "code": "service_unavailable",
    "message": "Service temporarily unavailable. Please retry later."
  }
}
```

Anthropic Messages：

```json
{
  "type": "error",
  "error": {
    "type": "api_error",
    "message": "Service temporarily unavailable. Please retry later."
  }
}
```

- HTTP 状态为 `503`；
- 只在最近恢复时间明确时返回 `Retry-After`，秒数向上取整并限制在 `1..300`；
- 返回 Unio `request_id` 响应头用于报障；
- 响应体和 header 不得出现 Channel、Provider、BaseURL、候选数、熔断状态或内部错误码；
- 真正不存在或无权限访问的模型仍按协议返回 `model_not_found`，不能把所有 404 都改成 503。

---

## 3. 目标架构

```mermaid
flowchart LR
    C["Client"] --> G1["Gateway A"]
    C --> G2["Gateway B"]
    G1 --> R["Redis global routing state"]
    G2 --> R
    G1 --> DB["PostgreSQL routing and audit facts"]
    G2 --> DB
    R --> CB["Channel breaker"]
    R --> PB["Provider breaker"]
    R --> CAP["Concurrency and TPM"]
    G1 --> P1["Provider A / BaseURL A"]
    G2 --> P1
    G1 --> P2["Provider B / BaseURL B"]
    G2 --> P2
    A["Admin API"] --> DB
    A --> R
```

运行时顺序：

```text
PostgreSQL 线路显式池与硬门禁
  -> Redis 批量读取 Provider/Channel breaker + 容量
  -> 排除 open / half-open busy
  -> capacity_score + error_rate + response_start_ewma 计算最终权重
  -> weighted without replacement 生成线路内 fallback 顺序
  -> attempt 前 Redis 原子准入：Provider + Channel breaker + Channel concurrency
  -> 调用真实上游
  -> Redis 原子记录结果和状态迁移
  -> PostgreSQL 持久化 request/attempt/usage/ledger/trace
```

Redis 只保存短期运行态；PostgreSQL 继续保存可审计事实。

---

## 4. 数据模型与迁移

当前数据库允许重建，本阶段直接修改源 migration，不保留旧 schema 兼容层。

### 4.1 `providers`

修改 `migrations/000007_providers.up.sql`：

```sql
base_url text NOT NULL
```

约束：

1. `base_url <> ''`；
2. 仅允许 `http/https`；
3. 不允许 userinfo、query、fragment；
4. 服务层统一规范化 scheme/host、默认端口和尾斜杠；path 大小写保持原样；
5. 规范化后的 `base_url` 全局唯一，避免同一故障域被拆成多个 Provider 绕过全局熔断；
6. Provider 更新 BaseURL 时必须清除该 Provider 的旧 Redis breaker 状态，并在 Admin 留结构化审计日志；
7. Provider 已有历史 request/attempt 时仍允许修改 BaseURL，因为历史 attempt 不依赖实时 URL 复算；routing trace 不保存完整 URL。

目标字段：

```text
providers(id, slug, name, base_url, status, archived_at, created_at, updated_at)
```

### 4.2 `channels`

修改 `migrations/000008_channels.up.sql`：

- 删除 `base_url`；
- 删除相关 CHECK、SQL 返回列、sqlc 字段和 Admin DTO；
- Channel 主动检测和 Gateway runtime 均通过 `provider_id -> providers.base_url` 获取地址。

目标职责：

```text
Channel = Provider 下的一组协议/adapter/凭据/模型/价格/限流事实
Provider = 唯一 API Root 与公共故障域
```

### 4.3 URL 拼接契约

Provider BaseURL 是 adapter root，不包含由标准 adapter 固定追加的 operation 路径。使用结构化 URL API 拼接，不允许继续散落 `strings.TrimRight(base, "/") + path`。

基线 operation：

| 协议/operation | 标准路径 |
|----------------|----------|
| OpenAI Chat Completions | `/v1/chat/completions` |
| OpenAI Responses | `/v1/responses` |
| OpenAI Responses Compact | `/v1/responses/compact` |
| Anthropic Messages | `/v1/messages` |

Provider-specific adapter 可以定义自己的相对前缀，但仍只能从 Provider BaseURL 派生。若同一供应商的两个协议必须使用不同 root，应创建两个 Provider。

### 4.4 请求与 attempt 时间事实

`request_records.response_started_at` 和 `request_attempts.response_started_at` 已存在，不新增重复字段。

需要统一写入规则：

1. 流式为第一个非 `SuppressEmit` chunk；
2. 非流式为首个上游响应字节；
3. fallback 前失败且未产生响应时保持 NULL；
4. request 级时间取最终对客户产生响应的 attempt；
5. 总耗时继续使用 `completed_at - started_at`；
6. 首响使用 `response_started_at - started_at`；
7. Channel/Provider 历史聚合分别按 attempt/request 事实计算，不读取 Redis 回填历史。

### 4.5 `app_settings`

删除定义和种子：

```text
admin_backend.channel_health_thresholds
```

替换现有 `gateway.circuit_breaker` 值形状，目标配置：

```json
{
  "enabled": true,
  "store_fail_open": true,
  "window_ms": 30000,
  "min_requests": 20,
  "failure_ratio": 0.5,
  "consecutive_failures": 3,
  "consecutive_window_ms": 10000,
  "half_open_successes": 2,
  "open_durations_ms": [15000, 30000, 60000, 120000, 300000],
  "provider_500_distinct_channels": 2,
  "response_start_target_ms": 2000,
  "response_start_weight": 0.35,
  "minimum_routing_factor": 0.05
}
```

所有字段必须严格解码、拒绝未知字段并支持热更新。改变配置不清除当前 Redis 状态；提供独立的管理员“清除熔断状态”动作，不把配置保存与状态清理隐式绑定。

### 4.6 sqlc 和查询

至少涉及：

- `sql/queries/admin/provider.sql`；
- `sql/queries/admin/channel.sql`；
- `sql/queries/api/channel_models.sql`；
- provider/channel archive replacement 查询；
- channel test 查询；
- route runtime pool 查询；
- provider/channel/model/dashboard 运维聚合。

`FindRouteCandidates` 必须选择 `p.base_url`，Go 侧构造 `channel.Runtime.BaseURL` 时来源只能是 Provider。

修改后执行 `sqlc generate`，并用 diff 检查所有生成结构中不再存在 `Channel.BaseUrl`。

---

## 5. Redis 全局熔断设计

### 5.1 Key

使用版本化 namespace，避免与 P3 进程内状态或未来状态结构冲突：

```text
<namespace>:breaker:v2:channel:<channel_id>
<namespace>:breaker:v2:provider:<provider_id>
```

状态不得包含 URL、credential、model、prompt 或上游正文。

Hash 字段建议：

```text
state                    closed|open|half_open
window_started_at_ms
successes
failures
consecutive_failures
last_failure_at_ms
open_until_ms
open_level
half_open_successes
half_open_lease_id
half_open_lease_until_ms
response_start_ewma_ms   Channel only
response_start_samples   Channel only
last_transition_at_ms
last_failure_category
```

Provider HTTP 500 跨渠道证据使用同一 Provider key 下的短期 distinct channel 集合，或单独版本化短 TTL key；实现必须有硬上限，不能随 channel 数无界增长。

### 5.2 Redis 时间与 TTL

- Lua 使用 Redis `TIME`，不信任 Gateway 本机时钟；
- closed 状态 TTL 至少覆盖窗口和稳定重置时间；
- open/half-open TTL 至少覆盖最大 `open_duration + lease`；
- 无样本且 closed 的 key 可自然过期；
- Provider/Channel 归档后由管理操作 best-effort 删除对应 key；遗漏也必须因 ID 不再入选而无业务影响。

### 5.3 原子操作

实现最少四个能力：

1. `SnapshotMany(providerIDs, channelIDs)`：候选准备时批量读取，不推进状态；
2. `AcquireAttempt(providerID, channelID, leaseID)`：原子检查 Provider/Channel 状态、竞争 half-open 探测并申请渠道并发名额；
3. `RecordResult(providerID, channelID, outcome)`：原子更新窗口、连续失败、EWMA、退避和状态迁移；
4. `Reset(scope, id)`：Admin 显式手动复位，写结构化日志。

`AcquireAttempt` 必须避免 TOCTOU：候选准备时 closed 不代表真正 attempt 前仍 closed。竞争失败不创建 request attempt，只在 routing trace 和 skip metric 中记录。

若当前并发 limiter 无法安全与 breaker 合并为一个 Lua，第一版允许两个有序原子操作，但必须处理补偿：breaker 获得 half-open lease 后并发申请失败时立即释放 lease。最终验收必须证明无永久 half-open busy。

### 5.4 状态迁移

```text
closed
  -> fast trigger or ratio trigger
open(level=n, until=redis_time+duration[n])
  -> until reached
half_open(global single lease)
  -> 2 sequential successes: closed and reset counters
  -> any attributable failure: open(level=min(n+1,max))
```

非渠道责任结果不增加失败。若该结果占用了 half-open lease，必须释放 lease但不得把它视为恢复成功；下一请求可以继续探测。

### 5.5 Redis 故障

已确认 fail-open：

1. breaker snapshot 读取失败时不因 breaker 摘除候选；
2. 仍执行 PostgreSQL 硬过滤、毛利、凭据、能力、线路边界；
3. 并发/TPM 使用各自既有故障策略，不被 breaker fail-open 隐式改变；
4. `RecordResult` 失败不影响已经完成的客户响应和账务；
5. 每次退化写有界日志，避免 Redis 故障时日志风暴；
6. Prometheus 和 Admin 明确显示 `breaker_store_unavailable`；
7. routing trace 标记 `breaker_store_degraded=true`。

---

## 6. Balanced 权重

P3 的容量计算保留：

```text
capacity_score = min(concurrency_remaining, tpm_remaining)
```

删除用户可见的 `health_factor`。内部直接根据可核验输入计算：

```text
error_factor = 1 - clamp(error_rate, 0, 1)
latency_penalty = response_start_ewma / (response_start_ewma + 2s)
routing_factor = max(0.05, error_factor * (1 - 0.35 * latency_penalty))
final_weight = capacity_score * routing_factor
```

规则：

1. 无错误样本时 `error_rate=0`；
2. 无首响样本时不施加延迟惩罚；
3. open/half-open busy 在评分前硬摘除；
4. half-open 探测不参与普通 weighted random，由全局 lease 单独控制；
5. Redis breaker 全部未知时只按容量分流并标记退化；
6. 总耗时、输出 token 数和 TPS 不进入最终权重；
7. sticky 仍在候选可用后置顶，不突破 Provider/Channel open；
8. fixed 不执行 weighted random，但仍遵守 Provider/Channel breaker 和准入；
9. routing trace 保存 `capacity_score/error_rate/response_start_ewma_ms/final_weight`，不保存中间命名因子。

---

## 7. Gateway 改造

### 7.1 Provider BaseURL 接线

1. routing SQL 从 Provider 读取 BaseURL；
2. `channel.Runtime.BaseURL` 继续作为 adapter 的纯运行时输入，来源改为 Provider；
3. Channel service/API 删除 BaseURL 校验与写入；
4. Provider service/API 增加 BaseURL 创建、更新、规范化和唯一冲突处理；
5. channel test 查询 Provider 并使用 Provider BaseURL；
6. adapter URL 构建统一使用结构化 helper，覆盖已有 OpenAI、Responses、Compact、Anthropic 和 provider-specific adapter；
7. adapter 仍不查数据库、不读 Redis。

### 7.2 首响观测

非流式：

1. 在上游 HTTP transport 使用 `httptrace.ClientTrace.GotFirstResponseByte` 捕获首字节；
2. 首字节只记录一次；
3. `Invoke` 完成后将时间事实交给 lifecycle；
4. attempt/request `response_started_at` 使用该时间；
5. 若 transport/adapter 不支持首字节事实，保持 NULL 并标记 `response_start_unknown`，不得回退使用完整耗时伪装。

流式：

1. 继续以第一个非 `SuppressEmit` chunk 为客户首响；
2. 在 chunk emit 时立即写 `response_started_at`；
3. stream 完成后 Redis 只记录 `response_started_at - upstream_started_at`；
4. 长尾总耗时继续进入 PostgreSQL/Prometheus性能指标，不进入权重。

抽出协议无关的 attempt timing 事实，OpenAI Chat、Responses 直传/桥接和 Anthropic 必须复用，不能各自复制计时规则。

### 7.3 熔断接线

替换：

- `lifecycle.ChannelCircuitBreaker`；
- Gateway 内存 snapshot；
- `/internal/v1/circuit-breaker`；
- Admin `gatewayruntime.Client` fan-out 和实例 worst-wins。

新增协议无关接口：

```text
BreakerStore.SnapshotMany
BreakerStore.AcquireAttempt
BreakerStore.RecordResult
BreakerStore.Reset
```

三条协议执行面必须共享同一实现：

- OpenAI Chat Completions；
- OpenAI Responses，包括直传和 bridge；
- Anthropic Messages；
- 流式和非流式。

每个真正放行的 half-open lease 都必须在成功、失败、取消、adapter 解析失败、attempt 创建失败和并发失败路径收口。

### 7.4 错误归因

扩展稳定上游分类时不得依赖字符串匹配上游 body：

- HTTP 状态以 status code 分类；
- 网络错误通过 `errors.Is/As` 和 transport 类型分类；
- EOF、unexpected EOF、connection reset 归 `server_error`；
- 2xx 协议不合法保留明确 protocol 类别；
- Provider 500 distinct-channel 证据只使用 channel ID 和 Redis 时间。

### 7.5 安全外部错误

统一修改 OpenAI Chat、Responses 和 Anthropic handler：

- `routing.ErrNoAvailableChannel` -> 通用 `503 service_unavailable`；
- 不回显请求模型名，避免把内部可售/可路由状态作为错误差异泄露；
- stream 首字节前可正常写协议错误 envelope；
- stream 已经开始后不得再写第二个 HTTP envelope，沿用现有流中断/partial settlement 边界；
- request record 保存安全 message，内部 detail 仍只在 Admin `include_internal=true` 返回。

### 7.6 生命周期不变量

1. breaker skip 不创建 attempt；
2. 已调用上游必须创建并终结 attempt；
3. fallback 永远限制在初始线路池；
4. Provider open 同时影响引用该 Provider 的所有线路；
5. Redis breaker 写失败不回滚已经完成的 settlement；
6. 失败请求不扣客户费用，除已有 partial/bill-on-disconnect 风险规则明确要求；
7. 成功请求 usage、cost、price、ledger 事实不因 P4 变化；
8. Channel/Provider breaker 不参与毛利判断。

---

## 8. Admin Backend 改造

### 8.1 Provider 管理

Provider DTO 增加 `base_url`：

```json
{
  "name": "StarAPI",
  "slug": "starapi",
  "base_url": "https://open.codex521.cc",
  "status": "enabled"
}
```

规则：

- 创建和更新都执行 URL 规范化；
- 重复故障域返回 409；
- 更新后立即影响下一次 routing；
- Provider 详情展示 BaseURL，但不得在公开 Gateway 错误或客户侧 API 返回；
- Provider 归档继续遵守线路空池/替代渠道事务护栏。

### 8.2 Channel 管理

- Channel 创建/编辑 DTO 删除 `base_url`；
- Channel 详情从 Provider 关系只读展示“上游地址”，不提供 Channel 覆盖；
- Channel 列表保留 provider 名、协议、adapter、credential 状态、限流和检测；
- 主动检测结果文案从“渠道健康”改为“最近检测”；
- 401/403 检测仍可翻转 `credential_valid`，其他检测失败只记录事实。

### 8.3 删除主观健康

Backend 删除：

- `appsettings.AdminBackendChannelHealthKey` 及 definition/decoder/tests；
- `opsutil.HealthBucket`；
- `channelops/providerops/dashboard` 的阈值读取和 HealthBucket 派生；
- `channel/providers/overview` DTO 中的 `health/health_bucket`；
- 仅为分桶服务的排序/过滤参数。

保留 `attempt_total/attempt_succeeded/success_rate` 等客观字段。

### 8.4 Redis 运行态查询

Admin API 通过 Redis BreakerStore 的只读实现获取：

- Provider/Channel `closed/open/half_open`；
- open 剩余时间；
- 当前窗口成功/失败数与错误率；
- 连续失败数；
- Channel 首响 EWMA；
- 最近状态迁移时间；
- Redis 数据源是否可用。

不再返回：

- Gateway 实例级 breaker snapshots；
- worst-wins 合并状态；
- `health_score/health_factor`；
- 把缺失快照伪装成绿色 closed 的默认值。

无 Redis 数据时应显示“无运行样本”；Redis 请求失败显示“数据源不可用”，不能显示“正常”。

### 8.5 Admin API

目标接口：

```text
GET    /admin/v1/routes/{id}/ops/runtime
GET    /admin/v1/providers/{id}/ops/runtime
GET    /admin/v1/channels/{id}/ops/runtime
DELETE /admin/v1/providers/{id}/ops/circuit-breaker
DELETE /admin/v1/channels/{id}/ops/circuit-breaker
```

`DELETE .../ops/circuit-breaker` 表示显式复位运行时状态，不删除业务实体；必须鉴权、记录结构化管理员操作日志并返回复位后的 closed/no-sample 状态。

route runtime channel 行目标字段：

```text
channel/provider identity
eligibility + exact exclusion reason
provider breaker state
channel breaker state
concurrency used/limit/remaining
TPM used/limit/remaining
error rate + sample count
response start EWMA
capacity score
final weight
1m/5m selected and fallback counts
```

### 8.6 历史性能

- “首响”作为主性能指标；
- “总耗时”作为副指标；
- 成功率、失败次数继续按所选时间范围；
- 不用历史 24h 成功率决定实时 breaker 标签；
- 不用 Redis 当前 EWMA回填历史图表。

### 8.7 线路当前状态与区间事实

删除当前 `deriveServiceable` 把区间内任意一次 no-available 直接映射为顶部“异常”的混合语义。线路详情拆成三类独立事实：

1. 配置状态：`enabled/disabled/archived`；
2. 当前服务状态：基于当前模型/协议的硬过滤候选和 Redis breaker，值为“可服务/不可服务/状态未知”；
3. 区间表现：请求数、成功率、fallback、no-available、首响和总耗时，只描述所选时间范围。

规则：

- 历史窗口出现过 no-available 不得把当前实时状态标成异常；
- 当前有效候选为 0 时显示“当前不可服务”；
- Redis 不可用且 breaker 状态未知时显示“状态未知”，不能显示正常或异常；
- `2 / 4` 候选但仍有可用冗余时显示候选事实，不生成主观异常标签；
- 区间异常需要提示时使用“所选区间存在失败”，并明确时间范围，不与实时状态共用徽章。

---

## 9. Admin Frontend 改造

### 9.1 Provider 页面

- Provider 新建/编辑表单增加 BaseURL；
- URL 校验错误显示在字段旁；
- Provider 详情首屏展示 BaseURL、状态、熔断和客观请求指标；
- Provider open 时显示“熔断中”和剩余时间；
- 提供明确的“复位熔断”操作和确认弹窗。

### 9.2 Channel 页面

- Channel 表单删除 BaseURL；
- Provider 选择后只读显示其 BaseURL；
- 删除“健康/降级/不健康”列、筛选和徽章；
- 保留“最近检测”“凭据”“熔断”“成功率”“错误”“首响”“总耗时”；
- Channel 详情中原“健康”区块替换为“运行事实”区块，不再产生综合结论。

### 9.3 线路实时页

表头调整为：

```text
顺序 | 渠道 | 资格 | 并发 | TPM | Provider 熔断 | 渠道熔断 | 错误率 | 首响 | 容量/当前权重 | 1m/5m 分流
```

- 删除“健康 100%”和 `health_factor`；
- “当前权重”保留精确值和 Tooltip，Tooltip 只解释客观输入；
- 首响未知显示 `--`，不得显示 `0ms`；
- open 渠道显示明确排除原因但不参与顺序编号；
- Redis 不可用时显示数据源异常和“权重已退化”，不显示虚假正常。

线路页顶部同步调整：配置状态、当前服务状态和区间表现分开显示；近 24 小时历史 no-available 不再覆盖当前实时状态。

### 9.4 Dashboard、Provider 和 Model 分析

- 删除健康分桶卡片和列；
- “异常渠道 Top”改为“失败渠道 Top”；
- 默认按上游失败次数排序，同时展示 attempt 数和失败率；
- 不用任意阈值把渠道归为健康/不健康；
- Provider/Model 下的渠道表删除健康徽章，保留成功率、失败、首响、总耗时。

### 9.5 系统设置

- 删除“渠道健康分桶阈值”；
- 熔断设置保留并扩展为全局 Redis 状态机配置；
- 字段说明必须表达触发条件、时间单位和 fail-open；
- 保存配置后不自动清除熔断状态；复位走独立操作。

---

## 10. 可观测性

### 10.1 Prometheus

新增或调整：

```text
unio_gateway_breaker_state{scope="channel|provider",id,state}
unio_gateway_breaker_transition_total{scope,from,to,reason}
unio_gateway_breaker_skip_total{scope,reason}
unio_gateway_breaker_store_operation_total{operation,result}
unio_gateway_breaker_store_latency_seconds{operation}
unio_gateway_breaker_store_unavailable
unio_gateway_provider_failure_total{provider_id,category}
unio_gateway_channel_failure_total{channel_id,category}
unio_gateway_upstream_response_start_seconds{provider_id,channel_id,protocol,operation}
unio_gateway_upstream_total_duration_seconds{provider_id,channel_id,protocol,operation}
unio_gateway_balanced_final_weight{route_id,channel_id}
```

要求：

- 不以 Provider/Channel 名称作为高基数 label；
- 不在 label 中放 URL、model、request ID 或错误正文；
- state gauge 保证同一 scope/id 只有一个状态值为 1；
- Redis 失败必须可以独立告警。

### 10.2 结构化日志

关键事件：

- breaker open/half-open/close/reset；
- Redis fail-open；
- Provider 500 跨渠道证据满足；
- half-open lease 超时回收；
- 客户 `service_unavailable` 映射；
- Provider BaseURL 修改。

字段只允许 ID、状态、分类、计数、时长和 request ID，不记录 credential、完整 URL 或上游 body。

### 10.3 Routing decision trace

候选评分调整：

```json
{
  "provider_breaker_state": "closed",
  "channel_breaker_state": "closed",
  "error_rate": 0.1,
  "error_samples": 20,
  "response_start_ewma_ms": 820,
  "capacity_score": 0.75,
  "final_weight": 0.612,
  "breaker_store_degraded": false
}
```

删除 `health_score/health_factor` 和 gateway instance snapshots。异常 trace 继续 100% 保存，普通成功保持稳定采样。

### 10.4 告警建议

发布门禁至少准备：

1. Redis breaker store 连续 30 秒不可用；
2. 任一 Provider open；
3. 同一 Provider 10 分钟内反复 open >= 3 次；
4. 线路有效 Provider 故障域少于 2；
5. `service_unavailable` 比例超过阈值；
6. half-open lease 超过租约仍未收口；
7. 首响 P95 明显升高但错误率未升高；
8. 总耗时升高但首响稳定，用于区分生成变长与连接变慢。

---

## 11. 实施步骤

开始编码时新建：

```text
docs/production/ROUTING_P4_IMPLEMENTATION_LOG.md
```

实施日志必须包含工作区保护、检查点、实际改动、测试结果、偏差、临时数据和恢复结果。每完成一个检查点立即更新，不允许最后一次性补写。

### A. 基线与文档治理

- [ ] 建立 P4 实施日志并记录当前 dirty worktree；
- [ ] 记录现有 DB/Redis/Admin/Gateway 测试基线；
- [ ] 在 P3 文档标注 P4 替代的健康度、进程内 breaker 和总耗时权重语义；
- [ ] 将确认决议写入 `DECISIONS.md`；
- [ ] 扫描 TODO/GAP/RELEASE_BLOCKERS，新增阻断项时按仓库规范登记。

### B. Schema 与契约

- [ ] 修改 providers/channels 源 migration；
- [ ] Provider 增加规范化、唯一 BaseURL；
- [ ] Channel 删除 BaseURL；
- [ ] 修改 Admin/API/routing/channeltest SQL；
- [ ] 执行 `sqlc generate`；
- [ ] 空库重建到最新 migration；
- [ ] 清理 Redis 旧 ID 和旧 breaker 状态。

### C. Adapter URL 与首响事实

- [ ] 建立结构化 URL 拼接 helper；
- [ ] 迁移 OpenAI Chat/Responses/Compact；
- [ ] 迁移 Anthropic Messages；
- [ ] 迁移 provider-specific adapters；
- [ ] 实现非流式首字节捕获；
- [ ] 统一流式首个 emit chunk 语义；
- [ ] request/attempt 时间事实和 Prometheus 对齐；
- [ ] 证明长流总耗时不进入分流权重。

### D. Redis BreakerStore

- [ ] 定义状态、配置和分类领域类型；
- [ ] 实现版本化 key 和批量 snapshot；
- [ ] 实现 Lua `AcquireAttempt`；
- [ ] 实现 Lua `RecordResult`；
- [ ] 实现 Channel 双触发；
- [ ] 实现 Provider 分级归因与跨渠道 500 证据；
- [ ] 实现 half-open 2 次成功和租约回收；
- [ ] 实现退避、稳定重置、TTL；
- [ ] 实现 fail-open 及观测；
- [ ] 实现管理员 reset。

### E. Gateway 生命周期

- [ ] 删除进程内 breaker 和 internal snapshot endpoint；
- [ ] 三协议候选准备接 Redis snapshot；
- [ ] attempt 前原子准入接线；
- [ ] 所有终态收口 lease 和结果；
- [ ] balanced 使用错误率 + 首响 + 容量；
- [ ] fixed/sticky/fallback 不变量回归；
- [ ] 外部通用 `service_unavailable` 映射；
- [ ] routing trace/metrics/logs 更新。

### F. Admin Backend

- [ ] Provider CRUD 接 BaseURL；
- [ ] Channel CRUD 删除 BaseURL；
- [ ] 主动检测读取 Provider BaseURL；
- [ ] 删除 channel health setting 和分桶；
- [ ] 删除 gatewayruntime fan-out；
- [ ] runtime API 改读 Redis；
- [ ] Provider/Channel breaker reset API；
- [ ] 首响主指标、总耗时副指标查询；
- [ ] Dashboard/Provider/Model 客观指标契约更新。

### G. Admin Frontend

- [ ] Provider 表单和详情迁移 BaseURL；
- [ ] Channel 表单删除 BaseURL；
- [ ] 删除全部主观健康配置、列和徽章；
- [ ] route runtime 表重排；
- [ ] Provider/Channel breaker 状态和 reset；
- [ ] 首响/总耗时主次展示；
- [ ] Redis degraded 和无样本状态；
- [ ] API 类型、组件测试和 Playwright 更新。

### H. 验收与发布

- [ ] 普通/race/DB/Redis/blackbox 全量测试；
- [ ] 两 Gateway 共享 breaker E2E；
- [ ] 透明故障代理 + StarAPI 真实成功链路；
- [ ] OpenAI/Anthropic 流式和非流式；
- [ ] Codex CLI 和 Claude CLI；
- [ ] Admin 真实浏览器验收；
- [ ] 空库重建、Redis 清理和最终配置复核；
- [ ] 更新 P4 实施日志、P3 替代说明和项目状态。

---

## 12. 测试计划

### 12.1 单元测试

#### Provider URL

- 规范化 scheme/host/端口/尾斜杠；
- 拒绝空值、非 http(s)、userinfo、query、fragment；
- 重复规范化 URL 冲突；
- adapter operation 路径拼接；
- provider-specific path；
- Channel DTO 不再接受 BaseURL。

#### 熔断状态机

- 3 次连续故障快速 open；
- 连续故障超过 10 秒不错误合并；
- 成功清空连续失败；
- 20 次 50% 比例 open；
- 样本不足不触发比例 open；
- 429 不进入快速连续失败；
- half-open 全局单 lease；
- 2 次连续探测成功关闭；
- half-open 失败重新打开；
- open duration 分级退避和上限；
- closed 稳定后重置级别；
- lease 超时自动回收；
- 非责任错误释放 lease但不记成功；
- Redis TIME 驱动，无本机时钟依赖；
- key TTL 和 reset。

#### Provider 归因

- DNS/TLS/EOF/timeout/502/503/504 同时计 Channel 和 Provider；
- 单渠道 HTTP 500 不计 Provider；
- 两条不同渠道短窗 HTTP 500 计 Provider；
- 401/403/429/4xx 不误熔断 Provider；
- Provider open 摘除其所有渠道；
- Provider half-open 只选择一条有效渠道探测。

#### 首响与权重

- 非流式 delayed first byte；
- 流式 delayed first chunk + long tail；
- 两条流总耗时不同但首响相同，最终权重相同；
- 首响慢只降权不直接 open；
- 总耗时不进入 scorer；
- 无首响样本保持中性；
- Redis unavailable 时只按容量退化；
- trace 中无 `health_factor`。

#### 外部错误

- 三个公开协议 surface 均返回安全 503；
- body/header 不出现内部错误码、channel/provider/BaseURL；
- `Retry-After` 边界；
- model not found 仍为协议原生 404；
- stream 首帧前/后的错误边界。

### 12.2 PostgreSQL/sqlc 集成测试

- 从空库执行 migration 1 -> latest；
- providers.base_url 必填和唯一；
- channels 无 base_url；
- Provider/Channel CRUD 往返；
- routing 从 Provider 解析 BaseURL；
- Channel test 从 Provider 解析 BaseURL；
- archive/replace/margin guard 不回归；
- request/attempt 首响和总耗时查询口径；
- Admin API 不再返回 health bucket；
- `sqlc generate` 二次执行无 diff。

### 12.3 真实 Redis 集成测试

不得只用内存 fake 代替最终门禁：

- 两个 BreakerStore 实例共享状态；
- 两个 Gateway 实例观察同一 open；
- Lua 并发竞争 half-open 只有一个成功；
- breaker + concurrency 准入无租约泄漏；
- Redis 重启/flush 后状态安全回到 no-sample；
- Redis 不可用时 fail-open 并产生指标；
- Redis 恢复后无需重启 Gateway；
- 高并发 `RecordResult` 计数不丢失；
- Provider/channel key TTL 正确。

### 12.4 生命周期集成测试

使用可控 upstream 覆盖：

- balanced Channel open 后只 fallback 线路内其他渠道；
- Provider open 后引用它的多线路同时摘除；
- 未绑定但可用的渠道永不进入 attempt；
- fixed 唯一渠道 open 时内部记录 no-available，外部只见通用 503；
- sticky 指向 open 渠道时失效并改绑；
- 429 cooldown 与 breaker 不相互污染；
- 401 credential gate 与 breaker 不重叠；
- 客户取消不惩罚 Channel/Provider；
- fallback 成功后的 usage/ledger/trace 完整；
- 失败请求无普通扣费；
- partial stream 和 bill-on-disconnect 既有语义回归。

### 12.5 Admin Backend 和前端测试

Backend：

- Provider BaseURL CRUD/冲突/归档；
- Channel 请求含 base_url 返回 400 或严格拒绝未知字段；
- health setting 不再注册；
- runtime Redis 正常/无样本/不可用；
- reset 鉴权、状态和日志；
- 历史首响/总耗时查询；
- 当前服务状态与区间 no-available 分离。

Frontend component：

- Provider 表单 BaseURL；
- Channel 表单无 BaseURL；
- 无健康阈值编辑器；
- 无健康徽章/列；
- open/half-open/closed、无样本、Redis 异常；
- 首响主指标和总耗时副指标；
- 当前权重 Tooltip；
- reset 确认和刷新。
- 当前服务状态与区间表现分开渲染。

Playwright：

- Provider 创建/编辑；
- Channel 创建绑定 Provider；
- 线路 runtime 真实轮询；
- 注入 Channel open 后页面 3 秒内更新；
- 注入 Provider open 后全部所属渠道显示排除；
- reset 后页面恢复；
- 页面控制台无错误，桌面和移动视口无重叠。

### 12.6 真实上游 E2E

真实上游 E2E 继续是发布硬门禁，使用用户提供的 StarAPI。不得用纯 mock 成功响应代替。

#### 隔离方式

1. 创建 `e2e-routing-p4-<run-id>` 前缀的 Provider、Channel、Model、Route、User、API Key 和余额；
2. Provider BaseURL 指向本地透明故障代理；
3. 代理正常模式完整转发到真实 StarAPI，保留真实响应、usage 和 request ID；
4. 代理只在指定故障场景注入错误，不生成伪造成功响应；
5. 凭据只从安全输入进入数据库/进程环境，不写文档、代码或测试输出；
6. 请求限制 max tokens 和并发，故障测试不主动打满真实上游配额；
7. 测试结束清理全部前缀数据和 Redis key，保留脱敏验收摘要。

#### 必测场景

1. OpenAI Responses 非流式真实 200、真实 usage、账务闭环；
2. OpenAI Responses 流式真实 200、首响与总耗时事实；
3. Anthropic Messages 非流式真实 200、真实 usage、账务闭环；
4. Anthropic Messages 流式真实 200、首响与总耗时事实；
5. 延迟首响注入：首响权重下降，总耗时不参与；
6. 长尾注入：首响不变、总耗时上升、权重不变；
7. Channel 连续 3 次 transport/5xx 故障，全局快速 open；
8. 两 Gateway 中 A 触发，B 下一请求立即跳过；
9. half-open 全局只放一个真实探测，连续 2 次成功恢复；
10. 单渠道 HTTP 500 不触发 Provider；跨两渠道后满足 Provider 证据；
11. Provider 502/503 故障后全部渠道全局摘除；
12. Redis 暂停时正常真实上游请求仍可通过，并产生 fail-open 观测；
13. 内部 no-available 对 OpenAI/Anthropic 外部只返回安全通用 503；
14. 历史测试 no-available 保留在区间指标，但当前候选恢复后线路顶部显示可服务；
15. Codex CLI 输出确定性成功标识；
16. Claude CLI 输出确定性成功标识；
17. Admin 在 3 秒刷新窗口内显示正确状态和客观指标。

若某协议没有至少一条真实可用渠道，不得把该协议 E2E 标记通过。Provider 容灾测试需要至少两个独立 BaseURL；只有一个 StarAPI BaseURL 时，可以验收 Provider 熔断行为，但不能宣称已具备真实 Provider 级容灾。

### 12.7 回归与性能

后端：

```bash
sqlc generate
go test ./...
go test -race ./...
go vet ./...
go build ./cmd/...
git diff --check
```

Admin：

```bash
bun run lint
bun run build
bun run test
bun run test:e2e
```

性能门禁：

- Redis breaker Lua P95 必须记录并评估；
- 同等本地压测下 Gateway 吞吐回退不超过 5%；
- breaker 读取批量化，不能按候选逐条 N+1 请求 Redis；
- Redis 故障日志必须采样/限速；
- Admin runtime 3 秒轮询不能造成 Redis 热 key 放大；
- 所有长流租约和 half-open lease 最终归零。

---

## 13. 发布、迁移与回滚

### 13.1 发布前

1. 导出现有 Provider/Channel/Route/Model/Price 配置的脱敏快照；
2. 停止 Gateway/Admin/Worker，避免新旧 schema 混跑；
3. 清理旧开发业务库并从空库执行 migrations；
4. 按新模型导入 Provider BaseURL 和 Channel；
5. 清理 Redis sticky、限流、并发和 breaker 旧 ID 状态；
6. 启动 Redis/PostgreSQL，再启动 Gateway/Worker/Admin，最后启动 Admin 前端；
7. 运行真实 smoke 后才开放客户流量。

### 13.2 不支持混合版本

P4 删除 `channels.base_url` 并移除进程内 breaker，新旧 Gateway/Admin 不能滚动混跑。发布必须作为停机维护窗口执行，或准备独立新环境后整体切流。

### 13.3 回滚

由于数据库允许重建，回滚策略是配置快照 + 旧版本空库重建，不提供在线 down migration 数据兼容承诺：

1. 停止 P4 全部进程；
2. 清理 `breaker:v2` Redis key；
3. 用旧代码源 migration 重建数据库；
4. 从发布前快照恢复旧 Provider/Channel BaseURL 配置；
5. 启动旧版本并执行协议 smoke；
6. request/ledger 历史若必须保留，应在发布前另行备份，不能依赖重建回滚。

### 13.4 灰度

即使是停机迁移，熔断执行仍分两步：

1. 预发布环境全功能开启，完成真实上游与故障注入；
2. 正式环境先确认 Redis/指标/Admin 正常，再启用 breaker；配置热更新生效，不重启 Gateway。

不得在没有告警和 reset 能力时启用 Provider breaker。

---

## 14. 风险与控制

| 风险 | 后果 | 控制 |
|------|------|------|
| Redis 成为熔断唯一状态源 | Redis 故障时失去主动摘除 | fail-open、独立告警、线路内 fallback 仍存在 |
| 单 Provider 只有一个 BaseURL | Provider open 后无容灾 | 通用 503；运营告警；商业上线补第二故障域 |
| Provider 500 误归因 | 全 Provider 被错误摘除 | 至少 2 条不同渠道短窗证据 |
| 非流式首字节采集缺失 | 权重误用完整耗时 | unknown 保持中性，禁止伪造 0ms/总耗时 |
| half-open lease 泄漏 | Provider/Channel 长期不可探测 | TTL、所有终态收口、并发故障测试 |
| BaseURL 规范化碰撞 | 配置导入失败 | 导入前报告冲突，不静默合并 |
| 删除健康字段导致前后端漂移 | 页面运行时报错 | 同切片更新 API 类型、build、component、Playwright |
| 429 同时参与 cooldown/比例窗口 | 长期限流后 Channel open | 不进入快速触发；测试恢复和 reset |
| Provider open 跨线路影响扩大 | 多条线路同时降级 | 这是故障域预期；Admin 显示受影响线路并告警 |
| 真实故障代理配置错误 | 把 mock 当真实成功 | 正常成功必须保留真实 upstream ID/usage，可审计核对 |

---

## 15. 完成定义

只有同时满足以下条件，P4 才能标记完成：

### Schema 与契约

- Provider 唯一持有 BaseURL，Channel 无 BaseURL；
- routing、channel test、Admin 和 adapter 全部使用新来源；
- 空库 migration 和 sqlc 无漂移；
- 新旧版本没有隐式兼容分支。

### Gateway

- Channel/Provider breaker 全部是 Redis 全局状态；
- 低流量连续故障和比例故障都能触发；
- Provider 分级错误归因符合决议；
- half-open 全局单探测、2 次成功恢复、租约无泄漏；
- balanced 只使用容量、错误率和首响，完整总耗时不参与；
- fixed/sticky/fallback/毛利/计费不回归；
- 外部无内部错误信息泄露。

### Admin

- 主观健康配置、字段、徽章和筛选全部删除；
- Provider/Channel/Route 展示客观实时状态；
- 首响为主、总耗时为副；
- 当前权重可解释；
- Redis 故障和无样本不会显示虚假正常；
- breaker 可安全复位。

### 验证

- 后端 normal/race/vet/build 全绿；
- PostgreSQL/Redis 集成测试全绿；
- Admin lint/build/component/Playwright 全绿；
- 两 Gateway 全局状态测试通过；
- StarAPI OpenAI/Anthropic 流式、非流式真实 E2E 通过；
- Codex CLI 和 Claude CLI 通过；
- 真实成功响应、usage、request/attempt、账务、trace 和 Admin 事实一致；
- 实施日志逐检查点完整，可用于复盘。

---

## 16. 对 P3 的替代关系

P3 以下内容由 P4 替代：

1. “进程内 ChannelCircuitBreaker + Admin 多实例 worst-wins”替换为 Redis Channel/Provider 全局 breaker；
2. “完整上游耗时 EWMA 参与健康因子”替换为首响 EWMA，完整总耗时只作副指标；
3. `health_score/health_factor` 用户可见语义替换为错误率、首响、容量和最终权重；
4. `channels.base_url` 替换为 `providers.base_url`；
5. Admin `healthy/degraded/unhealthy` 主观分桶和阈值配置删除；
6. 客户 no-available 错误统一为安全 `service_unavailable`。

P3 以下内容继续有效：

- 线路显式渠道池是唯一调度边界；
- mode 仅 `balanced/fixed`；
- 成本不参与 balanced 排序；
- sticky 不突破线路池；
- 毛利硬门禁；
- `/v1/models` 线路隔离；
- 归档替换护栏；
- routing decision trace 和真实上游 E2E 门禁。

---

## 17. 未决项

当前没有需要用户继续确认的产品或架构决议。以下三项已确认采用方案 A：

1. 一个 Provider 对应一个 BaseURL 和故障域；
2. Provider 错误使用分级归因，HTTP 500 需要跨渠道证据；
3. Redis breaker store 不可用时 fail-open。

实施中若出现会改变公开错误契约、Provider 故障域定义、Redis 失败策略、熔断阈值、计费或线路边界的新问题，必须暂停对应检查点，并向用户提供至少两个可选方案及推荐意见后再继续。
