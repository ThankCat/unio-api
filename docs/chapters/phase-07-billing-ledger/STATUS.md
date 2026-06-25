# Phase 7 Status

状态：in_progress（[TASK-7.23](PLAN.md#task-7-23-stream-partial-settlement) partial settlement 待实施）

## 已完成

| 任务 | 状态 | 说明 |
| --- | --- | --- |
| TASK-7.01 | done | 客户侧 price schema、provider/channel 成本价、请求级 cost snapshot 和价格生效窗口约束已完成；[GAP-7-009](../../production/TODO_REGISTER.md#gap-7-009)、[GAP-7-010](../../production/TODO_REGISTER.md#gap-7-010) 已关闭。 |
| TASK-7.02 | done | request/attempt 记录、状态机守卫、safe error message 和 internal error detail 审计字段已完成。 |
| TASK-7.03 | done | usage 记录已完成，非流式与流式 final usage source 已区分；manual adjustment 等来源属于后续后台/人工调整能力。 |
| TASK-7.04 | done | ledger credit/debit、reservation freeze/capture/release、部分余额授权、平台差额核销和外部事务内 debit 幂等重入已完成。 |
| TASK-7.05 | done | 非流式 settlement、调用上游前 authorization baseline、部分余额授权、平台差额核销、request-level settlement 成功重放和首次 settlement 失败 recovery 已完成。 |
| TASK-7.06 | done | OpenAI stream final usage 解析已完成。 |
| TASK-7.07 | done | stream 有 final usage 时可 settlement，调用上游前 authorization baseline、部分余额授权、平台差额核销、无 final usage 风险敞口记录、request/attempt 状态机守卫、request-level settlement 成功重放、首次 settlement 失败 recovery 和写出后 data-only SSE error chunk 已完成；[GAP-7-006](../../production/TODO_REGISTER.md#gap-7-006) 已关闭。 |
| TASK-7.17 | done | gateway request-level authorization、capture/release baseline、部分余额授权、平台差额核销、无 final usage 风险敞口记录和 provider/model 输入 token 估算已完成；[GAP-7-004](../../production/TODO_REGISTER.md#gap-7-004)、[GAP-7-013](../../production/TODO_REGISTER.md#gap-7-013)、[GAP-7-014](../../production/TODO_REGISTER.md#gap-7-014) 已关闭。 |
| TASK-7.18 | done | request_records 和 request_attempts 已增加 SQL 原子状态机守卫；重复终态更新会读回第一次终态事实，跨终态覆盖会返回 `requestlog_invalid_state_transition`；[GAP-7-003](../../production/TODO_REGISTER.md#gap-7-003) 已关闭。 |
| TASK-7.20 | done | provider/channel 成本价 schema、cost snapshot schema、sqlc 查询、billing 客户售价/成本价语义拆分、settlement 写入请求级 `cost_snapshots` 和幂等重放校验已完成；[GAP-7-009](../../production/TODO_REGISTER.md#gap-7-009) 已关闭。 |
| TASK-7.21 | done | safe/internal error 和 usage source 审计已完成；[GAP-7-005](../../production/TODO_REGISTER.md#gap-7-005)、[GAP-7-008](../../production/TODO_REGISTER.md#gap-7-008) 已关闭。 |
| TASK-7.22 | done | prices 已通过 PostgreSQL exclusion constraint 防止同一 model/currency/pricing_unit 出现重叠 enabled 生效窗口；[GAP-7-010](../../production/TODO_REGISTER.md#gap-7-010) 已关闭。 |
| TASK-7.19 | done | `settlement_recovery_jobs`、gateway recovery wrapper、worker claim/retry/dead 状态机和 worker-server 入口已完成；上游成功且有可靠 usage 后首次 settlement 失败不再 release，而是由 worker 幂等重试；[GAP-7-007](../../production/TODO_REGISTER.md#gap-7-007) 已关闭。 |

## 当前进行

| 任务 | 状态 | 说明 |
| --- | --- | --- |
| TASK-7.23 | P0 done | Stream partial settlement（路线 B/D）核心已落地：`usage.SourcePartialStreamEstimate`；`MarkAttemptSucceeded.final_usage_received` 改入参（partial 传 false）；`BuildPartialStreamFacts`（A2-i 合成，settlement 校验零改）；输出 token 复用 adapter tokenizer 增量计数（OpenAI tiktoken / Anthropic 估算器）；`RunStreamGeneric` 与 `message_stream.go` 两处循环按 emitted 分流（B/D partial、C release）、interrupt 重排；B4 只看 `streamFacts`；旧三处 `risk_exposure` 调用已移除（保留 settlement-failed/dead-finalize）。OpenAI + Anthropic 单测覆盖 B(cancel/interrupt)、D、!emitted release、B4，全绿（`go build`/`go vet`/`go test ./...` 通过）。 |
| TASK-7.23（migration） | done | **migration 000050**：`usage_records` + `settlement_recovery_jobs` 的 `usage_source` CHECK 增加 `partial_stream_estimate`。修复 partial 结算 / recovery job 落库被拒（线上 `settlement_recovery_jobs_usage_source_check` 报错根因）。 |
| TASK-7.23（E2E 实测） | done | **HTTP 端到端实测全部 PASS**（[BILLING_E2E_TEST_PLAN.md](BILLING_E2E_TEST_PLAN.md) §7）：真实上游 + 真实 Codex（codex exec）+ 本地 mock 覆盖路线 A/B/C/D、授权/部分授权、write_off、recovery 成功/dead、Admin 对账。全程 partial 路线 0 个 risk_exposure，仅 REC dead 记 1 个（正确）。 |
| TASK-7.23（fault inject） | done | 仅本地 E2E 用故障注入 env `BILLING_E2E_INJECT_SETTLEMENT_FAIL`（`once`=内联首结算失败由 worker 修复；`always`=raw 结算恒失败驱动 dead）。默认未设置 → 零生产影响，已在 `go test ./...` 验证（unset 后全绿）。 |

## 未完成

| 任务 | 状态 | 说明 |
| --- | --- | --- |
| TASK-7.23 P1 | todo | Admin 请求详情展示 `usage_source`（含 partial）；对用户说明 partial 计费语义。 |
| TASK-7.23 P2 | todo | 估算精度：reasoning/tool delta 计入 output、Anthropic cache 维度、Responses 直传可见文本计入、与 provider 账单抽样对账告警。 |

## 下一次进入本阶段前必须检查

```bash
rg -n "GAP-7-" docs/production/TODO_REGISTER.md cmd internal migrations sql
```

## 下节课 TODO

1. 进入阶段 8 前，按 `AGENTS.md` 扫描全局 TODO/GAP。
2. 复核阶段 8 observability / stability 的任务边界。
