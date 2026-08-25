package customer

import (
	"context"
	"errors"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/ThankCat/unio-gateway/internal/platform/store/sqlc"
)

// User 表示后台用户列表/详情的对外视图（不含 password_hash）。
type User struct {
	ID          int64
	Email       string
	DisplayName string
	// RPMLimit/RPDLimit/ConcurrencyLimit 是用户级限流：
	// nil 继承全局默认，0 表示不限，正数为具体上限。
	RPMLimit         *int64
	RPDLimit         *int64
	ConcurrencyLimit *int64
	CreatedAt        pgtype.Timestamptz
	UpdatedAt        pgtype.Timestamptz
}

// RateLimitsInput 是设置用户级限流的入参；nil 表示继承全局默认。
type RateLimitsInput struct {
	UserID           int64
	RPMLimit         *int64
	RPDLimit         *int64
	ConcurrencyLimit *int64
}

// Balance 表示某用户某币种的余额视图（金额为十进制字符串）。
type Balance struct {
	Currency        string
	Balance         string
	ReservedBalance string
}

// UserDetail 表示用户详情：基础信息 + 各币种余额。
type UserDetail struct {
	User
	Balances []Balance
}

// UserListParams 表示用户分页查询参数；Q 为空不过滤。
type UserListParams struct {
	Q      string
	Limit  int32
	Offset int32
}

// UserStore 定义用户读取所需的存储能力。
type UserStore interface {
	ListUsersPage(ctx context.Context, arg sqlc.ListUsersPageParams) ([]sqlc.ListUsersPageRow, error)
	CountUsers(ctx context.Context, q pgtype.Text) (int64, error)
	GetUserByID(ctx context.Context, id int64) (sqlc.GetUserByIDRow, error)
	ListUserBalancesByUser(ctx context.Context, userID int64) ([]sqlc.UserBalance, error)
	SetUserRateLimits(ctx context.Context, arg sqlc.SetUserRateLimitsParams) (sqlc.SetUserRateLimitsRow, error)
}

// UserService 提供 admin 用户查询与限流配置。
type UserService struct {
	store UserStore
}

// NewUserService 创建用户查询 service。
func NewUserService(store UserStore) *UserService {
	if store == nil {
		panic("customer: user store is required")
	}
	return &UserService{store: store}
}

// List 分页倒序列出用户，并返回满足过滤条件的总数。
func (s *UserService) List(ctx context.Context, params UserListParams) ([]User, int64, error) {
	q := textNarg(params.Q)

	rows, err := s.store.ListUsersPage(ctx, sqlc.ListUsersPageParams{
		Q:          q,
		PageLimit:  params.Limit,
		PageOffset: params.Offset,
	})
	if err != nil {
		return nil, 0, storeFailed(err, "list users")
	}

	total, err := s.store.CountUsers(ctx, q)
	if err != nil {
		return nil, 0, storeFailed(err, "count users")
	}

	users := make([]User, 0, len(rows))
	for _, row := range rows {
		users = append(users, User{
			ID:          row.ID,
			Email:       row.Email,
			DisplayName: row.DisplayName,
			CreatedAt:   row.CreatedAt,
			UpdatedAt:   row.UpdatedAt,
		})
	}

	return users, total, nil
}

// Get 读取单个用户详情，含各币种余额。
func (s *UserService) Get(ctx context.Context, id int64) (UserDetail, error) {
	row, err := s.store.GetUserByID(ctx, id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return UserDetail{}, notFound("user not found")
		}
		return UserDetail{}, storeFailed(err, "get user")
	}

	balanceRows, err := s.store.ListUserBalancesByUser(ctx, id)
	if err != nil {
		return UserDetail{}, storeFailed(err, "list user balances")
	}

	balances := make([]Balance, 0, len(balanceRows))
	for _, b := range balanceRows {
		balances = append(balances, Balance{
			Currency:        b.Currency,
			Balance:         numericString(b.Balance),
			ReservedBalance: numericString(b.ReservedBalance),
		})
	}

	return UserDetail{
		User: User{
			ID:               row.ID,
			Email:            row.Email,
			DisplayName:      row.DisplayName,
			RPMLimit:         int4Ptr(row.RpmLimit),
			RPDLimit:         int4Ptr(row.RpdLimit),
			ConcurrencyLimit: int4Ptr(row.ConcurrencyLimit),
			CreatedAt:        row.CreatedAt,
			UpdatedAt:        row.UpdatedAt,
		},
		Balances: balances,
	}, nil
}

// textNarg 把可选字符串过滤值转成 pgtype.Text：空串 → SQL NULL。
func textNarg(s string) pgtype.Text {
	s = strings.TrimSpace(s)
	if s == "" {
		return pgtype.Text{}
	}
	return pgtype.Text{String: s, Valid: true}
}

// SetRateLimits 设置用户级限流。
//
// 三个维度都允许为 nil（继承全局默认）或 0（不限），只拒绝负数——
// 负数没有可解释的语义，静默当成 0 会让管理员以为自己关掉了限流。
func (s *UserService) SetRateLimits(ctx context.Context, in RateLimitsInput) (User, error) {
	if in.UserID <= 0 {
		return User{}, invalidArgument("id", "user id must be positive")
	}
	for _, field := range []struct {
		name  string
		value *int64
	}{
		{"rpm_limit", in.RPMLimit},
		{"rpd_limit", in.RPDLimit},
		{"concurrency_limit", in.ConcurrencyLimit},
	} {
		if field.value != nil && *field.value < 0 {
			return User{}, invalidArgument(field.name, "limit must be >= 0 (0 means unlimited)")
		}
	}

	row, err := s.store.SetUserRateLimits(ctx, sqlc.SetUserRateLimitsParams{
		ID:               in.UserID,
		RpmLimit:         int4Narg(in.RPMLimit),
		RpdLimit:         int4Narg(in.RPDLimit),
		ConcurrencyLimit: int4Narg(in.ConcurrencyLimit),
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return User{}, notFound("user not found")
		}
		return User{}, storeFailed(err, "set user rate limits")
	}
	return User{
		ID:               row.ID,
		Email:            row.Email,
		DisplayName:      row.DisplayName,
		RPMLimit:         int4Ptr(row.RpmLimit),
		RPDLimit:         int4Ptr(row.RpdLimit),
		ConcurrencyLimit: int4Ptr(row.ConcurrencyLimit),
		CreatedAt:        row.CreatedAt,
		UpdatedAt:        row.UpdatedAt,
	}, nil
}

// int4Narg 把可空限流上限转成 pgtype.Int4（nil = 继承全局默认）。
func int4Narg(v *int64) pgtype.Int4 {
	if v == nil {
		return pgtype.Int4{}
	}
	return pgtype.Int4{Int32: int32(*v), Valid: true}
}

// int4Ptr 把可空限流上限转成 *int64。
func int4Ptr(v pgtype.Int4) *int64 {
	if !v.Valid {
		return nil
	}
	out := int64(v.Int32)
	return &out
}
