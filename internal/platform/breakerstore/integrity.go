package breakerstore

import (
	"context"
	"errors"
	"fmt"
	"strconv"
)

const StateEpochExpectedMarkerAbsent = "absent"

type StateEpochPrepareResult string

const (
	StateEpochPrepared         StateEpochPrepareResult = "prepared"
	StateEpochNewReadyObserved StateEpochPrepareResult = "new_ready_observed"
	StateEpochConflict         StateEpochPrepareResult = "conflict"
)

// StateIntegritySnapshot 是完整性 marker 的只读视图。pending 字段用于 durable
// operation 恢复；ready 的 last operation 字段用于 Commit 响应丢失后的跨存储终结。
type StateIntegritySnapshot struct {
	Exists   bool
	State    string // ready|pending
	Epoch    string
	Revision int64

	MarkerHash         string
	OperationToken     string
	TransitionHash     string
	ExpectedMarkerHash string
	OldEpoch           string
	OldRevision        int64
	NewEpoch           string
	NewRevision        int64
	LastOperationToken string
	LastTransitionHash string
}

// Ready 报告 marker 是否处于可准入的 ready，epoch/revision 与 canonical hash 均匹配。
func (s StateIntegritySnapshot) Ready(expectedEpoch string, expectedRevision int64) bool {
	return s.Exists && s.State == "ready" && s.Epoch == expectedEpoch && s.Revision == expectedRevision &&
		s.MarkerHash == StateIntegrityReadyMarkerHash(expectedEpoch, expectedRevision)
}

// StateIntegrityReadyMarkerHash 定义 ready marker 的 canonical SHA-256。只包含身份字段，
// 不把 Redis TIME 产生的可变时间纳入恢复比较。
func StateIntegrityReadyMarkerHash(epoch string, revision int64) string {
	return HashPayload(fmt.Sprintf(`{"epoch":%q,"revision":%d,"state":"ready"}`, epoch, revision))
}

// StateIntegrity 只读读取完整性 marker。
func (s *Store) StateIntegrity(ctx context.Context) (snapshot StateIntegritySnapshot, err error) {
	done := s.beginOperation(ctx, operationStateIntegrity)
	defer func() { done(stateIntegrityOperationResult(snapshot), err) }()

	res, err := s.epochRead.Run(ctx, s.client, []string{s.keys.stateIntegrityMarker()}).Result()
	if err != nil {
		return StateIntegritySnapshot{}, storeUnavailable(err, "breakerstore state integrity read")
	}
	arr, ok := res.([]interface{})
	if !ok || len(arr) < 14 {
		return StateIntegritySnapshot{}, storeUnavailable(errors.New("unexpected integrity reply"), "breakerstore state integrity read")
	}
	snap := StateIntegritySnapshot{}
	if exists, _ := arr[0].(int64); exists == 1 {
		snap.Exists = true
	}
	snap.State, _ = arr[1].(string)
	snap.Epoch, _ = arr[2].(string)
	snap.Revision, _ = arr[3].(int64)
	snap.MarkerHash, _ = arr[4].(string)
	snap.OperationToken, _ = arr[5].(string)
	snap.TransitionHash, _ = arr[6].(string)
	snap.ExpectedMarkerHash, _ = arr[7].(string)
	snap.OldEpoch, _ = arr[8].(string)
	snap.OldRevision, _ = arr[9].(int64)
	snap.NewEpoch, _ = arr[10].(string)
	snap.NewRevision, _ = arr[11].(int64)
	snap.LastOperationToken, _ = arr[12].(string)
	snap.LastTransitionHash, _ = arr[13].(string)
	return snap, nil
}

type StateEpochFenceInput struct {
	Token              string
	TransitionHash     string
	ExpectedMarkerHash string
	OldEpoch           string
	OldRevision        int64
	NewEpoch           string
	NewRevision        int64
}

// RecoverRuntimeStateEpochFence 执行 marker 五分支真值表。它同时是首次 Prepare
// 和 pending/op 丢失后的 recovery-only 入口，不提供 Abort。
func (s *Store) RecoverRuntimeStateEpochFence(ctx context.Context, in StateEpochFenceInput) (StateEpochPrepareResult, error) {
	if err := validateStateEpochFenceInput(in); err != nil {
		return "", err
	}
	res, err := s.epochPrepare.Run(ctx, s.client,
		[]string{s.keys.stateIntegrityMarker(), s.keys.runtimeControlOp(in.Token)},
		in.Token,
		in.OldEpoch,
		strconv.FormatInt(in.OldRevision, 10),
		in.NewEpoch,
		strconv.FormatInt(in.NewRevision, 10),
		in.TransitionHash,
		in.ExpectedMarkerHash,
		StateIntegrityReadyMarkerHash(in.NewEpoch, in.NewRevision),
	).Result()
	if err != nil {
		return "", storeUnavailable(err, "breakerstore recover state epoch fence")
	}
	arr, ok := res.([]interface{})
	if !ok || len(arr) == 0 {
		return "", storeUnavailable(errors.New("unexpected epoch prepare reply"), "breakerstore recover state epoch fence")
	}
	code, _ := arr[0].(string)
	result := StateEpochPrepareResult(code)
	if result != StateEpochPrepared && result != StateEpochNewReadyObserved && result != StateEpochConflict {
		return "", storeUnavailable(fmt.Errorf("unexpected epoch prepare result %q", code), "breakerstore recover state epoch fence")
	}
	return result, nil
}

