package bootstrap

import (
	"log/slog"
	"time"

	"github.com/ThankCat/unio-gateway/internal/core/routing"
)

// NewChatRouter 创建当前 server 进程使用的 chat routing 组件。
//
// defaultTimeout 是渠道未配 timeout_ms 时的兜底超时,初值来自运行时配置
// (gateway.default_channel_timeout),之后由 settingsApplier 热更新(Router.SetDefaultTimeout)。
// 渠道凭据明文存储（产品决策），routing 直接取用 channels.credential，无需 master key / cipher。
func NewChatRouter(store routing.Store, defaultTimeout time.Duration, logger *slog.Logger) *routing.Router {
	return routing.NewRouter(store, defaultTimeout, routing.WithLogger(logger))
}
