// Package apikeys 提供 Console 侧的 API 密钥自助管理。
//
// 两条贯穿全包的规则：
//
//  1. 归属。所有入参都带 UserID，并原样传给 SQL 的 user_id 条件。任何按 id 定位的读写
//     都必须同时匹配 user_id，否则用户能操作别人的密钥。
//  2. 明文。Create 返回的 Plaintext 是明文唯一一次露面的地方，不落库也不写日志；
//     其余方法的返回值里根本没有这个字段，从类型上就取不到。
//
// 消耗与请求数沿用「账本 USD 净扣费 > 0」口径，与 console/requests、console/usage 一致，
// 三个页面的数字必须能互相对上。
package apikeys

import (
	"context"
	"errors"
	"math/big"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/ThankCat/unio-gateway/internal/core/apikey"
	"github.com/ThankCat/unio-gateway/internal/platform/store/sqlc"
	"github.com/ThankCat/unio-gateway/internal/service/admin/opsutil"
	consoleservice "github.com/ThankCat/unio-gateway/internal/service/console"
)

// 密钥对外状态，由时间戳派生：revoked > disabled > expired > active。
const (
	StatusActive   = "active"
	StatusDisabled = "disabled"
	StatusExpired  = "expired"
	StatusRevoked  = "revoked"
)

// 列表分页与名称长度上限。密钥是低基数资源，个人账号通常十几把。
const (
	defaultPageSize = 50
	maxPageSize     = 200
	maxNameLen      = 64
	topModelLimit   = 3
)

// pgForeignKeyViolation 是 Postgres 外键冲突的 SQLSTATE。
const pgForeignKeyViolation = "23503"

// Store 是密钥自助管理所需的存储能力。
type Store interface {
	ListConsoleAPIKeys(context.Context, sqlc.ListConsoleAPIKeysParams) ([]sqlc.ListConsoleAPIKeysRow, error)
	CountConsoleAPIKeys(context.Context, sqlc.CountConsoleAPIKeysParams) (int64, error)
	SummarizeConsoleAPIKeys(context.Context, int64) (sqlc.SummarizeConsoleAPIKeysRow, error)
	SummarizeConsoleAPIKeyWindow(context.Context, sqlc.SummarizeConsoleAPIKeyWindowParams) (sqlc.SummarizeConsoleAPIKeyWindowRow, error)
	GetConsoleAPIKey(context.Context, sqlc.GetConsoleAPIKeyParams) (sqlc.GetConsoleAPIKeyRow, error)
	ListConsoleAPIKeyDailyCharge(context.Context, sqlc.ListConsoleAPIKeyDailyChargeParams) ([]sqlc.ListConsoleAPIKeyDailyChargeRow, error)
	ListConsoleAPIKeyTopModels(context.Context, sqlc.ListConsoleAPIKeyTopModelsParams) ([]sqlc.ListConsoleAPIKeyTopModelsRow, error)
	CreateConsoleAPIKey(context.Context, sqlc.CreateConsoleAPIKeyParams) (sqlc.CreateConsoleAPIKeyRow, error)
	UpdateConsoleAPIKey(context.Context, sqlc.UpdateConsoleAPIKeyParams) (sqlc.UpdateConsoleAPIKeyRow, error)
	RevokeConsoleAPIKey(context.Context, sqlc.RevokeConsoleAPIKeyParams) (sqlc.RevokeConsoleAPIKeyRow, error)
	DeleteConsoleAPIKey(context.Context, sqlc.DeleteConsoleAPIKeyParams) (int64, error)
}

var _ Store = (*sqlc.Queries)(nil)

// DailyCharge 是按天分桶的消耗，画列表迷你走势与详情趋势。
type DailyCharge struct {
	Day          time.Time
	RequestCount int64
	ChargeUSD    string
}

