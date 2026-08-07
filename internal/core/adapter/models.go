package adapter

import (
	"context"
	"time"

	"github.com/ThankCat/unio-gateway/internal/core/channel"
)

// ModelListItem 是上游模型列表中允许进入持久化快照的有限事实。
type ModelListItem struct {
	ID        string
	OwnedBy   string
	CreatedAt *time.Time
}

// ModelListResult 是一次完整上游模型枚举结果；Items 已按 ID 去重并排序。
type ModelListResult struct {
	Items []ModelListItem
}

// ModelLister 是 (protocol, adapter_key) 可选注册的上游模型枚举能力。
type ModelLister interface {
	ListModels(ctx context.Context, runtime channel.Runtime) (ModelListResult, error)
}
