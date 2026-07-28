# RPD 与 TPM 准入计量一致性改造计划

## 状态

- 状态：RPD、RPM、TPM、并发产品口径已确认；发布决策待实施前确认
- 创建日期：2026-07-28
- 涉及仓库：`unio-gateway`、`unio-admin`、`unio-blueprint`
- 改造完成条件：代码、测试与 Blueprint 同步完成后删除本临时计划

## 决策记录

### 已确认

- RPD 只统计最终成功请求；失败及 fallback 落败 Channel 不增加成功 RPD。
- 线路成功 RPD 等于本线路各 Channel 成功 RPD 求和。
- Route 顶部四项显示该线路全部用户桶的实际合计；Route 限额继续按 `(route, user)` 执行。
- Route RPM 统计入口允许请求，Channel RPM 统计真实上游 attempt；失败 attempt 只要 transport 已开始仍计 RPM。
- RPM、TPM、并发不要求 Route 等于 Channel 求和。
- Admin 不修改 RPM、RPD、TPM、并发的短名称，通过 tooltip 解释口径。
- 无权威 usage 时 TPM 不能归零：transport 已开始至少保留输入，可本地计算输出时保留输入加输出。
- 有限 TPM 必须在上游调用前按“输入估算 + 输出预算”完整预占，不能只预占输入或依赖流式实时追加。
- 客户未填写输出上限时，将模型上限写入上游请求；模型也没有上限时，写入现有 `maxOutputTokensFallback`，当前默认 `4096`。
- 完整预算超过当前剩余 TPM 时直接拒绝请求，不自动缩小客户的输出上限。
- fallback 时 Route TPM 只计一次用户逻辑请求；Channel TPM 独立记录每次真实上游 attempt。
- 并发创建、释放与续租策略保持现状。

### 实施前确认的发布决策

1. 新旧 TPM Redis 协议采用版本化 key 滚动切换，还是维护窗口整体切换。推荐版本化 key。
2. 成功 RPD 在 UTC 零点开始全新统计，还是实现 PostgreSQL 当日回填。推荐 UTC 零点切换，不回填无法可靠归因的旧桶。
3. 本次是否同时实现 Redis 状态丢失后的当日 RPD 重建。若不实现，必须接受并记录“恢复后当日计数从恢复点重新开始”的边界。

下文按已确认的产品口径展开。发布相关选择在实施前确认，并同步更新发布流程、测试和验收标准。

## 背景

当前线路运行态页面同时展示：

- 顶部线路 RPD：聚合 `(route, user)` 的入口 request-admission 日桶；
- 表格渠道 RPD：读取 Channel 全局 attempt-admission 日桶。

两者目前既不是同一统计口径，也存在 Channel RPD 日桶复用短 TTL 的实现问题：

- 线路 RPD 在请求入口准入时增加，后续请求失败也不回退；
- Channel RPD 在取得 `AttemptPermit` 时增加；只要真实 transport 已开始，当前 Finish 就保留计数；
- Channel RPD 与 RPM/TPM 共用约 7.5 分钟的短 TTL，不能保存完整 UTC 日数据；
- Channel 桶按 `channel_id` 全局统计，同一个 Channel 被多条 Route 复用时无法直接归因到某条线路；
- Admin 将两种不同口径都标记为 `RPD`，容易产生“顶部应等于表格求和”的预期。

本次改造将 RPD 的产品展示口径统一为“最终成功请求数”，同时保留准入阶段的预占能力，确保硬限额不会因并发成功提交而被突破。

在继续梳理线路与 Channel 的 RPM、TPM 和并发后，还确认了以下事实：

- 线路顶部用量聚合该 Route 下所有 `(route, user)` 桶，这是正确的全用户汇总；Route 上配置的限额继续按每个用户独立执行；
- Route RPM 表示入口允许请求数，Channel RPM 表示真实上游 attempt 数；fallback 会使一次 Route RPM 对应多次 Channel RPM；
- Route 并发覆盖认证后到 handler 返回的完整请求生命周期，Channel 并发只覆盖一次候选 permit/transport 生命周期；
- 当前 Route 与 Channel TPM 都只按输入估算预占，但最终对账包含输入与输出，因此并发下不能保证总 TPM 硬上限；
- transport 已开始但拿不到权威 usage 时，当前会释放 TPM 预占；部分流式结算也不会发布 request-admission TPM，可能把已知消耗记为零。

因此本计划同时补齐 TPM 的事前预算与无 usage 保底计量。RPM 和并发的计数口径保持不变，只在 Admin tooltip 中解释，不修改现有指标名称。

## 产品口径

### 线路顶部汇总

线路顶部 RPM、RPD、TPM 和并发继续汇总该 Route 下所有用户的当前用量：

```text
线路顶部指标 = Σ 该线路所有用户桶的对应指标
```

Route 配置的四类限额仍按 `(route, user)` 独立执行。例如 Route RPM 为 `100` 时，10 个用户理论上可合计达到 `1000 RPM`，Admin 顶部显示实际合计值。这是预期行为，本次不增加 Route 全局共享限额。

### 四项指标在什么时机创建

这里的“创建”分为两种：准入时先占用额度，以及最终成功后形成展示计数。RPD 的页面主值只展示已提交成功数，不展示在途预占。