// Key 是客户可见的密钥视图。这里没有明文字段，也不该有。
type Key struct {
	ID        int64
	Name      string
	KeyPrefix string
	// KeySuffix 为 nil 表示这把 key 建于记录尾段之前，展示层退化成只有前缀的掩码。
	KeySuffix *string
	Status    string
	// SpendLimit 为 nil 表示不限额。
	SpendLimit *string
	SpentTotal string
	// PeriodChargeUSD / RequestCount 是查询时间窗内的计费口径统计。
	PeriodChargeUSD string
	RequestCount    int64
	LastUsedAt      *time.Time
	ExpiresAt       *time.Time
	CreatedAt       time.Time
	UpdatedAt       time.Time
	Trend           []DailyCharge
}

// CreatedKey 是创建结果。Plaintext 只在这里出现一次。
type CreatedKey struct {
	Key
	Plaintext string
}

// TopModel 是详情页的模型排行项。
type TopModel struct {
	ModelID      string
	DisplayName  string
	RequestCount int64
	ChargeUSD    string
}

// Detail 是单把密钥的详情：Key 之上补趋势与模型排行。
type Detail struct {
	Key
	TopModels []TopModel
}

// Summary 是页面顶栏。KeyTotal / KeyActive / NearLimit 是账户当下事实，
// 与时间窗无关；后两项是窗口内合计。
type Summary struct {
	KeyTotal     int64
	KeyActive    int64
	NearLimit    int64
	RequestCount int64
	ChargeUSD    string
}

// Window 是统计时间窗，左闭右开。
type Window struct {
	From time.Time
	To   time.Time
	// TZ 是 IANA 时区名，决定按天分桶的边界。空值按 UTC。
	TZ string
}

// ListParams 是密钥列表查询条件。
type ListParams struct {
	UserID int64
	Window
	Search string
	// Status 为空表示不按状态过滤；非空必须是四个派生状态之一。
	Status string
	Limit  int32
	Offset int32
}

// GetParams 是单把密钥的查询条件。
type GetParams struct {
	UserID int64
	KeyID  int64
	Window
}

// CreateParams 是创建密钥的业务参数。
type CreateParams struct {
	UserID int64
	Name   string
	// SpendLimit 为 nil 或空串表示不限额。
	SpendLimit *string
	ExpiresAt  *time.Time
}

// UpdateParams 是更新密钥的业务参数。
// 每个字段配一个 Provided 标志：false 表示本次不动这个字段，
// true 且值为空/nil 表示清空（不限额 / 永不过期 / 启用）。
type UpdateParams struct {
	UserID int64
	KeyID  int64

	Name         *string
	NameProvided bool

	SpendLimit         *string
	SpendLimitProvided bool

	ExpiresAt       *time.Time
	ExpiresProvided bool

	Disabled         *bool
	DisabledProvided bool
}

// Service 提供密钥自助管理。
type Service struct {
	store Store
	now   func() time.Time
}

// NewService 创建密钥管理 service。
func NewService(store Store) *Service {
	if store == nil {
		panic("console apikeys: store is required")
	}
	return &Service{store: store, now: time.Now}
}

// List 返回当前用户的密钥列表与总数，并给每把密钥附上窗口内的按天走势。
func (s *Service) List(ctx context.Context, params ListParams) ([]Key, int64, *consoleservice.Error) {
	window, err := s.normalizeWindow(params.Window)
	if err != nil {
		return nil, 0, err
	}
	if statusErr := validateStatus(params.Status); statusErr != nil {
		return nil, 0, statusErr
	}
	limit, offset := clampPage(params.Limit, params.Offset)

	rows, listErr := s.store.ListConsoleAPIKeys(ctx, sqlc.ListConsoleAPIKeysParams{
		UserID:     params.UserID,
		FromTime:   pgtype.Timestamptz{Time: window.From, Valid: true},
		ToTime:     pgtype.Timestamptz{Time: window.To, Valid: true},
		Search:     opsutil.TextNarg(strings.TrimSpace(params.Search)),
		Status:     opsutil.TextNarg(params.Status),
		PageLimit:  limit,
		PageOffset: offset,
	})
	if listErr != nil {
		return nil, 0, consoleservice.RequestUnavailable("list api keys", listErr)
	}

	total, countErr := s.store.CountConsoleAPIKeys(ctx, sqlc.CountConsoleAPIKeysParams{
		UserID: params.UserID,
		Search: opsutil.TextNarg(strings.TrimSpace(params.Search)),
		Status: opsutil.TextNarg(params.Status),
	})
	if countErr != nil {
		return nil, 0, consoleservice.RequestUnavailable("count api keys", countErr)
	}

	// 走势一次查完整个用户再按 key 分派，避免每把密钥单独查一次。
	trends, trendErr := s.dailyCharges(ctx, params.UserID, nil, window)
	if trendErr != nil {
		return nil, 0, trendErr
	}

	keys := make([]Key, 0, len(rows))
	for _, row := range rows {
		key := s.keyFromListRow(row)
		key.Trend = trends[row.ID]
		keys = append(keys, key)
	}
	return keys, total, nil
}

