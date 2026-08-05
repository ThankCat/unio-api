package breakerstore

import (
	"context"
	"errors"
	"strconv"
	"time"

	"github.com/redis/go-redis/v9"
)

// TPM 观测的时间边界（§8.2）。桶存活期与允许回溯修正的窗口是两个独立参数：
// 前者决定 Admin 还能读到多久以前的分钟，后者决定可靠 usage 迟到多久还值得修正。
// 回溯窗口必须严格小于桶存活期，留出安全边界，避免修正与过期竞争。
const (
	tpmObservationBucketTTL = 2 * time.Hour
	// tpmCorrectionLookbackMinutes 是允许回溯修正的最大分钟数。
	tpmCorrectionLookbackMinutes = int64(90)
	// tpmFlushMarkerTTL 覆盖一次 flush 批次的重试窗口。
	tpmFlushMarkerTTL = 10 * time.Minute
	// tpmCorrectionMarkerTTL 与回溯窗口对齐：超窗的重放本来就会被丢弃，marker 无需更长。
	tpmCorrectionMarkerTTL = time.Duration(tpmCorrectionLookbackMinutes) * time.Minute
)

// TPMObservationKind 区分观测桶的归属维度。
type TPMObservationKind string

const (
	// TPMScopeRoute 是客户视角：一条线路这一分钟观察到的输入与输出。
	TPMScopeRoute TPMObservationKind = "route"
	// TPMScopeChannel 是上游视角：一个渠道这一分钟观察到的输入与输出。
	TPMScopeChannel TPMObservationKind = "channel"
)

// TPMObservationScope 定位唯一一个观测桶。
type TPMObservationScope struct {
	Kind   TPMObservationKind
	ID     int64
	Minute int64
}

// TPMObservationDelta 是一个分钟桶上的一组增量。record 路径要求全部非负；
// correct 路径允许负值，由 Lua 负责夹到 0。
type TPMObservationDelta struct {
	InputTokens       int64
	OutputTokens      int64
	ProvisionalTokens int64
	ObservedAttempts  int64
	MissingUsageCount int64
}

// Empty 判断该增量是否完全不需要写入。
func (d TPMObservationDelta) Empty() bool {
	return d.InputTokens == 0 && d.OutputTokens == 0 && d.ProvisionalTokens == 0 &&
		d.ObservedAttempts == 0 && d.MissingUsageCount == 0
}

// TPMObservationEntry 把一组增量绑定到一个具体的分钟桶。
type TPMObservationEntry struct {
	Scope TPMObservationScope
	Delta TPMObservationDelta
}

// TPMCorrectionResult 报告一次最终修正的落地情况。
// Expired 是目标分钟桶已经不存在而被放弃的数量，调用方据此计 expired_correction。
type TPMCorrectionResult struct {
	Duplicate bool
	Applied   int64
	Expired   int64
}

// TPMObservationSnapshot 是一个分钟桶的只读视图。
// TPM 定义为 input + output；Provisional 是其中仍由 Gateway 估算、尚未被可靠 usage 修正的部分。
type TPMObservationSnapshot struct {
	InputTokens       int64
	OutputTokens      int64
	ProvisionalTokens int64
	ObservedAttempts  int64
	MissingUsageCount int64
}

// TPM 返回该分钟的观测总量。
func (s TPMObservationSnapshot) TPM() int64 { return s.InputTokens + s.OutputTokens }

// TPMObservationMinute 返回某个时刻所属的自然 UTC 分钟号。
func TPMObservationMinute(now time.Time) int64 { return minuteBucket(now) }

// TPMCorrectionLookbackMinutes 是允许回溯修正的最大分钟数（§8.4）。
// 观测器据此在调用 Redis 之前就丢弃超窗分钟，不把它们交给 Lua。
func TPMCorrectionLookbackMinutes() int64 { return tpmCorrectionLookbackMinutes }

