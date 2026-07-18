package bootstrap

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"go.uber.org/zap"

	"github.com/ThankCat/unio-gateway/internal/platform/store/sqlc"
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
func (i *credentialInvalidator) MarkChannelCredentialInvalid(channelID int64) {
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		affected, err := i.queries.SetChannelCredentialInvalid(ctx, channelID)
		if err != nil {
			i.logger.Error("mark channel credential invalid failed",
				zap.Int64("channel_id", channelID), zap.Error(err))
			return
		}
		if affected == 0 {
			// 已是 invalid（并发下别的实例先翻了），非跳变，不重复写日志。
			return
		}

		if err := i.queries.InsertChannelTestLog(ctx, sqlc.InsertChannelTestLogParams{
			ChannelID:            channelID,
			Source:               "runtime_401",
			Success:              false,
			ErrorCode:            pgtype.Text{String: "credential_invalid", Valid: true},
			CredentialValidAfter: false,
			Message:              pgtype.Text{String: "连续 401 达阈值，自动标记凭据失效", Valid: true},
		}); err != nil {
			i.logger.Error("insert runtime_401 test log failed",
				zap.Int64("channel_id", channelID), zap.Error(err))
			return
		}

		i.logger.Warn("channel credential marked invalid (consecutive 401)",
			zap.Int64("channel_id", channelID))
	}()
}