// Summary 返回页面顶栏：账户当下的密钥构成 + 窗口内的请求与消耗合计。
func (s *Service) Summary(ctx context.Context, userID int64, window Window) (Summary, *consoleservice.Error) {
	normalized, err := s.normalizeWindow(window)
	if err != nil {
		return Summary{}, err
	}
	counts, countErr := s.store.SummarizeConsoleAPIKeys(ctx, userID)
	if countErr != nil {
		return Summary{}, consoleservice.RequestUnavailable("summarize api keys", countErr)
	}
	usage, usageErr := s.store.SummarizeConsoleAPIKeyWindow(ctx, sqlc.SummarizeConsoleAPIKeyWindowParams{
		UserID:   userID,
		FromTime: pgtype.Timestamptz{Time: normalized.From, Valid: true},
		ToTime:   pgtype.Timestamptz{Time: normalized.To, Valid: true},
	})
	if usageErr != nil {
		return Summary{}, consoleservice.RequestUnavailable("summarize api key window", usageErr)
	}
	return Summary{
		KeyTotal:     counts.KeyTotal,
		KeyActive:    counts.KeyActive,
		NearLimit:    counts.NearLimit,
		RequestCount: usage.RequestCount,
		ChargeUSD:    opsutil.NumericString(usage.ChargeUsd),
	}, nil
}

// Get 返回单把密钥的详情。密钥不属于当前用户时按 not_found 处理，
// 不区分「不存在」和「不属于你」——区分开等于确认了别人密钥的存在。
func (s *Service) Get(ctx context.Context, params GetParams) (Detail, *consoleservice.Error) {
	window, err := s.normalizeWindow(params.Window)
	if err != nil {
		return Detail{}, err
	}
	if params.KeyID <= 0 {
		return Detail{}, consoleservice.InvalidArgument("id", "The api key id must be positive.")
	}

	row, getErr := s.store.GetConsoleAPIKey(ctx, sqlc.GetConsoleAPIKeyParams{
		ID:       params.KeyID,
		UserID:   params.UserID,
		FromTime: pgtype.Timestamptz{Time: window.From, Valid: true},
		ToTime:   pgtype.Timestamptz{Time: window.To, Valid: true},
	})
	if getErr != nil {
		if errors.Is(getErr, pgx.ErrNoRows) {
			return Detail{}, notFound()
		}
		return Detail{}, consoleservice.RequestUnavailable("get api key", getErr)
	}

	trends, trendErr := s.dailyCharges(ctx, params.UserID, &params.KeyID, window)
	if trendErr != nil {
		return Detail{}, trendErr
	}

	models, modelErr := s.store.ListConsoleAPIKeyTopModels(ctx, sqlc.ListConsoleAPIKeyTopModelsParams{
		UserID:   params.UserID,
		ApiKeyID: params.KeyID,
		FromTime: pgtype.Timestamptz{Time: window.From, Valid: true},
		ToTime:   pgtype.Timestamptz{Time: window.To, Valid: true},
		RowLimit: topModelLimit,
	})
	if modelErr != nil {
		return Detail{}, consoleservice.RequestUnavailable("list api key top models", modelErr)
	}
	topModels := make([]TopModel, 0, len(models))
	for _, model := range models {
		topModels = append(topModels, TopModel{
			ModelID:      model.ModelID,
			DisplayName:  model.DisplayName,
			RequestCount: model.RequestCount,
			ChargeUSD:    opsutil.NumericString(model.ChargeUsd),
		})
	}

	key := Key{
		ID:              row.ID,
		Name:            row.Name,
		KeyPrefix:       row.KeyPrefix,
		KeySuffix:       opsutil.TextPtr(row.KeySuffix),
		Status:          s.deriveStatus(row.DisabledAt, row.RevokedAt, row.ExpiresAt),
		SpendLimit:      opsutil.NumericStringPtr(row.SpendLimit),
		SpentTotal:      opsutil.NumericString(row.SpentTotal),
		PeriodChargeUSD: opsutil.NumericString(row.PeriodChargeUsd),
		RequestCount:    row.RequestCount,
		LastUsedAt:      opsutil.TimeValue(row.LastUsedAt),
		ExpiresAt:       opsutil.TimeValue(row.ExpiresAt),
		CreatedAt:       row.CreatedAt.Time,
		UpdatedAt:       row.UpdatedAt.Time,
		Trend:           trends[row.ID],
	}
	return Detail{Key: key, TopModels: topModels}, nil
}