// RecordTPMObservations 原子写入一个 flush 批次。operationID 必须在重试之间保持稳定：
// 批次幂等完全由它决定，同一 id 的第二次执行不会重复累加。
// 观测是 best-effort：失败只上报指标，绝不置位共享基础设施故障 latch，也不阻断任何请求。
func (s *Store) RecordTPMObservations(
	ctx context.Context,
	operationID string,
	entries []TPMObservationEntry,
) (applied bool, err error) {
	done := s.beginObservationOperation(operationRecordTPMObservation)
	defer func() { done(observationApplyResult(applied), err) }()

	if operationID == "" {
		return false, configInvalid("tpm observation operation id is required")
	}
	if len(entries) == 0 {
		return false, nil
	}

	keys := make([]string, 0, len(entries)+1)
	keys = append(keys, s.keys.obsTPMFlushMarker(operationID))
	argv := make([]interface{}, 0, 2+len(entries)*5)
	argv = append(argv,
		strconv.FormatInt(tpmFlushMarkerTTL.Milliseconds(), 10),
		strconv.FormatInt(tpmObservationBucketTTL.Milliseconds(), 10),
	)
	for _, entry := range entries {
		key, keyErr := s.tpmObservationKey(entry.Scope)
		if keyErr != nil {
			return false, keyErr
		}
		if entry.Delta.InputTokens < 0 || entry.Delta.OutputTokens < 0 ||
			entry.Delta.ProvisionalTokens < 0 || entry.Delta.ObservedAttempts < 0 ||
			entry.Delta.MissingUsageCount < 0 {
			return false, configInvalid("tpm observation deltas must be non-negative")
		}
		keys = append(keys, key)
		argv = append(argv,
			strconv.FormatInt(entry.Delta.InputTokens, 10),
			strconv.FormatInt(entry.Delta.OutputTokens, 10),
			strconv.FormatInt(entry.Delta.ProvisionalTokens, 10),
			strconv.FormatInt(entry.Delta.ObservedAttempts, 10),
			strconv.FormatInt(entry.Delta.MissingUsageCount, 10),
		)
	}

	res, err := s.recordTPMObservation.Run(ctx, s.client, keys, argv...).Result()
	if err != nil {
		return false, storeUnavailable(err, "breakerstore record tpm observation")
	}
	code, ok := redisInt64(res)
	if !ok {
		return false, storeUnavailable(errors.New("unexpected tpm observation reply"), "breakerstore record tpm observation")
	}
	return code == 1, nil
}

// CorrectTPMObservations 用可靠 usage 修正已写入的观测。scope 是跨进程幂等键，
// 必须能唯一标识「这一次 request 或 attempt 的最终修正」，让 recovery 重放命中同一个 marker。
// 调用方必须先按 TPMCorrectionLookbackMinutes 过滤掉超窗分钟；Lua 只负责拒绝已经不存在的桶。
func (s *Store) CorrectTPMObservations(
	ctx context.Context,
	scope string,
	entries []TPMObservationEntry,
) (result TPMCorrectionResult, err error) {
	done := s.beginObservationOperation(operationCorrectTPMObservation)
	defer func() { done(correctionOperationResult(result), err) }()

	if scope == "" {
		return TPMCorrectionResult{}, configInvalid("tpm correction scope is required")
	}
	if len(entries) == 0 {
		return TPMCorrectionResult{}, nil
	}

	keys := make([]string, 0, len(entries)+1)
	keys = append(keys, s.keys.obsTPMCorrectionMarker(scope))
	argv := make([]interface{}, 0, 1+len(entries)*5)
	argv = append(argv, strconv.FormatInt(tpmCorrectionMarkerTTL.Milliseconds(), 10))
	for _, entry := range entries {
		key, keyErr := s.tpmObservationKey(entry.Scope)
		if keyErr != nil {
			return TPMCorrectionResult{}, keyErr
		}
		keys = append(keys, key)
		argv = append(argv,
			strconv.FormatInt(entry.Delta.InputTokens, 10),
			strconv.FormatInt(entry.Delta.OutputTokens, 10),
			strconv.FormatInt(entry.Delta.ProvisionalTokens, 10),
			strconv.FormatInt(entry.Delta.ObservedAttempts, 10),
			strconv.FormatInt(entry.Delta.MissingUsageCount, 10),
		)
	}

	res, err := s.correctTPMObservation.Run(ctx, s.client, keys, argv...).Result()
	if err != nil {
		return TPMCorrectionResult{}, storeUnavailable(err, "breakerstore correct tpm observation")
	}
	arr, ok := res.([]interface{})
	if !ok || len(arr) == 0 {
		return TPMCorrectionResult{}, storeUnavailable(errors.New("unexpected tpm correction reply"), "breakerstore correct tpm observation")
	}
	code, _ := arr[0].(string)
	switch code {
	case "duplicate":
		return TPMCorrectionResult{Duplicate: true}, nil
	case "applied":
		out := TPMCorrectionResult{}
		if len(arr) > 1 {
			out.Applied, _ = redisInt64(arr[1])
		}
		if len(arr) > 2 {
			out.Expired, _ = redisInt64(arr[2])
		}
		return out, nil
	default:
		return TPMCorrectionResult{}, storeUnavailable(errors.New("unknown tpm correction outcome: "+code), "breakerstore correct tpm observation")
	}
}

