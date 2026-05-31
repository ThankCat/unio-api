# Phase 9 Status

状态：planned

## 阶段判断

Phase 9 尚未正式进入实现，但已有探索代码和部分 MVP 能力：

| 来源 | 内容 | Phase 9 收口任务 |
| --- | --- | --- |
| 课 2 | stream final usage 在非空 choices 尾包可读 | TASK-9.07 |
| 课 3 | DeepSeek reasoning 临时合并进 content | TASK-9.07（需改回 OpenAI 双字段） |
| 课 4 | 客户端 `stream_options.include_usage` 尾包 | TASK-9.09 |
| `normalizer/` 包 | DeepSeek stream 差异过渡实现 | TASK-9.07 吸收，不再单独定义 Normalizer |

## 任务状态

| 任务 | 状态 | 说明 |
| --- | --- | --- |
| TASK-9.01 | partial | DEC-005 已写入；协议/链路/矩阵文档已创建。 |
| TASK-9.02 | planned | 请求禁止 silent drop。 |
| TASK-9.03 | partial | 公开 DTO 仅完成 include_usage 响应尾包。 |
| TASK-9.04 | planned | adapter contract OpenAI 语义扩展。 |
| TASK-9.05 | planned | 请求 OpenAI → upstream wire。 |
| TASK-9.06 | planned | 非流式响应翻译。 |
| TASK-9.07 | partial | stream translate 过渡代码在 `normalizer/`，待 refactor 收口。 |
| TASK-9.08 | planned | DeepSeek reasoning 多轮回传。 |
| TASK-9.09 | partial | stream usage 客户端尾包已完成，中间 chunk/null 语义未完成。 |
| TASK-9.10 | planned | tools / tool_calls。 |
| TASK-9.11 | planned | response_format。 |
| TASK-9.12 | planned | OpenAI SDK 黑盒验收。 |
| TASK-9.13 | partial | [COMPATIBILITY_MATRIX.md](COMPATIBILITY_MATRIX.md) 初版已完成。 |
| TASK-9.14 | planned | DeepSeek 上游全链路验收（最后执行）。 |
| TASK-9.15 | planned | gateway service DTO↔contract 完整映射（当前仅 role+content）。 |
| TASK-9.16 | planned | 请求校验从 Phase 4 text-only 升级到 OpenAI parity。 |
| TASK-9.17 | planned | authorization/token 估算输入不再剥窄 messages。 |

## 关联 GAP

| GAP | 优先级 | 状态 |
| --- | --- | --- |
| [GAP-9-001](../../production/TODO_REGISTER.md#gap-9-001) | P0 | todo |
| [GAP-9-002](../../production/TODO_REGISTER.md#gap-9-002) | P0 | todo |
| [GAP-9-003](../../production/TODO_REGISTER.md#gap-9-003) | P0 | partial |
| [GAP-9-004](../../production/TODO_REGISTER.md#gap-9-004) | P1 | todo |

## 下一次进入本阶段前必须检查

```bash
rg -n "GAP-9-" docs/production/TODO_REGISTER.md internal
go test ./internal/app/gatewayapi/... ./internal/core/adapter/... ./internal/service/gateway/...
```