// Create 生成一把新密钥。返回值里的 Plaintext 是它唯一一次露面。
func (s *Service) Create(ctx context.Context, params CreateParams) (CreatedKey, *consoleservice.Error) {
	name, nameErr := normalizeName(params.Name, true)
	if nameErr != nil {
		return CreatedKey{}, nameErr
	}
	spendLimit, limitErr := parseSpendLimit(params.SpendLimit)
	if limitErr != nil {
		return CreatedKey{}, limitErr
	}
	if expiresErr := s.validateExpiry(params.ExpiresAt); expiresErr != nil {
		return CreatedKey{}, expiresErr
	}

	generated, genErr := apikey.Generate()
	if genErr != nil {
		return CreatedKey{}, consoleservice.RequestUnavailable("generate api key", genErr)
	}

	row, createErr := s.store.CreateConsoleAPIKey(ctx, sqlc.CreateConsoleAPIKeyParams{
		UserID:     params.UserID,
		Name:       name,
		KeyPrefix:  generated.Prefix,
		KeySuffix:  pgtype.Text{String: generated.Suffix, Valid: true},
		KeyHash:    generated.Hash,
		ExpiresAt:  timestamptzNarg(params.ExpiresAt),
		SpendLimit: spendLimit,
	})
	if createErr != nil {
		return CreatedKey{}, consoleservice.RequestUnavailable("create api key", createErr)
	}

	return CreatedKey{
		Key: Key{
			ID:              row.ID,
			Name:            row.Name,
			KeyPrefix:       row.KeyPrefix,
			KeySuffix:       opsutil.TextPtr(row.KeySuffix),
			Status:          s.deriveStatus(row.DisabledAt, row.RevokedAt, row.ExpiresAt),
			SpendLimit:      opsutil.NumericStringPtr(row.SpendLimit),
			SpentTotal:      opsutil.NumericString(row.SpentTotal),
			PeriodChargeUSD: "0",
			LastUsedAt:      opsutil.TimeValue(row.LastUsedAt),
			ExpiresAt:       opsutil.TimeValue(row.ExpiresAt),
			CreatedAt:       row.CreatedAt.Time,
			UpdatedAt:       row.UpdatedAt.Time,
			Trend:           []DailyCharge{},
		},
		Plaintext: generated.Plaintext,
	}, nil
}

