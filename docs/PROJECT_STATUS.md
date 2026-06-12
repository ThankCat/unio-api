# Project Status

更新时间：2026-06-12

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
当前主线：阶段 13 后台管理（admin-server，垂直切片推进）。**产品定位已由 DEC-017 锚定：分档网关（卖档 Path B）**——对外售卖单位是模型/档（每档独立 model_id），provider/channel 对用户隐藏，售价覆盖档内最贵渠道，路由/重试锁档内绝不降级；全透明聚合市场（OpenRouter 式）顺延为「有量之后的第二产品线」。后台 = 运营内部工具。**Slice 2 已交付（2026-06-11，commit a2ce401）**：M3A channel↔model 绑定 + Model CRUD/provisioning + M4 定价（客户售价 `prices` + provider 成本价 `channel_cost_prices`，明确金额无倍率，售价生效窗用 DB `EXCLUDE`/`tstzrange` 约束保证不重叠）；新增 `internal/service/admin/{channelmodel,model,costprice,price}` + 对应 adminapi handler/sql/sqlc + 服务层与 handler 单测。**Slice 1（2026-06-09）**：M1 静态 token 认证（`ADMIN_API_TOKEN` 常量时间比对 + `AdminPrincipal` context 缝，JWT/RBAC/审计 deferred）+ M3 provider/channel CRUD + M2 channel 凭据只写轮换（AES-GCM 加密入库、不回读），关闭 GAP-6-003。前端 `unio-admin`（独立仓库，Vite+React+TS+Tailwind+shadcn，Bun）已交付登录、provider/channel/model、定价、渠道模型绑定页面（commit a4a589a）。**下一切片：M6 只读查询台。** 模块分解见 phase-13 ADMIN_MODULES_DRAFT.md，已交付契约见 phase-13 CONTRACT.md。
上一阶段：阶段 12 Capability Architecture（DEC-015）能力架构**代码层已收口**（用户授权方案 A，2026-06-09）：闸门默认 observe，capability 推断/档位/observe-enforce 闸门/观测审计/enforce 开关+三协议渲染/`/v1/models` cap-tags 全交付，已关闭 GAP-12-001/003/004/005/008/012。三类需外部前置的工作受控 deferred 阶段 13：`/console/v1/models`（GAP-12-006）、adapter 画像物化进真实 model 行（GAP-12-007，依赖 provisioning）、observe→enforce 实际切换 + 观察期 + 覆盖面复核（GAP-12-009/12-002）。
历史主线：阶段 11 OpenAI Responses API ingress（Codex 兼容，生产级）已于 2026-06-07 完成 acceptance sign-off，状态改为 done。
当前进度：阶段 11 TASK-11.01~11.16 全部 done——`/v1/responses`（非流式+流式 SSE）、`/responses/compact`（无状态降级、可计费）、`/responses/input_tokens`（本地估算、不调上游/不计费）、有状态 endpoint 501、`background:true` 400 全部路由已挂；方案 B 共享 `lifecycle.AttemptRunner`，chat 与 responses 复用同一份资金关键候选 fallback / settlement 循环。2026-06-06 真实 Codex CLI v0.130 端到端手测通过（14 个 Responses 请求全 succeeded，资金三方对账闭合 authorized=captured+released、reserved_balance=0）；mock + gated 真实 DeepSeek 黑盒（11 用例）已跑通。2026-06-07 验收命令组全绿：`sqlc generate`（无 drift）、`go build`、`go vet`、`go test ./internal/... ./cmd/...`（DB 门控测试无 DATABASE_URL 正确 Skip）、`git diff --check`、`RESPONSES_CHAT_BRIDGE.md` Verify 残留检查无输出。
阶段调整：OpenAI Responses API ingress 上提为阶段 11；新立阶段 12 Capability Architecture（能力声明 + 运行时闸门 + models.dev daily cron + cap-tags API，见 DEC-015）；原后台管理再次顺延为阶段 13（占位文档已就位）。
GAP 收口：本阶段必须关闭的 P0/P1 已收口（GAP-11-005 共享 AttemptRunner、GAP-11-010 reasoning_effort drift 均 done）。剩余 P1 GAP-11-001（无状态）/11-002（工具保真度）/11-007（compact 永久降级）/11-009（有状态 501 永久边界）均为已接受范围边界或永久限制，release_blocker 全为 no；新增 GAP-11-011（标准 SDK 完整流式事件未发，P2）。阶段 12 启动条件：阶段 11 公开 API 表面已冻结，先迁 `CAPABILITY_MATRIX.md` 静态约定到 `model_capabilities` 表。
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
| 阶段 11 | [OpenAI Responses API ingress](chapters/phase-11-openai-responses-api/STATUS.md) | done | 新增 OpenAI Responses API（Codex 兼容，生产级），responses-to-chat 桥接复用 OpenAI adapter/lifecycle/账务。TASK-11.01~11.16 全部 done：`/v1/responses`（非流式+流式 SSE）、`/responses/compact`（无状态降级）、`/responses/input_tokens`（本地估算）、有状态 501、background 400，方案 B 共享 `AttemptRunner`。2026-06-06 真实 Codex CLI v0.130 端到端手测 + 资金三方对账闭合，2026-06-07 acceptance sign-off 通过。剩余 P1 GAP（11-001 无状态 / 11-002 工具保真度 / 11-007 compact 永久降级 / 11-009 有状态 501 永久边界）均为已接受范围边界，非上线阻断。 |
| 阶段 12 | [能力架构 Capability Architecture](chapters/phase-12-capability-architecture/STATUS.md) | code-closed (deferrals→13) | 把"协议字段 vs 模型能力"从静态文档升级为运行时事实。**能力架构代码层全收口（用户授权方案 A，默认 observe）**：TASK-12.01 schema、TASK-12.04 models.dev 同步、TASK-12.02 推断 + 档位抽取、TASK-12.03 observe/enforce 闸门（`gate.go` 纯判定 + observe filter + `enforceCapability` 按表面开关 + 三协议推断/档位接线 + checker metric/审计 fail-open）、TASK-12.07 观测审计（三指标 + `request_records.capability_check_result` 列 + DB 往返测试）、TASK-12.08 enforce 开关 `CAPABILITY_ENFORCE_*` + 三协议 capability error 渲染（不暴露 channel 拓扑）、TASK-12.05 `/v1/models` cap-tags 全部交付。已关闭 GAP-12-001/003/004/005/008/012。**受控 deferred 阶段 13**：`/console/v1/models`（GAP-12-006）、adapter 画像物化进真实 model 行（GAP-12-007，依赖 provisioning）、observe→enforce 实际切换 + 观察期 + 覆盖面复核（GAP-12-009/12-002）。决策见 DEC-015。 |
| 阶段 13 | [后台管理](chapters/phase-13-admin/STATUS.md) | in_progress | admin-server 垂直切片推进，产品定位见 DEC-017（卖档网关）。Slice 1：静态 token 认证（M1）+ provider/channel CRUD（M3）+ 凭据只写轮换（M2），关闭 GAP-6-003。Slice 2：channel↔model 绑定 + Model CRUD/provisioning + 定价（M4 售价/成本价，DB EXCLUDE 约束保证售价生效窗不重叠）。真实 Postgres 全量测试全绿。下一切片：只读查询台（M6）→ 客户/项目/预算/手工调额（M7）→ 能力管理（M5）→ 工作台看板（M9）。 |

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

