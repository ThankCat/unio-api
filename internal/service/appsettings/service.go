package appsettings

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"

	messagesadapter "github.com/ThankCat/unio-gateway/internal/core/adapter/anthropic/messages"
	"github.com/ThankCat/unio-gateway/internal/core/runtimecontrol"
	"github.com/ThankCat/unio-gateway/internal/platform/breakerstore"
	"github.com/ThankCat/unio-gateway/internal/platform/failure"
	"github.com/ThankCat/unio-gateway/internal/platform/store/sqlc"
	"github.com/jackc/pgx/v5"
)

// RuntimeControlPublisher 是五个关键运行时 setting 的 durable publisher。
type RuntimeControlPublisher interface {
	Publish(ctx context.Context, req runtimecontrol.PublishRequest) (runtimecontrol.PublishResult, error)
}

// RuntimeControlStore 提供 setting control 的定位和只读同步状态。
type RuntimeControlStore interface {
	RequestRateLimitControl() breakerstore.ControlTarget
	GlobalConcurrencyControl() breakerstore.ControlTarget
	SettingControl(settingKey string) breakerstore.ControlTarget
	ReadControl(ctx context.Context, target breakerstore.ControlTarget, expectedRevision int64) (breakerstore.ControlSnapshot, error)
}

// WriteHook 在某个普通 setting 成功写入后同步执行（例如全局阈值改动后重算账号运行态）。
// 返回值原样附在 SettingWriteResult.RuntimeRefresh 里回给管理端；返回 error 不回滚已保存的值，
// 只在结果里以 RuntimeRefreshError 报出——写入已经落库且传播，副作用失败不应让管理员以为没保存。
type WriteHook func(ctx context.Context) (any, error)

// Service 是 admin 侧读写全局运行时配置的服务。
type Service struct {
	store            *SettingsStore
	runtimePublisher RuntimeControlPublisher
	runtimeStore     RuntimeControlStore
	writeHooks       map[string]WriteHook
}

// WithWriteHook 为指定 key 注册写入后钩子（同一 key 只保留最后一个）。
func (s *Service) WithWriteHook(key string, hook WriteHook) *Service {
	if s.writeHooks == nil {
		s.writeHooks = map[string]WriteHook{}
	}
	if hook == nil {
		delete(s.writeHooks, key)
		return s
	}
	s.writeHooks[key] = hook
	return s
}

// NewService 创建配置服务。
func NewService(store *SettingsStore) *Service {
	return &Service{store: store}
}

// NewServiceWithRuntimeControl 创建带 durable runtime-control 发布能力的管理服务。
func NewServiceWithRuntimeControl(store *SettingsStore, publisher RuntimeControlPublisher, runtimeStore RuntimeControlStore) *Service {
	return &Service{store: store, runtimePublisher: publisher, runtimeStore: runtimeStore}
}

// SettingItem 是通用配置列表项:注册元数据 + 当前生效值 + 生效来源。
type SettingItem struct {
	Key         string
	Category    string
	Label       string
	Description string
	HotReload   bool
	ReadOnly    bool
	Default     json.RawMessage
	Value       json.RawMessage
	Source      string // redis | db | default
	Revision    int64

	RuntimeActiveRevision  int64
	RuntimePendingRevision int64
	RuntimeSyncState       string // active | runtime_sync_pending | runtime_sync_required | stale | store_unavailable
}

