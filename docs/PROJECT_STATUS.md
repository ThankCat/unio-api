# Project Status

更新时间：2026-06-02

本文档只记录全局当前状态、阶段索引、上线阻断和下一步。

阶段细节、任务清单、GAP 收口过程和长交接内容，统一维护在对应章节文档：

```text
docs/chapters/phase-xx-*/PLAN.md
docs/chapters/phase-xx-*/STATUS.md
docs/chapters/phase-xx-*/ACCEPTANCE.md
docs/production/TODO_REGISTER.md
```

## 当前焦点

```text
当前主线：阶段 10 双协议 Gateway 全链路改造收口阶段
当前进度：双协议 ingress + adapter + 共享 lifecycle + SDK 黑盒回归 + durable closeout 全部完成；**2026-06-02 TASK-10.05 架构 B 终局泛型化已收口**——抽出 `lifecycle.RequestLifecycle` 集中承载协议无关 channel breaker / authorization release / metrics / request log 全部实现，两侧 service 的 thin wrapper 改为 1-line forward，消除 ~290 行字面相同的两侧复制代码。`go vet` + `go build` + `go test ./internal/...` + `go test -tags=blackbox`（OpenAI SDK 16 + Anthropic SDK 14 用例）全部通过。
下一小节：阶段 10 ACCEPTANCE sign-off + 阶段 11（后台管理）启动复核；可选回归（DS-OAI-05/06/08、DS-ANT-15）排到适当窗口
上一阶段状态：阶段 9 OpenAI Protocol Parity 已收口；阶段 10 双协议 Gateway 主线 + 共享 lifecycle 终局已收口
```

## 阶段总览

| 阶段 | 名称 | 状态 | 当前判断 |
| --- | --- | --- | --- |
| 阶段 1 | [Go Web 骨架](chapters/phase-01-go-web/STATUS.md) | partial | 基础骨架已完成，仍有 startup timeout/readiness 等生产欠账。 |
| 阶段 2 | [基础设施](chapters/phase-02-infrastructure/STATUS.md) | partial | PostgreSQL、Redis、migration、sqlc 基础能力已完成，migration runner 和 schema 版本检查仍未生产化。 |
| 阶段 3 | [用户与 API Key](chapters/phase-03-identity-api-key/STATUS.md) | partial | 用户、project、API key、认证和基础限流已完成，key 管理和 audit log 仍未完成。 |
| 阶段 4 | [OpenAI-compatible API](chapters/phase-04-openai-compatible-api/STATUS.md) | partial | `/v1/models`、`/v1/chat/completions`、SSE、严格 JSON 和 text-only Chat DTO 已完成，project 模型可见性后续扩展。 |
| 阶段 5 | [Adapter 边界](chapters/phase-05-adapter-boundary/STATUS.md) | partial | adapter 接口、OpenAI 非流式/流式、usage 映射和 SSE event reader 已完成，provider error metadata 进入阶段 8。 |
| 阶段 6 | [模型与渠道](chapters/phase-06-model-channel-routing/STATUS.md) | done | provider/channel/model/routing/fallback 和启动期 adapter preflight 已接入，后台策略推迟到阶段 11。 |
| 阶段 7 | [计费与账本](chapters/phase-07-billing-ledger/STATUS.md) | done | Gateway 计费主链路已打通，reservation、settlement、ledger、cost snapshot、recovery worker 和 stream 错误语义已收口。 |
| 阶段 8 | [可观测性与稳定性](chapters/phase-08-observability-stability/STATUS.md) | done | TASK-8.01 adapter metadata/error 分类、8.02 Prometheus metrics、8.03 structured logs+OpenTelemetry、8.04 channel 熔断、8.05 HTTP SSE Writer 全部完成；阶段 8 无遗留 P0/P1 production TODO。 |
| 阶段 9 | [OpenAI Protocol Parity](chapters/phase-09-openai-protocol-parity/STATUS.md) | done | C1~C6 已实现；C8 高级字段并入阶段 10 全量 OpenAI 契约，不再作为长期可选项。 |
| 阶段 10 | [双协议 Gateway 全链路改造](chapters/phase-10-dual-protocol-gateway/STATUS.md) | partial | 双协议 adapter + ingress 已通；共享 lifecycle（候选准备/熔断/retry/metrics/tracing/authorization/settlement/recovery/request log helpers）已抽出并被 OpenAI + Anthropic 共用；`/v1/chat/completions` 与 `/v1/messages` 均端到端接线并有 parity e2e；10.12 facts schema + 账务回归全绿；10.11 双协议错误与安全输出已收口（上游分类映射 + 脱敏 + anthropic-beta 宽进接受/出站 Drop，DEC-013）；10.15 命名与冗余复核已收口；10.13 OpenAI SDK 黑盒验收已收口（14 mock + 2 真实 DeepSeek smoke）；10.14 Anthropic SDK 黑盒验收已收口（12 mock + 2 真实 DeepSeek Anthropic endpoint smoke）；10.10 durable closeout 终局已收口（fallback 同步终态推进契约黑盒实证）；**10.05 共享 Lifecycle Executor 架构 B 终局已收口（2026-06-02，抽出 `lifecycle.RequestLifecycle` 集中承载协议无关 channel breaker/release/metrics/request log 全部实现，两侧 service 的 thin wrapper 改为 1-line forward，消除 ~290 行字面相同复制代码；nil-safe 纯只读/指标方法保留单元测试灵活性）**。待办：阶段 10 ACCEPTANCE sign-off + 可选回归（DS-OAI-05/06/08、DS-ANT-15）。 |
| 阶段 11 | [后台管理](chapters/phase-11-admin/STATUS.md) | planned | 原阶段 10 已顺延，进入前需复核 credential resolver 和后台管理边界。 |