| 指标 | Route 时机 | Channel 时机 | 失败或结束时如何处理 |
| --- | --- | --- | --- |
| RPM | 入口 request admission 通过时占用 `1`，后续 fallback 不重复占用 | 候选取得 `AttemptPermit` 时预占；真实 transport 开始后正式保留 | Route 后续失败仍保留；Channel 在 transport 前失败则释放，transport 开始后的失败仍保留 |
| RPD | 入口只预占额度，不增加成功数 | 候选只预占额度，不增加成功数 | 最终胜出时原子增加 Route 和胜出 Channel 成功数；失败、落败 fallback 和全部失败均释放预占，不增加成功数 |
| TPM | 候选计划完成后、第一次上游调用前，一次性预占候选池中的最大完整预算 | 每个候选在 `AttemptPermit` Acquire 时预占自己的输入加输出预算 | 无 transport 记 `0`；已有 transport 按权威 usage、本地输入加输出或已知输入对账，并释放未使用预算 |
| 并发 | 入口 request admission 通过时取得，覆盖整个 handler 生命周期 | `AttemptPermit` Acquire 时取得，只覆盖当前候选 attempt | Route 在 handler Finalize 时释放；Channel 在该 attempt Finish/Abort 时释放 |

所以创建时机并不统一发生在“打到上游”这一刻：Route RPM、RPD 预占和并发更早发生在入口准入；Route TPM 在候选计划完成后、上游调用前发生；Channel 四项都围绕一次候选 `AttemptPermit`，但 RPM 只有 transport 真正开始后才最终保留，RPD 只有最终成功后才形成成功计数。

### 哪些指标可以由 Channel 求和

只有“成功 RPD”要求满足 Route 与本线路 Channel 的严格求和关系：

```text
线路成功 RPD = Σ 本线路各 Channel 的成功 RPD
```

其他指标不能使用 Channel 行求和替代线路顶部值：

- RPM：一次入口请求可能依次调用多个 fallback Channel；
- TPM：失败 attempt 也可能消耗上游 Token，而线路请求只保留一份请求级准入/计量事实；
- 并发：线路并发覆盖路由、短等、授权、上游、结算和交付，Channel 并发只覆盖当前上游 attempt。

典型 fallback：

```text
Route 入口请求 = 1 RPM
Channel A 已发起上游请求但失败 = 1 RPM，0 成功 RPD
Channel B fallback 成功 = 1 RPM，1 成功 RPD

Route RPM = 1
Channel RPM 求和 = 2
Route 成功 RPD = Channel 成功 RPD 求和 = 1
```

### RPM 口径

- Route RPM：API Key 认证和入口准入通过后计 `1`，表示该用户在该 Route 上被接受的一次入口请求；后续候选 fallback 不重复增加。
- Channel RPM：候选取得 `AttemptPermit` 时先预占；若真实 transport 未开始则 Abort 释放，transport 一旦开始则保留，表示一次上游请求 attempt。
- 上游网络错误、超时、429、5xx、2xx 协议解析失败都可以在客户尚未收到有效输出时 fallback，但已经发生的 Channel attempt 仍计 RPM。
- candidate denial、本地 adapter 解析失败、请求编码失败或其他明确发生在 transport 前的失败不计 Channel RPM。
- Channel RPM 计数不代表成功；成功由成功 RPD、breaker outcome 和 request/attempt 状态分别表达。

### 并发口径

- Route 并发：入口 request-admission Acquire 时取得，handler 返回并 Finalize 时释放；等待、路由、授权、上游调用、结算和响应写出都属于在途请求。
- Channel 并发：AttemptPermit Acquire 时取得，Finish/Abort 时释放；只覆盖一个候选 attempt。
- 本次不修改并发租约、续租或故障行为。

### 一次请求如何计数

- 请求进入 Gateway 时不立即增加“成功 RPD”，只预占线路 RPD 额度。
- 每次候选 Acquire 只预占 Channel RPD 额度，不立即增加成功计数。
- 候选失败并 fallback 时，失败 Channel 的 RPD 预占必须释放。
- 最终成功时：
  - 线路成功 RPD 增加 `1`；
  - 仅最终胜出 Channel 的线路归因成功 RPD 增加 `1`；
  - 该请求最多成功提交一次。
- 所有候选均失败、请求取消或未进入成功点时：线路和各 Channel 的成功 RPD 都不增加。
- 成功点之后客户端断开，不回退成功计数，因为成功上游调用已经发生。

### 成功点定义

成功提交发生在“当前候选已经成为最终结果，fallback 被不可逆关闭”时：

- 非流式：上游返回可接受的成功响应，协议解析完成，结果可交付；
- 流式：收到第一个协议定义的有效 `FirstTokenEligible` 事件，准备向客户写出首个有效帧之前；
- 仅建立 TCP/HTTP transport、等待响应头、收到非 2xx、收到无效响应体或无效控制帧均不算成功；
- 2xx 后协议解析失败且继续 fallback 的候选不计成功 RPD。

### UTC 日归属

- 在 request admission Acquire 时冻结 `rpd_day_bucket`；
- 同一请求的线路成功计数和所有 Channel 归因计数都使用该冻结日桶；
- 跨 UTC 零点的长请求仍归属请求进入时的日桶，避免线路与 Channel 使用不同日期；
- Channel 全局容量预占可继续使用候选 Acquire 时的 Channel 日桶，但线路归因必须使用 request token 冻结的日桶。

### TPM 硬限额口径

TPM 限额不能只预占输入、结束后再追加输出，否则多个并发请求可以同时通过后把实际用量推过上限。严格限额必须在调用上游前取得完整预算：

```text
TPM 预占总额 = 保守输入 Token + 输出预算 Token
```

本计划采用完整预算预占，而不是依赖流式实时追加：

- request 层按 `max(candidate_input + candidate_output_budget)` 一次性预占，fallback 不重复预占；
- Channel 层每个 AttemptPermit 使用该候选的输入估算和输出预算单独预占；
- 上游请求的 `max_tokens` / `max_output_tokens` 不得超过已取得的输出预算；
- 有权威 usage 时按实际 billable TPM 对账并释放未使用预算；
- failed attempt 在进入 fallback 前先按已知消耗对账并释放剩余预算，再为下一候选取得新预算；
- 流式可以在本地实时累计输出 Token，用于更早形成实际值和运营展示，但不能替代调用前的最大预算预占；
- 非流式在完整响应返回前无法实时知道输出量，因此必须依赖事前输出预算。

