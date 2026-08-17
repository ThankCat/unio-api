package lifecycle

import (
	"time"

	"github.com/ThankCat/unio-gateway/internal/core/adapter"
	"github.com/ThankCat/unio-gateway/internal/platform/failure"
)

// ProvableRetryAfter returns only a recovery duration carried by authoritative upstream metadata,
// a structured failure field, or a stable internal retry contract. Zero means the caller cannot
// prove an earliest recovery time.
func ProvableRetryAfter(err error) time.Duration {
	if metadata, ok := adapter.UpstreamMetadataOf(err); ok && metadata.RetryAfter > 0 {
		return metadata.RetryAfter
	}
	for _, field := range failure.FieldsOf(err) {
		if field.Key != "retry_after_ms" {
			continue
		}
		milliseconds, ok := field.Value.(int64)
		if ok && milliseconds > 0 {
			return time.Duration(milliseconds) * time.Millisecond
		}
	}
	if failure.CodeOf(err) == failure.CodeLedgerBalanceTemporarilyReserved {
		return time.Second
	}
	return 0
}
