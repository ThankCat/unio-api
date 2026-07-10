# DESIGN: bill-on-cancel 渠道的成本对账

状态：阶段一已实施（2026-07-10）；阶段二 gated（待阶段一敞口数据评估）；阶段三待提案

阶段一实施与开放问题落地（2026-07-10）：

- `assumed_output_tokens` 口径（开放问题 1）：采用保守上界 = 候选模型 `max_output_tokens`，
  未配置时回退进程级 `AUTHORIZATION_MAX_OUTPUT_TOKENS_FALLBACK`（与预授权冻结上界同源）。
  动态估算（近 7 天成功 p50）留待阶段二一并评估。
- reason 枚举定稿：`upstream_timeout`（等首字节超时）/ `upstream_error`（5xx/传输层失败）/
  `client_canceled`（客户端在生成期间断开）。鉴权/限流/bad_request 是生成前拒绝，不记敞口。
- 双计防护：流式「已 emit」路径走 partial settlement（已有 cost_snapshots），不记敞口；
  只有「无任何结算成本落库」的失败/取消路径记敞口。
- 写入语义：best-effort + detached context（客户端断开也能写入），失败仅 warn 不阻断请求收口。
- 入口：渠道标记 `channels.upstream_bills_on_disconnect`（admin 渠道表单「断开仍计费」开关）；
  查询：`GET /admin/v1/channels/cost-exposures/summary?from&to`（按渠道聚合，默认近 7 天）与
  `GET /admin/v1/channels/{id}/cost-exposures`（明细分页）。

关联：DEC-029（并发上限 + 失败软冷却，已实施）、DEC-025（partial settlement）、DEC-028（TPM 预占释放）

## 1. 背景与问题

sub2api 类中转上游（如 anthropic_0.16 渠道背后的 `new.codex521.cc`）对流式请求采用
「断开不取消」模型：unio 因 header 超时/客户端取消断开连接后，上游用
`context.WithoutCancel` 继续向真实供应商生成、drain 到终止事件拿完整 usage、**照常从
unio 的渠道账户扣费**（源码证据：sub2api `gateway_anthropic_passthrough.go`
"continue draining upstream for usage" + `detachedBillingContext`）。

这造成三个账务问题：

1. **平台成本被系统性低估**：unio 侧「首字节前失败」的请求记 $0 成本并释放冻结，
   但渠道侧实际按完整补全扣了钱。事故窗口（07-06~09）中该渠道 110 次失败请求
   在 unio 账本上成本为 0，实际全部产生了上游成本。
2. **渠道毛利/成本对比失真**：dashboard 的渠道成本只统计结算成功的请求，
   bill-on-cancel 渠道的真实成本恒被低估，横向选渠道时误导运营。
3. **重试双重扣费不可见**：客户端重试 + fallback 到同一 bill-on-cancel 后端时，
   两次都被上游扣费，unio 只结算成功那次——损耗完全不可观测。

## 2. 目标与非目标

目标：

- 平台侧能看到 bill-on-cancel 渠道「失败请求也在烧钱」的估算/真实成本。
- 尽可能把「已付费但被丢弃」的上游产出转化为可结算事实（真实 usage）。
- 给渠道加成本预算护栏（对齐 OpenRouter spend cap 思路）。

非目标：

- 不改变客户侧计费口径（客户没收到内容就不扣客户的钱，DEC-025 语义不变）。
- 不试图让上游停止计费（上游行为不可控）。

## 3. 方案总览（三阶段，可独立交付）

### 阶段一：渠道属性 + 成本敞口影子记账（快解，低风险）

1. `channels` 新增 `upstream_bills_on_disconnect BOOLEAN NOT NULL DEFAULT FALSE`
   （admin 可编辑；语义：断开连接后上游仍会完成生成并计费）。
2. 新表 `channel_cost_exposures`（一张表一组 migration）：

   ```text
   id / request_record_id(FK) / attempt_id(FK) / channel_id(FK) / provider_id(FK)
   reason TEXT CHECK IN ('header_timeout','stream_interrupted','client_canceled')
   estimated_input_tokens BIGINT   -- 复用预授权阶段 ConservativeInputTokens
   assumed_output_tokens  BIGINT   -- 估算口径见 §4
   estimated_cost_amount  NUMERIC  -- 按渠道 channel_prices 成本价折算
   currency / created_at
   ```

