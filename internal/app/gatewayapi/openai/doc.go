// Package openai 是 Unio Gateway 暴露给客户的 OpenAI 协议族公开 HTTP 包根目录。
//
// 长期约定（与 anthropic/ 对称，见 docs/architecture/PROJECT_STRUCTURE.md）：
//
//   - 每个 OpenAI operation 都在协议族下独立子包，例如：
//
//     openai/chatcompletions  — POST /v1/chat/completions
//     openai/models           — GET  /v1/models
//
//     未来如新增 /v1/responses /v1/embeddings 等 endpoint，按同样方式新增子包。
//
//   - 本根包仅保留协议族级共享类型（例如未来共享的 OpenAI 原生 error shape）；
//     当前没有共享内容，故只保留本说明文件。
//
//   - 不允许在本根包再次平铺单一 operation 的 handler/dto/validation。这种"协议族
//     根包平铺"的反模式是 Phase 10.15 复核时收口的历史遗留，新代码必须直接落入
//     对应 operation 子包。
package openai
