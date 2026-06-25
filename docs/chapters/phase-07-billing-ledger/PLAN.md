# Phase 7 Plan - 计费与账本

## 目标

建立商业 API 网关的计费事实链路。

本阶段必须让每次请求能追溯：

```text
用户身份
项目和 API key
请求记录
上游 attempt
模型/provider/channel
usage
price snapshot
cost snapshot
ledger entry
余额变化
```

## 已完成主线

<a id="task-7-01-price-schema"></a>
### TASK-7.01 Price schema

状态：done

范围：

1. 建立客户侧价格表。
2. 支持 input/output/cached/reasoning token pricing unit。
3. provider/channel 成本价和 request-level cost snapshot 已在 TASK-7.20 完成。
4. 价格 enabled 生效窗口约束已在 TASK-7.22 完成。

关联 GAP：

- [GAP-7-009](../../production/TODO_REGISTER.md#gap-7-009) 已关闭
- [GAP-7-010](../../production/TODO_REGISTER.md#gap-7-010) 已关闭


<a id="task-7-02-request-attempt-record"></a>
### TASK-7.02 Request record 与 attempt record

状态：done

范围：

1. 创建 request record。
2. 创建 attempt record。
3. 记录 user/project/api_key/model/provider/channel。
4. 记录 succeeded/failed/canceled 状态。
5. request/attempt 状态机守卫已完成，终态不会被并发补偿或重复更新覆盖。

关联 GAP：

- [GAP-7-003](../../production/TODO_REGISTER.md#gap-7-003) 已关闭
- [GAP-7-005](../../production/TODO_REGISTER.md#gap-7-005) 已关闭


<a id="task-7-03-usage-record"></a>
### TASK-7.03 Usage record

状态：done

范围：

1. 记录 prompt/completion/total token。
2. 记录 cached/reasoning token。
3. 已区分非流式 response 与 stream final usage 来源。

关联 GAP：

- [GAP-7-008](../../production/TODO_REGISTER.md#gap-7-008) 已关闭


<a id="task-7-04-ledger-debit"></a>
### TASK-7.04 Ledger debit

状态：done

范围：

1. 支持 credit/debit ledger entry。
2. debit 使用事务更新 user balance。
3. ledger reservation pre-authorize/capture/release 已完成，并已接入 gateway request-level authorization。
4. 部分余额授权、差额核销和 settlement 幂等已完成。

关联 GAP：

- [GAP-7-011](../../production/TODO_REGISTER.md#gap-7-011)
- [GAP-7-012](../../production/TODO_REGISTER.md#gap-7-012)


<a id="task-7-05-non-stream-settlement"></a>
### TASK-7.05 非流式 settlement

状态：done

范围：

1. adapter 返回 usage 后创建 usage record。
2. 创建 price snapshot。
3. 创建 ledger debit。
4. 标记 request/attempt succeeded。
5. 调用上游前 request-level authorization 已接入。
6. 部分余额授权、差额核销、settlement 幂等和首次 settlement 失败 recovery 已完成。

关联 GAP：

- [GAP-7-001](../../production/TODO_REGISTER.md#gap-7-001)
- [GAP-7-007](../../production/TODO_REGISTER.md#gap-7-007)
- [GAP-7-012](../../production/TODO_REGISTER.md#gap-7-012)


<a id="task-7-06-stream-final-usage"></a>
### TASK-7.06 OpenAI stream final usage

状态：done

范围：

1. OpenAI stream request 设置 `stream_options.include_usage=true`。
2. adapter 解析 final usage chunk。
3. adapter stream contract 承载 usage-only chunk。
4. gateway 捕获 final usage，不向客户端转发 usage-only chunk。

<a id="task-7-07-stream-settlement"></a>
### TASK-7.07 Stream settlement

状态：done

范围：

1. 有 final usage 时执行 settlement。
2. 客户端取消但已拿到 final usage 时仍 settlement。
3. 无 final usage 时不强行估算扣费；已经可能产生上游成本的路径释放用户冻结余额，并记录 `risk_exposure`。
   **2026-06-25 修订：** emitted 且无 final usage 时改走 partial settlement（路线 B/D，见 [DEC-025](../../production/DECISIONS.md#dec-025-stream-partial-settlement)）；路线 D 另记渠道异常，不再 risk_exposure。
4. 调用上游前 request-level authorization 已接入。
5. request/attempt 状态机守卫、settlement 幂等、首次 settlement 失败 recovery 和 SSE 写出后 data-only error chunk 已完成。

关联 GAP：

- [GAP-7-002](../../production/TODO_REGISTER.md#gap-7-002)
- [GAP-7-006](../../production/TODO_REGISTER.md#gap-7-006) 已关闭
- [GAP-7-011](../../production/TODO_REGISTER.md#gap-7-011)


## 当前必须收口任务

<a id="task-7-17-preauthorization"></a>
### TASK-7.17 余额预检查与冻结闭环

状态：done

范围：

1. 请求调用上游前检查用户可用余额。
2. 非流式和流式请求已接入 request-level authorization。
3. 成功后按真实 usage capture，失败、取消、无 final usage 时 release 或进入异常状态。
4. 最终产品规则必须拆分 `estimated_amount` 与 `authorized_amount`。
5. 当 `available_balance <= 0` 时，调用上游前拒绝。
6. 当 `0 < available_balance < estimated_amount` 时，冻结全部可用余额并允许请求继续。
7. 当 `actual_amount > authorized_amount` 时，capture 已冻结金额，差额记为平台 `written_off_amount` / `platform_loss`，上游成功且有 usage 的请求仍应成功收口。
8. 为后续 project 预算或用量上限预留统一判断入口。
9. 所有余额、核销和异常动作都必须可审计。

已完成：

```text
authorization 已拆分 estimated_amount 与 authorized_amount。
available_balance <= 0 时仍会在调用上游前拒绝。
0 < available_balance < estimated_amount 时会冻结全部可用余额并允许请求继续。
actual_amount > authorized_amount 时会 capture 已冻结金额，并写入 ledger_billing_exceptions 的 write_off 事件作为平台核销事实。
stream 已经可能产生上游成本但没有 final usage 时会 release 用户冻结余额，并写入 ledger_billing_exceptions 的 risk_exposure 事件。
上游成功且有可靠 usage 的请求可按成功账务事实收口，不形成用户负余额或隐性欠费。
gateway authorization 已通过 adapter registry 调用 provider adapter 注册的 ChatInputTokenizer。
OpenAI adapter 已用 tiktoken-go/tokenizer 按 upstream model 估算 chat 输入 token。
```

剩余风险：

```text
无。后续新增 provider adapter 时必须注册自己的 ChatInputTokenizer，否则 authorization 会拒绝请求。
```

关联 GAP：

- [GAP-7-013](../../production/TODO_REGISTER.md#gap-7-013)

已关闭 GAP：

- [GAP-7-001](../../production/TODO_REGISTER.md#gap-7-001)
- [GAP-7-002](../../production/TODO_REGISTER.md#gap-7-002)
- [GAP-7-004](../../production/TODO_REGISTER.md#gap-7-004)
- [GAP-7-011](../../production/TODO_REGISTER.md#gap-7-011)
- [GAP-7-013](../../production/TODO_REGISTER.md#gap-7-013)
- [GAP-7-014](../../production/TODO_REGISTER.md#gap-7-014)


<a id="task-7-18-request-state-machine"></a>
### TASK-7.18 Request/attempt 状态机守卫

状态：done

范围：

1. 明确 pending、succeeded、failed、canceled 等状态转移。
2. SQL 更新必须带合法前置状态条件。
3. 终态不能被补偿任务或并发重试覆盖。
4. 重复终态更新应具备幂等语义。

已完成：

```text
request_records 已用 SQL 原子状态机守卫 pending -> running、running -> succeeded/failed/canceled。
request_attempts 已用 SQL 原子状态机守卫 running -> succeeded/failed/canceled。
重复写入同一终态会读回第一次终态事实，不覆盖已有 response/error/upstream metadata。
跨终态覆盖会返回 no rows，并由 requestlog.Store 映射为 requestlog_invalid_state_transition。
sqlc 测试已覆盖 request/attempt 终态幂等和非法覆盖场景。
```

关联 GAP：

- [GAP-7-003](../../production/TODO_REGISTER.md#gap-7-003)


<a id="task-7-19-settlement-idempotency"></a>
### TASK-7.19 Settlement 幂等

状态：done

范围：

1. 以 request_record_id 作为 settlement 幂等边界。已完成。
2. 重复 settlement 时检测既有 usage/snapshot/ledger。已完成。
3. 并发 settlement 不能把已成功请求误标失败。已完成。
4. 外部事务内的 ledger 幂等冲突需要稳定重入策略。已完成。
5. 上游成功且有可靠 usage 后，settlement 失败不能直接 release 冻结余额；gateway 已先持久化 recovery job。
6. settlement recovery 由 worker 持久化任务处理，不使用 gateway goroutine。已完成。
7. recovery worker 重试 settlement 前，usage、price snapshot、ledger capture 和 request 状态更新均复用 request-level 幂等 settlement。已完成。

已完成：

```text
新增 settlement_recovery_jobs 表保存 request/attempt/reservation、usage、价格快照和 route 事实。
gateway 成功拿到可靠 usage 后，会先创建 recovery job，再执行真实 settlement。
首次 settlement 失败时，非流式仍返回上游成功响应；流式有 final usage 时按成功账务事实收口，不 release 冻结余额。
worker 会 claim pending 或锁过期 running job，复用 ChatSettlementService 幂等重试 settlement。
成功后 job 标记 succeeded；失败按指数退避回到 pending；达到 max_attempts 或耗尽任务会标记 dead，等待人工处理。
```

关联 GAP：

- [GAP-7-007](../../production/TODO_REGISTER.md#gap-7-007)
- [GAP-7-012](../../production/TODO_REGISTER.md#gap-7-012) 已关闭

已关闭 GAP：

- [GAP-7-007](../../production/TODO_REGISTER.md#gap-7-007)


<a id="task-7-20-cost-snapshot"></a>
### TASK-7.20 成本价与毛利审计

状态：done

定价原则：

```text
第一版不支持倍率。
本阶段直接落地明确金额的成本价和请求级 cost snapshot。
结算层只消费明确金额，不在 settlement 时依赖模型倍率、补全倍率或分组倍率临时重算历史账单。
```

范围：

1. 区分客户侧售卖价和 provider/channel 成本价。
2. request settlement 保存 cost snapshot。
3. 支持按 channel/provider 分析毛利和 fallback 成本。
4. 第一版后台直接维护明确金额，不做倍率系统。
5. 历史账单复算以 price snapshot 和 cost snapshot 为准。

已完成：

```text
channel_cost_prices 表已建立，用 channel + model 表达 provider/channel 成本价。
cost_snapshots 表已建立，用请求级事实保存成本单价副本和实际成本金额。
channels 已增加 (id, provider_id) 唯一约束，支持 cost_snapshots 校验 channel/provider 归属一致。
billing 包已拆分客户售价计算和 provider 成本计算的命名与类型，避免把成本价快照伪装成客户售价快照。
SettleSuccessfulChat 已按最终 channel/model 和 attempt time 查询 active channel_cost_prices，计算 provider cost，并在同一笔 settlement 事务里写入 cost_snapshots。
重复 settlement 成功重放会读取既有 cost_snapshots，校验请求事实、成本单价和成本金额一致性。
```

剩余风险：

```text
GAP-7-009 已关闭。
价格 enabled 生效窗口重叠风险已由 TASK-7.22 / GAP-7-010 收口。
上游成功后的首次 settlement 失败 recovery 已由 TASK-7.19 / GAP-7-007 收口。
```

关联 GAP：

- [GAP-7-009](../../production/TODO_REGISTER.md#gap-7-009) 已关闭


<a id="task-7-21-error-usage-audit"></a>
### TASK-7.21 错误与 usage 审计字段

状态：done

范围：

1. 已区分安全展示文案和内部诊断详情：`error_message` 只保存 safe message，`internal_error_detail` 保存截断后的内部错误文本。
2. usage source 已区分非流式 `upstream_response` 和流式 final usage `upstream_stream`；manual_adjustment 等后台调整来源留到后续后台/人工调整能力。
3. 后续后台/console 请求日志默认只能展示 `error_message`；`internal_error_detail` 仅供内部 admin/排障权限查看。

关联 GAP：

- [GAP-7-005](../../production/TODO_REGISTER.md#gap-7-005) 已关闭
- [GAP-7-008](../../production/TODO_REGISTER.md#gap-7-008) 已关闭


<a id="task-7-22-price-effective-window"></a>
### TASK-7.22 价格生效窗口约束

状态：done

范围：

1. 防止同一 model/currency/pricing_unit 出现重叠 enabled 生效窗口。
2. 后台改价时事务化关闭旧价格并启用新价格。
3. 结算时价格选择确定且可审计。

已完成：

```text
prices 已使用 btree_gist + exclusion constraint，禁止同一 model/currency/pricing_unit 的 enabled 生效窗口重叠。
约束使用 [) 时间区间，允许相邻窗口无缝切换。
disabled 价格草稿允许重叠，不影响结算选择。
测试已覆盖 enabled 重叠失败、相邻窗口成功、disabled 重叠成功、不同 model/currency scope 不互相阻塞。
```

关联 GAP：

- [GAP-7-010](../../production/TODO_REGISTER.md#gap-7-010) 已关闭


<a id="task-7-23-stream-partial-settlement"></a>
### TASK-7.23 Stream partial settlement

状态：todo

范围：

1. 实现 [STREAM_PARTIAL_SETTLEMENT.md](STREAM_PARTIAL_SETTLEMENT.md) 路线 B/D：`emitted` + `streamFacts==nil` → partial settlement（B4：只看 `streamFacts`）。
2. 终态（A1/B3）：partial 后 attempt 与 request 都 `succeeded`；`MarkAttemptSucceeded` 的 `final_usage_received` 改入参，partial 传 `FALSE`（改 `sql/queries/request_attempts.sql` + `requestlog/store.go` + sqlc 重生成）。**不**把 attempt 标 failed。
3. 区分 B/D：合成 `Finish.RawReason` 落到 `upstream_finish_reason`（`stream_client_canceled_without_final_usage` / `stream_interrupted_without_final_usage` / `stream_final_usage_missing`）；D 指标 `Success`、B 指标 `Canceled`。
4. 登记 `usage.SourcePartialStreamEstimate`；`BuildPartialStreamFacts` 在 lifecycle 合成字段以通过现有 settlement 校验（A2-i，settlement/校验零改）。
5. 输出 token（B1-i）：`StreamChunkMeta` 加可见文本字段，各载体填充；复用 adapter tokenizer 增量计数（OpenAI tiktoken / Anthropic 估算器，回退启发式），估算偏保守。
6. **两处独立循环各实现一份**：`RunStreamGeneric`（OpenAI chat / Responses 直传 / Responses→chat）与 `message_stream.go`（Anthropic，**非**共享 RunStreamGeneric）；三分支按 `emitted` 分流；interrupt 分支重排 `MarkAttemptFailed` 使 partial 进入时 attempt 仍 `running`。
7. 首 token 前结束 → release（C，0 扣）；`streamFacts!=nil` → full bill（A）；partial **不**调用 `params.Finish`（不向客户端发合成 usage 尾帧）；已 emit 无 usage 流 **不再** 写 `risk_exposure`。
8. 渠道异常复用现有口径（succeeded + `final_usage_received=FALSE` + `upstream_finish_reason=stream_final_usage_missing`，统计渠道故障时排除取消）；无专用 Admin 指标。
9. 单测（OpenAI + Anthropic 各一套）：B(cancel/interrupt)、D、!emitted release、full bill 优先(B4)、capped write_off。
10. **改造完成且测试全绿后清理僵尸代码**（见 [STREAM_PARTIAL_SETTLEMENT.md §11](STREAM_PARTIAL_SETTLEMENT.md#11-改造收尾僵尸代码清理)）：移除两处循环中「已 emit 无 usage → `ReleaseAuthorizationForBillingException`/`risk_exposure`」的三处旧调用（cancel/interrupt/缺 usage）及被其取代的旧终态写入、过时断言；**保留** `stream_settlement_failed_after_upstream_success` 与 dead-finalize 的 `risk_exposure`（仍在用，勿删函数本体）。

关联决策：

- [DEC-025](../../production/DECISIONS.md#dec-025-stream-partial-settlement)（修订 [DEC-003](../../production/DECISIONS.md#dec-003-stream-无-final-usage-暂不扣费) 部分条款）

关联 GAP：

- [GAP-7-015](../../production/TODO_REGISTER.md#gap-7-015)
