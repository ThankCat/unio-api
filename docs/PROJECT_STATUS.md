# Project Status

更新时间：2026-06-01

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
当前主线：阶段 10 双协议 Gateway 全链路改造已完成规划，可进入实现
当前进度：TASK-10.01 ADR 与范围冻结已完成；双协议架构、ResponseFacts、字段矩阵、DeepSeek 双协议 mapping 和验收标准已落文档；DeepSeek Anthropic mapping 已按 2026-06-01 官方兼容表刷新
下一小节：TASK-10.02 目录迁移与依赖方向整理
上一阶段状态：阶段 9 OpenAI Protocol Parity 已收口
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
| 阶段 10 | [双协议 Gateway 全链路改造](chapters/phase-10-dual-protocol-gateway/STATUS.md) | partial | TASK-10.01 ADR 已完成；OpenAI Chat Completions + Anthropic Messages 双协议、DeepSeek 双入口、统一 ResponseFacts 和共享 lifecycle 已完成规划。 |
| 阶段 11 | [后台管理](chapters/phase-11-admin/STATUS.md) | planned | 原阶段 10 已顺延，进入前需复核 credential resolver 和后台管理边界。 |

## 当前上线阻断

公开生产 drop-in OpenAI 替换能力 Phase 9 C1~C6 已交付。Phase 10 将把 C8、OpenAI Chat Completions 与 Anthropic Messages 双协议对话链路作为商业级全量字段改造收口；本阶段不扩展图片、视频、音频、文件等模型能力，不支持的能力按模型契约和 provider mapping 前置 Reject。

完整阻断项以 [RELEASE_BLOCKERS.md](production/RELEASE_BLOCKERS.md) 为准。

## 验证状态

2026-05-31 Phase 9 收口验证：`go test ./internal/... -count=1` 全绿（含 C5~C6 typed 化 + streamtranslate + E2E）。

带 `DATABASE_URL` 的集成测试需本地 Postgres 运行；本次 SSE Writer 改动为纯 HTTP 层、无数据库接触，DB 集成测试可在本机起库后用标准命令复跑：

```bash
DATABASE_URL=postgres://unio:***@localhost:5432/unio?sslmode=disable go test ./...
go vet ./...
git diff --check
```

## 下一步

进入阶段 10 双协议 Gateway 全链路改造前先执行：

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

阶段 11 后台管理在 Phase 10 双协议 Gateway 链路稳定后进入实现。Phase 10 已完成 TASK-10.01 ADR，下一步从 TASK-10.02 开始，按小节推进目录迁移、schema、lifecycle、两个协议和 DeepSeek 双入口。
