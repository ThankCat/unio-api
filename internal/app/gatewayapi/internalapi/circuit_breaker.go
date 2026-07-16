// Package internalapi 提供 gateway 进程内只读运维端点（供 admin-server 拉取，不面向客户）。
package internalapi

import (
	"crypto/subtle"
	"net/http"
	"os"

	"github.com/ThankCat/unio-gateway/internal/platform/httpx"
	"github.com/ThankCat/unio-gateway/internal/service/gateway/lifecycle"
)

// CircuitBreakerSnapshotter 是熔断器只读快照能力（由 *lifecycle.ChannelCircuitBreaker 实现）。
type CircuitBreakerSnapshotter interface {
	Snapshot() lifecycle.ChannelBreakerSnapshot
}

// CircuitBreakerHandler 暴露 GET /internal/v1/circuit-breaker。
type CircuitBreakerHandler struct {
	Breaker  CircuitBreakerSnapshotter
	Token    string
	Instance string
}

// ServeHTTP 校验内部 token 后返回进程内熔断快照。
func (h CircuitBreakerHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if h.Token == "" || h.Breaker == nil {
		_ = httpx.WriteError(w, http.StatusNotFound, "not_found", "route not found")
		return
	}
	if !internalTokenOK(r.Header.Get("X-Unio-Internal-Token"), h.Token) {
		_ = httpx.WriteError(w, http.StatusUnauthorized, "unauthorized", "invalid internal token")
		return
	}

	snap := h.Breaker.Snapshot()
	snap.Instance = h.Instance
	if snap.Instance == "" {
		snap.Instance, _ = os.Hostname()
	}
	if snap.Channels == nil {
		snap.Channels = []lifecycle.ChannelBreakerEntry{}
	}
	_ = httpx.WriteJSON(w, http.StatusOK, snap)
}

func internalTokenOK(got, want string) bool {
	if got == "" || want == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(got), []byte(want)) == 1
}
