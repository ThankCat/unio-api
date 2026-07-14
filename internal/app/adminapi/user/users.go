package user

import (
	"context"
	"net/http"

	"github.com/ThankCat/unio-api/internal/app/adminapi/adminhttp"

	"github.com/ThankCat/unio-api/internal/service/admin/customer"
)

// UserService 定义 adminapi 查询用户所需的最小能力（M7 客户管理）。
type UserService interface {
	List(ctx context.Context, params customer.UserListParams) ([]customer.User, int64, error)
	Get(ctx context.Context, id int64) (customer.UserDetail, error)
}

// userDTO 是用户列表项响应体（不含 password_hash）。
type userDTO struct {
	ID          int64  `json:"id"`
	Email       string `json:"email"`
	DisplayName string `json:"display_name"`
	CreatedAt   string `json:"created_at"`
	UpdatedAt   string `json:"updated_at"`
}

// balanceDTO 是用户某币种余额响应体（金额为十进制字符串）。
type balanceDTO struct {
	Currency        string `json:"currency"`
	Balance         string `json:"balance"`
	ReservedBalance string `json:"reserved_balance"`
}

// userDetailDTO 是用户详情响应体：基础信息 + 各币种余额。
type userDetailDTO struct {
	userDTO
	Balances []balanceDTO `json:"balances"`
}

type usersHandler struct {
	service UserService
}

func (h *usersHandler) get(w http.ResponseWriter, r *http.Request) {
	id, err := adminhttp.PathID(r)
	if err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}

	detail, err := h.service.Get(r.Context(), id)
	if err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}

	adminhttp.WriteData(w, http.StatusOK, toUserDetailDTO(detail))
}

func toUserDTO(u customer.User) userDTO {
	return userDTO{
		ID:          u.ID,
		Email:       u.Email,
		DisplayName: u.DisplayName,
		CreatedAt:   adminhttp.RFC3339(u.CreatedAt.Time),
		UpdatedAt:   adminhttp.RFC3339(u.UpdatedAt.Time),
	}
}

func toUserDetailDTO(detail customer.UserDetail) userDetailDTO {
	balances := make([]balanceDTO, 0, len(detail.Balances))
	for _, b := range detail.Balances {
		balances = append(balances, balanceDTO{
			Currency:        b.Currency,
			Balance:         b.Balance,
			ReservedBalance: b.ReservedBalance,
		})
	}
	return userDetailDTO{
		userDTO:  toUserDTO(detail.User),
		Balances: balances,
	}
}
