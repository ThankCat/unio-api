package user

import (
	"context"
	"net/http"

	"github.com/ThankCat/unio-gateway/internal/app/adminapi/adminhttp"
	"github.com/ThankCat/unio-gateway/internal/platform/httpx"

	"github.com/ThankCat/unio-gateway/internal/service/admin/customer"
)

// UserService 定义 adminapi 查询用户所需的最小能力（M7 客户管理）。
type UserService interface {
	List(ctx context.Context, params customer.UserListParams) ([]customer.User, int64, error)
	Get(ctx context.Context, id int64) (customer.UserDetail, error)
	SetRateLimits(ctx context.Context, in customer.RateLimitsInput) (customer.User, error)
}

// userDTO 是用户列表项响应体（不含 password_hash）。
//
// 三个限流字段 null 表示继承全局默认，0 表示不限，正数为具体上限。
// 用 *int64 而非 int64：0 和「没设置」是两种不同意图，合并成一个值会丢掉这个区别。
type userDTO struct {
	ID               int64  `json:"id"`
	Email            string `json:"email"`
	DisplayName      string `json:"display_name"`
	RPMLimit         *int64 `json:"rpm_limit"`
	RPDLimit         *int64 `json:"rpd_limit"`
	ConcurrencyLimit *int64 `json:"concurrency_limit"`
	CreatedAt        string `json:"created_at"`
	UpdatedAt        string `json:"updated_at"`
}

// rateLimitsRequest 是设置用户限流的请求体。
// 字段缺省与显式 null 都表示「继承全局默认」：这两种写法对配额的效果一致，
// 不必为区分它们增加一层 provided 标记。
type rateLimitsRequest struct {
	RPMLimit         *int64 `json:"rpm_limit"`
	RPDLimit         *int64 `json:"rpd_limit"`
	ConcurrencyLimit *int64 `json:"concurrency_limit"`
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
		ID:               u.ID,
		Email:            u.Email,
		DisplayName:      u.DisplayName,
		RPMLimit:         u.RPMLimit,
		RPDLimit:         u.RPDLimit,
		ConcurrencyLimit: u.ConcurrencyLimit,
		CreatedAt:        adminhttp.RFC3339(u.CreatedAt.Time),
		UpdatedAt:        adminhttp.RFC3339(u.UpdatedAt.Time),
	}
}

// setRateLimits 设置用户级限流（PATCH /users/{id}/rate-limits）。
func (h *usersHandler) setRateLimits(w http.ResponseWriter, r *http.Request) {
	id, err := adminhttp.PathID(r)
	if err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}
	var req rateLimitsRequest
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}

	user, err := h.service.SetRateLimits(r.Context(), customer.RateLimitsInput{
		UserID:           id,
		RPMLimit:         req.RPMLimit,
		RPDLimit:         req.RPDLimit,
		ConcurrencyLimit: req.ConcurrencyLimit,
	})
	if err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}
	adminhttp.WriteData(w, http.StatusOK, toUserDTO(user))
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
