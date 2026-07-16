package requestlog

import (
	"crypto/rand"
	"encoding/hex"

	"github.com/ThankCat/unio-gateway/internal/platform/failure"
)

const requestIDRandomBytes = 16

// GenerateRequestID 生成 request_records.request_id 使用的服务端请求 ID。
// 这个 ID 是数据库中的唯一请求事实标识，不等同于客户端传入的 X-Request-ID。
func GenerateRequestID() (string, error) {
	var b [requestIDRandomBytes]byte

	if _, err := rand.Read(b[:]); err != nil {
		return "", failure.Wrap(
			failure.CodeRequestLogIDGenerateFailed,
			err,
			failure.WithMessage("generate request id"),
		)
	}

	return "req_" + hex.EncodeToString(b[:]), nil
}
