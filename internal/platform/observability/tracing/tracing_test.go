package tracing

import (
	"context"
	"testing"
)

// TestSetupDisabledIsNoop 验证未启用时 Setup 不报错、Shutdown 安全空操作。
func TestSetupDisabledIsNoop(t *testing.T) {
	provider, err := Setup(context.Background(), Options{Enabled: false})
	if err != nil {
		t.Fatalf("disabled setup returned err: %v", err)
	}
	if err := provider.Shutdown(context.Background()); err != nil {
		t.Fatalf("disabled shutdown returned err: %v", err)
	}
}

// TestSetupEnabledWithoutEndpointIsNoop 验证启用但缺 endpoint 时同样视为关闭，不初始化 exporter。
func TestSetupEnabledWithoutEndpointIsNoop(t *testing.T) {
	provider, err := Setup(context.Background(), Options{Enabled: true, Endpoint: ""})
	if err != nil {
		t.Fatalf("enabled-without-endpoint setup returned err: %v", err)
	}
	if err := provider.Shutdown(context.Background()); err != nil {
		t.Fatalf("shutdown returned err: %v", err)
	}
}

// TestNilProviderShutdownSafe 验证 nil Provider 的 Shutdown 安全。
func TestNilProviderShutdownSafe(t *testing.T) {
	var p *Provider
	if err := p.Shutdown(context.Background()); err != nil {
		t.Fatalf("nil provider shutdown returned err: %v", err)
	}
}
