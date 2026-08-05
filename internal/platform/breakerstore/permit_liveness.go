package breakerstore

import (
	"context"
	"errors"
	"strings"

	"github.com/redis/go-redis/v9"

	"github.com/ThankCat/unio-gateway/internal/platform/failure"
)

// IsAttemptPermitActive 判断一次 attempt 的跨进程运行凭证是否仍在续期。
// missing/finished/aborted 都表示原执行链已经不能继续；未知状态 fail-closed。
func (s *Store) IsAttemptPermitActive(ctx context.Context, permitID string) (bool, error) {
	if strings.TrimSpace(permitID) == "" {
		return false, configInvalid("attempt permit id is required")
	}

	status, err := s.client.HGet(ctx, s.keys.permit(permitID), "status").Result()
	switch {
	case errors.Is(err, redis.Nil):
		return false, nil
	case err != nil:
		return false, storeUnavailable(err, "breakerstore read attempt permit liveness")
	}

	switch status {
	case "active":
		return true, nil
	case "finished", "aborted":
		return false, nil
	default:
		return false, failure.Wrap(
			failure.CodeGatewayRuntimeSyncRequired,
			ErrRuntimeSyncRequired,
			failure.WithMessage("attempt permit has an unknown status"),
		)
	}
}