// List 返回全部已注册配置项(含元数据与本进程当前生效值/来源),供 admin 面板通用渲染。
func (s *Service) List(ctx context.Context) []SettingItem {
	defs := s.store.registry.List()
	out := make([]SettingItem, 0, len(defs))
	for _, d := range defs {
		if d.DedicatedControl {
			continue
		}
		item := SettingItem{
			Key:         d.Key,
			Category:    d.Category,
			Label:       d.Label,
			Description: d.Description,
			HotReload:   d.HotReload,
			ReadOnly:    d.ReadOnly,
			Default:     d.Default,
		}
		if isRuntimeControlSetting(d.Key) {
			record, err := s.store.Record(ctx, d.Key)
			if err != nil {
				item.Value = d.Default
				item.Source = "default"
				item.RuntimeSyncState = "runtime_sync_required"
				out = append(out, item)
				continue
			}
			item.Value = record.Value
			item.Source = "db"
			item.Revision = record.Revision
			s.applyRuntimeState(ctx, &item, record)
		} else if v, ok := s.store.Effective(ctx, d.Key); ok {
			item.Value = v.Value
			item.Source = v.Source
			if record, err := s.store.Record(ctx, d.Key); err == nil {
				item.Revision = record.Revision
			}
		}
		out = append(out, item)
	}
	return out
}

// SetRaw 按 key 校验并写入原始 JSON 值(通用写入路径)。
func (s *Service) SetRaw(ctx context.Context, key string, value json.RawMessage) error {
	_, err := s.SetRawWithResult(ctx, key, value)
	return err
}

// SettingWriteResult 区分 PostgreSQL 已保存与 Redis 执行面是否已激活。
type SettingWriteResult struct {
	Key             string `json:"key"`
	Revision        int64  `json:"revision"`
	State           string `json:"state"` // saved | active | runtime_sync_pending
	ActiveRevision  int64  `json:"active_revision"`
	PendingRevision int64  `json:"pending_revision"`
	// RuntimeRefresh 是写入后钩子的返回（例如账号运行态重算统计）；没有钩子时省略。
	RuntimeRefresh any `json:"runtime_refresh,omitempty"`
	// RuntimeRefreshError 是钩子失败的说明；值本身已保存，只是副作用没做完。
	RuntimeRefreshError string `json:"runtime_refresh_error,omitempty"`
}

// runWriteHook 执行 key 对应的写入后钩子（若有），结果写进 result；钩子失败不影响写入结论。
func (s *Service) runWriteHook(ctx context.Context, key string, result *SettingWriteResult) {
	hook, ok := s.writeHooks[key]
	if !ok || hook == nil {
		return
	}
	refresh, err := hook(ctx)
	if err != nil {
		result.RuntimeRefreshError = err.Error()
		return
	}
	result.RuntimeRefresh = refresh
}

