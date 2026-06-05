# Phase 11 Status

状态：planned

## 当前判断

阶段 11 为新增 `POST /v1/responses`（OpenAI Responses API ingress，Codex 兼容），是商业级、
生产级实现，排在后台管理（阶段 12）之前。方案已成文，尚未进入实现。核心策略是 responses-to-chat
桥接：在 gateway 内部把 Responses 请求下转到既有 `openai.ChatRequest` 契约，复用 Phase 10 的
OpenAI adapter、routing、lifecycle、settlement 与 `ResponseFacts`，不新增上游 Responses adapter，
不改账务 schema。

## 任务状态

| 任务 | 状态 | 说明 |
| --- | --- | --- |
| TASK-11.01 | planned | Responses 协议参考与字段冻结（含真实 Codex 抓包）。 |
| TASK-11.02 | planned | 模型指定与路由策略冻结（客户 model → 受支持上游模型）。 |
| TASK-11.03 | planned | DEC-014 决策与 `requestlog.OperationResponses` 审计枚举。 |
| TASK-11.04 | planned | Responses ingress DTO / decode / validation。 |
| TASK-11.05 | planned | Responses → 内部 ChatRequest 请求翻译。 |
| TASK-11.06 | planned | 非流式编排接入 lifecycle + 响应翻译。 |
| TASK-11.07 | planned | 流式 Chat chunk → Responses 命名事件状态机。 |
| TASK-11.08 | planned | 工具（custom/grammar/local_shell）、reasoning、text 特殊处理。 |
| TASK-11.09 | planned | Responses 原生错误与安全输出。 |
| TASK-11.10 | planned | bootstrap 装配 + `/v1/responses` 路由。 |
| TASK-11.11 | planned | 黑盒验收（mock + 真实 Codex/DeepSeek smoke）。 |
| TASK-11.12 | planned | 文档、命名与结构复核。 |

## 进入阶段 11 前置条件

1. 阶段 10 双协议 gateway 链路稳定（done）。
2. 已能用现有 bootstrap seed / 运营配置把某个客户模型名路由到 DeepSeek OpenAI channel。
3. 已抓到一份真实 Codex `/responses` 请求体作为字段冻结依据（TASK-11.01）。
4. 模型指定方式已冻结（TASK-11.02），运营可声明哪些模型可用于 Codex。

## 关联 GAP

GAP-11-001 ~ GAP-11-006 见 [TODO_REGISTER.md](../../production/TODO_REGISTER.md)。
