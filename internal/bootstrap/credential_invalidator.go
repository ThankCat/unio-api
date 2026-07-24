package bootstrap

import (
	"context"
	"time"

	"go.uber.org/zap"

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
			ChannelID:                       revision.ChannelID,
			ExpectedConfigRevision:          revision.ChannelConfigRevision,
			ExpectedOriginBaseUrlRevision: revision.OriginBaseURLRevision,
			ExpectedOriginStatusRevision:  revision.OriginStatusRevision,
		})
		if err != nil {
			i.logger.Error("mark channel credential invalid failed",
				zap.Int64("channel_id", revision.ChannelID), zap.Error(err))
			return
		}
		if !applied.StateChangeApplied {
			return
		}

		i.logger.Warn("channel credential marked invalid (consecutive 401)",
			zap.Int64("channel_id", revision.ChannelID),
			zap.Int64("config_revision", applied.CurrentConfigRevision))
	}()
}