// SetRawWithResult 校验并写入设置。四个关键 setting 强制走 durable publisher，普通 PUT 无法绕过。
func (s *Service) SetRawWithResult(ctx context.Context, key string, value json.RawMessage) (SettingWriteResult, error) {
	def, ok := s.store.registry.Get(key)
	if !ok {
		return SettingWriteResult{}, errors.New("appsettings: unknown key " + key)
	}
	if def.DedicatedControl {
		return SettingWriteResult{}, errors.New("appsettings: setting " + key + " requires its dedicated control API")
	}
	if def.ReadOnly {
		return SettingWriteResult{}, errors.New("appsettings: setting " + key + " is system-managed and read-only")
	}
	if def.Validate != nil {
		if err := def.Validate(value); err != nil {
			return SettingWriteResult{}, err
		}
	}
	if !isRuntimeControlSetting(key) {
		if err := s.store.Set(ctx, key, value); err != nil {
			return SettingWriteResult{}, err
		}
		result := SettingWriteResult{Key: key, State: "saved"}
		if record, err := s.store.Record(ctx, key); err == nil {
			result.Revision = record.Revision
		}
		s.runWriteHook(ctx, key, &result)
		return result, nil
	}

	canonical, err := canonicalRuntimeSetting(key, value)
	if err != nil {
		return SettingWriteResult{}, err
	}
	record, err := s.store.Record(ctx, key)
	if err != nil {
		return SettingWriteResult{}, failure.Wrap(failure.CodeAppSettingsStoreFailed, err, failure.WithMessage("appsettings: read critical setting"))
	}
	currentCanonical, currentErr := canonicalRuntimeSetting(key, record.Value)
	if currentErr == nil && bytes.Equal(currentCanonical, canonical) {
		result := SettingWriteResult{Key: key, Revision: record.Revision}
		s.fillWriteRuntimeState(ctx, &result, key, record.Revision, canonical)
		return result, nil
	}
	if s.runtimePublisher == nil || s.runtimeStore == nil {
		return SettingWriteResult{}, failure.New(
			failure.CodeGatewayBreakerStoreUnavailable,
			failure.WithMessage("appsettings: runtime-control publisher unavailable"),
		)
	}

	token, err := newRuntimeControlToken()
	if err != nil {
		return SettingWriteResult{}, failure.Wrap(failure.CodeConfigInvalid, err, failure.WithMessage("appsettings: generate runtime-control token"))
	}
	nextRevision := record.Revision + 1
	settingKey := key
	publishResult, err := s.runtimePublisher.Publish(ctx, runtimecontrol.PublishRequest{
		Kind:            runtimecontrol.KindAppSetting,
		Target:          runtimeControlTarget(s.runtimeStore, key),
		Token:           token,
		Payload:         string(canonical),
		CurrentRevision: record.Revision,
		NextRevision:    nextRevision,
		SettingKey:      &settingKey,
		BusinessCommit: func(ctx context.Context, tx pgx.Tx) error {
			_, updateErr := sqlc.New(tx).UpdateAppSettingAtRevision(ctx, sqlc.UpdateAppSettingAtRevisionParams{
				Value:           []byte(canonical),
				Description:     def.Description,
				NextRevision:    nextRevision,
				Key:             key,
				CurrentRevision: record.Revision,
			})
			if errors.Is(updateErr, pgx.ErrNoRows) {
				return failure.New(failure.CodeConfigInvalid, failure.WithMessage("appsettings: setting revision changed during publish"))
			}
			return updateErr
		},
	})
	if err != nil {
		return SettingWriteResult{}, err
	}
	result := SettingWriteResult{Key: key, Revision: nextRevision}
	switch publishResult.State {
	case runtimecontrol.PublishCommitted:
		result.State = "active"
		result.ActiveRevision = publishResult.ActiveRevision
		s.store.PublishCache(ctx, key, canonical)
	case runtimecontrol.PublishRuntimeSyncPending:
		result.State = "runtime_sync_pending"
		s.fillWriteRuntimeState(ctx, &result, key, nextRevision, canonical)
	default:
		result.State = string(publishResult.State)
	}
	return result, nil
}

func isRuntimeControlSetting(key string) bool {
	switch key {
	case GatewayRequestRateLimitDefaultsKey,
		GatewayConcurrencyDefaultsKey, GatewayCircuitBreakerKey, GatewayRoutingBalanceKey:
		return true
	default:
		return false
	}
}

func canonicalRuntimeSetting(key string, raw json.RawMessage) (json.RawMessage, error) {
	switch key {
	case GatewayRequestRateLimitDefaultsKey:
		settings, err := DecodeRateLimitDefaultsSettings(raw)
		if err != nil {
			return nil, err
		}
		return encodeRateLimitDefaultsSettings(settings), nil
	case GatewayConcurrencyDefaultsKey:
		settings, err := DecodeConcurrencyDefaultsSettings(raw)
		if err != nil {
			return nil, err
		}
		return encodeConcurrencyDefaultsSettings(settings), nil
	case GatewayCircuitBreakerKey:
		settings, err := DecodeCircuitBreakerSettings(raw)
		if err != nil {
			return nil, err
		}
		return encodeCircuitBreakerSettings(settings), nil
	case GatewayRoutingBalanceKey:
		settings, err := DecodeRoutingBalanceSettings(raw)
		if err != nil {
			return nil, err
		}
		return encodeRoutingBalanceSettings(settings), nil
	default:
		return nil, errors.New("appsettings: not a runtime-control setting")
	}
}

