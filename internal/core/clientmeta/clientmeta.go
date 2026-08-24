// Package clientmeta 在 ingress 与 lifecycle 之间传递客户端声明的会话轮次标识。
//
// 这些标识只用于本地请求审计（把一次客户端会话内的多个请求串联起来排查），不参与路由、
// 计费或准入，也不转发上游。抓包实测 Codex CLI v0.147 通过 x-codex-turn-metadata 请求头
// 与 body 内 client_metadata 携带同一组值；本包只做传递与长度约束，不解释语义。
package clientmeta

import (
	"context"
	"encoding/json"
	"strings"
)

type ctxKey struct{}

// maxFieldLength 是单个标识的保守长度上限：客户端可控输入，超长直接丢弃该字段。
// 实测值为 UUID 量级，128 足够宽松。
const maxFieldLength = 128

// Turn 是客户端声明的一次会话轮次标识。字段缺失时为空串。
type Turn struct {
	// ThreadID 跨轮稳定，标识一整段会话。
	ThreadID string
	// TurnID 标识单轮；一轮内多次上游尝试共享同一值。
	TurnID string
	// RequestKind 是客户端声明的请求种类（如 turn / compact）。
	RequestKind string
}

// IsZero 表示本次请求没有任何可用的客户端轮次标识。
func (t Turn) IsZero() bool {
	return t.ThreadID == "" && t.TurnID == "" && t.RequestKind == ""
}

// WithTurn 把轮次标识写入 ctx；全空时不写。
func WithTurn(ctx context.Context, turn Turn) context.Context {
	if turn.IsZero() {
		return ctx
	}
	return context.WithValue(ctx, ctxKey{}, turn)
}

// TurnFromContext 读取 ingress 捕获的轮次标识；未捕获时返回零值。
func TurnFromContext(ctx context.Context) Turn {
	turn, _ := ctx.Value(ctxKey{}).(Turn)
	return turn
}

// codexTurnMetadata 是 Codex x-codex-turn-metadata 头的载荷形状（只取审计需要的字段）。
type codexTurnMetadata struct {
	ThreadID    string `json:"thread_id"`
	TurnID      string `json:"turn_id"`
	RequestKind string `json:"request_kind"`
}

// ParseCodexTurnMetadata 解析 Codex 的 x-codex-turn-metadata 头。
//
// 头值是一个 JSON 字符串；解析失败或字段超长时静默降级为空值——审计字段缺失可以接受，
// 但不能让畸形的客户端输入影响请求主链路。
func ParseCodexTurnMetadata(header string) Turn {
	header = strings.TrimSpace(header)
	if header == "" || !strings.HasPrefix(header, "{") {
		return Turn{}
	}
	var meta codexTurnMetadata
	if err := json.Unmarshal([]byte(header), &meta); err != nil {
		return Turn{}
	}
	return Turn{
		ThreadID:    normalizeField(meta.ThreadID),
		TurnID:      normalizeField(meta.TurnID),
		RequestKind: normalizeField(meta.RequestKind),
	}
}

func normalizeField(v string) string {
	v = strings.TrimSpace(v)
	if len(v) > maxFieldLength {
		return ""
	}
	return v
}
