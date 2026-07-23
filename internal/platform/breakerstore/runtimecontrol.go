package breakerstore

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strconv"

	"github.com/redis/go-redis/v9"
)

// ControlTarget 标识一个 revisioned Redis 控制对象（§5.3.16：同一状态机服务全部目标）。
type ControlTarget struct {
	// controlKey 是控制 hash 的完整 Redis key。
	controlKey string
}

// RouteRateLimitControl / ChannelRateLimitControl / GlobalConcurrencyControl /
// ChannelAdmissionControl / SettingControl
// 构造各类控制目标。
func (s *Store) RouteRateLimitControl() ControlTarget {
	return ControlTarget{controlKey: s.keys.admissionRouteRate()}
}

func (s *Store) ChannelRateLimitControl() ControlTarget {
	return ControlTarget{controlKey: s.keys.admissionChannelRate()}
}

func (s *Store) GlobalConcurrencyControl() ControlTarget {
	return ControlTarget{controlKey: s.keys.admissionGlobalConcurrency()}
}

func (s *Store) ChannelAdmissionControl(channelID int64) ControlTarget {
	return ControlTarget{controlKey: s.keys.admissionChannel(channelID)}
}

// SettingControl 用于 gateway.circuit_breaker / gateway.routing_balance。
func (s *Store) SettingControl(settingKey string) ControlTarget {
	return ControlTarget{controlKey: s.keys.runtimeControlSetting(settingKey)}
}

// opKeyFor 依据控制 key 命名空间选择 admission:op 还是 runtime-control:op。
func (s *Store) opKeyFor(target ControlTarget, token string) string {
	// admission 控制使用 admission:v1:op；setting 控制使用 runtime-control:v1:op。
	if isAdmissionControl(target.controlKey) {
		return s.keys.admissionOp(token)
	}
	return s.keys.runtimeControlOp(token)
}

func isAdmissionControl(controlKey string) bool {
	return containsSeg(controlKey, ":admission:v1:")
}