// Update 修改名称 / 额度 / 有效期 / 启停。已吊销的密钥不可再改。
func (s *Service) Update(ctx context.Context, params UpdateParams) (Key, *consoleservice.Error) {
	if params.KeyID <= 0 {
		return Key{}, consoleservice.InvalidArgument("id", "The api key id must be positive.")
	}
	if !params.NameProvided && !params.SpendLimitProvided &&
		!params.ExpiresProvided && !params.DisabledProvided {
		return Key{}, consoleservice.InvalidArgument("body", "At least one field must be provided.")
	}

	arg := sqlc.UpdateConsoleAPIKeyParams{
		ID:                 params.KeyID,
		UserID:             params.UserID,
		NameProvided:       params.NameProvided,
		SpendLimitProvided: params.SpendLimitProvided,
		ExpiresProvided:    params.ExpiresProvided,
		DisabledProvided:   params.DisabledProvided,
	}

	if params.NameProvided {
		// 名称是必填项：提供了就不许是空串，否则列表里会出现一行没有名字的密钥。
		name, nameErr := normalizeName(derefString(params.Name), true)
		if nameErr != nil {
			return Key{}, nameErr
		}
		arg.Name = name
	}
	if params.SpendLimitProvided {
		spendLimit, limitErr := parseSpendLimit(params.SpendLimit)
		if limitErr != nil {
			return Key{}, limitErr
		}
		arg.SpendLimit = spendLimit
	}
	if params.ExpiresProvided {
		if expiresErr := s.validateExpiry(params.ExpiresAt); expiresErr != nil {
			return Key{}, expiresErr
		}
		arg.ExpiresAt = timestamptzNarg(params.ExpiresAt)
	}
	if params.DisabledProvided {
		if params.Disabled != nil && *params.Disabled {
			arg.DisabledAt = pgtype.Timestamptz{Time: s.now(), Valid: true}
		}
	}

	row, err := s.store.UpdateConsoleAPIKey(ctx, arg)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			// 不存在、不属于当前用户、或已吊销，都归到 not_found。
			return Key{}, notFound()
		}
		return Key{}, consoleservice.RequestUnavailable("update api key", err)
	}
	return s.keyFromMutationRow(
		row.ID, row.Name, row.KeyPrefix, row.SpendLimit, row.SpentTotal,
		row.LastUsedAt, row.ExpiresAt, row.DisabledAt, row.RevokedAt,
		row.CreatedAt, row.UpdatedAt,
	), nil
}

// Revoke 永久吊销密钥。不可逆，重复吊销按 not_found 处理。
func (s *Service) Revoke(ctx context.Context, userID, keyID int64) (Key, *consoleservice.Error) {
	if keyID <= 0 {
		return Key{}, consoleservice.InvalidArgument("id", "The api key id must be positive.")
	}
	row, err := s.store.RevokeConsoleAPIKey(ctx, sqlc.RevokeConsoleAPIKeyParams{
		ID:     keyID,
		UserID: userID,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Key{}, notFound()
		}
		return Key{}, consoleservice.RequestUnavailable("revoke api key", err)
	}
	return s.keyFromMutationRow(
		row.ID, row.Name, row.KeyPrefix, row.SpendLimit, row.SpentTotal,
		row.LastUsedAt, row.ExpiresAt, row.DisabledAt, row.RevokedAt,
		row.CreatedAt, row.UpdatedAt,
	), nil
}

// Delete 物理删除密钥。已产生调用历史的密钥删不掉——外键挡着，
// 那些请求记录是计费与审计的依据，不能因为删一把密钥就断链。
func (s *Service) Delete(ctx context.Context, userID, keyID int64) *consoleservice.Error {
	if keyID <= 0 {
		return consoleservice.InvalidArgument("id", "The api key id must be positive.")
	}
	affected, err := s.store.DeleteConsoleAPIKey(ctx, sqlc.DeleteConsoleAPIKeyParams{
		ID:     keyID,
		UserID: userID,
	})
	if err != nil {
		if isForeignKeyViolation(err) {
			return &consoleservice.Error{
				Code:    "api_key_in_use",
				Message: "This api key has request history. Revoke it instead of deleting.",
				Status:  409,
				Cause:   err,
			}
		}
		return consoleservice.RequestUnavailable("delete api key", err)
	}
	if affected == 0 {
		return notFound()
	}
	return nil
}

