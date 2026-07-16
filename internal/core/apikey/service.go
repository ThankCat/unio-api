package apikey

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/ThankCat/unio-gateway/internal/platform/failure"
	"github.com/ThankCat/unio-gateway/internal/platform/store/sqlc"
	"github.com/jackc/pgx/v5/pgtype"
)

var (
	// ErrInvalidUserID 表示创建 API Key 时 user_id 非法。
	ErrInvalidUserID = errors.New("invalid user id")

	// ErrInvalidName 表示创建 API Key 时 name 为空。
	ErrInvalidName = errors.New("invalid api key name")

	// ErrInvalidRoute 表示创建 API Key 时未提供合法线路（线路必填，无默认回落）。
	ErrInvalidRoute = errors.New("invalid route id")
)

// Store 定义 API Key 创建服务需要的数据库能力。
type Store interface {
	CreateAPIKey(ctx context.Context, arg sqlc.CreateAPIKeyParams) (sqlc.ApiKey, error)
}

// Service 负责 API Key 的业务创建流程。
type Service struct {
	store Store
}

// NewService 创建 API Key service。
func NewService(store Store) *Service {
	return &Service{store: store}
}

// CreatedKey 表示创建成功后返回给调用方的一次性结果。
type CreatedKey struct {
	ID        int64
	UserID    int64
	Name      string
	Plaintext string
	Prefix    string
	ExpiresAt *time.Time
}

// CreateParams 表示创建 API Key 需要的业务参数。
type CreateParams struct {
	UserID    int64
	Name      string
	ExpiresAt *time.Time
	// RouteID 是 Key 绑定的线路 ID，必填（> 0）：线路必须显式绑定，无默认回落。
	RouteID int64
}

// Create 创建新的 API Key。Plaintext 只能返回给调用方一次，不能保存到数据库。
func (s *Service) Create(ctx context.Context, params CreateParams) (*CreatedKey, error) {
	// TODO(阶段3/production): [GAP-3-007] API Key 创建缺少审计日志；开放 key 管理 API 前；接入 audit log 记录 actor、user、api_key 和操作结果。
	if params.UserID <= 0 {
		return nil, failure.Wrap(
			failure.CodeAPIKeyInvalidUserID,
			ErrInvalidUserID,
			failure.WithMessage(ErrInvalidUserID.Error()),
		)
	}

	name := strings.TrimSpace(params.Name)
	if name == "" {
		return nil, failure.Wrap(
			failure.CodeAPIKeyInvalidName,
			ErrInvalidName,
			failure.WithMessage(ErrInvalidName.Error()),
		)
	}

	// 线路必填：API Key 必须显式绑定一条线路（无默认线路回落）。DB NOT NULL 是最终兜底。
	if params.RouteID <= 0 {
		return nil, failure.Wrap(
			failure.CodeAPIKeyInvalidRoute,
			ErrInvalidRoute,
			failure.WithMessage(ErrInvalidRoute.Error()),
		)
	}

	generatedKey, err := Generate()
	if err != nil {
		return nil, failure.Wrap(
			failure.CodeAPIKeyGenerateFailed,
			err,
			failure.WithMessage("generate api key"),
		)
	}

	expiresAt := pgtype.Timestamptz{Valid: false}
	if params.ExpiresAt != nil {
		expiresAt = pgtype.Timestamptz{Time: *params.ExpiresAt, Valid: true}
	}

	storeParams := sqlc.CreateAPIKeyParams{
		UserID:       params.UserID,
		Name:         name,
		KeyPrefix:    generatedKey.Prefix,
		KeyHash:      generatedKey.Hash,
		KeyPlaintext: pgtype.Text{String: generatedKey.Plaintext, Valid: true},
		ExpiresAt:    expiresAt,
		RouteID:      params.RouteID,
	}
	storedKey, err := s.store.CreateAPIKey(ctx, storeParams)
	if err != nil {
		return nil, failure.Wrap(
			failure.CodeAPIKeyStoreFailed,
			err,
			failure.WithMessage("create api key"),
		)
	}

	var createdExpiresAt *time.Time
	if storedKey.ExpiresAt.Valid {
		t := storedKey.ExpiresAt.Time
		createdExpiresAt = &t
	}

	return &CreatedKey{
		ID:        storedKey.ID,
		UserID:    storedKey.UserID,
		Name:      storedKey.Name,
		Plaintext: generatedKey.Plaintext,
		Prefix:    storedKey.KeyPrefix,
		ExpiresAt: createdExpiresAt,
	}, nil
}
