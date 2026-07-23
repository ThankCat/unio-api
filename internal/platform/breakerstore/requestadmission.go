package breakerstore

import (
	"context"
	"errors"
	"strconv"
	"time"
)

// RequestAdmissionOutcome 是入口准入的稳定结果（§7.5、§2.14.10）。
type RequestAdmissionOutcome string

const (
	RequestAllowed            RequestAdmissionOutcome = "allowed"
	RequestLimited            RequestAdmissionOutcome = "limited"
	RequestStoreUnavailable   RequestAdmissionOutcome = "store_unavailable"
	RequestRuntimeStateLost   RequestAdmissionOutcome = "runtime_state_lost"
	RequestStaleEpoch         RequestAdmissionOutcome = "stale_integrity_epoch"
	RequestRuntimeSyncReq     RequestAdmissionOutcome = "runtime_sync_required"
	RequestRuntimeSyncPending RequestAdmissionOutcome = "runtime_sync_pending"
	RequestStaleSettingRev    RequestAdmissionOutcome = "stale_setting_revision"
	RequestConflict           RequestAdmissionOutcome = "conflict"
)

// RequestAdmissionInput 是入口准入的身份、可信认证快照 override 与 expected revision 输入。
// nil override 继承 Redis committed active global control；0 表示显式不限，正数表示显式上限。
// 调用方不得传入已经合并本机默认值的 effective limit。
type RequestAdmissionInput struct {
	RequestAdmissionID string
	Fingerprint        string
	RouteID            int64
	UserID             int64

	IntegrityEpoch            string
	IntegrityRevision         int64
	RouteRateRevision         int64
	GlobalConcurrencyRevision int64

	RPMLimitOverride         *int64
	RPDLimitOverride         *int64
	TPMLimitOverride         *int64
	ConcurrencyLimitOverride *int64
}

// RequestAdmissionResult 汇报入口准入结果。
type RequestAdmissionResult struct {
	Outcome          RequestAdmissionOutcome
	LimitedDimension string // limited 时为 rpm|rpd|concurrency
	SyncTarget       string // runtime_sync/stale 时为 route_rate|global_concurrency|circuit_breaker
	LeaseUntilMs     int64
	RenewIntervalMs  int64
}

func minuteBucket(now time.Time) int64 { return now.Unix() / 60 }
func dayBucket(now time.Time) int64    { return now.Unix() / 86400 }

// AcquireRequestAdmission 入口一次性取得 route-user RPM/RPD + concurrency 与 request-admission token。
// 真实超限返回 limited（由调用方映射 429）；完整性/控制/基础设施问题返回对应稳定码（映射 503）。
func (s *Store) AcquireRequestAdmission(ctx context.Context, in RequestAdmissionInput) (result RequestAdmissionResult, err error) {
	done := s.beginOperation(ctx, operationAcquireRequest)
	defer func() { done(requestAdmissionOperationResult(result), err) }()

	if err := validateRequestAdmissionInput(in); err != nil {
		return RequestAdmissionResult{}, err
	}
	if s.localRuntimeInfrastructureFault(ctx) {
		return RequestAdmissionResult{Outcome: RequestStoreUnavailable}, nil
	}
	now := time.Now()
	keys := []string{
		s.keys.admissionRouteRate(),
		s.keys.admissionGlobalConcurrency(),
		s.keys.runtimeControlSetting("gateway.circuit_breaker"),
		s.keys.stateIntegrityMarker(),
		s.keys.admissionRequest(in.RequestAdmissionID),
		s.keys.requestRPMBucket(in.RouteID, in.UserID, minuteBucket(now)),
		s.keys.requestRPDBucket(in.RouteID, in.UserID, dayBucket(now)),
		s.keys.requestConcurrency(in.RouteID, in.UserID),
		s.keys.runtimeInfrastructureFault(),
		s.keys.runtimeReconciliationProof(),
	}
	argv := []interface{}{
		in.RequestAdmissionID, in.Fingerprint,
		strconv.FormatInt(in.RouteID, 10), strconv.FormatInt(in.UserID, 10),
		in.IntegrityEpoch, strconv.FormatInt(in.IntegrityRevision, 10),
		strconv.FormatInt(in.RouteRateRevision, 10), strconv.FormatInt(in.GlobalConcurrencyRevision, 10),
		requestLimitOverrideArg(in.RPMLimitOverride),
		requestLimitOverrideArg(in.RPDLimitOverride),
		requestLimitOverrideArg(in.TPMLimitOverride),
		requestLimitOverrideArg(in.ConcurrencyLimitOverride),
	}
	res, err := s.acquireRequest.Run(ctx, s.client, keys, argv...).Result()
	if err != nil {
		return RequestAdmissionResult{}, storeUnavailable(err, "breakerstore acquire request admission")
	}
	arr, ok := res.([]interface{})
	if !ok || len(arr) == 0 {
		return RequestAdmissionResult{}, storeUnavailable(errors.New("unexpected acquire request reply"), "breakerstore acquire request admission")
	}
	code, _ := arr[0].(string)
	if code == runtimeRedisInstanceChanged {
		s.ensureRuntimeInfrastructureFault(ctx)
		code = string(RequestStoreUnavailable)
	}
	out := RequestAdmissionResult{Outcome: RequestAdmissionOutcome(code)}
	switch code {
	case "allowed", "idempotent":
		out.Outcome = RequestAllowed
		if len(arr) > 1 {
			out.LeaseUntilMs, _ = arr[1].(int64)
		}
		if len(arr) > 2 {
			out.RenewIntervalMs, _ = arr[2].(int64)
		}
	case "limited":
		if len(arr) > 1 {
			out.LimitedDimension, _ = arr[1].(string)
		}
	case "runtime_sync_required", "runtime_sync_pending", "stale_setting_revision":
		if len(arr) > 1 {
			out.SyncTarget, _ = arr[1].(string)
		}
	}
	return out, nil
}

