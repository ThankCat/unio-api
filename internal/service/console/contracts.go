package console

import "github.com/ThankCat/unio-gateway/internal/platform/store/sqlc"

// DB 定义 Console 应用服务共用的数据库能力。
type DB interface {
	sqlc.DBTX
}