// dailyCharges 查窗口内的按天消耗，按 api_key_id 分组返回。
// keyID 为 nil 时返回该用户全部密钥的分桶。
func (s *Service) dailyCharges(
	ctx context.Context,
	userID int64,
	keyID *int64,
	window Window,
) (map[int64][]DailyCharge, *consoleservice.Error) {
	arg := sqlc.ListConsoleAPIKeyDailyChargeParams{
		UserID:   userID,
		FromTime: pgtype.Timestamptz{Time: window.From, Valid: true},
		ToTime:   pgtype.Timestamptz{Time: window.To, Valid: true},
		Tz:       window.TZ,
	}
	if keyID != nil {
		arg.ApiKeyID = pgtype.Int8{Int64: *keyID, Valid: true}
	}
	rows, err := s.store.ListConsoleAPIKeyDailyCharge(ctx, arg)
	if err != nil {
		return nil, consoleservice.RequestUnavailable("list api key daily charge", err)
	}
	out := make(map[int64][]DailyCharge, len(rows))
	for _, row := range rows {
		out[row.ApiKeyID] = append(out[row.ApiKeyID], DailyCharge{
			Day:          row.BucketStart.Time,
			RequestCount: row.RequestCount,
			ChargeUSD:    opsutil.NumericString(row.ChargeUsd),
		})
	}
	return out, nil
}

func (s *Service) keyFromListRow(row sqlc.ListConsoleAPIKeysRow) Key {
	return Key{
		ID:              row.ID,
		Name:            row.Name,
		KeyPrefix:       row.KeyPrefix,
		KeySuffix:       opsutil.TextPtr(row.KeySuffix),
		Status:          s.deriveStatus(row.DisabledAt, row.RevokedAt, row.ExpiresAt),
		SpendLimit:      opsutil.NumericStringPtr(row.SpendLimit),
		SpentTotal:      opsutil.NumericString(row.SpentTotal),
		PeriodChargeUSD: opsutil.NumericString(row.PeriodChargeUsd),
		RequestCount:    row.RequestCount,
		LastUsedAt:      opsutil.TimeValue(row.LastUsedAt),
		ExpiresAt:       opsutil.TimeValue(row.ExpiresAt),
		CreatedAt:       row.CreatedAt.Time,
		UpdatedAt:       row.UpdatedAt.Time,
	}
}

// keyFromMutationRow 把 update / revoke 的 RETURNING 行装成 Key。
// 写操作不返回窗口统计，PeriodChargeUSD 留 0——调用方要最新数字应该重新拉列表。
func (s *Service) keyFromMutationRow(
	id int64,
	name, keyPrefix string,
	spendLimit, spentTotal pgtype.Numeric,
	lastUsedAt, expiresAt, disabledAt, revokedAt, createdAt, updatedAt pgtype.Timestamptz,
) Key {
	return Key{
		ID:              id,
		Name:            name,
		KeyPrefix:       keyPrefix,
		Status:          s.deriveStatus(disabledAt, revokedAt, expiresAt),
		SpendLimit:      opsutil.NumericStringPtr(spendLimit),
		SpentTotal:      opsutil.NumericString(spentTotal),
		PeriodChargeUSD: "0",
		LastUsedAt:      opsutil.TimeValue(lastUsedAt),
		ExpiresAt:       opsutil.TimeValue(expiresAt),
		CreatedAt:       createdAt.Time,
		UpdatedAt:       updatedAt.Time,
		Trend:           []DailyCharge{},
	}
}

// deriveStatus 按优先级派生状态：吊销 > 停用 > 过期 > 启用。
// 与 SQL 里的状态过滤表达式必须保持一致。
func (s *Service) deriveStatus(disabledAt, revokedAt, expiresAt pgtype.Timestamptz) string {
	switch {
	case revokedAt.Valid:
		return StatusRevoked
	case disabledAt.Valid:
		return StatusDisabled
	case expiresAt.Valid && !expiresAt.Time.After(s.now()):
		return StatusExpired
	default:
		return StatusActive
	}
}

