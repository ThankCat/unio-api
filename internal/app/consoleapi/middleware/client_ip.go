package middleware

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"strings"
)

type clientIPContextKey struct{}

// ClientIPResolver extracts client addresses only from trusted proxy chains.
type ClientIPResolver struct {
	trusted []netip.Prefix
}

// NewClientIPResolver parses the configured trusted proxy CIDRs.
func NewClientIPResolver(cidrs []string) (*ClientIPResolver, error) {
	resolver := &ClientIPResolver{}
	for _, raw := range cidrs {
		prefix, err := netip.ParsePrefix(raw)
		if err != nil {
			return nil, fmt.Errorf("parse trusted proxy CIDR %q: %w", raw, err)
		}
		resolver.trusted = append(resolver.trusted, prefix.Masked())
	}
	return resolver, nil
}

// ClientIP stores the resolved client address in the request context.
func ClientIP(resolver *ClientIPResolver) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx := context.WithValue(r.Context(), clientIPContextKey{}, resolver.Resolve(r))
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// ClientIPFromContext returns the trusted client address or "unknown".
func ClientIPFromContext(ctx context.Context) string {
	if value, ok := ctx.Value(clientIPContextKey{}).(string); ok && value != "" {
		return value
	}
	return "unknown"
}

// Resolve walks X-Forwarded-For from the trusted peer toward the client.
func (r *ClientIPResolver) Resolve(request *http.Request) string {
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

func (r *ClientIPResolver) isTrusted(address netip.Addr) bool {
	for _, prefix := range r.trusted {
		if prefix.Contains(address) {
			return true
		}
	}
	return false
}
