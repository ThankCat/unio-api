package config

import (
	"log/slog"
	"strings"
	"testing"
)

func TestLoadDefaultRedisDB(t *testing.T) {
	t.Setenv("REDIS_DB", "")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("load config: %v", err)
	}

	if cfg.Redis.DB != 0 {
		t.Fatalf("expected redis db %d, got %d", 0, cfg.Redis.DB)
	}
}

func TestLoadRedisDBFromEnv(t *testing.T) {
	t.Setenv("REDIS_DB", "2")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("load config: %v", err)
	}

	if cfg.Redis.DB != 2 {
		t.Fatalf("expected redis db %d, got %d", 2, cfg.Redis.DB)
	}
}

func TestLoadInvalidRedisDB(t *testing.T) {
	t.Setenv("REDIS_DB", "abc")

	_, err := Load()
	if err == nil {
		t.Fatal("expected error, got nil")
	}

	// 错误信息要带配置名，方便启动失败时快速定位。
	if !strings.Contains(err.Error(), "REDIS_DB") {
		t.Fatalf("expected error to contain REDIS_DB, got %q", err.Error())
	}
}

func TestLoadLogLevelDebug(t *testing.T) {
	t.Setenv("LOG_LEVEL", "debug")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("load config: %v", err)
	}

	if cfg.Log.Level != slog.LevelDebug {
		t.Fatalf("expected log level %v, got %v", slog.LevelDebug, cfg.Log.Level)
	}
}

func TestLoadInvalidLogLevel(t *testing.T) {
	t.Setenv("LOG_LEVEL", "trace")

	_, err := Load()
	if err == nil {
		t.Fatal("expected error, got nil")
	}

	// 错误信息要带配置名，方便定位启动失败原因。
	if !strings.Contains(err.Error(), "LOG_LEVEL") {
		t.Fatalf("expected error to contain LOG_LEVEL, got %q", err.Error())
	}
}
