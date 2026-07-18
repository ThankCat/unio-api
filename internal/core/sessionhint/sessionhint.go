// Package sessionhint 在 ingress 与 gateway service 之间传递客户端会话标识
//（会话粘性路由的会话键来源之一，大 uncache 缺口 P0）。
//
// HTTP handler 从请求头捕获会话 ID（OpenAI 族：session-id；Anthropic 族：
// x-claude-code-session-id）写入 ctx；协议 service 按各自协议的提取顺序消费
//（body 字段优先，头部回退）。只传递不解释：这里不做任何格式校验，是否可用由提取器判定。
package sessionhint

import (
	"context"
	"encoding/json"
	"regexp"
	"strings"
)

type ctxKey struct{}

// WithClientSessionID 把 ingress 捕获的客户端会话头写入 ctx；空白串不写。
func WithClientSessionID(ctx context.Context, id string) context.Context {
	id = strings.TrimSpace(id)
	if id == "" {
		return ctx
	}
	return context.WithValue(ctx, ctxKey{}, id)
}

// ClientSessionID 读取 ingress 捕获的客户端会话头；未捕获返回空串。
func ClientSessionID(ctx context.Context) string {
	id, _ := ctx.Value(ctxKey{}).(string)
	return id
}

// maxSessionKeyLength 是会话键的保守长度上限：会话键是客户端可控输入，超长直接视为无信号
//（入 Redis 键前还会定长哈希，这里只挡明显滥用）。
const maxSessionKeyLength = 512

// OpenAISessionKey 按 OpenAI 族提取顺序产出 sticky 会话键（决议 6）：
// body prompt_cache_key 优先（实测 Codex 必带、= session id、跨轮稳定），
// 缺失回退 ingress 捕获的 session-id 头；两者皆缺返回空串（本请求不粘）。
func OpenAISessionKey(ctx context.Context, promptCacheKey *string) string {
	if promptCacheKey != nil {
		if key := normalizeSessionKey(*promptCacheKey); key != "" {
			return key
		}
	}
	return normalizeSessionKey(ClientSessionID(ctx))
}

// AnthropicSessionKey 按 Anthropic 族提取顺序产出 sticky 会话键（决议 5）：
// x-claude-code-session-id 头优先（实测 Claude Code 必带、跨轮稳定、换会话变新），
// 缺失回退 body metadata.user_id 内嵌的 "_session_<id>" 后缀。严格解析：任一环节失败
// 即返回空串不粘（R9，不猜第三方格式）。
func AnthropicSessionKey(ctx context.Context, metadata json.RawMessage) string {
	if key := normalizeSessionKey(ClientSessionID(ctx)); key != "" {
		return key
	}
	return sessionKeyFromAnthropicMetadata(metadata)
}

// sessionKeyFromAnthropicMetadata 从 Anthropic metadata.user_id 提取会话段。
// Claude Code 的 user_id 形如 "user_<hash>_account_<uuid>_session_<uuid>"；
// 取最后一个 "_session_" 后缀并要求是 UUID 形状，否则视为无信号。
func sessionKeyFromAnthropicMetadata(metadata json.RawMessage) string {
	if len(metadata) == 0 {
		return ""
	}
	var meta struct {
		UserID string `json:"user_id"`
	}
	if err := json.Unmarshal(metadata, &meta); err != nil {
		return ""
	}
	const marker = "_session_"
	idx := strings.LastIndex(meta.UserID, marker)
	if idx < 0 {
		return ""
	}
	session := meta.UserID[idx+len(marker):]
	if !uuidShapePattern.MatchString(session) {
		return ""
	}
	return session
}

// uuidShapePattern 校验会话段是 UUID 形状（宽松到无连字符变体，但拒绝任意杂串）。
var uuidShapePattern = regexp.MustCompile(`^[0-9a-fA-F][0-9a-fA-F-]{7,63}$`)

// normalizeSessionKey 修剪空白并拒绝超长键（客户端可控输入，R6 第一道闸）。
func normalizeSessionKey(key string) string {
	key = strings.TrimSpace(key)
	if key == "" || len(key) > maxSessionKeyLength {
		return ""
	}
	return key
}