每个候选的有效输出预算按以下规则解析：

1. 读取客户请求显式、合法的 `max_tokens` / `max_completion_tokens` / `max_output_tokens`；
2. 读取当前候选模型的 `models.max_output_tokens`；
3. 两者都有正数时取较小值，避免客户上限超过模型能力时无意义地过度预占；
4. 只有一方有正数时使用该值；
5. 两者都没有有效正数时，复用现有账务授权的 `maxOutputTokensFallback`，当前内置默认值为 `4096`，配置源为 `AUTHORIZATION_MAX_OUTPUT_TOKENS_FALLBACK`。

#### 客户没有输出上限时的处理

当前 `maxOutputTokensFallback` 只用于账务预授权估算，不会改写转发给上游的请求。比如 Gateway 预占了 `4096` 个输出 Token，但上游没有收到这个上限，实际就仍可能输出超过 `4096`，TPM 因而不是硬限额。

本次确定向上游写入解析后的输出上限：

- 客户有上限时，使用客户上限与模型上限中的较小值；
- 客户没填时，使用模型上限；
- 客户和模型都没有上限时，使用现有 `maxOutputTokensFallback`，当前默认 `4096`；
- TPM 预占、账务预授权和上游实际最大输出必须使用同一个解析结果。

这会改变过去“未设置上限即可由上游决定最大输出”的行为：缺少客户和模型上限的请求今后最多输出默认 `4096`。这是保证 TPM 为硬限额所必需的约束。

#### 剩余 TPM 不够完整预算时的处理

例如本次输入为 `1000`，输出上限为 `4000`，完整预算就是 `5000`；当前分钟只剩 `3000 TPM`：

- Gateway 在上游调用前拒绝本次请求；
- 不自动把输出上限从 `4000` 缩小到 `2000`；
- 客户可以主动降低输出上限后重试。

不能先超额放行，再依靠流式实时扣减补救。

#### fallback 时 Route TPM 计几次

Route 与 Channel 按不同主体计量：

- Route TPM 表示用户发起的一次逻辑请求，输入只计一次，输出计最终交付或已向客户交付的部分输出；
- 请求进入过真实 transport 但最终全部失败且没有客户输出时，Route TPM 至少保留一次 request 输入估算；
- Channel TPM 表示各次真实上游 attempt，每个失败或成功 attempt 都按自身输入加已知输出计量；
- 因此 fallback 时 Channel TPM 求和可以大于 Route TPM，这是上游实际尝试成本与客户逻辑请求用量的正常差异。

Route TPM 不累计所有 fallback attempt 的上游消耗，避免用户因平台选路或上游故障重复消耗 Route TPM 配额。各 attempt 的真实资源消耗由 Channel TPM 承担和展示。

### 无权威 usage 的 TPM 保底计量

准入 TPM 与账务权威 usage 必须分离。账务可以拒绝不可靠 usage，但准入限额不能因此把已知消耗记为零。

TPM 对账事实按以下优先级选择：

1. 上游权威 usage；
2. Gateway 根据已解析响应或流式事件计算的本地 Token；
3. 仅能确认输入时，使用 AttemptPermit 已冻结的保守输入估算。

request session 的 quota usage 不能继续采用“第一次发布后永久锁定”：

- fallback 失败 attempt 发布的 `input_fallback` 只是 provisional 事实；
- 后续胜出 attempt 的 `gateway_local` 或 `upstream` 可以按优先级覆盖 provisional 事实；
- 同一优先级不得用更弱或更小的不完整快照倒退已记录事实；
- `upstream` 为最终权威后不再被本地估算替换；
- 全部 attempt 失败时，Route TPM 使用已真实进入 transport 的候选输入估算最大值，只计一次逻辑请求，不累加所有 fallback 输入；
- 完全没有 transport 时 Route TPM 为 `0`。

具体规则：

- transport 未开始：TPM 为 `0`，释放全部预占；
- transport 已开始但未取得任何有效输出：至少保留已知输入 Token；
- 流式取得部分输出：保留输入 Token，加 Gateway 已累计的输出 Token；
- 非流式取得完整成功响应但缺 usage：根据完整协议响应本地计算输出 Token；
- 上游返回权威 usage：覆盖本地估算并按权威值对账；
- cache 命中情况无法从本地确认时，不猜测 cache read 优惠，按预占输入保守计入准入 TPM；
- 本地输出计量必须覆盖可计费文本、reasoning、tool call/function arguments 等协议字段，不能只累计普通 visible text；
- 对账结果记录 `usage_source = upstream | gateway_local | input_fallback`，供 Admin tooltip、日志和指标解释精度。

Route request-admission TPM 与 Channel TPM 都使用同一优先级，但资源主体不同：Route TPM 按 `(route, user)` 约束客户入口，Channel TPM 按全局 `channel_id` 约束上游容量。fallback 失败 attempt 的 Channel TPM 可以大于最终 Route 请求 TPM，因此两者不要求求和相等。

### TPM 分钟窗口归属

- Request TPM 在一次性 Reserve 时冻结 `(route, user, minute)` 桶；
- Channel TPM 在每个 AttemptPermit Acquire 时冻结 `(channel, minute)` 桶；
- Finish 必须对账各自冻结的原始桶，不能按完成时间切换到新分钟；
- 跨分钟长请求仍把完整预算和最终 actual 归到准入分钟，这是固定窗口“按请求进入时预占预算”的口径；
- 不把流式 chunk 按生成时间拆到多个分钟桶，否则非流式与流式会形成不同限额语义，也无法在调用前保证完整预算；
- fallback 发生在下一分钟时，新 Channel attempt 使用其实际 Acquire 分钟，Route request 仍只使用原 Reserve 分钟。
- request token / permit 续租必须同时延长仍需对账的原始 TPM 桶生命周期，确保长流结束时原桶仍存在；
- 延长 TTL 不会让旧分钟用量混入当前分钟，因为读取和限额判断仍按分钟号选择 key；
- Finish 遇到原始 TPM 桶意外丢失时不得静默成功，应返回稳定 runtime fault 并进入既有 fail-closed/恢复流程。