3. 写入时机：attempt 以 timeout / 传输层失败（send/read stream failed）终态、且命中渠道
   `upstream_bills_on_disconnect=true` 时，由 lifecycle 在 MarkAttemptFailed 旁写入
   （best-effort，失败仅告警不阻断请求收口）。
4. 消费：dashboard 渠道成本视图叠加 `结算成本 + SUM(exposures)`，请求详情页展示
   「本次失败可能已产生上游成本 ≈ $X」。

验收：事故重放（对 mock 上游打 timeout）后，渠道成本视图出现敞口金额；
正常渠道（flag=false）零新增行为。

### 阶段二：后台 drain 转正（把"失败"变成"慢成功/真实成本"）

对 `upstream_bills_on_disconnect=true` 渠道的流式请求，header 超时/客户端取消时
**不取消上游连接**，改为移交进程内 drainer：

1. adapter 层：`StreamTimeoutContext` 的 header 超时不再 cancel 整个流 ctx，而是
   发出「移交信号」；HTTP 响应体的所有权转移给 drainer goroutine（detached context，
   上限受 `drain_max_duration`（建议 = stream idle timeout）与 drainer 并发上限约束）。
2. drainer 读到终止事件（含 usage）后：
   - 客户已断开：不向客户交付；以真实 usage 写一条 `channel_cost_exposures`
     （reason 升级为 `drained_with_usage`，金额=真实成本），客户侧仍不扣费。
   - 客户仍在（仅渠道 timeout、请求被 fallback 接管的场景）：丢弃 drain 产出
     （fallback 已经在别的渠道交付），照记成本敞口。
3. 资源护栏：drainer 全局并发上限（复用 DEC-029 ConcurrencyLimiter，drain 占用渠道
   在途名额直到读完）；超限时退化为阶段一的估算记账。
4. 状态机：attempt 保持 failed 终态不变（客户视角事实不变），drain 结果只进
   exposures 表——**不引入新的 attempt 状态**，把复杂度锁在旁路。

验收：mock 上游「header 超时后 30s 才回完整流」场景，unio 请求按现状失败/fallback，
但 exposures 表 5 分钟内出现带真实 usage 的成本行；drainer 并发达到上限时优雅降级。

### 阶段三：渠道消费上限（spend cap，可选）

`channels.daily_cost_budget` + worker 每小时聚合（结算成本 + 敞口）超预算即自动停用渠道
并通知（复用渠道检测的通知路径）。对齐 OpenRouter「设硬性消费上限」的运营建议。

## 4. 估算口径（阶段一）

- `estimated_input_tokens`：预授权 ConservativeInputTokens（已有）。
- `assumed_output_tokens`：min(客户 max_tokens, 模型 max_output_tokens, 全局兜底)。
  偏保守（按上限估）；阶段二拿到真实 usage 后覆盖。
- 金额 = 按 attempt 命中渠道的 `channel_prices` 成本价折算（复用 billing 计价函数，
  不 float、NUMERIC 全程）。

## 5. 与现有机制的交互

- **并发上限（DEC-029）**：阶段二 drain 占用渠道在途名额（真实占用上游容量），
  防止 drain 风暴；名额不足时跳过 drain 退化为估算。
- **熔断/软冷却**：drain 结果不回喂熔断（原始失败已记录一次，避免双计）。
- **TPM 回填（DEC-028）**：drain 拿到真实 usage 后不回填 TPM（请求已收口、预占已释放，
  回填会造成负漂移）；仅记成本。
- **settlement**：exposures 与 ledger 完全隔离——不动客户余额、不进 usage_records，
  纯平台侧成本观测。审计边界清晰，出错最多是"成本高估/低估"，不可能错扣客户。

## 6. 开放问题（评审时定）

1. `assumed_output_tokens` 按 max 上限估算偏保守，是否改用「该渠道近 7 天成功请求的
   p50 输出」动态估算？（更准但引入统计依赖）
2. 阶段二 drainer 进程内实现 vs 交给 worker-server（跨进程需传递 HTTP 响应体所有权，
   进程内简单得多；单实例部署下建议进程内）。
3. exposures 保留策略（建议按 request_records 同周期清理）。

## 7. 排期建议

阶段一 ~1 天（表 + 写入点 + dashboard 叠加）；阶段二 ~3-5 天（adapter 移交 + drainer +
护栏 + E2E）；阶段三 ~1 天。建议先只做阶段一，观察敞口金额量级再决定阶段二是否值得。
