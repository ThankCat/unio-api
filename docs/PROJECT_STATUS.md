# Project Status

更新时间：2026-06-05

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
当前主线：阶段 11 OpenAI Responses API ingress（Codex 兼容，生产级）实现中——`POST /v1/responses` 主路径（非流式 + 流式 SSE）已落地
当前进度：阶段 10 双协议 Gateway 已于 2026-06-03 完成 acceptance sign-off，状态改为 done。双协议 ingress + adapter + 共享 lifecycle + SDK 黑盒回归 + durable closeout 全部收口；`sqlc generate`、`go build ./internal/... ./cmd/...`、`go vet ./internal/... ./cmd/...`、`go test ./internal/... ./cmd/...`、`git diff --check`、OpenAI SDK blackbox 与 Anthropic SDK blackbox 均通过。真实 DeepSeek smoke 本次未额外打开密钥 gate。
阶段调整：OpenAI Responses API ingress 上提为阶段 11；新立阶段 12 Capability Architecture（能力声明 + 运行时闸门 + models.dev daily cron + cap-tags API，见 DEC-015）；原后台管理再次顺延为阶段 13（占位文档已就位）。
下一小节：阶段 11 协议冻结（TASK-11.01）与请求翻译（TASK-11.04/11.05）已完成（含真实 Codex v0.130 抓包 fixture 交叉确认）。TASK-11.06/11.07 已落地 `POST /v1/responses` 非流式编排 + 流式命名事件状态机，并采纳方案 B 抽出共享 `lifecycle.AttemptRunner`，让 OpenAI chat 与 responses 复用同一份资金关键候选 fallback 循环（Anthropic Messages 保留自身循环）；`go build ./...` 与 `go test ./internal/... ./cmd/...` 全过、0 失败，路由已挂 `POST /v1/responses`。剩余：其余 endpoint（compact/input_tokens/有状态 501，TASK-11.11~11.13 路由未挂）、DeepSeek reasoning_effort 复核（TASK-11.09）、工具/错误补齐（TASK-11.08/11.10）、黑盒验收与 fixture 脱敏（TASK-11.15）。阶段 12 启动条件：阶段 11 公开 API 表面冻结后，先迁 `CAPABILITY_MATRIX.md` 静态约定到 `model_capabilities` 表。
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
| 阶段 6 | [模型与渠道](chapters/phase-06-model-channel-routing/STATUS.md) | done | provider/channel/model/routing/fallback 和启动期 adapter preflight 已接入，后台策略推迟到阶段 13。 |
| 阶段 7 | [计费与账本](chapters/phase-07-billing-ledger/STATUS.md) | done | Gateway 计费主链路已打通，reservation、settlement、ledger、cost snapshot、recovery worker 和 stream 错误语义已收口。 |
| 阶段 8 | [可观测性与稳定性](chapters/phase-08-observability-stability/STATUS.md) | done | TASK-8.01 adapter metadata/error 分类、8.02 Prometheus metrics、8.03 structured logs+OpenTelemetry、8.04 channel 熔断、8.05 HTTP SSE Writer 全部完成；阶段 8 无遗留 P0/P1 production TODO。 |
| 阶段 9 | [OpenAI Protocol Parity](chapters/phase-09-openai-protocol-parity/STATUS.md) | done | C1~C6 已实现；C8 高级字段并入阶段 10 全量 OpenAI 契约，不再作为长期可选项。 |
| 阶段 10 | [双协议 Gateway 全链路改造](chapters/phase-10-dual-protocol-gateway/STATUS.md) | done | 双协议 adapter + ingress、共享 lifecycle、`/v1/chat/completions` 与 `/v1/messages` 端到端链路、facts schema、账务回归、错误安全输出、SDK 黑盒验收、durable closeout 和 10.05 架构 B 终局均已收口；2026-06-03 acceptance sign-off 通过。 |
| 阶段 11 | [OpenAI Responses API ingress](chapters/phase-11-openai-responses-api/STATUS.md) | in_progress | 新增 `POST /v1/responses`（Codex 兼容，生产级），responses-to-chat 桥接复用 OpenAI adapter/lifecycle/账务。主路径（非流式 + 流式 SSE，TASK-11.04~11.07 done）已落地，方案 B 抽共享 `AttemptRunner` 与 chat 复用资金关键循环。剩余 compact/input_tokens/有状态 endpoint、DeepSeek 复核与黑盒验收。 |
| 阶段 12 | [能力架构 Capability Architecture](chapters/phase-12-capability-architecture/STATUS.md) | planned | 把"协议字段 vs 模型能力"从静态文档升级为运行时事实：`models` / `model_capabilities` / `channel_capability_overrides` 表 + ingress capability inference + routing capability filter + models.dev daily cron + cap-tags API；observe → enforce 灰度切换。决策见 DEC-015，欠账 GAP-12-001 ~ GAP-12-009。 |
| 阶段 13 | [后台管理](chapters/phase-13-admin/STATUS.md) | planned | 原后台管理再次顺延为阶段 13，排在能力架构之后；进入前需复核 credential resolver、admin auth、CRUD 与能力管理后台接入。 |

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
docs/chapters/phase-11-openai-responses-api/PLAN.md
docs/chapters/phase-11-openai-responses-api/RESPONSES_CHAT_BRIDGE.md
docs/chapters/phase-11-openai-responses-api/STATUS.md
docs/chapters/phase-11-openai-responses-api/ACCEPTANCE.md
docs/production/TODO_REGISTER.md
docs/production/DECISIONS.md
docs/production/RELEASE_BLOCKERS.md
```

阶段 11 OpenAI Responses API ingress 现在可以进入启动前复核。优先处理：TASK-11.01 用真实 Codex `/responses` 抓包冻结 Responses↔Chat 字段映射（清零 `RESPONSES_CHAT_BRIDGE.md` 的 `Verify`）；TASK-11.02 冻结模型指定与路由策略（客户 model → 受支持上游模型，是否引入别名）；命名 SSE 事件状态机与工具/reasoning（apply_patch）处理边界；以及进入实现前对全局 P0/P1 TODO 的再次筛查。

阶段 11 收口后进入阶段 12 能力架构（Capability Architecture，DEC-015）：先落 schema（TASK-12.01）与 models.dev 同步（TASK-12.04）作为种子，再做 ingress capability inference（TASK-12.02）+ routing capability filter（TASK-12.03）的 observe 模式上线，最后切 enforce。阶段 12 不动公开 API 表面与账务事实；阶段 11 静态 `CAPABILITY_MATRIX.md` 在阶段 12 完成时迁入运行时表。

阶段 13 后台管理顺延：credential resolver、admin auth、CRUD、capability 后台编辑等复核随其排期推进，依赖阶段 12 schema 与公开 cap-tags 列表。