func requestLimitOverrideArg(limit *int64) string {
	if limit == nil {
		return "inherit"
	}
	return strconv.FormatInt(*limit, 10)
}

// ReserveResult 是 TPM 预占的稳定结果。
type ReserveResult string

const (
	ReserveReserved         ReserveResult = "reserved"
	ReserveLimited          ReserveResult = "limited"
	ReserveConflict         ReserveResult = "conflict"
	ReserveUnknown          ReserveResult = "unknown_request_admission"
	ReserveRuntimeStateLost ReserveResult = "runtime_state_lost"
	ReserveStaleEpoch       ReserveResult = "stale_integrity_epoch"
	ReserveStoreUnavailable ReserveResult = "store_unavailable"
)

// ReserveRequestTokens 对已签发 request token 一次性幂等预占 route-user TPM（§2.14.11）。
// 实际限额只从 request token 内由 Acquire 冻结的 effective TPM 读取；expected epoch 必须来自
// 本次调用前的 PostgreSQL 强一致读取，Lua 与 Redis marker、token 冻结 epoch 三方校验后才允许写入。
func (s *Store) ReserveRequestTokens(
	ctx context.Context,
	requestAdmissionID string,
	routeID, userID, estimatedTokens int64,
	integrityEpoch string,
	integrityRevision int64,
) (result ReserveResult, err error) {
	done := s.beginOperation(ctx, operationReserveRequest)
	defer func() { done(string(result), err) }()

	if err := validateReserveRequestTokensInput(requestAdmissionID, routeID, userID, estimatedTokens); err != nil {
		return "", err
	}
	if err := validateRequestLifecycleInput(requestAdmissionID, routeID, userID, integrityEpoch, integrityRevision); err != nil {
		return "", err
	}
	if s.localRuntimeInfrastructureFault(ctx) {
		return ReserveStoreUnavailable, nil
	}
	now := time.Now()
	keys := []string{
		s.keys.stateIntegrityMarker(),
		s.keys.admissionRequest(requestAdmissionID),
		s.keys.requestTPMBucket(routeID, userID, minuteBucket(now)),
		s.keys.runtimeInfrastructureFault(),
		s.keys.runtimeReconciliationProof(),
	}
	res, err := s.reserveRequest.Run(ctx, s.client, keys,
		strconv.FormatInt(estimatedTokens, 10), strconv.FormatInt(routeID, 10), strconv.FormatInt(userID, 10),
		integrityEpoch, strconv.FormatInt(integrityRevision, 10)).Result()
	if err != nil {
		return "", storeUnavailable(err, "breakerstore reserve request tokens")
	}
	arr, ok := res.([]interface{})
	if !ok || len(arr) == 0 {
		return "", storeUnavailable(errors.New("unexpected reserve reply"), "breakerstore reserve request tokens")
	}
	code, _ := arr[0].(string)
	if code == runtimeRedisInstanceChanged {
		s.ensureRuntimeInfrastructureFault(ctx)
		code = string(ReserveStoreUnavailable)
	}
	return ReserveResult(code), nil
}

// RequestAdmissionLifecycleOutcome 是 request token 续租/终态的稳定结果。
type RequestAdmissionLifecycleOutcome string

