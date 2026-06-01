# Phase 10 Status

状态：partial

## 阶段判断

Phase 10 已完成 ADR 与范围冻结，尚未开始生产代码改造。当前已完成改造计划、
架构边界、协议字段矩阵、DeepSeek 双协议 mapping 草案和验收标准。DeepSeek Anthropic
mapping 已按 2026-06-01 官方兼容表刷新，并保存带来源日期的项目内参考摘要。

本阶段不是局部补丁。关闭前必须完成 OpenAI Chat Completions Create 与 Anthropic Messages Create 两个公开操作的全量对话链路。
这里的“全量”指字段识别、校验、响应翻译、usage、错误和账务事实完整，不表示本阶段扩展图片、视频、音频、文件等模型能力；相关字段必须显式 Reject 或按 mapping 转换。

## 任务状态

| 任务 | 状态 | 说明 |
| --- | --- | --- |
| TASK-10.01 | done | DEC-010 / DEC-011、双协议架构、ResponseFacts、DeepSeek 双协议 tokenizer 独立实现边界与范围冻结已完成；DeepSeek Anthropic mapping 已按 2026-06-01 官方兼容表刷新。 |
| TASK-10.02 | planned | 目录迁移与依赖方向整理。 |
| TASK-10.03 | planned | channel protocol 与 adapter registry。 |
| TASK-10.04 | planned | ResponseFacts、usage 与审计 schema。 |
| TASK-10.05 | planned | 共享 Lifecycle Executor。 |
| TASK-10.06 | planned | OpenAI Chat Completions 全量字段契约。 |
| TASK-10.07 | planned | DeepSeek OpenAI adapter 全量转换；编码前必须先用黑盒冻结 OpenAI mapping 中所有 `Verify`。 |
| TASK-10.08 | planned | Anthropic Messages 全量字段入口。 |
| TASK-10.09 | planned | DeepSeek Anthropic adapter 全量转换；编码前必须先用黑盒冻结 Anthropic mapping 中所有 `Verify`。 |
| TASK-10.10 | planned | 双协议 stream 生命周期。 |
| TASK-10.11 | planned | 双协议错误与安全输出。 |
| TASK-10.12 | planned | Migration、sqlc 与账务回归。 |
| TASK-10.13 | planned | OpenAI SDK 黑盒验收。 |
| TASK-10.14 | planned | Anthropic SDK 黑盒验收。 |
| TASK-10.15 | planned | 文档、命名与冗余复核。 |

## 已识别的现有迁移点

| 当前实现 | Phase 10 目标 |
| --- | --- |
| `internal/app/gatewayapi` OpenAI 文件平铺 | `internal/app/gatewayapi/openai` |
| `internal/service/gateway/chat_*` | `service/gateway/lifecycle` + `service/gateway/openai/chatcompletions` |
| `internal/core/adapter/chat.go` 的 OpenAI 语义 DTO | `internal/core/adapter/openai` |
| `internal/core/adapter/openai/streamtranslate` | `internal/core/adapter/openai/deepseek/stream.go` |
| `providers.adapter` | 删除 runtime 职责；`channel.Runtime.AdapterKey` 改由 `channels.adapter_key` 提供 |
| OpenAI 偏向的 `ChatUsage` | 协议无关 `usage.Facts` |
| OpenAI 偏向的 `adapter.ChatInputTokenizer` | `openai.ChatInputTokenizer` + `anthropic.MessagesInputTokenizer` |

## 进入实现前

按 [PLAN.md](PLAN.md) 的顺序从 TASK-10.02 目录迁移开始。

## 交接说明

本轮已完成 Phase 10 规划自检与文档修正：

1. 已明确 Phase 10 的“全量”是双协议对话 endpoint 的字段识别、校验、响应翻译、
   usage、错误和账务事实完整，不表示扩展图片、视频、音频、文件等模型能力。
2. 已把 DeepSeek OpenAI / Anthropic mapping 的 `Verify` 清理要求提前为 adapter
   编码前置条件；未黑盒冻结前不得写对应 provider adapter 生产代码。
3. 已明确 `routing candidate` 只表示 SQL 同协议数据库候选；registry capability
   过滤发生在 lifecycle，并产出最终 fallback plan / attempt plan。
4. 已定死 `providers.adapter` 迁移策略：Phase 10 删除其 runtime 职责，
   `channel.Runtime.AdapterKey` 只来自 `channels.adapter_key`。
5. 已明确 `server_tool_usage` 只是 ResponseFacts / usage line item 的受控账务事实，
   不是提前实现模型能力系统；未登记 key 不能自动入账。
6. 已修正 DeepSeek OpenAI mapping 表格列数问题，并把生产代码历史注释中的
   admin 阶段编号同步为阶段 11。

下一次进入实现时，从 [PLAN.md#task-10-02-directory-layout](PLAN.md#task-10-02-directory-layout)
开始。进入 `TASK-10.07` 和 `TASK-10.09` 前，必须先完成各自 DeepSeek mapping
中所有 `Verify` 的最小黑盒冻结。
