// Package capability 编排 admin 管理端（M5 能力管理）的能力数据读写、models.dev 同步、
// adapter 画像物化与 enforce 只读状态。
//
// 它复用 core/capability 的 Store（写入前做 key 注册表 / 支持级别校验，渠道层只能减法），
// 不绕过校验直接写 sqlc。enforce 切换不在本切片范围：只读展示部署 env 的开关状态 + observe
// 期判定分布，真正翻 enforce 仍是改 gateway env + 重启的运营动作（见 phase-13 STATUS Slice 5）。
package capability

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	core "github.com/ThankCat/unio-api/internal/core/capability"
	"github.com/ThankCat/unio-api/internal/platform/failure"
)

// Store 是 admin 能力管理所需的数据访问能力（由 core/capability.Store 满足）。
type Store interface {
	LookupModelByID(ctx context.Context, id int64) (core.Model, error)
	ListModelCapabilities(ctx context.Context, modelID int64) ([]core.ModelCapability, error)
	UpsertModelCapability(ctx context.Context, params core.UpsertModelCapabilityParams) (core.ModelCapability, error)
	DeleteModelCapability(ctx context.Context, modelID int64, key core.Key) error
	ListChannelOverrides(ctx context.Context, channelID int64) ([]core.ChannelOverride, error)
	UpsertChannelOverride(ctx context.Context, params core.UpsertChannelOverrideParams) (core.ChannelOverride, error)
	DeleteChannelOverride(ctx context.Context, channelID int64, key core.Key) error
}

// SetModelCapabilityInput 是写入模型能力声明的入参（source 固定 manual）。
type SetModelCapabilityInput struct {
	ModelID      int64
	Key          string
	SupportLevel string
	Limits       json.RawMessage
	Actor        string
}

// SetChannelOverrideInput 是写入渠道收紧策略的入参（只能减：limited / unsupported）。
type SetChannelOverrideInput struct {
	ChannelID    int64
	Key          string
	SupportLevel string
	Limits       json.RawMessage
	Reason       string
	Actor        string
}

// CapabilityService 编排模型能力 / 渠道收紧策略读写。
type CapabilityService struct {
	store Store
}

// NewCapabilityService 创建能力数据管理服务。
func NewCapabilityService(store Store) *CapabilityService {
	return &CapabilityService{store: store}
}

// Keys 返回全部已注册能力 key（升序），供前端下拉与校验。
func (s *CapabilityService) Keys() []string {
	keys := core.RegisteredKeys()
	out := make([]string, len(keys))
	for i, k := range keys {
		out[i] = string(k)
	}
	return out
}

// ListModelCapabilities 列出指定模型已声明的能力。
func (s *CapabilityService) ListModelCapabilities(ctx context.Context, modelID int64) ([]core.ModelCapability, error) {
	if modelID <= 0 {
		return nil, invalidArgument("id", "model id must be positive")
	}
	if err := ensureModelExists(ctx, s.store, modelID); err != nil {
		return nil, err
	}
	return s.store.ListModelCapabilities(ctx, modelID)
}

// SetModelCapability 写入/覆盖模型能力声明（阶段 14 起能力不带 source）。
func (s *CapabilityService) SetModelCapability(ctx context.Context, in SetModelCapabilityInput) (core.ModelCapability, error) {
	if in.ModelID <= 0 {
		return core.ModelCapability{}, invalidArgument("id", "model id must be positive")
	}
	key := core.Key(strings.TrimSpace(in.Key))
	if !core.IsRegisteredKey(key) {
		return core.ModelCapability{}, invalidArgument("capability_key", "capability key is not registered")
	}
	level := core.SupportLevel(strings.TrimSpace(in.SupportLevel))
	if !core.IsValidSupportLevel(level) {
		return core.ModelCapability{}, invalidArgument("support_level", "support_level must be full, limited or unsupported")
	}
	if err := validateLimits(level, in.Limits); err != nil {
		return core.ModelCapability{}, err
	}
	if err := ensureModelExists(ctx, s.store, in.ModelID); err != nil {
		return core.ModelCapability{}, err
	}

	return s.store.UpsertModelCapability(ctx, core.UpsertModelCapabilityParams{
		ModelID:      in.ModelID,
		Key:          key,
		SupportLevel: level,
		Limits:       normalizeLimits(in.Limits),
		UpdatedBy:    actorPtr(in.Actor),
	})
}

// DeleteModelCapability 撤销模型对某能力的声明（幂等，缺失也视为成功）。
func (s *CapabilityService) DeleteModelCapability(ctx context.Context, modelID int64, key string) error {
	if modelID <= 0 {
		return invalidArgument("id", "model id must be positive")
	}
	k := core.Key(strings.TrimSpace(key))
	if !core.IsRegisteredKey(k) {
		return invalidArgument("capability_key", "capability key is not registered")
	}
	return s.store.DeleteModelCapability(ctx, modelID, k)
}

