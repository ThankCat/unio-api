// Package dto 定义 Anthropic Messages API 的协议 DTO（请求 / 响应 / 流式事件）。
//
// DTO 是 service 编排的输入输出契约，因此归属 service 层；HTTP 层（internal/app/gatewayapi）
// 只负责解码、校验与错误渲染，并以类型别名重新导出这些类型以保持既有调用方不变。
// 本包只依赖标准库，绝不反向依赖 HTTP 或 service 编排。
package dto
