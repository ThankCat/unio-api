# Phase 9 Acceptance

## 产品验收

1. 客户只修改 `base_url` 和 `api_key`，OpenAI SDK 在 C1~C4 范围内可直接运行。
2. 对外协议以 OpenAI Chat Completions 为准；不对外暴露 vendor 私有字段名作为正式契约。
3. Compatibility Matrix 明确 Supported / Passthrough / Rejected；不存在 silent drop。
4. DeepSeek 等 OpenAI-compatible 上游的差异，只在 adapter 内翻译，gateway 无 vendor 分支。

## 功能验收（C1~C4，Phase 9 最小 done 标准）

C1~C4 done 表示 chat/reasoning/stream usage 等核心 drop-in 能力就绪；**不包含** tools（C5）或 JSON mode（C6）。

1. 非流式/流式 chat 基础参数完整透传。
2. `stream_options.include_usage=true` 行为对齐 OpenAI（含尾包 usage chunk）。
3. 响应含 `reasoning_content` 与 `content` 分离（流式 delta + 非流式 message）。
4. usage 含 cached/reasoning details（对外 OpenAI details 结构）。
5. DeepSeek thinking / reasoning 多轮 assistant 历史可回传 upstream。
6. 流式响应翻译统一在 adapter stream translate 模块；**不再维护独立 Normalizer 架构定义**。

## 功能验收（DeepSeek 上游，TASK-9.14）

1. DS-01~DS-07 用例全部通过（见 [DEEPSEEK_UPSTREAM.md](DEEPSEEK_UPSTREAM.md)）。
2. 全链路符合 [END_TO_END_PIPELINE.md](END_TO_END_PIPELINE.md)，gateway 无 vendor 分支。

## 功能验收（C5+，可分批 done）

1. tools / tool_calls / tool role 可用。
2. `response_format` 至少支持 json_object。
3. multimodal content 至少 passthrough 到支持的上游。

## 生产验收

1. 不支持字段明确 400，不能静默丢弃。
2. settlement 仍基于 `adapter.ChatUsage` 内部事实，不受对外 DTO 扩展影响。
3. 用户可见错误仍由 HTTP 层安全映射，不透传上游原始 body。
4. request/attempt/usage/ledger 审计字段不因 parity 扩展而丢失。

## 测试验收

1. `go test ./internal/app/gatewayapi/... ./internal/core/adapter/... ./internal/service/gateway/...` 通过。
2. OpenAI Python SDK 黑盒测试（TASK-9.12）通过 C1~C4。
3. DeepSeek reasoning + stream usage 回归测试通过。
4. 新增字段均有「支持 / 拒绝 / passthrough」测试，不允许 silent drop 回归。

## 文档验收

1. [PLAN.md](PLAN.md) 任务状态与代码一致。
2. [OPENAI_PROTOCOL.md](OPENAI_PROTOCOL.md) 覆盖请求/非流式/流式 OpenAI 字段解释。
3. [END_TO_END_PIPELINE.md](END_TO_END_PIPELINE.md) 覆盖完整七步链路。
4. [DEEPSEEK_UPSTREAM.md](DEEPSEEK_UPSTREAM.md) 覆盖 DeepSeek 请求/响应映射与 DS-01~07 用例。
5. [COMPATIBILITY_MATRIX.md](COMPATIBILITY_MATRIX.md) 与实现状态同步。
6. [DECISIONS.md](../../production/DECISIONS.md) 含 OpenAI-first ADR（DEC-005）。
7. Phase 4 text-only MVP 边界已标注由 Phase 9 取代。
8. 文档中不再单独定义 Normalizer 架构；统一称为 adapter 响应翻译 / stream translate。