## 设计原则

### 预占与成功计数分离

预占用于限流，成功计数用于展示和归因，不能继续复用同一个字段表达两种语义。

#### 现有预占/容量桶

- `(route, user, day)` RPD：线路用户级硬限额占用，包含已成功请求和当前在途预占；
- `(channel, day)` RPD：Channel 全局硬限额占用，包含已成功 attempt 和当前在途预占；
- 限额判断继续使用“已成功保留 + 当前在途预占”，避免剩余额度为 1 时多个并发请求同时通过。

#### 新增成功归因桶

建议新增独立 Redis key：

```text
admission:v1:route-success-rpd:{route_id}:{day_bucket}
admission:v1:route-channel-success-rpd:{route_id}:{channel_id}:{day_bucket}
```

要求：

- 两个成功桶必须在同一个 Redis Lua 操作中同时 `INCR`；
- 相同 `request_admission_id` 只能提交一次；
- 相同成功提交重试必须幂等；
- 不同 permit 试图为同一请求重复提交时必须返回冲突并 fail closed；
- 成功桶只包含已提交成功，不包含在途预占，因此 Admin 可直接做严格求和。

### TPM 预算与实际计量分离

TPM 同样不能用一个数字同时表达“并发安全预算”和“最终实际消耗”。request token 与 permit 至少需要冻结：

```text
tpm_input_estimate
tpm_output_budget
tpm_reserved_total
tpm_accounted_actual
tpm_usage_source
```

要求：

- 限额判断使用 `tpm_reserved_total`，保证并发请求不会共同超发；
- 最终展示和历史占用使用 `tpm_accounted_actual`；
- active 请求在容量快照中仍占用完整预留预算，Finish 后释放未使用部分；
- `tpm_usage_source` 只描述准入计量可信度，不自动成为账务结算依据；
- Route 与 Channel 使用同一 Token 计算函数和 source 枚举，避免两层对同一 usage 做不同解释。

### 为什么保留 Channel 全局桶

Channel 限额主体仍是 `channel_id`，一个 Channel 可以被多条 Route 复用，因此：

- `(channel, day)` 桶继续用于全局 Channel RPD 限额和容量评分；
- `(route, channel, day)` 桶只用于线路内成功归因和 Admin 展示；
- 不能直接用 `(route, channel, day)` 替换 Channel 全局容量桶，否则会破坏跨 Route 共享限额。

## Redis 生命周期设计

### 1. Request admission Acquire

保持当前入口预占模型，但补充 token 字段：

```text
rpd_day_bucket
rpd_success_state = none | committed
rpd_success_permit_id
rpd_success_channel_id
```

行为：

- 检查 `(route, user, day)` RPD 限额；
- 通过后预增现有 route-user RPD 占用桶；
- 不写线路成功桶；
- 将日桶、原始 RPD 占用桶 key 和成功状态写入 request token。

RPM 和并发继续在该阶段取得，不延后到候选或 transport：

- RPM 表示一次已通过入口准入的客户请求；
- 并发表示该客户请求仍在 Gateway handler 生命周期内；
- 非生成端点、协议校验失败和后续路由失败仍按当前规则消耗入口 RPM，并短暂占用入口并发；
- RPD 不再因入口允许而形成成功计数。

### 1A. Request TPM Reserve

现有一次性 `ReserveRequestTokens` 从只接收输入估算改为接收完整 TPM 预算：

```text
input_estimate
output_budget
reserved_total = input_estimate + output_budget
```

行为：

- 候选计划为每个候选保留输入估算和输出预算，并计算 request 级最大候选总预算；
- 按 `(route, user, minute)` 原子检查并预占 `reserved_total`；
- 将输入、输出和总额固化到 request token；
- 相同 request token 的同预算重试返回幂等成功，不同预算返回冲突；
- fallback 不重复 Reserve request TPM；
- active 请求在 Admin 路线 TPM 中按完整预算占用，避免剩余额度被其他并发请求重复使用。

### 2. Attempt Acquire

行为：

- 检查 Channel 全局 RPD 限额；
- 通过后预增 Channel 全局 RPD 占用桶；
- 将原始 Channel RPD 桶 key 固化到 permit；
- permit 冻结 `route_id` 和 request token 的 `rpd_day_bucket`，供成功提交构造归因 key；
- 不写线路或 Route-Channel 成功桶。

同时将 Channel TPM 从只预占输入改为预占候选完整预算：

- 使用候选自身输入估算与有效输出预算；
- 原子检查 Channel 全局 TPM 后增加 `reserved_total`；
- permit 固化输入、输出、总额和原始 TPM 桶 key；
- candidate denial 不写 RPM、RPD、TPM 或并发；
- Permit 成功但 transport 未开始时，Abort 归还完整 TPM 预算及其他候选资源。

`AcquireAttemptInput` / `AttemptPermit` 需要补充可信 `RouteID` 与冻结日桶；值由 request-admission session 绑定，调用方不得自行声明。

### 3. 成功提交

新增一次性操作，建议命名：

```text
CommitAttemptSuccessRPD
```

触发位置：

- 非流式：成功响应解析完成、正式选择该候选之后；
- 流式：首个 `FirstTokenEligible` 事件、写客户响应之前。

Lua 必须在一个原子操作内：

