package ledger

import (
	"context"
	"fmt"

	"github.com/ThankCat/unio-gateway/internal/platform/failure"
)

// BalanceEligibility 表示用户余额进入计费请求前的资格状态。
type BalanceEligibility string

const (
	// BalanceEligibilityPositiveAvailable 表示至少有一个币种存在正可用余额。
	BalanceEligibilityPositiveAvailable BalanceEligibility = "positive_available"
	// BalanceEligibilityTemporarilyReserved 表示总余额为正但当前余额全部被在途请求冻结。
	BalanceEligibilityTemporarilyReserved BalanceEligibility = "temporarily_reserved"
	// BalanceEligibilityInsufficient 表示没有正总余额可供授权。
	BalanceEligibilityInsufficient BalanceEligibility = "insufficient"
)

// BalanceEligibilityStore 定义余额资格检查所需的只读存储能力。
// 返回值来自数据库中的有界状态枚举，不接受任意外部字符串。
type BalanceEligibilityStore interface {
	GetUserBalanceEligibility(ctx context.Context, userID int64) (string, error)
}

// BalanceEligibilityService 判断用户是否具备进入计费请求生命周期的最低余额资格。
type BalanceEligibilityService struct {
	store BalanceEligibilityStore
}

// NewBalanceEligibilityService 创建只读余额资格服务。
func NewBalanceEligibilityService(store BalanceEligibilityStore) *BalanceEligibilityService {
	return &BalanceEligibilityService{store: store}
}

// GetBalanceEligibility 返回用户余额当前的资格状态。
func (s *BalanceEligibilityService) GetBalanceEligibility(ctx context.Context, userID int64) (BalanceEligibility, error) {
	state, err := s.store.GetUserBalanceEligibility(ctx, userID)
	if err != nil {
		return "", ledgerFailure(
			failure.CodeLedgerStoreFailed,
			err,
			"check user balance eligibility",
		)
	}

	eligibility := BalanceEligibility(state)
	switch eligibility {
	case BalanceEligibilityPositiveAvailable,
		BalanceEligibilityTemporarilyReserved,
		BalanceEligibilityInsufficient:
		return eligibility, nil
	default:
		return "", ledgerFailure(
			failure.CodeLedgerStoreFailed,
			fmt.Errorf("unknown balance eligibility %q", state),
			"decode user balance eligibility",
		)
	}
}
