package codexresponses

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/ThankCat/unio-gateway/internal/core/channel"
	"github.com/ThankCat/unio-gateway/internal/core/clientmeta"
	"github.com/ThankCat/unio-gateway/internal/core/sessionhint"
	"github.com/buger/jsonparser"
)

// Codex 订阅后端按会话身份把请求路由到持有 prompt cache 前缀的分片：官方 codex-rs 在每次
// 请求上让 body prompt_cache_key 与 session-id / thread-id / x-client-request-id 头四处同值
// （= thread id），2026-05 起同时发连字符与下划线两种拼写。缺少这些头时同一会话的连续请求
// 会落到随机分片，缓存命中呈现忽有忽无（本地实测同会话相邻请求在 0%～99% 间跳动）。
//
// 号池多客户共用同一账号，客户原始会话键不能原样出站（Sub2API 2026-03 因此发生跨用户会话
// 关联）。这里按「客户 API Key × 上游账号 × 会话键」派生稳定标识：同一客户同一会话在同一账号
// 上恒定（亲和保住），跨客户或 failover 换号后不同（不碰撞、不串号）。
var sessionAffinityHeaders = []string{
	"session-id",
	"session_id",
	"thread-id",
	"thread_id",
	"x-client-request-id",
}

// sessionAffinityNamespace 是会话身份派生种子的版本前缀；改动派生公式必须升版本，避免新旧标识混用。
const sessionAffinityNamespace = "unio:codex:session-affinity:v1"

// identityNamespace 是 client_metadata 内设备/轮次 id 的 1:1 映射种子前缀（按客户 × 账号隔离）。
const identityNamespace = "unio:codex:identity:v1"

// deviceNamespace 是 device 收敛模式下账号固定 installation_id 的派生种子前缀。
const deviceNamespace = "unio:codex:device:v1"

// upstreamSessionID 派生本次出站向 Codex 后端声明的会话身份；ctx 无会话亲和事实时返回空串。
//
// 输出为 UUIDv4 形状（官方客户端的 thread id 形态，36 字符，低于上游对 prompt_cache_key 的
// 64 字符上限）。
func upstreamSessionID(ctx context.Context, ch channel.Runtime) string {
	affinity := sessionhint.UpstreamAffinityFromContext(ctx)
	key := strings.TrimSpace(affinity.SessionKey)
	if key == "" {
		return ""
	}
	seed := fmt.Sprintf("%s|key:%d|account:%s|session:%s",
		sessionAffinityNamespace, affinity.APIKeyID, ch.Account.UpstreamAccountID, key)
	return stableUUID(seed)
}

// scopedIdentity 把客户端原始 id 按「客户 × 账号 × 种类」1:1 映射成稳定 UUID：同一客户在同一账号上
// 恒定（上游看到的设备/轮次数量与真实一致），换客户或换账号即不同（同一台真实设备不会在两个账号上
// 以同一个 id 出现）。raw 为空返回空。
func scopedIdentity(ctx context.Context, ch channel.Runtime, kind, raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	affinity := sessionhint.UpstreamAffinityFromContext(ctx)
	return stableUUID(fmt.Sprintf("%s|kind:%s|key:%d|account:%s|value:%s",
		identityNamespace, kind, affinity.APIKeyID, ch.Account.UpstreamAccountID, raw))
}

// installationIdentity 决定出站 installation_id：device 收敛模式下为账号固定值（由账号种子派生，
// 与客户无关），否则按客户 × 账号 1:1 映射。
func installationIdentity(ctx context.Context, ch channel.Runtime, raw string) string {
	if ch.Account.ConvergesDevice() {
		return stableUUID(deviceNamespace + "|seed:" + ch.Account.FingerprintSeed)
	}
	return scopedIdentity(ctx, ch, "installation", raw)
}

// windowIdentity 把 <thread_id>:<window_number> 的 thread 分量换成派生会话身份，保留窗口序号。
func windowIdentity(sessionID, raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	suffix := ""
	if idx := strings.LastIndexByte(raw, ':'); idx >= 0 {
		suffix = raw[idx:]
	}
	return sessionID + suffix
}

// stableUUID 把种子确定性映射成 UUIDv4 形状的字符串（同种子恒同值）。
func stableUUID(seed string) string {
	sum := sha256.Sum256([]byte(seed))
	b := sum[:16]
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x",
		binary.BigEndian.Uint32(b[0:4]),
		binary.BigEndian.Uint16(b[4:6]),
		binary.BigEndian.Uint16(b[6:8]),
		binary.BigEndian.Uint16(b[8:10]),
		b[10:16])
}

// applySessionAffinityHeaders 写入全部会话亲和头，并把客户端的 x-codex-turn-metadata /
// x-codex-window-id 按同一命名空间改写后转发（客户端未发则不发）。id 为空时不写任何头。
func applySessionAffinityHeaders(httpReq *http.Request, ch channel.Runtime, id string) {
	if id == "" {
		return
	}
	for _, name := range sessionAffinityHeaders {
		httpReq.Header.Set(name, id)
	}
	turn := clientmeta.TurnFromContext(httpReq.Context())
	if turn.WindowIDHeader != "" {
		httpReq.Header.Set("x-codex-window-id", windowIdentity(id, turn.WindowIDHeader))
	}
	if turn.TurnMetadataHeader != "" {
		if rewritten, ok := rewriteTurnMetadata(httpReq.Context(), ch, id, turn.TurnMetadataHeader); ok {
			httpReq.Header.Set("x-codex-turn-metadata", rewritten)
		}
	}
}

