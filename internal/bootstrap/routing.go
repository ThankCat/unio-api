package bootstrap

import (
	"log/slog"
	"time"

	"github.com/ThankCat/unio-api/internal/core/routing"
)

const defaultChatRouteTimeout = 30 * time.Second

// NewChatRouter 创建当前 server 进程使用的 chat routing 组件。
//
// 渠道凭据明文存储（产品决策），routing 直接取用 channels.credential，无需 master key / cipher。
func NewChatRouter(store routing.Store, logger *slog.Logger) *routing.Router {
	return routing.NewRouter(store, defaultChatRouteTimeout, routing.WithLogger(logger))
}
