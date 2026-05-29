# Project Status

更新时间：2026-05-29

本文档只记录全局当前状态、阶段索引、上线阻断和下一步。

阶段细节、任务清单、GAP 收口过程和长交接内容，统一维护在对应章节文档：

```text
docs/chapters/phase-xx-*/PLAN.md
docs/chapters/phase-xx-*/STATUS.md
docs/chapters/phase-xx-*/ACCEPTANCE.md
docs/chapters/phase-xx-*/HANDOFF.md
docs/production/TODO_REGISTER.md
```

## 当前焦点

```text
当前主线：阶段 8 可观测性与稳定性
当前进度：TASK-8.01~8.04 已完成，只剩 TASK-8.05 HTTP SSE Writer
下一小节：TASK-8.05 HTTP SSE Writer（关联 GAP-8-002）
上一阶段状态：阶段 7 计费与账本已收口
```

## 阶段总览

| 阶段 | 名称 | 状态 | 当前判断 |
| --- | --- | --- | --- |
| 阶段 1 | [Go Web 骨架](chapters/phase-01-go-web/STATUS.md) | partial | 基础骨架已完成，仍有 startup timeout/readiness 等生产欠账。 |
| 阶段 2 | [基础设施](chapters/phase-02-infrastructure/STATUS.md) | partial | PostgreSQL、Redis、migration、sqlc 基础能力已完成，migration runner 和 schema 版本检查仍未生产化。 |
| 阶段 3 | [用户与 API Key](chapters/phase-03-identity-api-key/STATUS.md) | partial | 用户、project、API key、认证和基础限流已完成，key 管理和 audit log 仍未完成。 |
| 阶段 4 | [OpenAI-compatible API](chapters/phase-04-openai-compatible-api/STATUS.md) | partial | `/v1/models`、`/v1/chat/completions`、SSE、严格 JSON 和 text-only Chat DTO 已完成，project 模型可见性后续扩展。 |
| 阶段 5 | [Adapter 边界](chapters/phase-05-adapter-boundary/STATUS.md) | partial | adapter 接口、OpenAI 非流式/流式、usage 映射和 SSE event reader 已完成，provider error metadata 进入阶段 8。 |
| 阶段 6 | [模型与渠道](chapters/phase-06-model-channel-routing/STATUS.md) | done | provider/channel/model/routing/fallback 和启动期 adapter preflight 已接入，后台策略推迟到阶段 9。 |
| 阶段 7 | [计费与账本](chapters/phase-07-billing-ledger/STATUS.md) | done | Gateway 计费主链路已打通，reservation、settlement、ledger、cost snapshot、recovery worker 和 stream 错误语义已收口。 |
| 阶段 8 | [可观测性与稳定性](chapters/phase-08-observability-stability/STATUS.md) | in_progress | TASK-8.01 adapter metadata/error 分类、8.02 Prometheus metrics、8.03 structured logs+OpenTelemetry、8.04 channel 熔断均已完成；只剩 TASK-8.05 HTTP SSE Writer。 |
| 阶段 9 | [后台管理](chapters/phase-09-admin/STATUS.md) | planned | 尚未正式进入，进入前需复核 credential resolver 和后台管理边界。 |

## 当前上线阻断

当前没有阶段 8 P0 release blocker。

完整阻断项以 [RELEASE_BLOCKERS.md](production/RELEASE_BLOCKERS.md) 为准。

## 验证状态

最近一次验证通过（含 DATABASE_URL 集成测试，24 包全绿）：

```bash
DATABASE_URL=postgres://unio:***@localhost:5432/unio?sslmode=disable go test ./...
go vet ./...
git diff --check
```

## 下一步

继续阶段 8 最后一节 TASK-8.05 前先执行：

```bash
rg -n "TODO|GAP-" AGENTS.md docs cmd internal migrations sql
```

然后阅读：

```text
docs/chapters/phase-08-observability-stability/HANDOFF.md   ← 今日交接，优先读
docs/chapters/phase-08-observability-stability/PLAN.md      ← TASK-8.05 计划
docs/chapters/phase-08-observability-stability/STATUS.md
docs/production/TODO_REGISTER.md
```