1. 校验完整性 epoch、request token、permit 和冻结身份；
2. 校验 request token 仍为 active；
3. 校验 permit 仍为 active；
4. 若当前 permit 已提交，返回幂等成功；
5. 若 request token 已由其他 permit 提交，返回冲突；
6. 增加 `route-success-rpd`；
7. 增加 `route-channel-success-rpd`；
8. 在 request token 写入 committed、胜出 permit 和 Channel；
9. 在 permit 写入 `rpd_success_committed=1`；
10. 设置完整 UTC 日 TTL。

成功提交必须发生在客户可见成功输出之前。提交结果未知时禁止 fallback 到第二个上游，避免同一客户请求产生两个成功上游调用。

### 4. Attempt Finish / Abort

- `Abort`：保持释放 Channel RPM/RPD/TPM 预占；
- `Finish` 且 permit 已成功提交：保留 Channel 全局 RPD 预占，作为成功占用；
- `Finish` 且 permit 未成功提交：释放 Channel 全局 RPD 预占；
- 网络失败、超时、上游非成功状态、协议解析失败、无有效首字、fallback 失败 attempt 都属于未提交，必须释放；
- Channel RPM 与 RPD 分开处理：真实 transport 开始后 RPM 始终保留；RPD 只在成功提交后保留；
- TPM 不从 RPD 成功状态反推，按本 attempt 已知消耗独立对账；
- 有上游权威 usage：按权威 billable TPM 对账；
- 无权威 usage 但有本地输出计量：按 `input_estimate + local_output_tokens` 对账；
- 无权威 usage 且无有效输出：transport 已开始时至少保留 `input_estimate`；
- 若实际值超过 permit 预留总额，记录 overage 指标并保留完整实际值；正常路径应通过上游输出上限避免该情况；
- Finish 原子释放 `reserved_total - accounted_actual`，并保存 `tpm_usage_source`；
- breaker/TTFT 继续按各自现有 outcome 处理。

### 5. Request admission Finish

- request token 已成功提交：保留 route-user RPD 预占，作为成功占用；
- request token 未成功提交：释放 route-user RPD 预占；
- request session 接收独立的准入 TPM 计量事实，不再只接受账务权威 usage；
- 上游权威 usage 可用时按权威值对账；
- partial stream、本地完整响应计量或 input fallback 可按对应 source 对账；
- 只有完全未进入 transport、没有任何应计 Token 时才将 request TPM 归零并释放全部预算；
- 释放并发租约；
- first-terminal-wins，重复 Finish 不得重复释放。

### 6. TTL

- 所有 RPD 预占桶与成功桶必须覆盖完整 UTC 日窗口；
- TTL 不得复用 permit 派生的分钟级 `bucket_ttl_ms`；
- 建议按冻结 `day_bucket` 计算“UTC 次日零点 + terminal/recovery buffer”的绝对过期时间；
- Channel 全局 RPD 当前短 TTL 必须一并修复；
- `0=不限` 时仍预占并记录成功计数。

### 失败流量约束

- 本次明确将 RPD 定义为成功请求额度，失败请求释放 RPD 预占；
- 失败请求仍受 RPM 和并发限制，不能绕过入口保护；
- 如果后续需要限制“每日失败/尝试次数”，应新增独立的 Attempt RPD 指标或限额，不能复用成功 RPD；
- 若上游供应商会把 4xx/5xx 或网络失败计入其配额，需另建 Provider/Channel attempt quota，本次成功 RPD 不承担该职责。

## Gateway 代码改造范围

### BreakerStore key 与类型

预计修改：

- `internal/platform/breakerstore/keys.go`
- `internal/platform/breakerstore/requestadmission.go`
- `internal/platform/breakerstore/types.go`
- `internal/platform/breakerstore/validation.go`
- `internal/platform/breakerstore/store.go`

工作项：

- 新增线路成功与 Route-Channel 成功 key builder；
- 为 request token / permit 增加 Route、日桶和成功提交身份；
- 为 request token / permit 增加 TPM 输入估算、输出预算、预留总额、实际计量和 usage source；
- 将 request/attempt TPM input/result 类型从单一 estimate 扩展为结构化预算与实际计量；
- 新增成功提交 input/result 与稳定错误结果；
- 扩充校验、指纹和观测指标。

### Lua

预计修改：

- `internal/platform/breakerstore/requestadmission_lua.go`
- `internal/platform/breakerstore/lua.go`

工作项：

- request Acquire 冻结日桶和成功状态；
- 新增原子成功提交脚本；
- Attempt Finish 对未提交成功的真实 transport 释放 Channel RPD；
- Request Finish 对未提交成功的请求释放 route-user RPD；
- Channel RPD 改为完整日 TTL；
- Request TPM Reserve 按输入加输出预算原子预占；
- Attempt Acquire 按候选完整预算预占 Channel TPM；
- Attempt Finish 与 Request Finish 按 usage source 原子对账并释放未使用预算；
- actual 大于 reservation 时完整记录 actual，并产生稳定 overage 观测；
- 保持 first-terminal-wins 与畸形 key fail closed。

### Request admission session

预计修改：

- `internal/service/gateway/requestadmission/session.go`
- `internal/app/gatewayapi/middleware/request_admission.go`

工作项：

- 向 attempt 绑定可信 RouteID 和冻结日桶；
- request session 终态不再无条件保留入口 RPD；
- 成功提交状态由 Redis request token 权威保存，避免仅靠进程内 bool；
- `Reserve` 接收结构化 TPM 预算；
- 将只接受账务权威值的 `PublishAuthoritativeUsage` 扩展为独立的 quota usage 发布能力，并保留 source；
- partial/local/input-fallback 计量可用于准入 TPM 对账，但不得自动升级为账务权威 usage；
- quota usage 按 `upstream > gateway_local > input_fallback` 单调升级，后续成功事实可覆盖前序 fallback provisional 事实；
- 全部 fallback 失败时从真实 transport attempt 中选择一次最大输入计量，不累加为 Route TPM；
- renewer、Finalize 和错误映射保持现有所有权边界。

