# Project Status

更新时间：2026-06-03

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
当前主线：阶段 11 后台管理启动前边界复核
当前进度：阶段 10 双协议 Gateway 已于 2026-06-03 完成 acceptance sign-off，状态改为 done。双协议 ingress + adapter + 共享 lifecycle + SDK 黑盒回归 + durable closeout 全部收口；`sqlc generate`、`go build ./internal/... ./cmd/...`、`go vet ./internal/... ./cmd/...`、`go test ./internal/... ./cmd/...`、`git diff --check`、OpenAI SDK blackbox 与 Anthropic SDK blackbox 均通过。真实 DeepSeek smoke 本次未额外打开密钥 gate。
下一小节：进入阶段 11 前置复核；重点复核 credential resolver、admin auth、audit log、provider/channel/model/price CRUD 和 project policy 管理边界
上一阶段状态：阶段 10 双协议 Gateway 已完成；阶段 9 OpenAI Protocol Parity 已收口
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
| 阶段 10 | [双协议 Gateway 全链路改造](chapters/phase-10-dual-protocol-gateway/STATUS.md) | done | 双协议 adapter + ingress、共享 lifecycle、`/v1/chat/completions` 与 `/v1/messages` 端到端链路、facts schema、账务回归、错误安全输出、SDK 黑盒验收、durable closeout 和 10.05 架构 B 终局均已收口；2026-06-03 acceptance sign-off 通过。 |
| 阶段 11 | [后台管理](chapters/phase-11-admin/STATUS.md) | planned | 原阶段 10 已顺延，进入前需复核 credential resolver 和后台管理边界。 |

## 当前上线阻断

公开生产 drop-in OpenAI 替换能力 Phase 9 C1~C6 已交付。Phase 10 已把 C8、OpenAI Chat Completions 与 Anthropic Messages 双协议对话链路作为商业级全量字段改造收口；本阶段不扩展图片、视频、音频、文件等模型能力，provider 无法转换的合法协议字段按 DEC-012 在 adapter 出站 Drop，不因 provider 能力在 ingress 400。

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

2026-06-03 Phase 10 acceptance sign-off：本次未修改生产代码，最终验收命令通过。

```bash
sqlc generate                                      # 通过
go build ./internal/... ./cmd/...                  # 通过
go vet ./internal/... ./cmd/...                    # 通过
go test ./internal/... ./cmd/...                   # 通过
git diff --check                                   # 通过
go test -tags=blackbox -count=1 ./internal/blackbox/openaisdk/...      # 通过
go test -tags=blackbox -count=1 ./internal/blackbox/anthropicsdk/...   # 通过
```

说明：本地沙箱禁止 `httptest` 监听端口，`go test ./internal/... ./cmd/...` 与两套 SDK blackbox 已在非沙箱环境重跑并通过。真实 DeepSeek smoke 本次未额外打开 `DEEPSEEK_BLACKBOX` / `DEEPSEEK_API_KEY` gate。

## 下一步

阶段 10 已签核完成。进入阶段 11 前先执行：

```bash
rg -n "TODO|GAP-" AGENTS.md docs cmd internal migrations sql
rg -n '^\| <a id="gap-[^"]+"></a>\[[^\]]+\].*\| P[01] \| (todo|deferred) \|' docs/production/TODO_REGISTER.md
```

然后阅读：

```text
docs/PROJECT_STATUS.md
docs/chapters/README.md
docs/chapters/phase-11-admin/PLAN.md
docs/chapters/phase-11-admin/STATUS.md
docs/chapters/phase-11-admin/ACCEPTANCE.md
docs/production/TODO_REGISTER.md
docs/production/DECISIONS.md
docs/production/RELEASE_BLOCKERS.md
```

阶段 11 后台管理现在可以进入启动前复核。优先处理：`GAP-6-001` credential resolver、安全轮换与后台管理边界；admin auth 与 audit log；provider/channel/model/price CRUD；project policy 管理；以及进入实现前对全局 P0/P1 TODO 的再次筛查。