// normalizeWindow 校验时间窗并补默认时区。
func (s *Service) normalizeWindow(window Window) (Window, *consoleservice.Error) {
	if window.From.IsZero() || window.To.IsZero() {
		return Window{}, consoleservice.InvalidArgument("from", "Both from and to are required.")
	}
	if !window.To.After(window.From) {
		return Window{}, consoleservice.InvalidArgument("to", "The to time must be after from.")
	}
	if window.TZ == "" {
		window.TZ = "UTC"
	} else if _, err := time.LoadLocation(window.TZ); err != nil {
		return Window{}, consoleservice.InvalidArgument("tz", "The tz must be a valid IANA time zone name.")
	}
	return window, nil
}

// validateExpiry 拒绝已经过去的过期时间：建出来就是 expired 的密钥没有意义，
// 而且用户几乎总是想设未来的日期，多半是填错了。
func (s *Service) validateExpiry(expiresAt *time.Time) *consoleservice.Error {
	if expiresAt == nil {
		return nil
	}
	if !expiresAt.After(s.now()) {
		return consoleservice.InvalidArgument("expires_at", "The expiry must be in the future.")
	}
	return nil
}

func notFound() *consoleservice.Error {
	return &consoleservice.Error{
		Code:    "api_key_not_found",
		Message: "The api key was not found.",
		Status:  404,
	}
}

func validateStatus(status string) *consoleservice.Error {
	switch status {
	case "", StatusActive, StatusDisabled, StatusExpired, StatusRevoked:
		return nil
	default:
		return consoleservice.InvalidArgument("status", "The status filter is not supported.")
	}
}

func normalizeName(raw string, required bool) (string, *consoleservice.Error) {
	name := strings.TrimSpace(raw)
	if name == "" {
		if required {
			return "", consoleservice.InvalidArgument("name", "The name must not be empty.")
		}
		return "", nil
	}
	if len([]rune(name)) > maxNameLen {
		return "", consoleservice.InvalidArgument("name", "The name is too long.")
	}
	return name, nil
}

// parseSpendLimit 把可选金额解析成 numeric：nil/空串 → NULL（不限额）。
// 只接受非负十进制，拒绝科学计数法和前后缀符号——这个值会直接进 numeric(20,10)。
func parseSpendLimit(raw *string) (pgtype.Numeric, *consoleservice.Error) {
	if raw == nil || strings.TrimSpace(*raw) == "" {
		return pgtype.Numeric{Valid: false}, nil
	}
	text := strings.TrimSpace(*raw)
	var out pgtype.Numeric
	if err := out.Scan(text); err != nil {
		return pgtype.Numeric{}, consoleservice.InvalidArgument("spend_limit", "The spend limit must be a decimal amount.")
	}
	if !out.Valid || out.NaN {
		return pgtype.Numeric{}, consoleservice.InvalidArgument("spend_limit", "The spend limit must be a decimal amount.")
	}
	if out.Int != nil && out.Int.Sign() < 0 {
		return pgtype.Numeric{}, consoleservice.InvalidArgument("spend_limit", "The spend limit must not be negative.")
	}
	if out.Int != nil && out.Int.Cmp(big.NewInt(0)) == 0 {
		return pgtype.Numeric{}, consoleservice.InvalidArgument("spend_limit", "The spend limit must be greater than zero.")
	}
	return out, nil
}

func clampPage(limit, offset int32) (int32, int32) {
	if limit <= 0 {
		limit = defaultPageSize
	}
	if limit > maxPageSize {
		limit = maxPageSize
	}
	if offset < 0 {
		offset = 0
	}
	return limit, offset
}

func timestamptzNarg(t *time.Time) pgtype.Timestamptz {
	if t == nil {
		return pgtype.Timestamptz{Valid: false}
	}
	return pgtype.Timestamptz{Time: *t, Valid: true}
}

func derefString(v *string) string {
	if v == nil {
		return ""
	}
	return *v
}

func isForeignKeyViolation(err error) bool {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Code == pgForeignKeyViolation
	}
	return false
}
