package console

import "github.com/ThankCat/unio-gateway/internal/platform/store/sqlc"

// DB is the database contract shared by Console application services.
type DB interface {
	sqlc.DBTX
}