## 当前上线阻断

公开生产 drop-in OpenAI 替换能力 Phase 9 C1~C6 已交付。Phase 10 将把 C8、OpenAI Chat Completions 与 Anthropic Messages 双协议对话链路作为商业级全量字段改造收口；本阶段不扩展图片、视频、音频、文件等模型能力，不支持的能力按模型契约和 provider mapping 前置 Reject。

完整阻断项以 [RELEASE_BLOCKERS.md](production/RELEASE_BLOCKERS.md) 为准。

## 验证状态

2026-06-02 Phase 10（DEC-012 双侧 + TASK-10.12B 收口后）：本地库已 `drop`→`up` 重建到新 facts schema，`sqlc generate` 无 drift。

```bash
go build ./internal/... ./cmd/...   # 通过
go vet ./internal/... ./cmd/...     # 通过
DATABASE_URL=postgres://unio:***@localhost:5432/unio?sslmode=disable \
  go test ./internal/...            # 全绿（含 ledger/sqlc/chatcompletions 集成测试）
git diff --check                    # 通过
```

仓库级 `go test ./...` 仍会被既有 `seed/` 目录双 `main` 阻断，与 Phase 10 无关。

带 `DATABASE_URL` 的集成测试需本地 Postgres；改表源 migration 后，本地库需先 `drop`/`down`→`up` 再跑 sqlc/DB 测试。

## 下一步

进入阶段 10 ACCEPTANCE sign-off / 可选回归 / 阶段 11 启动复核前先执行：

```bash
rg -n "TODO|GAP-" AGENTS.md docs cmd internal migrations sql
rg -n "streamtranslate|providers\\.adapter|adapter\\.ChatRequest|adapter\\.ChatResponse|adapter\\.ChatUsage" docs internal migrations sql
```

然后阅读：

```text
docs/protocol/openai_chat_completion.md
docs/protocol/anthropic_message.md
docs/chapters/phase-10-dual-protocol-gateway/PLAN.md
docs/chapters/phase-10-dual-protocol-gateway/STATUS.md
docs/chapters/phase-10-dual-protocol-gateway/ACCEPTANCE.md
docs/chapters/phase-10-dual-protocol-gateway/ARCHITECTURE.md
docs/chapters/phase-10-dual-protocol-gateway/RESPONSE_FACTS.md
docs/chapters/phase-10-dual-protocol-gateway/OPENAI_CHAT_COMPLETIONS_MATRIX.md
docs/chapters/phase-10-dual-protocol-gateway/ANTHROPIC_MESSAGES_MATRIX.md
docs/chapters/phase-10-dual-protocol-gateway/DEEPSEEK_OPENAI_MAPPING.md
docs/chapters/phase-10-dual-protocol-gateway/DEEPSEEK_ANTHROPIC_MAPPING.md
docs/production/TODO_REGISTER.md
docs/production/DECISIONS.md
docs/production/RELEASE_BLOCKERS.md
```

阶段 11 后台管理在 Phase 10 双协议 Gateway 链路稳定后进入实现。Phase 10 当前主线：DEC-012 协议为先 Drop（双侧）、TASK-10.12 facts 账务回归、TASK-10.10B-2b（Anthropic service/handler + 共享 lifecycle + e2e parity）、TASK-10.11（双协议错误与安全输出 + DEC-013 anthropic-beta 宽进）、TASK-10.15（命名与冗余复核）、TASK-10.13（OpenAI SDK 黑盒验收）、TASK-10.14（Anthropic SDK 黑盒验收）与 **TASK-10.10 durable closeout 终局（fallback 同步终态推进契约黑盒实证）** 与 **10.05 架构 B 终局（`lifecycle.RequestLifecycle` thin wrapper 收口）** 均已完成。下一步：阶段 10 ACCEPTANCE sign-off + 可选回归（DS-OAI-05/06/08、DS-ANT-15）+ 阶段 11 启动复核。
