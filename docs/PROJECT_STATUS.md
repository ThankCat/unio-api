# Project Status

更新时间：2026-05-31

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
当前主线：阶段 9 OpenAI Protocol Parity 已立项；与阶段 10 后台管理可并行，但 drop-in 兼容优先于 admin
当前进度：Phase 9 PLAN/STATUS/ACCEPTANCE 已创建；探索代码（stream translate / include_usage）待 TASK-9.07~9.09 收口
下一小节：TASK-9.01 ADR → TASK-9.02 禁止 silent drop → TASK-9.07 吸收 normalizer/ 过渡代码
上一阶段状态：阶段 8 可观测性与稳定性已收口
```

## 阶段总览

| 阶段 | 名称 | 状态 | 当前判断 |
| --- | --- | --- | --- |
| 阶段 1 | [Go Web 骨架](chapters/phase-01-go-web/STATUS.md) | partial | 基础骨架已完成，仍有 startup timeout/readiness 等生产欠账。 |
| 阶段 2 | [基础设施](chapters/phase-02-infrastructure/STATUS.md) | partial | PostgreSQL、Redis、migration、sqlc 基础能力已完成，migration runner 和 schema 版本检查仍未生产化。 |
| 阶段 3 | [用户与 API Key](chapters/phase-03-identity-api-key/STATUS.md) | partial | 用户、project、API key、认证和基础限流已完成，key 管理和 audit log 仍未完成。 |
| 阶段 4 | [OpenAI-compatible API](chapters/phase-04-openai-compatible-api/STATUS.md) | partial | `/v1/models`、`/v1/chat/completions`、SSE、严格 JSON 和 text-only Chat DTO 已完成，project 模型可见性后续扩展。 |
| 阶段 5 | [Adapter 边界](chapters/phase-05-adapter-boundary/STATUS.md) | partial | adapter 接口、OpenAI 非流式/流式、usage 映射和 SSE event reader 已完成，provider error metadata 进入阶段 8。 |
| 阶段 6 | [模型与渠道](chapters/phase-06-model-channel-routing/STATUS.md) | done | provider/channel/model/routing/fallback 和启动期 adapter preflight 已接入，后台策略推迟到阶段 10。 |
| 阶段 7 | [计费与账本](chapters/phase-07-billing-ledger/STATUS.md) | done | Gateway 计费主链路已打通，reservation、settlement、ledger、cost snapshot、recovery worker 和 stream 错误语义已收口。 |
| 阶段 8 | [可观测性与稳定性](chapters/phase-08-observability-stability/STATUS.md) | done | TASK-8.01 adapter metadata/error 分类、8.02 Prometheus metrics、8.03 structured logs+OpenTelemetry、8.04 channel 熔断、8.05 HTTP SSE Writer 全部完成；阶段 8 无遗留 P0/P1 production TODO。 |
| 阶段 9 | [OpenAI Protocol Parity](chapters/phase-09-openai-protocol-parity/STATUS.md) | planned | 公开 API 完整 OpenAI 兼容；`normalizer/` 过渡代码并入 stream response translation，不再单独定义 Normalizer 架构。 |
| 阶段 10 | [后台管理](chapters/phase-10-admin/STATUS.md) | planned | 尚未正式进入，进入前需复核 credential resolver 和后台管理边界。 |

## 当前上线阻断

公开生产 drop-in OpenAI 替换能力尚未完成；Phase 9 P0 GAP 为当前阻断项。

完整阻断项以 [RELEASE_BLOCKERS.md](production/RELEASE_BLOCKERS.md) 为准。

## 验证状态

2026-05-30 TASK-8.05 收口验证：`go build ./...`、`go vet ./...`、`go test ./...`（24 包全绿，含新增 SSE Writer 测试）、`git diff --check` 均通过。

带 `DATABASE_URL` 的集成测试需本地 Postgres 运行；本次 SSE Writer 改动为纯 HTTP 层、无数据库接触，DB 集成测试可在本机起库后用标准命令复跑：

```bash
DATABASE_URL=postgres://unio:***@localhost:5432/unio?sslmode=disable go test ./...
go vet ./...
git diff --check
```

## 下一步

进入阶段 9 OpenAI Protocol Parity 前先执行：

```bash
rg -n "TODO|GAP-" AGENTS.md docs cmd internal migrations sql
rg -n "normalizer|Normalizer" docs internal
```

然后阅读：

```text
docs/chapters/phase-09-openai-protocol-parity/PLAN.md
docs/chapters/phase-09-openai-protocol-parity/STATUS.md
docs/chapters/phase-09-openai-protocol-parity/ACCEPTANCE.md
docs/chapters/phase-09-openai-protocol-parity/COMPATIBILITY_MATRIX.md
docs/production/TODO_REGISTER.md      ← 复核 GAP-9-001~004
docs/production/DECISIONS.md
docs/production/RELEASE_BLOCKERS.md
```

阶段 10 后台管理可在 Phase 9 C1~C4 推进同时并行规划；若产品目标是 drop-in 替换 OpenAI，Phase 9 应优先于 Admin CRUD。
