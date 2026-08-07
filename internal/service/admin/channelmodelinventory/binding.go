package channelmodelinventory

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/ThankCat/unio-gateway/internal/platform/store/sqlc"
	"github.com/ThankCat/unio-gateway/internal/service/admin/modelcatalog"
)

const maxBatchBindings = 100

type BatchBindingInput struct {
	ModelID       int64
	UpstreamModel string
}

type BindingResult struct {
	ID               int64
	ChannelID        int64
	ModelID          int64
	ModelExternalID  string
	ModelDisplayName string
	UpstreamModel    string
	Status           string
}

// BindBatch 原子创建一组 disabled 绑定；完全相同的现有绑定按幂等成功返回，不隐式覆盖。
func (s *Service) BindBatch(ctx context.Context, channelID int64, inputs []BatchBindingInput) ([]BindingResult, error) {
	if channelID <= 0 {
		return nil, invalidArgument("channel_id", "channel id must be positive")
	}
	inputs, err := normalizeBatchBindings(inputs)
	if err != nil {
		return nil, err
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return nil, storeFailed(err, "begin channel model batch binding")
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := s.queries.WithTx(tx)
	channel, err := q.GetChannel(ctx, channelID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, notFound("channel not found")
		}
		return nil, storeFailed(err, "get channel for batch binding")
	}
	if channel.Status == "archived" {
		return nil, conflict("archived channel cannot bind models")
	}

	results := make([]BindingResult, 0, len(inputs))
	for _, input := range inputs {
		model, lookupErr := q.LookupModelByID(ctx, input.ModelID)
		if lookupErr != nil {
			if errors.Is(lookupErr, pgx.ErrNoRows) {
				return nil, invalidArgument("model_id", fmt.Sprintf("model %d not found", input.ModelID))
			}
			return nil, storeFailed(lookupErr, "lookup model for batch binding")
		}
		binding, getErr := q.GetChannelModel(ctx, sqlc.GetChannelModelParams{ChannelID: channelID, ModelID: input.ModelID})
		if errors.Is(getErr, pgx.ErrNoRows) {
			binding, getErr = q.CreateChannelModel(ctx, sqlc.CreateChannelModelParams{
				ChannelID: channelID, ModelID: input.ModelID, UpstreamModel: input.UpstreamModel, Status: "disabled",
			})
			if getErr != nil {
				return nil, storeFailed(getErr, "create channel model batch binding")
			}
		} else if getErr != nil {
			return nil, storeFailed(getErr, "get channel model batch binding")
		} else if binding.UpstreamModel != input.UpstreamModel {
			return nil, conflict(fmt.Sprintf("model %d is already bound to upstream model %q", input.ModelID, binding.UpstreamModel))
		}
		results = append(results, bindingResult(binding, model))
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, storeFailed(err, "commit channel model batch binding")
	}
	return results, nil
}

// AdoptAndBind 委托参考目录服务在一个事务中完成采纳/复用和 disabled 绑定。
func (s *Service) AdoptAndBind(ctx context.Context, in modelcatalog.AdoptAndBindInput) (BindingResult, error) {
	if s.catalog == nil {
		return BindingResult{}, conflict("catalog adopter is unavailable")
	}
	result, err := s.catalog.AdoptAndBind(ctx, in)
	if err != nil {
		return BindingResult{}, err
	}
	model, err := s.queries.LookupModelByID(ctx, result.ModelID)
	if err != nil {
		return BindingResult{}, storeFailed(err, "read adopted model after binding")
	}
	return bindingResult(result.Binding, model), nil
}

func normalizeBatchBindings(inputs []BatchBindingInput) ([]BatchBindingInput, error) {
	if len(inputs) == 0 || len(inputs) > maxBatchBindings {
		return nil, invalidArgument("bindings", "bindings must contain 1 to 100 items")
	}
	seen := make(map[int64]string, len(inputs))
	result := make([]BatchBindingInput, 0, len(inputs))
	for _, input := range inputs {
		upstream := strings.TrimSpace(input.UpstreamModel)
		if input.ModelID <= 0 || upstream == "" {
			return nil, invalidArgument("bindings", "each binding requires a positive model_id and upstream_model")
		}
		if previous, ok := seen[input.ModelID]; ok {
			if previous != upstream {
				return nil, invalidArgument("bindings", fmt.Sprintf("model %d appears with different upstream models", input.ModelID))
			}
			continue
		}
		seen[input.ModelID] = upstream
		result = append(result, BatchBindingInput{ModelID: input.ModelID, UpstreamModel: upstream})
	}
	return result, nil
}

func bindingResult(binding sqlc.ChannelModel, model sqlc.Model) BindingResult {
	return BindingResult{
		ID: binding.ID, ChannelID: binding.ChannelID, ModelID: binding.ModelID,
		ModelExternalID: model.ModelID, ModelDisplayName: model.DisplayName,
		UpstreamModel: binding.UpstreamModel, Status: binding.Status,
	}
}