const (
	RequestLifecycleRenewed          RequestAdmissionLifecycleOutcome = "renewed"
	RequestLifecycleFinished         RequestAdmissionLifecycleOutcome = "finished"
	RequestLifecycleTerminal         RequestAdmissionLifecycleOutcome = "terminal"
	RequestLifecycleExpired          RequestAdmissionLifecycleOutcome = "expired"
	RequestLifecycleUnknown          RequestAdmissionLifecycleOutcome = "unknown_request_admission"
	RequestLifecycleRuntimeStateLost RequestAdmissionLifecycleOutcome = "runtime_state_lost"
	RequestLifecycleStaleEpoch       RequestAdmissionLifecycleOutcome = "stale_integrity_epoch"
	RequestLifecycleRuntimeSyncReq   RequestAdmissionLifecycleOutcome = "runtime_sync_required"
	RequestLifecycleConflict         RequestAdmissionLifecycleOutcome = "conflict"
)

// RenewRequestAdmission 延长 active token 与 route-user concurrency lease。expected epoch 必须来自
// 本次调用前的 PostgreSQL 强一致读取；Lua 原子校验 marker 与 token 冻结 epoch 后才允许写入。
func (s *Store) RenewRequestAdmission(
	ctx context.Context,
	requestAdmissionID string,
	routeID, userID int64,
	integrityEpoch string,
	integrityRevision int64,
) (outcome RequestAdmissionLifecycleOutcome, err error) {
	done := s.beginOperation(ctx, operationRenewRequest)
	defer func() { done(string(outcome), err) }()

	if err := validateRequestLifecycleInput(requestAdmissionID, routeID, userID, integrityEpoch, integrityRevision); err != nil {
		return "", err
	}
	keys := []string{
		s.keys.stateIntegrityMarker(),
		s.keys.admissionRequest(requestAdmissionID),
		s.keys.requestConcurrency(routeID, userID),
	}
	res, err := s.renewRequest.Run(ctx, s.client, keys,
		requestAdmissionID,
		strconv.FormatInt(routeID, 10),
		strconv.FormatInt(userID, 10),
		integrityEpoch,
		strconv.FormatInt(integrityRevision, 10),
	).Result()
	if err != nil {
		return "", storeUnavailable(err, "breakerstore renew request admission")
	}
	return parseRequestLifecycleReply(res, "breakerstore renew request admission")
}

// FinishRequestAdmission 唯一终态：释放并发、保留 RPM/RPD、按可空权威 usage 对账/释放 TPM。
// authoritativeTPM<0 表示无权威 usage（释放预占）。expected epoch 必须来自本次调用前的
// PostgreSQL 强一致读取；marker/token epoch mismatch 时 Lua 保证零写入。
func (s *Store) FinishRequestAdmission(
	ctx context.Context,
	requestAdmissionID string,
	routeID, userID int64,
	authoritativeTPM int64,
	integrityEpoch string,
	integrityRevision int64,
) (outcome RequestAdmissionLifecycleOutcome, err error) {
	done := s.beginOperation(ctx, operationFinishRequest)
	defer func() { done(string(outcome), err) }()

	if err := validateRequestLifecycleInput(requestAdmissionID, routeID, userID, integrityEpoch, integrityRevision); err != nil {
		return "", err
	}
	authoritative := ""
	if authoritativeTPM >= 0 {
		authoritative = strconv.FormatInt(authoritativeTPM, 10)
	}
	keys := []string{
		s.keys.stateIntegrityMarker(),
		s.keys.admissionRequest(requestAdmissionID),
		s.keys.requestConcurrency(routeID, userID),
	}
	res, err := s.finishRequest.Run(ctx, s.client, keys,
		requestAdmissionID,
		strconv.FormatInt(routeID, 10),
		strconv.FormatInt(userID, 10),
		authoritative,
		integrityEpoch,
		strconv.FormatInt(integrityRevision, 10),
	).Result()
	if err != nil {
		return "", storeUnavailable(err, "breakerstore finish request admission")
	}
	return parseRequestLifecycleReply(res, "breakerstore finish request admission")
}

func parseRequestLifecycleReply(res interface{}, operation string) (RequestAdmissionLifecycleOutcome, error) {
	arr, ok := res.([]interface{})
	if !ok || len(arr) == 0 {
		return "", storeUnavailable(errors.New("unexpected request lifecycle reply"), operation)
	}
	code, _ := arr[0].(string)
	outcome := RequestAdmissionLifecycleOutcome(code)
	switch outcome {
	case RequestLifecycleRenewed,
		RequestLifecycleFinished,
		RequestLifecycleTerminal,
		RequestLifecycleExpired,
		RequestLifecycleUnknown,
		RequestLifecycleRuntimeStateLost,
		RequestLifecycleStaleEpoch,
		RequestLifecycleRuntimeSyncReq,
		RequestLifecycleConflict:
		return outcome, nil
	}
	return "", storeUnavailable(errors.New("unknown request lifecycle outcome: "+code), operation)
}
