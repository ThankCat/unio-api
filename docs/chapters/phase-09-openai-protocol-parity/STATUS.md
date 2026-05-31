# Phase 9 Status

状态：done

## 阶段判断

Phase 9 C1~C6 核心 OpenAI parity 已实现并通过黑盒/E2E 测试；`go test ./internal/...` 全绿。C8 高级字段（logprobs、seed 等）留待后续迭代。

| 来源 | 内容 | Phase 9 收口 |
| --- | --- | --- |
| 课 2~4 | stream usage / include_usage | Done |
| `streamtranslate/` | 原 normalizer 过渡代码 | Done（已 rename） |

## 任务状态

| 任务 | 状态 | 说明 |
| --- | --- | --- |
| TASK-9.01 | done | DEC-005 + 协议/链路/矩阵文档。 |
| TASK-9.02 | done | 双轨 decode + Reject + Extensions passthrough。 |
| TASK-9.03 | done | 公开 DTO 完整 OpenAI 形状。 |
| TASK-9.04 | done | adapter contract OpenAI 语义。 |
| TASK-9.05 | done | request wire + extensions/thinking。 |
| TASK-9.06 | done | 非流式响应翻译。 |
| TASK-9.07 | done | `streamtranslate/` 包；双字段 + tool_calls delta。 |
| TASK-9.08 | done | reasoning 多轮回传 upstream。 |
| TASK-9.09 | done | include_usage + usage:null。 |
| TASK-9.10 | done | tools/tool_calls/tool role typed 化。 |
| TASK-9.11 | done | response_format typed 化。 |
| TASK-9.12 | done | SDK 形状 + HTTP handler 黑盒。 |
| TASK-9.13 | done | Compatibility Matrix 与实现同步。 |
| TASK-9.14 | done | DS-01~DS-07 E2E。 |
| TASK-9.15 | done | gateway DTO↔contract 完整映射。 |
| TASK-9.16 | done | OpenAI parity 校验。 |
| TASK-9.17 | done | authorization parity messages。 |

## 关联 GAP

| GAP | 优先级 | 状态 |
| --- | --- | --- |
| [GAP-9-001](../../production/TODO_REGISTER.md#gap-9-001) | P0 | done |
| [GAP-9-002](../../production/TODO_REGISTER.md#gap-9-002) | P0 | done |
| [GAP-9-003](../../production/TODO_REGISTER.md#gap-9-003) | P0 | done |
| [GAP-9-004](../../production/TODO_REGISTER.md#gap-9-004) | P1 | done |

## 验证

```bash
go test ./internal/... -count=1   # 2026-05-31 全绿
```

## 后续（C8 / 可选）

1. 可选 Python OpenAI SDK 实机脚本（`UNIO_BASE_URL`）。
2. C8：`logprobs`、`n`、`seed`、multimodal 校验增强。