2026-06-07 Phase 11 acceptance sign-off：本次未修改生产代码，验收命令组全绿，状态由 in_progress 改为 done。

```bash
sqlc generate                       # 通过，无 drift
go build ./internal/... ./cmd/...   # 通过
go vet ./internal/... ./cmd/...     # 通过
go test ./internal/... ./cmd/...    # 全绿（DB 门控测试无 DATABASE_URL 正确 Skip）
git diff --check                    # 通过
# RESPONSES_CHAT_BRIDGE.md `Verify` 残留检查：无输出
```

说明：本次在无 `DATABASE_URL` 环境运行，DB 门控集成测试自动 Skip；2026-06-06 已在真实 DB/Redis + 真实 Codex CLI v0.130 + 真实 DeepSeek 完成端到端手测与资金三方对账闭合（见 [phase-11 STATUS](chapters/phase-11-openai-responses-api/STATUS.md) 端到端验收记录）。

## 下一步

阶段 13 后台管理进行中。Slice 1（认证 + provider/channel CRUD + 凭据轮换）与 Slice 2（channel↔model 绑定 + model provisioning + 定价 M4）已交付，channel↔model 绑定与 model provisioning 已就绪（关 GAP-12-007/GAP-11-006 的前提已满足）。**下一切片：M6 只读查询台**（request/usage/billing 只读，风险低），再做客户/项目/预算/手工调额（M7，含 GAP-6-005 路由闸门）、能力管理（M5）、工作台看板（M9）。每片按"后端 → `unio-admin` 前端 → 联调"推进，参见：

```text
docs/chapters/phase-13-admin/ADMIN_MODULES_DRAFT.md   # 模块分解 + 资源地图 + 推进顺序
docs/chapters/phase-13-admin/PLAN.md
docs/chapters/phase-13-admin/STATUS.md
docs/chapters/phase-13-admin/CONTRACT.md              # 已交付端点契约
docs/production/TODO_REGISTER.md
docs/production/DECISIONS.md
```

开新切片前先扫 GAP：

```bash
rg -n "TODO|GAP-" AGENTS.md docs cmd internal migrations sql
rg -n '^\| <a id="gap-[^"]+"></a>\[[^\]]+\].*\| P[01] \| (todo|deferred) \|' docs/production/TODO_REGISTER.md
```

阶段 13 内待收口 GAP：GAP-6-001（credential 存储方案，13.02 定稿）、GAP-6-005（project 禁用/专属 channel，M7）、GAP-12-007（adapter 画像物化，M3A+M5）、GAP-12-009/12-002（enforce 切换 + 覆盖面复核，M5 + prod 观察期）、GAP-3-002（API key 管理 + 审计，M7）、GAP-12-011（sync 运营残留，M8）。明确 deferred 至后续阶段：console-server 全部（含 GAP-12-006 `/console/v1/models`）、支付/充值、JWT/RBAC/审计。

阶段 11 剩余 P1 GAP 均为已接受范围边界或永久限制，非上线阻断：GAP-11-001（无状态会话）、GAP-11-002（Codex 工具桥接保真度）、GAP-11-007（compact 无状态降级）、GAP-11-009（有状态 501 + background 400）；P2 GAP-11-011（标准 SDK 完整流式事件未发）。

> 本地开发库：源 migration 曾原地改表，跑 DB 测试前需 `migrate -path migrations -database "$DATABASE_URL" drop -f && migrate ... up` 重置到当前迁移，否则旧 schema 落后导致 DB 测试失败。
