// Package logging 基于 zap 构造进程级结构化日志。
package logging

import (
	"fmt"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"

	"github.com/ThankCat/unio-gateway/internal/platform/config"
	"github.com/ThankCat/unio-gateway/internal/platform/failure"
)

// New 按 LogConfig 构造 *zap.Logger，对齐 zap 官方预设：
//
//   - FORMAT=console → zap.NewDevelopmentConfig（Console，给人看）
//   - FORMAT=json    → zap.NewProductionConfig（JSON，给采集系统）
//
// console 额外调整：彩色级别、RFC3339 时间、" | " 分隔（v1.28 Development 默认无色且用 Tab）。
// json 使用 RFC3339 时间（比 Production 默认的 epoch 更易读），其余保持官方 Production。
//
// 仅覆盖 Level，并把输出固定到 stdout（与原先进程日志习惯一致）。
func New(cfg config.LogConfig) (*zap.Logger, error) {
	var zcfg zap.Config
	switch cfg.Format {
	case config.LogFormatConsole:
		zcfg = zap.NewDevelopmentConfig()
		applyConsoleEncoderTweaks(&zcfg.EncoderConfig)
	case config.LogFormatJSON:
		zcfg = zap.NewProductionConfig()
		zcfg.EncoderConfig.EncodeTime = zapcore.RFC3339TimeEncoder
	default:
		return nil, failure.New(
			failure.CodeConfigUnsupported,
			failure.WithMessage(fmt.Sprintf("unsupported LOG_FORMAT %q", cfg.Format)),
		)
	}

	zcfg.Level = zap.NewAtomicLevelAt(cfg.Level)
	zcfg.OutputPaths = []string{"stdout"}
	zcfg.ErrorOutputPaths = []string{"stderr"}

	logger, err := zcfg.Build()
	if err != nil {
		return nil, failure.Wrap(
			failure.CodeConfigInvalid,
			err,
			failure.WithMessage("build zap logger"),
		)
	}
	return logger, nil
}

// MustNewConsole 构造 Development + 本地可读调整的 console logger，供配置加载失败等极早路径使用。
func MustNewConsole() *zap.Logger {
	zcfg := zap.NewDevelopmentConfig()
	applyConsoleEncoderTweaks(&zcfg.EncoderConfig)
	zcfg.Level = zap.NewAtomicLevelAt(zapcore.InfoLevel)
	zcfg.OutputPaths = []string{"stdout"}
	zcfg.ErrorOutputPaths = []string{"stderr"}
	logger, err := zcfg.Build()
	if err != nil {
		// Build 在默认 DevelopmentConfig 下几乎不会失败；兜底避免 main 启动路径 panic。
		return zap.NewNop()
	}
	return logger
}

func applyConsoleEncoderTweaks(enc *zapcore.EncoderConfig) {
	// v1.28 Development 默认是 CapitalLevelEncoder（无色）；本地显式开彩色级别。
	enc.EncodeLevel = zapcore.CapitalColorLevelEncoder
	enc.EncodeTime = zapcore.RFC3339TimeEncoder
	// 默认 Tab 在终端里过宽；用 " | " 分隔时间/级别/caller/消息。
	enc.ConsoleSeparator = " | "
}