func containsSeg(s, seg string) bool {
	return len(s) >= len(seg) && (indexOf(s, seg) >= 0)
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

// ControlSnapshot 是控制对象的只读视图。
type ControlSnapshot struct {
	ActiveRevision  int64
	PendingRevision int64
	ActivePayload   string
	PendingPayload  string
	// SyncState: active|pending|stale|ahead|absent。
	SyncState string
}

// ControlPrepareResult 是 Prepare 的结果码。
type ControlPrepareResult string

const (
	ControlPrepared               ControlPrepareResult = "prepared"
	ControlPrepareCommitted       ControlPrepareResult = "committed"
	ControlPrepareAborted         ControlPrepareResult = "aborted"
	ControlPrepareInvalid         ControlPrepareResult = "invalid"
	ControlPrepareStale           ControlPrepareResult = "stale"
	ControlPrepareConflict        ControlPrepareResult = "conflict"
	ControlPreparePendingConflict ControlPrepareResult = "conflict_pending"
)

// HashPayload 计算规范化 payload 的小写 SHA-256（§4.5.1：payload_hash）。
func HashPayload(payload string) string {
	sum := sha256.Sum256([]byte(payload))
	return hex.EncodeToString(sum[:])
}

const controlOpTTLMs = int64(24 * 60 * 60 * 1000) // 终态 op/tombstone 至少保留 24 小时（§5.5.8）。

// PrepareControl 在 Redis 建立 pending fence（application 已确认 PostgreSQL operation=preparing 后调用）。
func (s *Store) PrepareControl(ctx context.Context, target ControlTarget, token string, currentRevision, nextRevision int64, payload string) (ControlPrepareResult, int64, error) {
	res, err := s.controlPrepare.Run(ctx, s.client,
		[]string{target.controlKey, s.opKeyFor(target, token)},
		token, strconv.FormatInt(currentRevision, 10), strconv.FormatInt(nextRevision, 10),
		HashPayload(payload), payload, strconv.FormatInt(controlOpTTLMs, 10)).Result()
	if err != nil {
		return "", 0, storeUnavailable(err, "breakerstore prepare control")
	}
	arr, ok := res.([]interface{})
	if !ok || len(arr) == 0 {
		return "", 0, storeUnavailable(errors.New("unexpected prepare reply"), "breakerstore prepare control")
	}
	code, _ := arr[0].(string)
	var activeRev int64
	if len(arr) > 1 {
		activeRev, _ = arr[1].(int64)
	}
	return ControlPrepareResult(code), activeRev, nil
}

// CommitControl 激活 pending（application 已确认 PostgreSQL operation=db_committed 后调用）。
func (s *Store) CommitControl(ctx context.Context, target ControlTarget, token, payload string) (int64, error) {
	res, err := s.controlCommit.Run(ctx, s.client,
		[]string{target.controlKey, s.opKeyFor(target, token)},
		token, HashPayload(payload), strconv.FormatInt(controlOpTTLMs, 10)).Result()
	if err != nil {
		return 0, storeUnavailable(err, "breakerstore commit control")
	}
	arr, ok := res.([]interface{})
	if !ok || len(arr) == 0 {
		return 0, storeUnavailable(errors.New("unexpected commit reply"), "breakerstore commit control")
	}
	code, _ := arr[0].(string)
	if code != "committed" {
		return 0, errors.New("breakerstore commit control: " + code)
	}
	var activeRev int64
	if len(arr) > 1 {
		activeRev, _ = arr[1].(int64)
	}
	return activeRev, nil
}

// AbortControl 撤销未提交 pending（仅业务 revision 未提交时）。
func (s *Store) AbortControl(ctx context.Context, target ControlTarget, token, payload string) error {
	res, err := s.controlAbort.Run(ctx, s.client,
		[]string{target.controlKey, s.opKeyFor(target, token)},
		token, HashPayload(payload), strconv.FormatInt(controlOpTTLMs, 10)).Result()
	if err != nil {
		return storeUnavailable(err, "breakerstore abort control")
	}
	arr, _ := res.([]interface{})
	if len(arr) > 0 {
		if code, _ := arr[0].(string); code == "aborted" {
			return nil
		} else {
			return errors.New("breakerstore abort control: " + code)
		}
	}
	return storeUnavailable(errors.New("unexpected abort reply"), "breakerstore abort control")
}

// ReadControl 只读控制态；expectedRevision<=0 表示不比较（返回 active|pending|absent）。
func (s *Store) ReadControl(ctx context.Context, target ControlTarget, expectedRevision int64) (ControlSnapshot, error) {
	exp := ""
	if expectedRevision > 0 {
		exp = strconv.FormatInt(expectedRevision, 10)
	}
	res, err := s.controlRead.Run(ctx, s.client, []string{target.controlKey}, exp).Result()
	if err != nil {
		return ControlSnapshot{}, storeUnavailable(err, "breakerstore read control")
	}
	arr, ok := res.([]interface{})
	if !ok || len(arr) < 5 {
		return ControlSnapshot{}, storeUnavailable(errors.New("unexpected read reply"), "breakerstore read control")
	}
	snap := ControlSnapshot{}
	snap.ActiveRevision, _ = arr[0].(int64)
	snap.PendingRevision, _ = arr[1].(int64)
	snap.ActivePayload, _ = arr[2].(string)
	snap.PendingPayload, _ = arr[3].(string)
	snap.SyncState, _ = arr[4].(string)
	return snap, nil
}

// RestoreMissingControl 仅当控制缺失时安装 PostgreSQL 当前 revision 的 active（recovery-only，§5.3.18）。
func (s *Store) RestoreMissingControl(ctx context.Context, target ControlTarget, revision int64, payload string) (bool, error) {
	res, err := s.controlRestore.Run(ctx, s.client, []string{target.controlKey},
		strconv.FormatInt(revision, 10), HashPayload(payload), payload).Result()
	if err != nil {
		return false, storeUnavailable(err, "breakerstore restore control")
	}
	arr, _ := res.([]interface{})
	if len(arr) > 0 {
		code, _ := arr[0].(string)
		return code == "installed", nil
	}
	return false, storeUnavailable(errors.New("unexpected restore reply"), "breakerstore restore control")
}

// RecoverCommittedControl 依据 PostgreSQL db_committed 事实，把缺失或停在 current 的 Redis control
// 原子恢复为 next committed。已有冲突 revision/pending 时拒绝覆盖。
func (s *Store) RecoverCommittedControl(ctx context.Context, target ControlTarget, token string, currentRevision, nextRevision int64, payload string) (int64, error) {
	res, err := s.controlRecoverCommitted.Run(ctx, s.client,
		[]string{target.controlKey, s.opKeyFor(target, token)},
		token, strconv.FormatInt(currentRevision, 10), strconv.FormatInt(nextRevision, 10),
		HashPayload(payload), payload, strconv.FormatInt(controlOpTTLMs, 10)).Result()
	if err != nil {
		return 0, storeUnavailable(err, "breakerstore recover committed control")
	}
	arr, ok := res.([]interface{})
	if !ok || len(arr) == 0 {
		return 0, storeUnavailable(errors.New("unexpected recovery reply"), "breakerstore recover committed control")
	}
	code, _ := arr[0].(string)
	if code != "committed" {
		return 0, errors.New("breakerstore recover committed control: " + code)
	}
	var revision int64
	if len(arr) > 1 {
		revision, _ = arr[1].(int64)
	}
	return revision, nil
}

// RecoverAbortedControl 依据 PostgreSQL 未提交业务 revision 的事实恢复旧 active，并撤销同 operation pending。
// pendingPayloadHash 来自 durable operation；已有其它 revision/pending 时拒绝覆盖。
func (s *Store) RecoverAbortedControl(ctx context.Context, target ControlTarget, token string, currentRevision, nextRevision int64, pendingPayloadHash, currentPayload string) error {
	res, err := s.controlRecoverAborted.Run(ctx, s.client,
		[]string{target.controlKey, s.opKeyFor(target, token)},
		token, strconv.FormatInt(currentRevision, 10), strconv.FormatInt(nextRevision, 10),
		pendingPayloadHash, HashPayload(currentPayload), currentPayload, strconv.FormatInt(controlOpTTLMs, 10)).Result()
	if err != nil {
		return storeUnavailable(err, "breakerstore recover aborted control")
	}
	arr, ok := res.([]interface{})
	if !ok || len(arr) == 0 {
		return storeUnavailable(errors.New("unexpected recovery reply"), "breakerstore recover aborted control")
	}
	code, _ := arr[0].(string)
	if code != "aborted" {
		return errors.New("breakerstore recover aborted control: " + code)
	}
	return nil
}

var _ = redis.Nil
