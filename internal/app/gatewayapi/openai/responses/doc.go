// Package responses 是 Unio Gateway 暴露给客户的 OpenAI Responses API（POST /v1/responses）
// 公开 HTTP 子包，主要用于 Codex 兼容。
//
// 架构（DEC-014，responses-to-chat 桥接）：
//
//   - 本子包只负责 Responses 协议的 ingress DTO、decode、协议结构校验与原生错误渲染。
//   - Responses 请求在 service 层桥接为内部 openai.ChatRequest 契约，复用既有 OpenAI
//     adapter / routing / lifecycle / settlement，不新增上游 Responses adapter。
//   - 字段语义映射（Pass/Adapt/Drop/Reject）以
//     docs/chapters/phase-11-openai-responses-api/RESPONSES_CHAT_BRIDGE.md 为准。
//
// 边界（与 chatcompletions 子包对称）：
//
//   - ingress 只校验协议合法性（DEC-012「协议为先」）；合法但 provider 无法转换的字段不在
//     此 Reject，保留进 Extensions 由 adapter 出站 Drop。
//   - HTTP 层只处理协议、DTO、错误渲染；业务编排在 service/gateway/openai/responses。
package responses