// CommitRuntimeStateEpoch 激活同 operation pending marker。
func (s *Store) CommitRuntimeStateEpoch(ctx context.Context, in StateEpochFenceInput) (bool, error) {
	if err := validateStateEpochFenceInput(in); err != nil {
		return false, err
	}
	res, err := s.epochCommit.Run(ctx, s.client,
		[]string{s.keys.stateIntegrityMarker(), s.keys.runtimeControlOp(in.Token)},
		in.Token,
		in.TransitionHash,
		in.NewEpoch,
		strconv.FormatInt(in.NewRevision, 10),
		StateIntegrityReadyMarkerHash(in.NewEpoch, in.NewRevision),
		strconv.FormatInt(controlOpTTLMs, 10),
	).Result()
	if err != nil {
		return false, storeUnavailable(err, "breakerstore commit state epoch")
	}
	arr, ok := res.([]interface{})
	if !ok || len(arr) == 0 {
		return false, storeUnavailable(errors.New("unexpected epoch commit reply"), "breakerstore commit state epoch")
	}
	code, _ := arr[0].(string)
	return code == "committed", nil
}

// PrepareStateEpoch 保留为测试/受信任工具的简化入口；生产 coordinator 必须使用
// 带 durable token/expected hash 的 RecoverRuntimeStateEpochFence。
func (s *Store) PrepareStateEpoch(ctx context.Context, oldEpoch string, oldRevision int64, newEpoch string, newRevision int64, transitionHash string) (string, error) {
	in := legacyStateEpochFenceInput(oldEpoch, oldRevision, newEpoch, newRevision, transitionHash)
	result, err := s.RecoverRuntimeStateEpochFence(ctx, in)
	return string(result), err
}

// CommitStateEpoch 保留简化测试入口；它只提交当前 marker 中的同一 pending operation。
func (s *Store) CommitStateEpoch(ctx context.Context, newEpoch string, newRevision int64) (bool, error) {
	snap, err := s.StateIntegrity(ctx)
	if err != nil {
		return false, err
	}
	if snap.Ready(newEpoch, newRevision) {
		return true, nil
	}
	if snap.State != "pending" || snap.NewEpoch != newEpoch || snap.NewRevision != newRevision {
		return false, nil
	}
	return s.CommitRuntimeStateEpoch(ctx, StateEpochFenceInput{
		Token: snap.OperationToken, TransitionHash: snap.TransitionHash, ExpectedMarkerHash: snap.ExpectedMarkerHash,
		OldEpoch: snap.OldEpoch, OldRevision: snap.OldRevision, NewEpoch: snap.NewEpoch, NewRevision: snap.NewRevision,
	})
}

// BootstrapStateEpoch 是测试 fixture/受信任工具便捷入口。Gateway 启动不调用它，
// 而由 PostgreSQL durable operation coordinator 完成 bootstrap。
func (s *Store) BootstrapStateEpoch(ctx context.Context, epoch string, revision int64, transitionHash string) (bool, error) {
	in := legacyStateEpochFenceInput("", 0, epoch, revision, transitionHash)
	result, err := s.RecoverRuntimeStateEpochFence(ctx, in)
	if err != nil || result != StateEpochPrepared {
		return false, err
	}
	return s.CommitRuntimeStateEpoch(ctx, in)
}

type RuntimeReadinessInput struct {
	Epoch                    string
	EpochRevision            int64
	RouteRateLimitRevision   int64
	ChannelRateLimitRevision int64
	ConcurrencyRevision      int64
	CircuitBreakerRevision   int64
	RoutingBalanceRevision   int64
}

type RuntimeReadinessResult struct {
	Ready  bool
	Reason string
}

