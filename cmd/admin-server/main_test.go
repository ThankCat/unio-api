package main

import (
	"net/http"
	"testing"
	"time"

	"github.com/ThankCat/unio-gateway/internal/platform/config"
)

func TestNewAdminHTTPServerKeepsBoundedRequestReads(t *testing.T) {
	handler := http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})
	server := newAdminHTTPServer(":8521", handler, config.HTTPConfig{
		ReadHeaderTimeout: 30 * time.Second,
		AdminReadTimeout:  10 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
	})

	if server.ReadHeaderTimeout != 30*time.Second {
		t.Fatalf("ReadHeaderTimeout=%v, want 30s", server.ReadHeaderTimeout)
	}
	if server.ReadTimeout != 10*time.Second {
		t.Fatalf("ReadTimeout=%v, want 10s", server.ReadTimeout)
	}
	if server.WriteTimeout != 30*time.Second {
		t.Fatalf("WriteTimeout=%v, want 30s", server.WriteTimeout)
	}
	if server.IdleTimeout != 60*time.Second {
		t.Fatalf("IdleTimeout=%v, want 60s", server.IdleTimeout)
	}
}