// bindCodexSession 把派生身份写入请求体：顶层 prompt_cache_key（已有则覆盖，缺失则注入），
// 以及 client_metadata 内的设备/会话/轮次 id（存在时）。
//
// 字节级 splice：只动 prompt_cache_key 与 client_metadata 两个键，其余字节（含客户端字段顺序、
// 数字原文）原样保留。无会话亲和事实时零改动；非对象 body 交上游拒绝，不在此猜。
func bindCodexSession(ctx context.Context, ch channel.Runtime, body []byte) ([]byte, error) {
	id := upstreamSessionID(ctx, ch)
	if id == "" {
		return body, nil
	}
	// jsonparser.Set 可能复用输入切片的底层数组，先拷贝避免污染调用方持有的 body。
	out := append([]byte(nil), body...)
	if current, err := jsonparser.GetString(out, "prompt_cache_key"); err != nil || current != id {
		value, err := json.Marshal(id)
		if err != nil {
			return body, nil
		}
		next, err := jsonparser.Set(out, value, "prompt_cache_key")
		if err != nil {
			return body, nil
		}
		out = next
	}
	if rewritten, ok := rewriteClientMetadata(ctx, ch, id, out); ok {
		if next, err := jsonparser.Set(out, rewritten, "client_metadata"); err == nil {
			out = next
		}
	}
	return out, nil
}

// identityFields 是 client_metadata 与 x-codex-turn-metadata 内需要改写的身份字段及其映射种类。
// session / thread 直接等于派生会话身份（官方客户端四处同值）；installation 按收敛模式；
// turn 与 root_turn 按客户 × 账号 1:1 映射；window 换 thread 分量保留序号。
// 其余字段（sandbox、workspaces、request_kind、context_window_id、turn_started_at_unix_ms 等）原样保留。
var identityFields = []struct {
	name string
	kind string
}{
	{name: "x-codex-installation-id", kind: "installation"},
	{name: "installation_id", kind: "installation"},
	{name: "session_id", kind: "session"},
	{name: "thread_id", kind: "session"},
	{name: "x-client-request-id", kind: "session"},
	{name: "turn_id", kind: "turn"},
	{name: "root_turn_id", kind: "turn"},
	{name: "x-codex-window-id", kind: "window"},
	{name: "window_id", kind: "window"},
}

// rewriteIdentityFields 就地改写一个身份对象；返回是否有改动。
func rewriteIdentityFields(ctx context.Context, ch channel.Runtime, sessionID string, fields map[string]any) bool {
	changed := false
	for _, field := range identityFields {
		raw, ok := fields[field.name].(string)
		if !ok || strings.TrimSpace(raw) == "" {
			continue
		}
		var next string
		switch field.kind {
		case "installation":
			next = installationIdentity(ctx, ch, raw)
		case "session":
			next = sessionID
		case "window":
			next = windowIdentity(sessionID, raw)
		default:
			next = scopedIdentity(ctx, ch, field.kind, raw)
		}
		if next != "" && next != raw {
			fields[field.name] = next
			changed = true
		}
	}
	return changed
}

// rewriteTurnMetadata 改写 x-codex-turn-metadata JSON 字符串内的身份字段；非对象或解析失败返回 ok=false。
func rewriteTurnMetadata(ctx context.Context, ch channel.Runtime, sessionID, raw string) (string, bool) {
	var fields map[string]any
	if err := json.Unmarshal([]byte(raw), &fields); err != nil || fields == nil {
		return "", false
	}
	if !rewriteIdentityFields(ctx, ch, sessionID, fields) {
		return raw, true
	}
	rebuilt, err := json.Marshal(fields)
	if err != nil {
		return "", false
	}
	return string(rebuilt), true
}

// rewriteClientMetadata 改写 body 顶层 client_metadata 对象（含内嵌的 x-codex-turn-metadata 字符串），
// 返回重编码后的对象原文；client_metadata 不存在、非对象或无改动时返回 ok=false。
func rewriteClientMetadata(ctx context.Context, ch channel.Runtime, sessionID string, body []byte) ([]byte, bool) {
	raw, dataType, _, err := jsonparser.Get(body, "client_metadata")
	if err != nil || dataType != jsonparser.Object {
		return nil, false
	}
	var fields map[string]any
	if err := json.Unmarshal(raw, &fields); err != nil || fields == nil {
		return nil, false
	}
	changed := rewriteIdentityFields(ctx, ch, sessionID, fields)
	if embedded, ok := fields["x-codex-turn-metadata"].(string); ok && embedded != "" {
		if rewritten, ok := rewriteTurnMetadata(ctx, ch, sessionID, embedded); ok && rewritten != embedded {
			fields["x-codex-turn-metadata"] = rewritten
			changed = true
		}
	}
	if !changed {
		return nil, false
	}
	rebuilt, err := json.Marshal(fields)
	if err != nil {
		return nil, false
	}
	return rebuilt, true
}