// CheckRuntimeReadiness 原子校验 marker 与五个关键 control 可用，并校验 control
// payload 与其 SHA-256 一致。Redis 连接/脚本错误以基础设施错误返回。
func (s *Store) CheckRuntimeReadiness(ctx context.Context, in RuntimeReadinessInput) (result RuntimeReadinessResult, err error) {
	done := s.beginOperation(ctx, operationRuntimeReadiness)
	defer func() {
		observationResult := operationResultNotReady
		if result.Ready {
			observationResult = operationResultReady
		}
		done(observationResult, err)
	}()
	if s.localRuntimeInfrastructureFault(ctx) {
		return RuntimeReadinessResult{Reason: RuntimeReadinessReasonStoreFaultLatched}, nil
	}

	res, err := s.runtimeReady.Run(ctx, s.client, []string{
		s.keys.stateIntegrityMarker(),
		s.keys.admissionRouteRate(),
		s.keys.admissionChannelRate(),
		s.keys.admissionGlobalConcurrency(),
		s.keys.runtimeControlSetting("gateway.circuit_breaker"),
		s.keys.runtimeControlSetting("gateway.routing_balance"),
		s.keys.runtimeInfrastructureFault(),
		s.keys.runtimeReconciliationProof(),
	}, runtimeReadinessArgs(in)...).Result()
	if err != nil {
		return RuntimeReadinessResult{}, storeUnavailable(err, "breakerstore runtime readiness")
	}
	arr, ok := res.([]interface{})
	if !ok || len(arr) == 0 {
		return RuntimeReadinessResult{}, storeUnavailable(errors.New("unexpected readiness reply"), "breakerstore runtime readiness")
	}
	code, _ := arr[0].(string)
	if code == runtimeRedisInstanceChanged {
		s.ensureRuntimeInfrastructureFault(ctx)
		code = RuntimeReadinessReasonStoreFaultLatched
	}
	if code != "ready" {
		return RuntimeReadinessResult{Reason: code}, nil
	}
	if len(arr) != 11 {
		return RuntimeReadinessResult{}, storeUnavailable(errors.New("incomplete readiness payload reply"), "breakerstore runtime readiness")
	}
	if !validRuntimeControlProofs(arr[1:]) {
		return RuntimeReadinessResult{Reason: "control_payload_mismatch"}, nil
	}
	return RuntimeReadinessResult{Ready: true, Reason: "ready"}, nil
}

func runtimeReadinessArgs(in RuntimeReadinessInput) []interface{} {
	return []interface{}{
		in.Epoch,
		strconv.FormatInt(in.EpochRevision, 10),
		strconv.FormatInt(in.RouteRateLimitRevision, 10),
		strconv.FormatInt(in.ChannelRateLimitRevision, 10),
		strconv.FormatInt(in.ConcurrencyRevision, 10),
		strconv.FormatInt(in.CircuitBreakerRevision, 10),
		strconv.FormatInt(in.RoutingBalanceRevision, 10),
		StateIntegrityReadyMarkerHash(in.Epoch, in.EpochRevision),
	}
}

func validRuntimeControlProofs(values []interface{}) bool {
	if len(values) != 10 {
		return false
	}
	for index := 0; index < len(values); index += 2 {
		payload, payloadOK := values[index].(string)
		payloadHash, hashOK := values[index+1].(string)
		if !payloadOK || !hashOK || payload == "" || payloadHash == "" || HashPayload(payload) != payloadHash {
			return false
		}
	}
	return true
}

func validateStateEpochFenceInput(in StateEpochFenceInput) error {
	if in.Token == "" || in.TransitionHash == "" || in.ExpectedMarkerHash == "" || in.NewEpoch == "" || in.NewRevision < 1 {
		return errors.New("breakerstore: incomplete state epoch fence input")
	}
	if in.OldRevision < 0 || (in.OldEpoch == "" && in.OldRevision != 0) || (in.OldEpoch != "" && in.OldRevision < 1) {
		return errors.New("breakerstore: invalid old state epoch identity")
	}
	if in.ExpectedMarkerHash != StateEpochExpectedMarkerAbsent && len(in.ExpectedMarkerHash) != 64 {
		return errors.New("breakerstore: invalid expected state epoch marker hash")
	}
	return nil
}

func legacyStateEpochFenceInput(oldEpoch string, oldRevision int64, newEpoch string, newRevision int64, transitionHash string) StateEpochFenceInput {
	expected := StateEpochExpectedMarkerAbsent
	if oldEpoch != "" {
		expected = StateIntegrityReadyMarkerHash(oldEpoch, oldRevision)
	}
	return StateEpochFenceInput{
		Token:              "legacy-" + transitionHash,
		TransitionHash:     transitionHash,
		ExpectedMarkerHash: expected,
		OldEpoch:           oldEpoch,
		OldRevision:        oldRevision,
		NewEpoch:           newEpoch,
		NewRevision:        newRevision,
	}
}
