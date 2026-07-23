package appsettings

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/redis/go-redis/v9"
	"go.uber.org/zap"

	"github.com/ThankCat/unio-gateway/internal/platform/store/sqlc"
)

// Queries 是 SettingsStore 依赖的最小 DB 能力(由 *sqlc.Queries 实现)。
type Queries interface {
	GetAppSetting(ctx context.Context, key string) ([]byte, error)
	GetAppSettingRecord(ctx context.Context, key string) (sqlc.GetAppSettingRecordRow, error)
	UpsertAppSetting(ctx context.Context, arg sqlc.UpsertAppSettingParams) error
	SeedAppSetting(ctx context.Context, arg sqlc.SeedAppSettingParams) error
}

// localCacheTTL 是各进程本地缓存的去抖时长:热路径在此窗口内不打 Redis/DB。
// Redis 是跨进程实时源,本地缓存只为削峰;窗口很短以保证秒级生效。
const localCacheTTL = 3 * time.Second

// SettingsStore 是运行时配置的读写中枢。
//
// 读:本地短缓存 →(miss/过期)Redis →(miss)DB 源;命中 DB 后回填 Redis。
// 写:先写 DB(权威),再刷新 Redis(跨进程实时生效);Redis 不可用不阻断写入(下次读经 DB 兜底)。
// Redis 存的是权威源的镜像,故「当前生效值」可直接在 Redis 观测(KEY: <ns>:settings:<key>)。
type SettingsStore struct {
	queries  Queries
	redis    redis.Cmdable // 可为 nil:退化为 DB + 本地缓存(仍可用,仅无跨进程实时)
	keyNS    string
	registry *Registry
	logger   *zap.Logger

	mu    sync.Mutex
	local map[string]localEntry
}

type localEntry struct {
	value  json.RawMessage
	expiry time.Time
}

// NewSettingsStore 创建配置中枢。redis 传 nil 时降级为 DB + 本地缓存。
func NewSettingsStore(queries Queries, redisClient redis.Cmdable, keyNamespace string, registry *Registry, logger *zap.Logger) *SettingsStore {
	if logger == nil {
		logger = zap.NewNop()
	}
	if registry == nil {
		registry = DefaultRegistry()
	}
	return &SettingsStore{
		queries:  queries,
		redis:    redisClient,
		keyNS:    keyNamespace,
		registry: registry,
		logger:   logger,
		local:    make(map[string]localEntry),
	}
}

func (s *SettingsStore) redisKey(key string) string {
	return fmt.Sprintf("%s:settings:%s", s.keyNS, key)
}

// Raw 返回指定 key 的当前生效值(规范 JSON):本地缓存 → Redis → DB → 注册表默认。
// 任何一层失败都记 warn 并回退到默认,绝不因基础设施抖动让配置读取失败(读侧永不 error)。
func (s *SettingsStore) Raw(ctx context.Context, key string) json.RawMessage {
	def, ok := s.registry.Get(key)
	if !ok {
		s.logger.Warn("appsettings: unknown key requested", zap.String("key", key))
		return nil
	}

	if v, ok := s.readLocal(key); ok {
		return v
	}

	if v, ok := s.readRedis(ctx, key); ok {
		s.writeLocal(key, v)
		return v
	}

	v := s.readDBOrDefault(ctx, key, def)
	s.writeRedis(ctx, key, v)
	s.writeLocal(key, v)
	return v
}

func (s *SettingsStore) readLocal(key string) (json.RawMessage, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.local[key]
	if !ok || time.Now().After(e.expiry) {
		return nil, false
	}
	return e.value, true
}

func (s *SettingsStore) writeLocal(key string, v json.RawMessage) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.local[key] = localEntry{value: v, expiry: time.Now().Add(localCacheTTL)}
}

func (s *SettingsStore) readRedis(ctx context.Context, key string) (json.RawMessage, bool) {
	if s.redis == nil {
		return nil, false
	}
	raw, err := s.redis.Get(ctx, s.redisKey(key)).Bytes()
	if err != nil {
		if !errors.Is(err, redis.Nil) {
			s.logger.Warn("appsettings: redis get failed, falling back to db",
				zap.String("key", key), zap.String("error", err.Error()))
		}
		return nil, false
	}
	return raw, true
}

func (s *SettingsStore) writeRedis(ctx context.Context, key string, v json.RawMessage) {
	if s.redis == nil {
		return
	}
	if err := s.redis.Set(ctx, s.redisKey(key), []byte(v), 0).Err(); err != nil {
		s.logger.Warn("appsettings: redis set failed (non-fatal)",
			zap.String("key", key), zap.String("error", err.Error()))
	}
}