// ListChannelOverrides 列出指定渠道的能力收紧策略。
func (s *CapabilityService) ListChannelOverrides(ctx context.Context, channelID int64) ([]core.ChannelOverride, error) {
	if channelID <= 0 {
		return nil, invalidArgument("id", "channel id must be positive")
	}
	return s.store.ListChannelOverrides(ctx, channelID)
}

// SetChannelOverride 写入/覆盖渠道收紧策略；support_level 只能是 limited / unsupported（只能减）。
func (s *CapabilityService) SetChannelOverride(ctx context.Context, in SetChannelOverrideInput) (core.ChannelOverride, error) {
	if in.ChannelID <= 0 {
		return core.ChannelOverride{}, invalidArgument("id", "channel id must be positive")
	}
	key := core.Key(strings.TrimSpace(in.Key))
	if !core.IsRegisteredKey(key) {
		return core.ChannelOverride{}, invalidArgument("capability_key", "capability key is not registered")
	}
	level := core.SupportLevel(strings.TrimSpace(in.SupportLevel))
	if !core.IsValidChannelOverrideLevel(level) {
		return core.ChannelOverride{}, invalidArgument("support_level", "channel override support_level must be limited or unsupported")
	}
	if err := validateLimits(level, in.Limits); err != nil {
		return core.ChannelOverride{}, err
	}

	return s.store.UpsertChannelOverride(ctx, core.UpsertChannelOverrideParams{
		ChannelID:    in.ChannelID,
		Key:          key,
		SupportLevel: level,
		Limits:       normalizeLimits(in.Limits),
		Reason:       trimPtr(in.Reason),
		UpdatedBy:    actorPtr(in.Actor),
	})
}

// DeleteChannelOverride 撤销渠道对某能力的收紧策略（幂等）。
func (s *CapabilityService) DeleteChannelOverride(ctx context.Context, channelID int64, key string) error {
	if channelID <= 0 {
		return invalidArgument("id", "channel id must be positive")
	}
	k := core.Key(strings.TrimSpace(key))
	if !core.IsRegisteredKey(k) {
		return invalidArgument("capability_key", "capability key is not registered")
	}
	return s.store.DeleteChannelOverride(ctx, channelID, k)
}

// modelLookup 是校验模型存在所需的最小读取能力（admin Store 与 core.Store 均满足）。
type modelLookup interface {
	LookupModelByID(ctx context.Context, id int64) (core.Model, error)
}

// ensureModelExists 校验模型存在，缺失返回 admin_not_found。
func ensureModelExists(ctx context.Context, l modelLookup, modelID int64) error {
	if _, err := l.LookupModelByID(ctx, modelID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) || failure.CodeOf(err) == failure.CodeCapabilityNotFound {
			return notFound("model not found")
		}
		return err
	}
	return nil
}

// validateLimits 校验 limits：仅在 limited 级别允许，且必须是合法 JSON。
func validateLimits(level core.SupportLevel, limits json.RawMessage) error {
	if !core.LimitsJSONPresent(limits) {
		return nil
	}
	if !json.Valid(limits) {
		return invalidArgument("limits", "limits must be valid JSON")
	}
	if level != core.SupportLevelLimited {
		return invalidArgument("limits", "limits are only allowed when support_level is limited")
	}
	return nil
}

// normalizeLimits 把空白/null 归一为 nil（写 NULL）。
func normalizeLimits(limits json.RawMessage) json.RawMessage {
	return core.NormalizeLimitsJSON(limits)
}

// actorPtr 把审计执行者转成可选指针；空串视为未知（nil）。
func actorPtr(actor string) *string {
	actor = strings.TrimSpace(actor)
	if actor == "" {
		return nil
	}
	return &actor
}

// trimPtr 把字符串 Trim 后转成可选指针；空串 → nil。
func trimPtr(s string) *string {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	return &s
}

// tsNarg 把可选时间过滤值转成 pgtype.Timestamptz：nil 表示不过滤（SQL NULL）。
func tsNarg(t *time.Time) pgtype.Timestamptz {
	if t == nil {
		return pgtype.Timestamptz{}
	}
	return pgtype.Timestamptz{Time: *t, Valid: true}
}

func invalidArgument(field, message string) error {
	return failure.New(
		failure.CodeAdminInvalidArgument,
		failure.WithMessage(message),
		failure.WithField("field", field),
	)
}

func notFound(message string) error {
	return failure.New(failure.CodeAdminNotFound, failure.WithMessage(message))
}

func storeFailed(cause error, message string) error {
	return failure.Wrap(failure.CodeAdminStoreFailed, cause, failure.WithMessage(message))
}
