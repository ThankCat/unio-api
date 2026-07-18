package logging_test

import (
	"testing"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"

	"github.com/ThankCat/unio-gateway/internal/platform/config"
	"github.com/ThankCat/unio-gateway/internal/platform/logging"
)

func TestSmokeFormats(t *testing.T) {
	for _, format := range []string{config.LogFormatConsole, config.LogFormatJSON} {
		logger, err := logging.New(config.LogConfig{Level: zapcore.InfoLevel, Format: format})
		if err != nil {
			t.Fatalf("format %s: %v", format, err)
		}
		logger.Info("http request",
			zap.String("method", "POST"),
			zap.String("path", "/v1/responses"),
			zap.Int("status", 200),
			zap.Int64("duration_ms", 11561),
			zap.String("model", "gpt-5.4"),
		)
		_ = logger.Sync()
	}
}

func TestNewRejectsBadFormat(t *testing.T) {
	_, err := logging.New(config.LogConfig{Level: zapcore.InfoLevel, Format: "text"})
	if err == nil {
		t.Fatal("expected error")
	}
}