func (s *Service) applyRuntimeState(ctx context.Context, item *SettingItem, record SettingRecord) {
	if s.runtimeStore == nil {
		item.RuntimeSyncState = "runtime_sync_required"
		return
	}
	snapshot, err := s.runtimeStore.ReadControl(ctx, runtimeControlTarget(s.runtimeStore, item.Key), record.Revision)
	if err != nil {
		item.RuntimeSyncState = "store_unavailable"
		return
	}
	item.RuntimeActiveRevision = snapshot.ActiveRevision
	item.RuntimePendingRevision = snapshot.PendingRevision
	switch snapshot.SyncState {
	case "pending":
		item.RuntimeSyncState = "runtime_sync_pending"
	case "absent", "stale":
		item.RuntimeSyncState = "runtime_sync_required"
	case "ahead":
		item.RuntimeSyncState = "stale"
	default:
		activeCanonical, err := canonicalRuntimeSetting(item.Key, json.RawMessage(snapshot.ActivePayload))
		expectedCanonical, expectedErr := canonicalRuntimeSetting(item.Key, record.Value)
		if err != nil || expectedErr != nil || !bytes.Equal(activeCanonical, expectedCanonical) {
			item.RuntimeSyncState = "stale"
		} else {
			item.RuntimeSyncState = "active"
		}
	}
}

func (s *Service) fillWriteRuntimeState(ctx context.Context, result *SettingWriteResult, key string, revision int64, expected json.RawMessage) {
	if s.runtimeStore == nil {
		result.State = "runtime_sync_pending"
		return
	}
	snapshot, err := s.runtimeStore.ReadControl(ctx, runtimeControlTarget(s.runtimeStore, key), revision)
	if err != nil {
		result.State = "runtime_sync_pending"
		return
	}
	result.ActiveRevision = snapshot.ActiveRevision
	result.PendingRevision = snapshot.PendingRevision
	activeCanonical, canonicalErr := canonicalRuntimeSetting(key, json.RawMessage(snapshot.ActivePayload))
	if snapshot.SyncState == "active" && canonicalErr == nil && bytes.Equal(activeCanonical, expected) {
		result.State = "active"
	} else {
		result.State = "runtime_sync_pending"
	}
}

func newRuntimeControlToken() (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	return "rctl_" + hex.EncodeToString(raw[:]), nil
}

func runtimeControlTarget(store RuntimeControlStore, key string) breakerstore.ControlTarget {
	switch key {
	case GatewayRequestRateLimitDefaultsKey:
		return store.RequestRateLimitControl()
	case GatewayConcurrencyDefaultsKey:
		return store.GlobalConcurrencyControl()
	default:
		return store.SettingControl(key)
	}
}

// GetAnthropicBetaPolicy 读取当前 Anthropic beta 策略(生效值)。
func (s *Service) GetAnthropicBetaPolicy(ctx context.Context) messagesadapter.BetaPolicy {
	return GetAnthropicBetaPolicy(ctx, s.store)
}

// SetAnthropicBetaPolicy 校验并写入 Anthropic beta 策略。
func (s *Service) SetAnthropicBetaPolicy(ctx context.Context, policy messagesadapter.BetaPolicy) error {
	return SetAnthropicBetaPolicy(ctx, s.store, policy)
}

// GetEmailSMTP 读取当前 SMTP 发信配置（含密码；Admin 专用面板回显，可读可写）。
func (s *Service) GetEmailSMTP(ctx context.Context) EmailSMTPConfig {
	return EmailSMTP(ctx, s.store)
}

// SetEmailSMTP 校验并写入 SMTP 发信配置（写 DB + 刷新 Redis/本地，免重启热生效）。
func (s *Service) SetEmailSMTP(ctx context.Context, cfg EmailSMTPConfig) error {
	return SetEmailSMTP(ctx, s.store, cfg)
}
