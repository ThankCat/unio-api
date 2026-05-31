# Release Blockers

本文档只记录公开生产前必须解决的阻断项。

## 当前阻断项

| ID | GAP | 阶段 | 阻断原因 | 关联任务 |
| --- | --- | --- | --- | --- |
| RB-9-001 | [GAP-9-001](TODO_REGISTER.md#gap-9-001) | 阶段 9 | 公开 Chat Completions 请求字段 silent drop，破坏 OpenAI drop-in 替换承诺。 | [TASK-9.02](../chapters/phase-09-openai-protocol-parity/PLAN.md#task-9-02-request-no-silent-drop) |
| RB-9-002 | [GAP-9-002](TODO_REGISTER.md#gap-9-002) | 阶段 9 | 对外响应缺少 reasoning_content、usage details 等 OpenAI 字段；非流式只返回 content。 | [TASK-9.03](../chapters/phase-09-openai-protocol-parity/PLAN.md#task-9-03-public-openai-dto) |
| RB-9-003 | [GAP-9-003](TODO_REGISTER.md#gap-9-003) | 阶段 9 | 流式响应翻译未对齐 OpenAI chunk 语义；过渡 normalizer 将 reasoning 合并进 content。 | [TASK-9.07](../chapters/phase-09-openai-protocol-parity/PLAN.md#task-9-07-stream-response-translate) |

## 使用规则

1. 任何 `P0` 且 `release_blocker=yes` 的 GAP 必须同步进入本文档。
2. blocker 关闭时，先完成代码和测试，再更新 TODO register，最后移出本文档。
3. 本文档不记录普通优化项，只记录影响公开生产、资金、安全、账务或用户契约的阻断项。
