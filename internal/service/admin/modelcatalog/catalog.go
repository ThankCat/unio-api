// Package modelcatalog 编排 admin 管理端的 models.dev 目录浏览与「从目录采纳/刷新/提醒」。
//
// 目录是写入侧的素材库，运行时永不读取（阶段 14）。采纳是一次快照拷贝并建立关联；
// 刷新用目录最新值覆盖运营模型的元数据 + 能力并更新基线指纹；提醒状态控制追更弹窗。
package modelcatalog

import (
	"context"
	"encoding/json"
	"errors"
	"math/big"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/ThankCat/unio-api/internal/core/capability"
	"github.com/ThankCat/unio-api/internal/platform/failure"
	"github.com/ThankCat/unio-api/internal/platform/store/sqlc"
)

// modelIDPattern 与 admin model 服务保持一致：字母数字开头，允许 . _ : -，长度 1..128（不允许 /）。
var modelIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`)

// TxBeginner 提供事务能力（由 pgxpool 满足），用于采纳/刷新的原子写入。
type TxBeginner interface {
	Begin(ctx context.Context) (pgx.Tx, error)
}

// Service 编排目录浏览与采纳/刷新/提醒。
type Service struct {
	db      TxBeginner
	queries *sqlc.Queries
}

// NewService 创建目录管理服务。
func NewService(db TxBeginner, queries *sqlc.Queries) *Service {
	return &Service{db: db, queries: queries}
}

// Entry 是目录条目浏览视图。
type Entry struct {
	CanonicalID              string
	Lab                      string
	DisplayName              string
	ContextWindowTokens      *int64
	MaxOutputTokens          *int64
	InputPriceUSDPerMTokens  *string
	OutputPriceUSDPerMTokens *string
	ReleaseDate              *time.Time
	RemovedUpstream          bool
	Fingerprint              string
	CapabilityCount          int64
	AdoptedCount             int64
}

// CapabilityHint 是目录条目的能力提示。
type CapabilityHint struct {
	Key          string
	SupportLevel string
	Limits       json.RawMessage
}

// EntryDetail 是目录单条详情（含能力提示，供采纳预填）。
type EntryDetail struct {
	Entry
	Capabilities []CapabilityHint
}

// ListParams 是目录分页/搜索入参。
type ListParams struct {
	Query  string
	Lab    string
	Limit  int32
	Offset int32
}

// ListResult 是目录分页结果。
type ListResult struct {
	Items []Entry
	Total int64
}

// AdoptInput 是「从目录采纳」的最终入参（采纳界面可编辑后的值）。
type AdoptInput struct {
	CanonicalID              string
	ModelID                  string
	DisplayName              string
	OwnedBy                  string
	Status                   string
	ContextWindowTokens      *int64
	MaxOutputTokens          *int64
	InputPriceUSDPerMTokens  *string
	OutputPriceUSDPerMTokens *string
	ReleaseDate              *time.Time
	Capabilities             []CapabilityHint
}

// ReminderAction 是更新提醒动作。
type ReminderAction string

const (
	// ReminderDismiss 忽略本次更新（记下当前目录指纹）。
	ReminderDismiss ReminderAction = "dismiss"
	// ReminderMute 永久忽略更新。
	ReminderMute ReminderAction = "mute"
	// ReminderUnmute 取消永久忽略。
	ReminderUnmute ReminderAction = "unmute"
	// ReminderSnooze 稍后提醒（需 snooze_until）。
	ReminderSnooze ReminderAction = "snooze"
)

// List 分页/搜索目录条目。
func (s *Service) List(ctx context.Context, params ListParams) (ListResult, error) {
	q := textParam(params.Query)
	lab := textParam(params.Lab)

	rows, err := s.queries.ListModelCatalogPage(ctx, sqlc.ListModelCatalogPageParams{
		Q:          q,
		Lab:        lab,
		PageLimit:  params.Limit,
		PageOffset: params.Offset,
	})
	if err != nil {
		return ListResult{}, storeFailed(err, "list model catalog")
	}

	total, err := s.queries.CountModelCatalog(ctx, sqlc.CountModelCatalogParams{Q: q, Lab: lab})
	if err != nil {
		return ListResult{}, storeFailed(err, "count model catalog")
	}

	items := make([]Entry, 0, len(rows))
	for _, row := range rows {
		items = append(items, Entry{
			CanonicalID:              row.CanonicalID,
			Lab:                      row.Lab,
			DisplayName:              row.DisplayName,
			ContextWindowTokens:      int64Ptr(row.ContextWindowTokens),
			MaxOutputTokens:          int64Ptr(row.MaxOutputTokens),
			InputPriceUSDPerMTokens:  numericString(row.InputPriceUsdPerMillionTokens),
			OutputPriceUSDPerMTokens: numericString(row.OutputPriceUsdPerMillionTokens),
			ReleaseDate:              datePtr(row.ReleaseDate),
			RemovedUpstream:          row.RemovedUpstreamAt.Valid,
			Fingerprint:              row.Fingerprint,
			CapabilityCount:          row.CapabilityCount,
			AdoptedCount:             row.AdoptedCount,
		})
	}

	return ListResult{Items: items, Total: total}, nil
}

// Get 读取目录单条详情 + 能力提示。
func (s *Service) Get(ctx context.Context, canonicalID string) (EntryDetail, error) {
	canonicalID = strings.TrimSpace(canonicalID)
	if canonicalID == "" {
		return EntryDetail{}, invalidArgument("canonical_id", "canonical_id is required")
	}

	row, err := s.queries.GetModelCatalogEntry(ctx, canonicalID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return EntryDetail{}, notFound("catalog entry not found")
		}
		return EntryDetail{}, storeFailed(err, "get model catalog entry")
	}

	caps, err := s.listCapabilityHints(ctx, canonicalID)
	if err != nil {
		return EntryDetail{}, err
	}

	return EntryDetail{
		Entry: Entry{
			CanonicalID:              row.CanonicalID,
			Lab:                      row.Lab,
			DisplayName:              row.DisplayName,
			ContextWindowTokens:      int64Ptr(row.ContextWindowTokens),
			MaxOutputTokens:          int64Ptr(row.MaxOutputTokens),
			InputPriceUSDPerMTokens:  numericString(row.InputPriceUsdPerMillionTokens),
			OutputPriceUSDPerMTokens: numericString(row.OutputPriceUsdPerMillionTokens),
			ReleaseDate:              datePtr(row.ReleaseDate),
			RemovedUpstream:          row.RemovedUpstreamAt.Valid,
			Fingerprint:              row.Fingerprint,
			AdoptedCount:             row.AdoptedCount,
			CapabilityCount:          int64(len(caps)),
		},
		Capabilities: caps,
	}, nil
}

func (s *Service) listCapabilityHints(ctx context.Context, canonicalID string) ([]CapabilityHint, error) {
	rows, err := s.queries.ListModelCatalogCapabilities(ctx, canonicalID)
	if err != nil {
		return nil, storeFailed(err, "list catalog capabilities")
	}
	hints := make([]CapabilityHint, 0, len(rows))
	for _, row := range rows {
		hints = append(hints, CapabilityHint{
			Key:          row.CapabilityKey,
			SupportLevel: row.SupportLevel,
			Limits:       json.RawMessage(row.Limits),
		})
	}
	return hints, nil
}

// Adopt 在单事务内从目录采纳创建模型：建 model（source=catalog）+ 批量能力 + 目录关联（基线=当前指纹）。
// 返回新建模型的内部 ID，供上层回读完整模型。
func (s *Service) Adopt(ctx context.Context, in AdoptInput) (int64, error) {
	canonicalID := strings.TrimSpace(in.CanonicalID)
	if canonicalID == "" {
		return 0, invalidArgument("canonical_id", "canonical_id is required")
	}
	modelID := strings.TrimSpace(in.ModelID)
	if !modelIDPattern.MatchString(modelID) {
		return 0, invalidArgument("model_id", "model_id must match ^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$")
	}
	displayName := strings.TrimSpace(in.DisplayName)
	if displayName == "" {
		return 0, invalidArgument("display_name", "display_name is required")
	}
	ownedBy := strings.TrimSpace(in.OwnedBy)
	if ownedBy == "" {
		return 0, invalidArgument("owned_by", "owned_by is required")
	}
	status := strings.TrimSpace(in.Status)
	if status != "enabled" && status != "disabled" {
		return 0, invalidArgument("status", "status must be enabled or disabled")
	}
	caps, err := validateCapabilities(in.Capabilities)
	if err != nil {
		return 0, err
	}
	inputPrice, err := numericParam(in.InputPriceUSDPerMTokens)
	if err != nil {
		return 0, invalidArgument("input_price_usd_per_million_tokens", "input price must be a non-negative decimal")
	}
	outputPrice, err := numericParam(in.OutputPriceUSDPerMTokens)
	if err != nil {
		return 0, invalidArgument("output_price_usd_per_million_tokens", "output price must be a non-negative decimal")
	}

	// 采纳基线 = 目录条目当前指纹（采纳后目录再变才提示更新）。
	entry, err := s.queries.GetModelCatalogEntry(ctx, canonicalID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return 0, notFound("catalog entry not found")
		}
		return 0, storeFailed(err, "get catalog entry for adopt")
	}

	tx, err := s.db.Begin(ctx)
	if err != nil {
		return 0, storeFailed(err, "begin adopt transaction")
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := s.queries.WithTx(tx)

	model, err := q.CreateModelFromCatalog(ctx, sqlc.CreateModelFromCatalogParams{
		ModelID:                        modelID,
		DisplayName:                    displayName,
		OwnedBy:                        ownedBy,
		Status:                         status,
		MaxOutputTokens:                int8Param(in.MaxOutputTokens),
		ContextWindowTokens:            int8Param(in.ContextWindowTokens),
		InputPriceUsdPerMillionTokens:  inputPrice,
		OutputPriceUsdPerMillionTokens: outputPrice,
		ReleaseDate:                    dateParam(in.ReleaseDate),
	})
	if err != nil {
		if isUniqueViolation(err) {
			return 0, conflict("model_id already exists")
		}
		return 0, storeFailed(err, "create model from catalog")
	}

	for _, c := range caps {
		if _, err := q.UpsertModelCapability(ctx, sqlc.UpsertModelCapabilityParams{
			ModelID:       model.ID,
			CapabilityKey: c.Key,
			SupportLevel:  c.SupportLevel,
			Limits:        limitsBytes(c.Limits),
		}); err != nil {
			return 0, storeFailed(err, "write adopted capability")
		}
	}

	if _, err := q.CreateModelCatalogLink(ctx, sqlc.CreateModelCatalogLinkParams{
		ModelID:            model.ID,
		CanonicalID:        canonicalID,
		AdoptedFingerprint: entry.Fingerprint,
	}); err != nil {
		return 0, storeFailed(err, "create catalog link")
	}

	if err := tx.Commit(ctx); err != nil {
		return 0, storeFailed(err, "commit adopt transaction")
	}

	return model.ID, nil
}

// Refresh 在单事务内用目录最新值覆盖采纳模型的元数据 + 能力，并把基线指纹更新为最新（model_id 不变）。
func (s *Service) Refresh(ctx context.Context, modelID int64) error {
	if modelID <= 0 {
		return invalidArgument("id", "model id must be positive")
	}

	link, err := s.queries.GetModelCatalogLink(ctx, modelID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return notFound("model is not adopted from catalog")
		}
		return storeFailed(err, "get catalog link")
	}

	entry, err := s.queries.GetModelCatalogEntry(ctx, link.CanonicalID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return notFound("catalog entry not found")
		}
		return storeFailed(err, "get catalog entry for refresh")
	}
	hints, err := s.listCapabilityHints(ctx, link.CanonicalID)
	if err != nil {
		return err
	}

	tx, err := s.db.Begin(ctx)
	if err != nil {
		return storeFailed(err, "begin refresh transaction")
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := s.queries.WithTx(tx)

	if _, err := q.RefreshAdoptedModelFromCatalog(ctx, sqlc.RefreshAdoptedModelFromCatalogParams{
		ID:                             modelID,
		DisplayName:                    entry.DisplayName,
		OwnedBy:                        entry.Lab,
		MaxOutputTokens:                entry.MaxOutputTokens,
		ContextWindowTokens:            entry.ContextWindowTokens,
		InputPriceUsdPerMillionTokens:  entry.InputPriceUsdPerMillionTokens,
		OutputPriceUsdPerMillionTokens: entry.OutputPriceUsdPerMillionTokens,
		ReleaseDate:                    entry.ReleaseDate,
	}); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return notFound("model not found")
		}
		return storeFailed(err, "refresh model metadata")
	}

	if err := q.DeleteModelCapabilitiesByModel(ctx, modelID); err != nil {
		return storeFailed(err, "clear model capabilities")
	}
	for _, h := range hints {
		if _, err := q.UpsertModelCapability(ctx, sqlc.UpsertModelCapabilityParams{
			ModelID:       modelID,
			CapabilityKey: h.Key,
			SupportLevel:  h.SupportLevel,
			Limits:        limitsBytes(h.Limits),
		}); err != nil {
			return storeFailed(err, "rewrite model capability")
		}
	}

	if err := q.UpdateModelCatalogLinkBaseline(ctx, sqlc.UpdateModelCatalogLinkBaselineParams{
		ModelID:            modelID,
		AdoptedFingerprint: entry.Fingerprint,
	}); err != nil {
		return storeFailed(err, "update catalog link baseline")
	}

	if err := tx.Commit(ctx); err != nil {
		return storeFailed(err, "commit refresh transaction")
	}

	return nil
}

// SetReminder 调整采纳模型的更新提醒状态。
func (s *Service) SetReminder(ctx context.Context, modelID int64, action ReminderAction, snoozeUntil *time.Time) error {
	if modelID <= 0 {
		return invalidArgument("id", "model id must be positive")
	}

	link, err := s.queries.GetModelCatalogLink(ctx, modelID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return notFound("model is not adopted from catalog")
		}
		return storeFailed(err, "get catalog link")
	}

	switch action {
	case ReminderDismiss:
		entry, err := s.queries.GetModelCatalogEntry(ctx, link.CanonicalID)
		if err != nil {
			return storeFailed(err, "get catalog entry for dismiss")
		}
		if err := s.queries.SetModelCatalogLinkDismissed(ctx, sqlc.SetModelCatalogLinkDismissedParams{
			ModelID:              modelID,
			DismissedFingerprint: pgtype.Text{String: entry.Fingerprint, Valid: true},
		}); err != nil {
			return storeFailed(err, "dismiss catalog update")
		}
	case ReminderMute:
		if err := s.queries.SetModelCatalogLinkMuted(ctx, sqlc.SetModelCatalogLinkMutedParams{ModelID: modelID, ReminderMuted: true}); err != nil {
			return storeFailed(err, "mute catalog reminder")
		}
	case ReminderUnmute:
		if err := s.queries.SetModelCatalogLinkMuted(ctx, sqlc.SetModelCatalogLinkMutedParams{ModelID: modelID, ReminderMuted: false}); err != nil {
			return storeFailed(err, "unmute catalog reminder")
		}
	case ReminderSnooze:
		if snoozeUntil == nil {
			return invalidArgument("snooze_until", "snooze_until is required for snooze action")
		}
		if err := s.queries.SetModelCatalogLinkSnooze(ctx, sqlc.SetModelCatalogLinkSnoozeParams{
			ModelID:             modelID,
			ReminderSnoozeUntil: pgtype.Timestamptz{Time: *snoozeUntil, Valid: true},
		}); err != nil {
			return storeFailed(err, "snooze catalog reminder")
		}
	default:
		return invalidArgument("action", "action must be dismiss, mute, unmute or snooze")
	}

	return nil
}

func validateCapabilities(in []CapabilityHint) ([]CapabilityHint, error) {
	seen := make(map[string]struct{}, len(in))
	out := make([]CapabilityHint, 0, len(in))
	for _, c := range in {
		key := capability.Key(strings.TrimSpace(c.Key))
		if !capability.IsRegisteredKey(key) {
			return nil, invalidArgument("capability_key", "capability key is not registered: "+string(key))
		}
		level := capability.SupportLevel(strings.TrimSpace(c.SupportLevel))
		if !capability.IsValidSupportLevel(level) {
			return nil, invalidArgument("support_level", "support_level must be full, limited or unsupported")
		}
		if capability.LimitsJSONPresent(c.Limits) {
			if level != capability.SupportLevelLimited {
				return nil, invalidArgument("limits", "limits only allowed at limited support level")
			}
			if !json.Valid(c.Limits) {
				return nil, invalidArgument("limits", "limits must be valid JSON")
			}
		}
		if _, dup := seen[string(key)]; dup {
			return nil, invalidArgument("capability_key", "duplicate capability key: "+string(key))
		}
		seen[string(key)] = struct{}{}
		out = append(out, CapabilityHint{
			Key:          string(key),
			SupportLevel: string(level),
			Limits:       capability.NormalizeLimitsJSON(c.Limits),
		})
	}
	return out, nil
}

func limitsBytes(raw json.RawMessage) []byte {
	n := capability.NormalizeLimitsJSON(raw)
	if len(n) == 0 {
		return nil
	}
	return []byte(n)
}

func invalidArgument(field, message string) error {
	return failure.New(failure.CodeAdminInvalidArgument, failure.WithMessage(message), failure.WithField("field", field))
}

func notFound(message string) error {
	return failure.New(failure.CodeAdminNotFound, failure.WithMessage(message))
}

func conflict(message string) error {
	return failure.New(failure.CodeAdminConflict, failure.WithMessage(message))
}

func storeFailed(cause error, message string) error {
	return failure.Wrap(failure.CodeAdminStoreFailed, cause, failure.WithMessage(message))
}

func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}

func textParam(s string) pgtype.Text {
	if strings.TrimSpace(s) == "" {
		return pgtype.Text{}
	}
	return pgtype.Text{String: strings.TrimSpace(s), Valid: true}
}

func int8Param(v *int64) pgtype.Int8 {
	if v == nil {
		return pgtype.Int8{}
	}
	return pgtype.Int8{Int64: *v, Valid: true}
}

func dateParam(v *time.Time) pgtype.Date {
	if v == nil {
		return pgtype.Date{}
	}
	return pgtype.Date{Time: *v, Valid: true}
}

func numericParam(value *string) (pgtype.Numeric, error) {
	if value == nil || strings.TrimSpace(*value) == "" {
		return pgtype.Numeric{}, nil
	}
	var n pgtype.Numeric
	if err := n.Scan(strings.TrimSpace(*value)); err != nil {
		return pgtype.Numeric{}, err
	}
	if n.Valid && n.Int != nil && n.Int.Sign() < 0 {
		return pgtype.Numeric{}, errors.New("price must be non-negative")
	}
	return n, nil
}

func int64Ptr(value pgtype.Int8) *int64 {
	if !value.Valid {
		return nil
	}
	out := value.Int64
	return &out
}

func datePtr(value pgtype.Date) *time.Time {
	if !value.Valid {
		return nil
	}
	out := value.Time
	return &out
}

func numericString(value pgtype.Numeric) *string {
	if !value.Valid || value.NaN || value.InfinityModifier != pgtype.Finite {
		return nil
	}
	if value.Int == nil {
		zero := "0"
		return &zero
	}
	negative := value.Int.Sign() < 0
	digits := new(big.Int).Abs(value.Int).String()
	exp := int(value.Exp)

	var formatted string
	switch {
	case exp == 0:
		formatted = digits
	case exp > 0:
		formatted = digits + strings.Repeat("0", exp)
	default:
		scale := -exp
		if len(digits) <= scale {
			digits = strings.Repeat("0", scale-len(digits)+1) + digits
		}
		point := len(digits) - scale
		formatted = digits[:point] + "." + digits[point:]
	}
	if negative {
		formatted = "-" + formatted
	}
	return &formatted
}
