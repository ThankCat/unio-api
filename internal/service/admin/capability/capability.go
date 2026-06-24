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

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	core "github.com/ThankCat/unio-api/internal/core/capability"
	"github.com/ThankCat/unio-api/internal/platform/failure"
	"github.com/ThankCat/unio-api/internal/platform/store/sqlc"
)

// Store 是 admin 能力管理所需的数据访问能力（由 core/capability.Store 满足）。
type Store interface {
	LookupModelByID(ctx context.Context, id int64) (core.Model, error)
	ListModelCapabilities(ctx context.Context, modelID int64) ([]core.ModelCapability, error)
	UpsertModelCapability(ctx context.Context, params core.UpsertModelCapabilityParams) (core.ModelCapability, error)
	DeleteModelCapability(ctx context.Context, modelID int64, key core.Key) error
	ListCapabilityKeys(ctx context.Context) ([]core.CapabilityKey, error)
	GetCapabilityKey(ctx context.Context, key core.Key) (core.CapabilityKey, error)
	CreateCapabilityKey(ctx context.Context, params core.CreateCapabilityKeyParams) (core.CapabilityKey, error)
	UpdateCapabilityKey(ctx context.Context, params core.UpdateCapabilityKeyParams) (core.CapabilityKey, error)
	DeleteCapabilityKey(ctx context.Context, key core.Key) error
	CapabilityKeyExists(ctx context.Context, key core.Key) (bool, error)
}

// TxBeginner 提供事务能力（由 pgxpool 满足），用于批量能力覆盖的原子写入。
type TxBeginner interface {
	Begin(ctx context.Context) (pgx.Tx, error)
}

// SetModelCapabilityInput 是写入模型能力声明的入参（source 固定 manual）。
type SetModelCapabilityInput struct {
	ModelID      int64
	Key          string
	SupportLevel string
	Limits       json.RawMessage
	Actor        string
}

// ModelCapabilityItem 是批量声明里的一条能力（key + 档位 + 可选 limits）。
type ModelCapabilityItem struct {
	Key          string
	SupportLevel string
	Limits       json.RawMessage
}

// ReplaceModelCapabilitiesInput 是「整表覆盖」模型能力声明的入参（声明式 replace-all）。
type ReplaceModelCapabilitiesInput struct {
	ModelID int64
	Items   []ModelCapabilityItem
	Actor   string
}

// CapabilityService 编排模型能力声明读写（渠道收紧已移除，DEC-023；能力闸门已移除，DEC-024）。
type CapabilityService struct {
	store   Store
	db      TxBeginner
	queries *sqlc.Queries
}

// NewCapabilityService 创建能力数据管理服务。
//
// db / queries 供批量整表覆盖（ReplaceModelCapabilities）原子写入；仅做单条 CRUD 时可传 nil。
func NewCapabilityService(store Store, db TxBeginner, queries *sqlc.Queries) *CapabilityService {
	return &CapabilityService{store: store, db: db, queries: queries}
}

// ListKeys 返回能力 key 字典（含中文描述），供前端下拉/矩阵与运维区分（DEC-024）。
func (s *CapabilityService) ListKeys(ctx context.Context) ([]core.CapabilityKey, error) {
	return s.store.ListCapabilityKeys(ctx)
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
	exists, err := s.store.CapabilityKeyExists(ctx, key)
	if err != nil {
		return core.ModelCapability{}, err
	}
	if !exists {
		return core.ModelCapability{}, invalidArgument("capability_key", "capability key is not in the capability dictionary")
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
	return s.store.DeleteModelCapability(ctx, modelID, k)
}

// ReplaceModelCapabilities 以声明式整表覆盖某模型的能力声明（一次保存多条，DEC-024 §6.2）。
//
// 先全量校验（key 在字典内 / 档位合法 / limits 仅 limited 允许 / key 不重复），任一不合法整批拒绝；
// 再在一个事务里清空该模型旧声明并写入新集合（replace-all），保证原子。返回覆盖后的完整能力列表。
func (s *CapabilityService) ReplaceModelCapabilities(ctx context.Context, in ReplaceModelCapabilitiesInput) ([]core.ModelCapability, error) {
	if in.ModelID <= 0 {
		return nil, invalidArgument("id", "model id must be positive")
	}
	if s.db == nil || s.queries == nil {
		return nil, storeFailed(errors.New("capability service not configured for batch writes"), "replace model capabilities")
	}
	if err := ensureModelExists(ctx, s.store, in.ModelID); err != nil {
		return nil, err
	}

	updatedBy := pgtype.Text{}
	if actor := strings.TrimSpace(in.Actor); actor != "" {
		updatedBy = pgtype.Text{String: actor, Valid: true}
	}

	seen := make(map[core.Key]struct{}, len(in.Items))
	params := make([]sqlc.UpsertModelCapabilityParams, 0, len(in.Items))
	for _, item := range in.Items {
		key := core.Key(strings.TrimSpace(item.Key))
		if key == "" {
			return nil, invalidArgument("capability_key", "capability key must not be empty")
		}
		if _, dup := seen[key]; dup {
			return nil, invalidArgument("capability_key", "duplicate capability key: "+string(key))
		}
		level := core.SupportLevel(strings.TrimSpace(item.SupportLevel))
		if !core.IsValidSupportLevel(level) {
			return nil, invalidArgument("support_level", "support_level must be full, limited or unsupported")
		}
		if err := validateLimits(level, item.Limits); err != nil {
			return nil, err
		}
		exists, err := s.store.CapabilityKeyExists(ctx, key)
		if err != nil {
			return nil, err
		}
		if !exists {
			return nil, invalidArgument("capability_key", "capability key is not in the capability dictionary: "+string(key))
		}
		seen[key] = struct{}{}
		params = append(params, sqlc.UpsertModelCapabilityParams{
			ModelID:       in.ModelID,
			CapabilityKey: string(key),
			SupportLevel:  string(level),
			Limits:        normalizeLimits(item.Limits),
			UpdatedBy:     updatedBy,
		})
	}

	tx, err := s.db.Begin(ctx)
	if err != nil {
		return nil, storeFailed(err, "begin replace capabilities transaction")
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := s.queries.WithTx(tx)

	if err := q.DeleteModelCapabilitiesByModel(ctx, in.ModelID); err != nil {
		return nil, storeFailed(err, "clear model capabilities")
	}
	for _, p := range params {
		if _, err := q.UpsertModelCapability(ctx, p); err != nil {
			return nil, storeFailed(err, "write model capability")
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, storeFailed(err, "commit replace capabilities transaction")
	}

	return s.store.ListModelCapabilities(ctx, in.ModelID)
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
