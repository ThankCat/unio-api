package main

import (
	"net/http"
	"testing"
	"time"

	"github.com/ThankCat/unio-gateway/internal/platform/config"
)

func TestNewGatewayHTTPServerHasNoAbsoluteReadOrWriteTimeout(t *testing.T) {
	handler := http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})
	server := newGatewayHTTPServer(":8520", handler, config.HTTPConfig{
		ReadHeaderTimeout: 30 * time.Second,
		AdminReadTimeout:  10 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
	})

	if server.ReadHeaderTimeout != 30*time.Second {
		t.Fatalf("ReadHeaderTimeout=%v, want 30s", server.ReadHeaderTimeout)
	}
	if server.ReadTimeout != 0 {
		t.Fatalf("ReadTimeout=%v, want 0", server.ReadTimeout)
	}
	if server.WriteTimeout != 0 {
		t.Fatalf("WriteTimeout=%v, want 0", server.WriteTimeout)
	}
	if server.IdleTimeout != 60*time.Second {
		t.Fatalf("IdleTimeout=%v, want 60s", server.IdleTimeout)
	}
}
