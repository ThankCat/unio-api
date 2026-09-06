package httpx

import (
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"strings"
)

// TrustedClientIPResolver 仅从可信代理链中提取客户端地址，供安全决策（登录限速、审计归因）使用。
//
// 与 ExtractClientIP 的区别：后者无条件采信 X-Forwarded-For 首项，只能用于展示；本解析器只有在
// TCP 对端落在可信代理 CIDR 内时才向 XFF 链回溯，且从最靠近本进程的一跳开始反向遍历，
// 遇到第一个非可信地址即停——客户端自行伪造的前置项永远不会被采信。
// 未配置任何可信 CIDR 时恒返回 RemoteAddr 的 IP（去端口）。
type TrustedClientIPResolver struct {
	trusted []netip.Prefix
}

// NewTrustedClientIPResolver 解析配置的可信代理 CIDR。
func NewTrustedClientIPResolver(cidrs []string) (*TrustedClientIPResolver, error) {
	resolver := &TrustedClientIPResolver{}
	for _, raw := range cidrs {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		prefix, err := netip.ParsePrefix(raw)
		if err != nil {
			return nil, fmt.Errorf("parse trusted proxy CIDR %q: %w", raw, err)
		}
		resolver.trusted = append(resolver.trusted, prefix.Masked())
	}
	return resolver, nil
}

// Resolve 从可信对端开始反向遍历 X-Forwarded-For，定位真实客户端；无法识别时返回 "unknown"。
func (r *TrustedClientIPResolver) Resolve(request *http.Request) string {
	remote := request.RemoteAddr
	if host, _, err := net.SplitHostPort(remote); err == nil {
		remote = host
	}
	peer, err := netip.ParseAddr(strings.Trim(remote, "[]"))
	if err != nil {
		return "unknown"
	}
	if !r.isTrusted(peer) {
		return peer.String()
	}
	chain := strings.Split(request.Header.Get("X-Forwarded-For"), ",")
	current := peer
	for i := len(chain) - 1; i >= 0; i-- {
		candidate, parseErr := netip.ParseAddr(strings.TrimSpace(chain[i]))
		if parseErr != nil {
			continue
		}
		current = candidate
		if !r.isTrusted(candidate) {
			return candidate.String()
		}
	}
	return current.String()
}

func (r *TrustedClientIPResolver) isTrusted(address netip.Addr) bool {
	for _, prefix := range r.trusted {
		if prefix.Contains(address) {
			return true
		}
	}
	return false
}