### Attempt 生命周期

预计修改：

- `internal/service/gateway/lifecycle/attempt_permit.go`
- `internal/service/gateway/lifecycle/attempt_runner.go`
- `internal/service/gateway/lifecycle/attempt_runner_stream.go`
- 各协议 adapter/service 的成功边界接线文件

工作项：

- `AttemptPermitOwner` 增加幂等的成功 RPD 提交能力；
- 非流式在最终成功候选确定后提交；
- 流式在首个有效协议事件、客户 write 前提交；
- 成功提交失败或结果未知时停止执行，不继续 fallback；
- 在每个 attempt 前解析候选输出预算并传入 Permit Acquire；
- 非流式缺 usage 时从完整协议响应提取所有可计费输出字段并本地计量；
- 流式持续累计普通文本、reasoning、tool call/function arguments 等输出 Token；
- failed/partial attempt 在 fallback 或终态前发布本地 TPM 计量事实；
- 将已解析的有效输出预算注入或收紧到上游请求，确保 actual 不会正常超过 reservation；
- Finish/Abort 继续唯一收口 permit、breaker、并发与 TPM。

### 输出预算解析

预计修改：

- `internal/service/gateway/lifecycle/authorization.go`
- `internal/service/gateway/lifecycle/candidates.go`
- OpenAI Chat Completions、Responses、Responses Compact 与 Anthropic Messages 的 request mapping
- 对应 tokenizer 与测试

工作项：

- 抽取协议无关的 `ResolvedOutputBudget`，由 TPM 准入和账务授权共用；
- 客户显式上限优先，否则使用候选模型 `max_output_tokens`，再复用现有 `maxOutputTokensFallback`；
- `Candidate` 增加候选自身的输入估算和输出预算，不再只保留全池最大输入；
- request 级预算取 fallback 候选中 `input + output budget` 的最大值；
- attempt 级预算使用当前候选有效值；
- 防止 TPM 预占上限、账务授权上限和实际转发上限三套逻辑漂移。

## Admin API 与前端

### API 字段

现有 Channel 容量字段继续保留：

```text
rpd_used
rpd_limit
rpd_remaining
```

它们表示 Channel 全局容量事实，仍用于路由评分和限额判断。

新增成功归因字段，建议：

```text
route_usage.rpd_success
channels[].route_rpd_success
```

避免直接复用现有 `rpd_used`，否则会再次把全局容量与线路归因混为一谈。

预计修改：

- `internal/platform/breakerstore/route_usage.go`
- `internal/service/admin/routeruntime/runtime.go`
- `internal/app/adminapi/route/runtime.go`
- 对应 Gateway 测试

### Admin 展示

预计修改：

- `unio-admin/src/lib/api/routesOps.ts`
- `unio-admin/src/components/routes/RouteRuntimeSection.tsx`
- `unio-admin/tests/components/RouteRuntimeSection.test.tsx`

展示规则：

- 保留当前 RPM、RPD、TPM、并发短标签，不增加“入口”“Attempt”等长名称；
- 顶部所有指标继续显示该 Route 下全部用户桶的合计；
- 每个 Channel 行的主值显示 `route_rpd_success`；
- Channel 全局 RPD 容量 `rpd_used / rpd_limit` 保留在次级信息或 tooltip 中；
- Route RPM tooltip：该线路所有用户已通过入口准入的请求总数；一次客户请求只计一次，fallback 不重复计；
- Channel RPM tooltip：该 Channel 已开始真实上游 transport 的 attempt 数；429、5xx、超时和可 fallback 失败仍计入；
- RPD tooltip：只统计最终成功请求；顶部等于本线路各 Channel 成功 RPD 求和；
- Route TPM tooltip：该线路所有用户当前分钟的 request-admission TPM 实际值与在途预算合计；
- Channel TPM tooltip：该 Channel 跨 Route 的上游 TPM 实际值与在途预算合计；无权威 usage 时使用本地或输入保底计量；
- Route 并发 tooltip：该线路所有用户当前仍在 handler 生命周期内的请求；
- Channel 并发 tooltip：当前持有该 Channel AttemptPermit 的上游 attempt；
- tooltip 明确 Channel 全局容量可能包含其他 Route，只有 `route_rpd_success` 是当前线路归因值；
- 页面增加开发态断言或测试：顶部成功 RPD 等于当前线路全部 Channel 的成功 RPD 求和；
- 页面不得对 RPM、TPM 或并发增加 Route 等于 Channel 求和的断言；
- 排序和容量评分仍使用 Channel 全局 remaining，不改为线路归因值。

## 数据恢复与发布

### 旧 Redis 数据

- 现有短 TTL Channel RPD 不可信，不能迁移为成功 RPD；
- 新成功桶上线后从零开始，Admin 应显示明确的观测起点或在发布说明中声明当日数据不完整；
- 不将旧入口 RPD 直接灌入成功桶，因为无法知道各请求最终 Channel 和成功状态。
- 现有 TPM 桶只包含输入预占或旧对账语义，不能直接解释为新完整预算；发布切换时必须隔离旧、新 writer。

### TPM 版本切换

旧 Gateway 与新 Gateway 不能在同一 TPM key 上长期混跑。发布前选择一种方式：

- 方式 A（推荐，版本化）：为新 request token / permit schema 和 TPM 资源使用新版本 key；新实例只读写新版本，旧 key 等 TTL 自然回收；
- 方式 B（维护窗口）：停止新 admission，等待旧 request session 和 AttemptPermit 排空或达到明确超时，整体替换 Gateway，再从新的分钟窗口开始；
- 禁止在没有协议隔离的情况下滚动发布，因为旧实例只预占输入、新实例预占完整预算，会使限额事实不可解释；
- Admin 在切换窗口只展示当前 active 版本，不把旧、新 TPM 桶直接求和；
- 若选择版本化 key，需要同步升级 runtime snapshot、Route usage 聚合和恢复/清理逻辑。