// TPMObservations 批量读取同一分钟上多个 scope 的观测值（Admin 只读展示用）。
// 缺失桶按零处理；读失败由调用方显示为 unavailable，绝不回退到别的数据源伪装成实时 TPM。
func (s *Store) TPMObservations(
	ctx context.Context,
	kind TPMObservationKind,
	ids []int64,
	minute int64,
) (out map[int64]TPMObservationSnapshot, err error) {
	done := s.beginObservationOperation(operationReadTPMObservation)
	defer func() { done(operationResultSuccess, err) }()

	out = make(map[int64]TPMObservationSnapshot, len(ids))
	if len(ids) == 0 {
		return out, nil
	}

	pipe := s.client.Pipeline()
	cmds := make(map[int64]*redis.MapStringStringCmd, len(ids))
	for _, id := range ids {
		if id <= 0 {
			continue
		}
		if _, seen := cmds[id]; seen {
			continue
		}
		key, keyErr := s.tpmObservationKey(TPMObservationScope{Kind: kind, ID: id, Minute: minute})
		if keyErr != nil {
			return nil, keyErr
		}
		cmds[id] = pipe.HGetAll(ctx, key)
	}
	if _, execErr := pipe.Exec(ctx); execErr != nil && !errors.Is(execErr, redis.Nil) {
		return nil, storeUnavailable(execErr, "breakerstore read tpm observation")
	}
	for id, cmd := range cmds {
		fields, cmdErr := cmd.Result()
		if cmdErr != nil {
			if errors.Is(cmdErr, redis.Nil) {
				out[id] = TPMObservationSnapshot{}
				continue
			}
			return nil, storeUnavailable(cmdErr, "breakerstore read tpm observation")
		}
		out[id] = TPMObservationSnapshot{
			InputTokens:       sampleHashInt(fields, "input_tokens"),
			OutputTokens:      sampleHashInt(fields, "output_tokens"),
			ProvisionalTokens: sampleHashInt(fields, "provisional_tokens"),
			ObservedAttempts:  sampleHashInt(fields, "observed_attempts"),
			MissingUsageCount: sampleHashInt(fields, "missing_usage_count"),
		}
	}
	return out, nil
}

func (s *Store) tpmObservationKey(scope TPMObservationScope) (string, error) {
	if scope.ID <= 0 {
		return "", configInvalid("tpm observation scope id must be positive")
	}
	if scope.Minute < 0 {
		return "", configInvalid("tpm observation minute must be non-negative")
	}
	switch scope.Kind {
	case TPMScopeRoute:
		return s.keys.obsTPMRoute(scope.ID, scope.Minute), nil
	case TPMScopeChannel:
		return s.keys.obsTPMChannel(scope.ID, scope.Minute), nil
	default:
		return "", configInvalid("unknown tpm observation scope kind")
	}
}

func observationApplyResult(applied bool) string {
	if applied {
		return operationResultApplied
	}
	return operationResultIgnored
}

func correctionOperationResult(result TPMCorrectionResult) string {
	if result.Duplicate {
		return operationResultIgnored
	}
	return operationResultApplied
}
