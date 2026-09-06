package httpx

import (
	"net/http"
	"testing"
)

func TestTrustedClientIPResolverIgnoresXFFFromUntrustedPeer(t *testing.T) {
	resolver, err := NewTrustedClientIPResolver([]string{"10.0.0.0/8"})
	if err != nil {
		t.Fatal(err)
	}
	req := &http.Request{RemoteAddr: "203.0.113.9:51234", Header: http.Header{}}
	req.Header.Set("X-Forwarded-For", "198.51.100.1")
	if got := resolver.Resolve(req); got != "203.0.113.9" {
		t.Fatalf("untrusted peer must resolve to RemoteAddr, got %q", got)
	}
}

func TestTrustedClientIPResolverWalksChainFromTrustedPeer(t *testing.T) {
	resolver, err := NewTrustedClientIPResolver([]string{"10.0.0.0/8", "172.16.0.0/12"})
	if err != nil {
		t.Fatal(err)
	}
	req := &http.Request{RemoteAddr: "10.1.2.3:443", Header: http.Header{}}
	// 客户端伪造的前置项（1.1.1.1）不能被采信；从最靠近本进程的一跳反向找第一个非可信地址。
	req.Header.Set("X-Forwarded-For", "1.1.1.1, 198.51.100.7, 172.16.5.5")
	if got := resolver.Resolve(req); got != "198.51.100.7" {
		t.Fatalf("resolve = %q, want 198.51.100.7", got)
	}
}

func TestTrustedClientIPResolverWithoutCIDRsUsesRemoteAddr(t *testing.T) {
	resolver, err := NewTrustedClientIPResolver(nil)
	if err != nil {
		t.Fatal(err)
	}
	req := &http.Request{RemoteAddr: "[2001:db8::1]:8443", Header: http.Header{}}
	req.Header.Set("X-Forwarded-For", "1.1.1.1")
	if got := resolver.Resolve(req); got != "2001:db8::1" {
		t.Fatalf("resolve = %q, want 2001:db8::1", got)
	}
	if got := resolver.Resolve(&http.Request{RemoteAddr: "garbage", Header: http.Header{}}); got != "unknown" {
		t.Fatalf("unparseable remote must be unknown, got %q", got)
	}
}

func TestNewTrustedClientIPResolverRejectsInvalidCIDR(t *testing.T) {
	if _, err := NewTrustedClientIPResolver([]string{"10.0.0.0/8", "not-a-cidr"}); err == nil {
		t.Fatal("expected error for invalid CIDR")
	}
	if _, err := NewTrustedClientIPResolver([]string{" ", "10.0.0.0/8"}); err != nil {
		t.Fatalf("blank entries must be skipped: %v", err)
	}
}