### 可选当日回填

若要求上线当日立即完整，可在只读维护命令中基于 PostgreSQL 当前 UTC 日数据回填：

- `request_records.status = succeeded`；
- 使用 `route_id` 与 `final_channel_id`；
- 每个成功 request 只回填一次；
- 回填前停止 Gateway 写入或使用明确水位，避免与实时提交重复。

优先选择 UTC 零点发布并从零开始，避免增加一次性回填复杂度。

### Redis 状态丢失

- 成功计数是 Admin 和 RPD 连续限额的重要事实；
- Redis 实例变化后继续沿用现有 fail-closed 原则；
- 在解除基础设施 fault 前，应评估是否需要从 PostgreSQL 当前 UTC 日成功请求重建：
  - route-user RPD 成功占用；
  - Channel 全局成功占用；
  - Route 成功 RPD；
  - Route-Channel 成功 RPD；
- 若本次不实现重建，必须在 Blueprint 明确记录“Redis 状态丢失后当日成功 RPD 从恢复点重新开始”的剩余边界。

## 测试计划

### BreakerStore / Lua

- [ ] Request Acquire 只增加预占，不增加成功桶。
- [ ] Attempt Acquire 只增加 Channel 全局预占，不增加成功桶。
- [ ] pre-transport Abort 释放 Channel RPD。
- [ ] transport 已开始但网络失败时 Finish 释放 Channel RPD。
- [ ] 上游非 2xx、解析失败、无有效首字时释放 Channel RPD。
- [ ] 非流式最终成功只提交一次线路与胜出 Channel。
- [ ] 流式首个有效协议事件只提交一次。
- [ ] 同 permit 重试成功提交返回幂等成功。
- [ ] 不同 permit 对同 request 重复提交返回冲突。
- [ ] 全部候选失败时 Request Finish 释放 route-user RPD。
- [ ] 成功请求 Finish 保留 route-user 和胜出 Channel 的容量占用。
- [ ] fallback A 失败、B 成功：Route=1、A=0、B=1。
- [ ] fallback 多次失败后成功仍只计最终 Channel 一次。
- [ ] 多条 Route 共享一个 Channel：Channel 全局容量合并，Route-Channel 成功归因分别独立。
- [ ] 跨 UTC 零点请求使用冻结日桶，线路与 Channel 归因保持同日。
- [ ] 所有 RPD key TTL 覆盖完整日窗口。
- [ ] `0=不限` 仍记录预占与成功。
- [ ] 畸形 key、stale epoch、pending revision 和 store fault 均零部分写入并 fail closed。
- [ ] Request TPM Reserve 使用 `input + output budget` 检查限额并固化三项预算。
- [ ] Attempt Acquire 使用当前候选完整预算检查 Channel TPM。
- [ ] 相同 request/permit 的同预算重试幂等，不同预算冲突。
- [ ] pre-transport Abort 释放完整 TPM 预算。
- [ ] transport 已开始且无 usage、无输出时保留 input estimate，释放 output budget。
- [ ] 有本地输出计量时按 `input + local output` 对账。
- [ ] 有权威 usage 时覆盖本地计量并按权威值对账。
- [ ] actual 大于 reservation 时完整记入 actual 并记录 overage，不产生负计数。
- [ ] active reservation 与 finished actual 在容量快照中的 Used 语义正确。
- [ ] 输入估算为 `0`、后续取得正数 local/upstream actual 时仍能补记 TPM。

### 生命周期与协议

- [ ] 非流式成功在客户响应前提交。
- [ ] 非流式 2xx 但解析失败不提交并允许合法 fallback。
- [ ] 流式首个 `FirstTokenEligible` 在客户 write 前提交。
- [ ] 流式仅收到 ping/usage/finish 控制帧不提交。
- [ ] 成功点后客户端断开保留计数。
- [ ] 成功提交结果未知时不执行第二个上游 fallback。
- [ ] panic、attempt 持久化失败、permit Finish/Abort 异常不产生重复提交。
- [ ] OpenAI Chat Completions、Responses、Responses Compact 与 Anthropic Messages 均覆盖。
- [ ] 客户显式输出上限、候选模型上限和缺省策略解析一致。
- [ ] request 级输出预算取候选池保守最大值，attempt 级使用当前候选值。
- [ ] 转发给上游的输出上限不超过已预占预算。
- [ ] 完整预算超过剩余 TPM 时稳定拒绝，不自动收紧客户输出上限，也不得先超额放行。
- [ ] 非流式完整响应缺 usage 时，本地计量普通文本、reasoning 和 tool/function arguments。
- [ ] 流式每类协议事件都能累计可计费输出，控制帧不计输出。
- [ ] 流式首字前失败且无输出：Channel TPM 保留输入，允许合法 fallback。
- [ ] 流式部分输出后中断：Route/Channel TPM 使用 input + local output，不归零。
- [ ] partial settlement 可发布 quota usage，但不自动升级为账务权威 usage。
- [ ] fallback A 失败、B 成功：Route RPM=1，A RPM=1，B RPM=1；Route 成功 RPD=1，A=0，B=1。
- [ ] Permit denial 或明确 pre-transport 失败不增加 Channel RPM。

### Admin

