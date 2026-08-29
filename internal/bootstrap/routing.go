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
// fxRates 供跨币种候选比价（D5）；nil 时跨币种候选被剔除，同币种不受影响。
func NewChatRouter(store routing.Store, defaultResponseTimeout time.Duration, logger *zap.Logger, fxRates routing.FxRateSource) *routing.Router {
	opts := []routing.Option{routing.WithLogger(logger)}
	if fxRates != nil {
		opts = append(opts, routing.WithFxRates(fxRates))
	}
	return routing.NewRouter(store, defaultResponseTimeout, opts...)
}
