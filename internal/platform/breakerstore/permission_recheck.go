package breakerstore

import (
	"context"
	"errors"
	"strconv"
	"time"

	"github.com/google/uuid"
)

const (
	maxPermissionRecheckLease   = 24 * time.Hour
	maxPermissionRecheckBackoff = 24 * time.Hour
)

// PermissionRecheckTask 是 Redis 原子领取的一次 Channel-Model 权限复检租约。
// 只保存内部 ID、revision 与 claim token，不保存 credential、URL、模型字符串或请求正文。
type PermissionRecheckTask struct {
	ChannelID              int64
	ModelID                int64
	ChannelConfigRevision  int64
	OriginRevision         int64
	ProviderStatusRevision int64
	Attempt                int64
	ClaimToken             string
}

// PermissionRecheckOutcome 是一次已领取复检的收口类型。
type PermissionRecheckOutcome string

const (
	PermissionRecheckSucceeded PermissionRecheckOutcome = "succeeded"
	PermissionRecheckFailed    PermissionRecheckOutcome = "failed"
	PermissionRecheckStale     PermissionRecheckOutcome = "stale"
)

// PermissionRecheckDisposition 描述 CAS 收口是否应用到领取时的那一版暂停记录。
type PermissionRecheckDisposition string

const (
	PermissionRecheckCleared     PermissionRecheckDisposition = "cleared"
	PermissionRecheckRescheduled PermissionRecheckDisposition = "rescheduled"
	PermissionRecheckMarkedStale PermissionRecheckDisposition = "stale"
	PermissionRecheckAbsent      PermissionRecheckDisposition = "absent"
	PermissionRecheckSuperseded  PermissionRecheckDisposition = "superseded"
)

// ClaimPermissionRecheck 原子领取一个已到期任务。队列 member 唯一且领取即改写为租约到期时间，
// 因此多个 worker-server 不会在租约内重复探测；worker 崩溃后任务可再次领取。
func (s *Store) ClaimPermissionRecheck(ctx context.Context, workerID string, lease time.Duration) (task *PermissionRecheckTask, err error) {
	done := s.beginOperation(ctx, operationClaimPermissionRecheck)
	defer func() {
		result := operationResultIdle
		if task != nil {
			result = "claimed"
		}
		done(result, err)
	}()

	if workerID == "" {
		return nil, configInvalid("permission recheck worker id is required")
	}
	if lease <= 0 || lease > maxPermissionRecheckLease {
		return nil, configInvalid("permission recheck lease is invalid")
	}
	claimToken := uuid.NewString()
	res, err := s.permissionRecheckClaim.Run(ctx, s.client,
		[]string{s.keys.permissionRecheckQueue()},
		strconv.FormatInt(lease.Milliseconds(), 10), workerID, claimToken,
	).Result()
	if err != nil {
		return nil, storeUnavailable(err, "breakerstore claim permission recheck")
	}
	arr, ok := res.([]interface{})
	if !ok || len(arr) == 0 {
		return nil, storeUnavailable(errors.New("unexpected permission recheck claim reply"), "breakerstore claim permission recheck")
	}
	code, _ := arr[0].(string)
	switch code {
	case "idle":
		return nil, nil
	case "invalid":
		return nil, ErrRuntimeSyncRequired
	case "claimed":
		if len(arr) != 8 {
			return nil, storeUnavailable(errors.New("invalid permission recheck task shape"), "breakerstore claim permission recheck")
		}
		values := make([]int64, 6)
		for i := range values {
			parsed, valid := redisInt64(arr[i+1])
			if !valid || parsed <= 0 {
				return nil, storeUnavailable(errors.New("invalid permission recheck task value"), "breakerstore claim permission recheck")
			}
			values[i] = parsed
		}
		token, valid := redisString(arr[7])
		if !valid || token == "" || token != claimToken {
			return nil, storeUnavailable(errors.New("invalid permission recheck claim token"), "breakerstore claim permission recheck")
		}
		return &PermissionRecheckTask{
			ChannelID: values[0], ModelID: values[1], ChannelConfigRevision: values[2],
			OriginRevision: values[3], ProviderStatusRevision: values[4],
			Attempt: values[5], ClaimToken: token,
		}, nil
	default:
		return nil, storeUnavailable(errors.New("unknown permission recheck claim code"), "breakerstore claim permission recheck")
	}
}

// CompletePermissionRecheck 以 claim token + Channel/Model + 三类 revision CAS 收口。
// 成功清除暂停；失败按 Redis TIME 退避重排；stale 只移出旧队列。过期或被新版覆盖的 claim 不改状态。
func (s *Store) CompletePermissionRecheck(
	ctx context.Context,
	task PermissionRecheckTask,
	outcome PermissionRecheckOutcome,
	retryAfter time.Duration,
) (disposition PermissionRecheckDisposition, err error) {
	done := s.beginOperation(ctx, operationFinishPermissionCheck)
	defer func() { done(string(disposition), err) }()

	if err := validatePermissionRecheckTask(task); err != nil {
		return "", err
	}
	if outcome != PermissionRecheckSucceeded && outcome != PermissionRecheckFailed && outcome != PermissionRecheckStale {
		return "", configInvalid("permission recheck outcome is invalid")
	}
	if outcome == PermissionRecheckFailed {
		if retryAfter <= 0 || retryAfter > maxPermissionRecheckBackoff {
			return "", configInvalid("permission recheck retry backoff is invalid")
		}
	} else {
		retryAfter = 0
	}

	permissionKey := s.keys.channelModelPermission(task.ChannelID, task.ModelID)
	res, err := s.permissionRecheckComplete.Run(ctx, s.client,
		[]string{permissionKey, s.keys.permissionRecheckQueue()},
		string(outcome), task.ClaimToken,
		strconv.FormatInt(task.ChannelID, 10), strconv.FormatInt(task.ModelID, 10),
		strconv.FormatInt(task.ChannelConfigRevision, 10), strconv.FormatInt(task.OriginRevision, 10),
		strconv.FormatInt(task.ProviderStatusRevision, 10), strconv.FormatInt(retryAfter.Milliseconds(), 10),
	).Result()
	if err != nil {
		return "", storeUnavailable(err, "breakerstore complete permission recheck")
	}
	arr, ok := res.([]interface{})
	if !ok || len(arr) == 0 {
		return "", storeUnavailable(errors.New("unexpected permission recheck completion reply"), "breakerstore complete permission recheck")
	}
	code, _ := arr[0].(string)
	disposition = PermissionRecheckDisposition(code)
	switch disposition {
	case PermissionRecheckCleared, PermissionRecheckRescheduled, PermissionRecheckMarkedStale,
		PermissionRecheckAbsent, PermissionRecheckSuperseded:
		return disposition, nil
	default:
		return "", storeUnavailable(errors.New("unknown permission recheck completion code"), "breakerstore complete permission recheck")
	}
}

func validatePermissionRecheckTask(task PermissionRecheckTask) error {
	if task.ChannelID <= 0 || task.ModelID <= 0 || task.ChannelConfigRevision <= 0 ||
		task.OriginRevision <= 0 || task.ProviderStatusRevision <= 0 || task.Attempt <= 0 || task.ClaimToken == "" {
		return configInvalid("permission recheck task is invalid")
	}
	return nil
}
