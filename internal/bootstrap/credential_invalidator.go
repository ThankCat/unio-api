package bootstrap

import (
	"context"
	"time"

	"go.uber.org/zap"

	"github.com/ThankCat/unio-gateway/internal/platform/logging"
	"github.com/ThankCat/unio-gateway/internal/platform/store/sqlc"
	"github.com/ThankCat/unio-gateway/internal/service/gateway/lifecycle"
)

// credentialInvalidator 是 lifecycle.CredentialInvalidator 的生产实现（阶段二凭据闸门）。
//
// 当某渠道连续 401 达阈值时被调用：异步 best-effort 把 channels.credential_valid 翻为 false，
// 并在「真跳变」（受影响行数=1）时补写一条 source=runtime_401 的事件日志。
// 全程用独立 background context + 超时，不受在途请求 ctx 取消影响，也不阻塞请求热路径。
type credentialInvalidator struct {
	queries *sqlc.Queries
	logger  *zap.Logger
}

func newCredentialInvalidator(queries *sqlc.Queries, logger *zap.Logger) *credentialInvalidator {
	return &credentialInvalidator{queries: queries, logger: logger}
}

// MarkChannelCredentialInvalid 实现 lifecycle.CredentialInvalidator。
func (i *credentialInvalidator) MarkChannelCredentialInvalid(revision lifecycle.CredentialRevision) {
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		applied, err := i.queries.ApplyRuntime401CredentialInvalidation(ctx, sqlc.ApplyRuntime401CredentialInvalidationParams{
			ChannelID:              revision.ChannelID,
			ExpectedConfigRevision: revision.ChannelConfigRevision,
			ExpectedOriginRevision: revision.OriginRevision,
			ExpectedStatusRevision: revision.ProviderStatusRevision,
		})
		if err != nil {
			logging.Error(i.logger, "runtime", "credential", "channel credential invalidation failed",
				zap.Int64("channel_id", revision.ChannelID),
				zap.Int64("config_revision", revision.ChannelConfigRevision),
				zap.String("reason", "consecutive_401"),
				zap.String("error_code", "channel_credential_invalidation_failed"),
				zap.String("error_category", "persistence"),
				zap.String("error_message", err.Error()),
			)
			return
		}
		if !applied.StateChangeApplied {
			return
		}

		logging.Warn(i.logger, "runtime", "credential", "channel credential marked invalid",
			zap.Int64("channel_id", revision.ChannelID),
			zap.Int64("config_revision", applied.CurrentConfigRevision),
			zap.String("reason", "consecutive_401"),
			zap.Int("threshold", revision.Threshold),
		)
	}()
}