- [ ] 顶部成功 RPD 使用 `route_usage.rpd_success`。
- [ ] Channel 主值使用 `route_rpd_success`。
- [ ] Channel 全局容量仍可查看且继续影响容量评分。
- [ ] 顶部值等于表格 Channel 成功值求和。
- [ ] 空候选、运行态不可用、stale 和 shared Channel 场景展示正确。
- [ ] Route 顶部四项正确汇总所有用户桶，不把每用户限额误当成 Route 全局上限。
- [ ] RPM tooltip 明确 Route 入口请求与 Channel 上游 attempt 的区别。
- [ ] TPM tooltip 明确 actual、active reservation 和无 usage 保底来源。
- [ ] 并发 tooltip 明确 request 生命周期与 attempt 生命周期的区别。
- [ ] RPM、TPM、并发不显示或测试错误的 Route/Channel 求和等式。

### 建议验证命令

```bash
cd /Users/chenhao/Project/unio/unio-gateway
gofmt -w <changed-go-files>
go test ./internal/platform/breakerstore/...
go test ./internal/service/gateway/requestadmission/...
go test ./internal/service/gateway/lifecycle/...
go test ./internal/service/gateway/openai/...
go test ./internal/service/gateway/anthropic/...
go test ./internal/service/admin/routeruntime/...
go test ./internal/app/adminapi/route/...
go test ./...

cd /Users/chenhao/Project/unio/unio-admin
npm test -- RouteRuntimeSection
npm run lint
npm run build
```

## 实施顺序

1. [x] 确认 3 项 TPM 产品决策：注入缺省输出上限、预算不足时直接拒绝、fallback 的 Route TPM 只计一次逻辑请求。
2. [ ] 先为当前错误行为补回归测试，固定 Channel RPD 短 TTL、失败 transport 错计 RPD、TPM 只预占输入和无 usage 归零问题。
3. [ ] 抽取 request/attempt 共用的输出预算解析，统一 TPM 准入、账务授权和上游转发上限。
4. [ ] 将 Request TPM Reserve 与 Attempt Acquire 改为完整预算预占。
5. [ ] 增加本地输出 Token 计量和 `upstream | gateway_local | input_fallback` source。
6. [ ] 新增成功 RPD key、类型、冻结字段和原子提交 Lua。
7. [ ] 修改 Attempt Finish/Abort 与 Request Finish 的 RPD/TPM 对账规则。
8. [ ] 接入非流式和流式成功点，确保 RPD 提交发生在客户可见输出之前。
9. [ ] 增加 Route 与 Route-Channel 成功聚合 API。
10. [ ] 修改 Admin tooltip，解释 RPM、RPD、TPM 和并发口径，分离成功归因与 Channel 全局容量。
11. [ ] 完成 shared Channel、fallback、跨日、TPM 预算、无 usage、幂等和故障测试。
12. [ ] 确认发布方式、UTC 零点切换或当日 PostgreSQL 回填，以及是否实现 Redis 丢失后的当日 RPD 重建。
13. [ ] 按最终代码事实更新 Blueprint：
   - `docs/products/gateway/features/admission-control.md`
   - `docs/products/gateway/decisions/adr-0007-atomic-admission-control.md`
   - `docs/products/gateway/features/request-lifecycle.md`
   - 必要时更新路由负载均衡与数据生命周期文档。
14. [ ] Blueprint 校验通过后删除本计划。

## 验收标准

- fallback 失败候选不增加成功 RPD；
- 一次最终成功请求只增加线路成功 RPD `1` 和胜出 Channel 成功 RPD `1`；
- 全部失败请求成功 RPD 为 `0`；
- 任意 Route 页面顶部成功 RPD 严格等于表格内本线路 Channel 成功 RPD 求和；
- shared Channel 不会把其他 Route 的成功数混入当前 Route；
- Channel 全局 RPD 限额仍跨 Route 生效；
- 线路和 Channel RPD 硬限额在并发情况下不超发；
- 所有 RPD 桶覆盖完整 UTC 日窗口；
- 重试、重复 Finish/Abort、进程异常和 store 错误不会重复增加或错误释放计数；
- Admin 文案能明确区分“本线路成功 RPD”和“Channel 全局容量 RPD”；
- Gateway、Admin 相关测试及全量构建通过；
- Route RPM 仍按入口允许请求计数，Channel RPM 仍按真实 transport attempt 计数；失败 fallback 不绕过 Channel RPM。
- Route 顶部 RPM、RPD、TPM、并发正确汇总全部用户桶。
- 只有成功 RPD 满足 Route 等于当前线路 Channel 求和；RPM、TPM、并发无错误求和约束。
- 有限 TPM 在并发情况下按完整输入加输出预算准入，不因输出后到而正常超发。
- 客户没有输出上限时，上游请求使用模型上限；模型也没有上限时使用 `maxOutputTokensFallback`。
- 完整预算超过剩余 TPM 时直接拒绝，不在客户不知情时自动缩小输出上限。
- fallback 时 Route TPM 只计一次逻辑请求，Channel TPM 分别保留每次真实 attempt 的已知消耗。
- transport 已开始但无权威 usage 时，Route/Channel TPM 至少保留输入；可本地计量输出时保留输入加输出。
- partial stream 和完整响应缺 usage 不再把已知 TPM 记为零。
- Admin 保持简短指标名称，通过 tooltip 清楚解释入口、attempt、成功、全局容量和计量来源。

## 非目标

- 本次不改变 RPM 和并发的现有计数口径；
- 本次不将失败 attempt 计入成功 RPD；失败 attempt 继续由 request attempts、错误率、breaker 和可观测指标表达；
- 本次不改变 Channel 全局限额主体；
- 本次不修改计费口径，计费仍以现有 request/usage/settlement 权威事实为准；
- 本次 TPM 本地计量只服务于准入限额与容量展示，不自动作为客户扣费依据；
- 本次不要求 Route RPM、TPM 或并发等于 Channel 求和；
- 本次不修改并发续租失败策略；
- 本次不通过前端求和修补后端事实，等式必须由原子成功提交和明确数据维度保证。
