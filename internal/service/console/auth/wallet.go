package auth

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"

	"github.com/ThankCat/unio-gateway/internal/platform/store/sqlc"
	"github.com/ThankCat/unio-gateway/internal/service/admin/opsutil"
	consoleservice "github.com/ThankCat/unio-gateway/internal/service/console"
)

const usdCurrency = "USD"

func zeroUSDBalance() Balance {
	return Balance{Currency: usdCurrency, Total: "0", Reserved: "0", Available: "0"}
}

func balanceFromRow(row sqlc.UserBalance, found bool) Balance {
	if !found {
		return zeroUSDBalance()
	}
	total := opsutil.NumericString(row.Balance)
	reserved := opsutil.NumericString(row.ReservedBalance)
	return Balance{
		Currency:  usdCurrency,
		Total:     total,
		Reserved:  reserved,
		Available: opsutil.SubtractDecimal(total, reserved),
	}
}

func (s *Service) loadUSDWallet(ctx context.Context, user User, userID int64) (User, *consoleservice.Error) {
	row, err := s.queries.GetUserBalance(ctx, sqlc.GetUserBalanceParams{
		UserID:   userID,
		Currency: usdCurrency,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		user.Balance = zeroUSDBalance()
		return user, nil
	}
	if err != nil {
		return User{}, requestUnavailable("read user usd wallet", err)
	}
	user.Balance = balanceFromRow(row, true)
	return user, nil
}
