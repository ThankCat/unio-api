package modelcatalog

import (
	"context"

	"github.com/ThankCat/unio-gateway/internal/platform/failure"
	"github.com/ThankCat/unio-gateway/internal/platform/store/sqlc"
)

// Model 表示 Unio 对外可见的模型。
type Model struct {
	ID      string
	OwnedBy string
	// Capabilities 是模型已声明（非 unsupported）的 cap-tags，升序去重；未声明为空切片。
	Capabilities []string
	// Protocols 是该模型当前实际可用的入口协议（openai / anthropic），升序去重。
	// 它来自可用渠道的协议并集，不是一份独立声明，所以不会出现「写着支持却调不通」。
	Protocols []string
}

// Store 定义 model catalog 读取可用模型所需的最小数据库能力。
type Store interface {
	ListAvailableModels(ctx context.Context) ([]sqlc.ListAvailableModelsRow, error)
	ListModelProtocols(ctx context.Context) ([]sqlc.ListModelProtocolsRow, error)
}

// Service 负责查询当前 user 可见的模型列表。
type Service struct {
	store Store
}

// NewService 创建 model catalog service。
func NewService(store Store) *Service {
	return &Service{store: store}
}

// ListAvailableModels 返回全部对外可见的模型。
//
// 不做用户级过滤：模型 enabled 的前置条件就是「至少有一条可用渠道能供」，所以列出即可调用。
// requiredCapabilities 非空时按 cap-tags 做 AND 过滤（模型 cap 集合必须包含全部请求 cap），
// 供 /v1/models?capability=a,b 预检；空过滤返回全部可见模型。未识别的 capability key 不报错，
// 自然匹配不到模型（lenient filter 语义）。
func (s *Service) ListAvailableModels(ctx context.Context, requiredCapabilities []string) ([]Model, error) {
	rows, err := s.store.ListAvailableModels(ctx)
	if err != nil {
		return nil, failure.Wrap(
			failure.CodeModelCatalogStoreFailed,
			err,
			failure.WithMessage("list available models"),
		)
	}
	protocolRows, err := s.store.ListModelProtocols(ctx)
	if err != nil {
		return nil, failure.Wrap(
			failure.CodeModelCatalogStoreFailed,
			err,
			failure.WithMessage("list model protocols"),
		)
	}
	protocols := make(map[string][]string, len(protocolRows))
	for _, row := range protocolRows {
		protocols[row.ModelID] = row.Protocols
	}

	models := make([]Model, 0, len(rows))
	for _, row := range rows {
		caps := row.CapabilityKeys
		if caps == nil {
			caps = []string{}
		}
		if !capabilitiesSatisfy(caps, requiredCapabilities) {
			continue
		}
		protos := protocols[row.ModelID]
		if protos == nil {
			protos = []string{}
		}
		models = append(models, Model{
			ID:           row.ModelID,
			OwnedBy:      row.OwnedBy,
			Capabilities: caps,
			Protocols:    protos,
		})
	}

	return models, nil
}

// capabilitiesSatisfy 判断模型 cap 集合是否包含全部 required（AND 语义）；required 为空恒为 true。
func capabilitiesSatisfy(modelCaps, required []string) bool {
	if len(required) == 0 {
		return true
	}

	have := make(map[string]struct{}, len(modelCaps))
	for _, c := range modelCaps {
		have[c] = struct{}{}
	}
	for _, want := range required {
		if _, ok := have[want]; !ok {
			return false
		}
	}

	return true
}
