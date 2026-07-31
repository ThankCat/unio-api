package bootstrap

import (
	"time"

	"go.uber.org/zap"

	"github.com/ThankCat/unio-gateway/internal/core/routing"
)

// NewChatRouter 创建当前 server 进程使用的 chat routing 组件。
//
// defaultResponseTimeout 是渠道未配 response_timeout_ms 时的兜底响应超时。
// 渠道凭据明文存储（产品决策），routing 直接取用 channels.credential，无需 master key / cipher。
func NewChatRouter(store routing.Store, defaultResponseTimeout time.Duration, logger *zap.Logger) *routing.Router {
	return routing.NewRouter(store, defaultResponseTimeout, routing.WithLogger(logger))
}
