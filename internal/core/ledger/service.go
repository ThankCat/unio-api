package ledger

import (
	"context"

	"github.com/ThankCat/unio-gateway/internal/platform/store/sqlc"
	"github.com/jackc/pgx/v5"
)

// TxBeginner 定义 ledger service 开启数据库事务所需能力。
type TxBeginner interface {
	Begin(ctx context.Context) (pgx.Tx, error)
}

// Service 负责用户余额变动和账本流水写入。
type Service struct {
	db      TxBeginner
	queries *sqlc.Queries
}

// NewService 创建 ledger service。
func NewService(db TxBeginner, queries *sqlc.Queries) *Service {
	return &Service{db: db, queries: queries}
}
