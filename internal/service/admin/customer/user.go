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
	CreatedAt   pgtype.Timestamptz
	UpdatedAt   pgtype.Timestamptz
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
}

// UserService 提供 admin 用户只读查询。
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
			ID:          row.ID,
			Email:       row.Email,
			DisplayName: row.DisplayName,
			CreatedAt:   row.CreatedAt,
			UpdatedAt:   row.UpdatedAt,
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