func (s *SettingsStore) readDBOrDefault(ctx context.Context, key string, def Definition) json.RawMessage {
	raw, err := s.queries.GetAppSetting(ctx, key)
	if err != nil {
		if !errors.Is(err, pgx.ErrNoRows) {
			s.logger.Warn("appsettings: db read failed, using default",
				zap.String("key", key), zap.String("error", err.Error()))
		}
		return def.Default
	}
	return raw
}

// SeedDefaults 把注册表全部配置项的默认值写入 DB 缺行(启动 seed,用户决策 §11.2)。
//
// 语义:INSERT … ON CONFLICT DO NOTHING——只补缺行,绝不覆盖运维已改过的值;幂等且并发安全,
// gateway 与 admin 启动都调用。seed 后 DB 即完整配置清单(每个注册 key 都有行 + description)。
// 注意:seed 过的行即固化,后续代码默认值升级不会自动改 DB 已有行;面板以 default≠value 提示偏离。
//
// 失败不阻断启动(读侧本就有注册表默认兜底),只记 warn;返回首个错误供调用方观测。
func (s *SettingsStore) SeedDefaults(ctx context.Context) error {
	var firstErr error
	for _, def := range s.registry.List() {
		err := s.queries.SeedAppSetting(ctx, sqlc.SeedAppSettingParams{
			Key:         def.Key,
			Value:       []byte(def.Default),
			Description: def.Description,
		})
		if err != nil {
			s.logger.Warn("appsettings: seed default failed (non-fatal)",
				zap.String("key", def.Key), zap.String("error", err.Error()))
			if firstErr == nil {
				firstErr = err
			}
		}
	}
	return firstErr
}

// Set 校验并写入配置:先写 DB(权威),再刷新 Redis 与本地缓存(跨进程秒级生效)。
func (s *SettingsStore) Set(ctx context.Context, key string, value json.RawMessage) error {
	def, ok := s.registry.Get(key)
	if !ok {
		return fmt.Errorf("appsettings: unknown key %q", key)
	}
	if isRuntimeControlSetting(key) {
		return fmt.Errorf("appsettings: key %q requires durable runtime-control publisher", key)
	}
	if def.Validate != nil {
		if err := def.Validate(value); err != nil {
			return err
		}
	}

	if err := s.queries.UpsertAppSetting(ctx, sqlc.UpsertAppSettingParams{
		Key:         key,
		Value:       []byte(value),
		Description: def.Description,
	}); err != nil {
		return err
	}

	// 变更留痕(用户决策:不建审计表,info 日志即可)。
	s.logger.Info("appsettings: setting updated",
		zap.String("key", key), zap.String("value", string(value)))

	s.writeRedis(ctx, key, value)
	s.writeLocal(key, value)
	return nil
}

// SettingRecord 是 app_settings 的 PostgreSQL 管理事实；关键 P4 设置不得从普通 Redis cache 推断 revision。
type SettingRecord struct {
	Key         string
	Value       json.RawMessage
	Description string
	Revision    int64
}

// Record 强一致读取 PostgreSQL 设置行及 revision。
func (s *SettingsStore) Record(ctx context.Context, key string) (SettingRecord, error) {
	row, err := s.queries.GetAppSettingRecord(ctx, key)
	if err != nil {
		return SettingRecord{}, err
	}
	return SettingRecord{
		Key:         row.Key,
		Value:       json.RawMessage(row.Value),
		Description: row.Description,
		Revision:    row.Revision,
	}, nil
}

// PublishCache 在 durable control 已确认 committed 后刷新普通 settings 镜像与本地缓存。
// 该缓存只服务旧的普通设置读取面，不是五个 P4 关键设置的执行权威。
func (s *SettingsStore) PublishCache(ctx context.Context, key string, value json.RawMessage) {
	s.writeRedis(ctx, key, value)
	s.writeLocal(key, value)
}

// EffectiveView 是某个 key 在「本进程此刻」的生效快照(供可观测接口)。
type EffectiveView struct {
	Key       string
	Value     json.RawMessage
	Source    string // "local" | "redis" | "db" | "default"
	HotReload bool
}

// Effective 绕过本地缓存,报告某 key 此刻从各层看到的生效值与来源,用于验证配置是否已传播到本进程。
func (s *SettingsStore) Effective(ctx context.Context, key string) (EffectiveView, bool) {
	def, ok := s.registry.Get(key)
	if !ok {
		return EffectiveView{}, false
	}
	view := EffectiveView{Key: key, HotReload: def.HotReload}
	if v, ok := s.readRedis(ctx, key); ok {
		view.Value, view.Source = v, "redis"
		return view, true
	}
	raw, err := s.queries.GetAppSetting(ctx, key)
	if err == nil {
		view.Value, view.Source = raw, "db"
		return view, true
	}
	view.Value, view.Source = def.Default, "default"
	return view, true
}
