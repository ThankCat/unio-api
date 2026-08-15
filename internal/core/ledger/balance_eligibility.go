package ledger

import (
	"context"

	"github.com/ThankCat/unio-gateway/internal/platform/failure"
)

// PositiveBalanceStore 定义正余额资格检查所需的只读存储能力。
type PositiveBalanceStore interface {
	HasPositiveAvailableUserBalance(ctx context.Context, userID int64) (bool, error)
}

// BalanceEligibilityService 判断用户是否具备进入计费请求生命周期的最低余额资格。
type BalanceEligibilityService struct {
	store PositiveBalanceStore
}

// NewBalanceEligibilityService 创建只读余额资格服务。
func NewBalanceEligibilityService(store PositiveBalanceStore) *BalanceEligibilityService {
	return &BalanceEligibilityService{store: store}
}

// HasPositiveAvailableBalance 判断用户是否至少有一个币种的可用余额大于零。
func (s *BalanceEligibilityService) HasPositiveAvailableBalance(ctx context.Context, userID int64) (bool, error) {
	positive, err := s.store.HasPositiveAvailableUserBalance(ctx, userID)
	if err != nil {
		return false, ledgerFailure(
			failure.CodeLedgerStoreFailed,
			err,
			"check positive available user balance",
		)
	}

	return positive, nil
}
