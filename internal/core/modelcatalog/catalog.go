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
}

// Store 定义 model catalog 读取可用模型所需的最小数据库能力。
type Store interface {
	ListAvailableModelsForUser(ctx context.Context, userID int64) ([]sqlc.ListAvailableModelsForUserRow, error)
}

// Service 负责查询当前 user 可见的模型列表。
type Service struct {
	store Store
}

// NewService 创建 model catalog service。
func NewService(store Store) *Service {
	return &Service{store: store}
}

// ListAvailableModels 返回当前 user 可见的 OpenAI-compatible 模型。
//
// requiredCapabilities 非空时按 cap-tags 做 AND 过滤（模型 cap 集合必须包含全部请求 cap），
// 供 /v1/models?capability=a,b 预检；空过滤返回全部可见模型。未识别的 capability key 不报错，
// 自然匹配不到模型（lenient filter 语义）。
func (s *Service) ListAvailableModels(ctx context.Context, userID int64, requiredCapabilities []string) ([]Model, error) {
	// TODO(阶段6/production): [GAP-6-006] /v1/models 已支持 user_model_policies 模型 allow-list/deny-list 与 cap-tags 暴露，但尚未表达用户禁用、预算约束或专属 channel 策略；与 routing 共用 user/channel policy，预算可用性由 reservation 统一判断。
	rows, err := s.store.ListAvailableModelsForUser(ctx, userID)
	if err != nil {
		return nil, failure.Wrap(
			failure.CodeModelCatalogStoreFailed,
			err,
			failure.WithMessage("list available models"),
		)
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
		models = append(models, Model{
			ID:           row.ModelID,
			OwnedBy:      row.OwnedBy,
			Capabilities: caps,
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
