package logging

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"gopkg.in/natefinch/lumberjack.v2"

	"github.com/ThankCat/unio-gateway/internal/platform/config"
	"github.com/ThankCat/unio-gateway/internal/platform/failure"
)

const (
	gatewayServerName     = "gateway"
	maxGatewayLogLineSize = 1 << 20
	metaTypeKey           = "_unio_log_type"
	metaEventKey          = "_unio_log_event"
)

// DebugSession 是 Gateway 临时 DEBUG 会话的进程内权威表示。
type DebugSession struct {
	SessionID       string    `json:"session_id"`
	ExpiresAt       time.Time `json:"expires_at"`
	Reason          string    `json:"reason"`
	EnabledByUserID int64     `json:"enabled_by_user_id"`
	Revision        int64     `json:"revision"`
}

// GatewayStatus 是内部运维接口返回的本实例日志状态。
type GatewayStatus struct {
	InstanceID      string     `json:"instance_id"`
	Environment     string     `json:"environment"`
	BaselineLevel   string     `json:"baseline_level"`
	EffectiveLevel  string     `json:"effective_level"`
	DebugSessionID  string     `json:"debug_session_id,omitempty"`
	ExpiresAt       *time.Time `json:"expires_at,omitempty"`
	AppliedRevision int64      `json:"applied_revision"`
}

// GatewayRuntime 持有 gateway-server 的文件 sink、动态级别和本地到期计时器。
type GatewayRuntime struct {
	Logger *zap.Logger

	mu              sync.Mutex
	level           zap.AtomicLevel
	baseline        zapcore.Level
	environment     string
	instanceID      string
	active          *DebugSession
	appliedRevision int64
	expiryTimer     *time.Timer
	rotator         *lumberjack.Logger
	archiver        *gatewayArchiveCompressor
	closed          bool
}

// NewGateway 创建 Gateway 专用日志运行时。文件始终为 JSONL，控制台只由配置决定。
func NewGateway(cfg config.GatewayLogConfig, instanceID string) (*GatewayRuntime, error) {
	if strings.TrimSpace(cfg.FilePath) == "" {
		return nil, failure.New(failure.CodeConfigInvalid, failure.WithMessage("gateway log file path is required"))
	}
	if err := os.MkdirAll(filepath.Dir(cfg.FilePath), 0o755); err != nil {
		return nil, failure.Wrap(failure.CodeConfigInvalid, err, failure.WithMessage("create gateway log directory"))
	}

	rotator := &lumberjack.Logger{
		Filename:   cfg.FilePath,
		MaxSize:    cfg.MaxSizeMB,
		MaxBackups: cfg.MaxBackups,
		MaxAge:     cfg.MaxAgeDays,
		Compress:   false,
		LocalTime:  true,
	}
	level := zap.NewAtomicLevelAt(cfg.BaselineLevel)
	fileSink := zapcore.AddSync(&maxLineWriteSyncer{
		WriteSyncer: zapcore.AddSync(rotator),
		maxBytes:    maxGatewayLogLineSize,
	})
	resolvedInstanceID := resolveInstanceID(instanceID)
	fileCore := newGatewayEnvelopeCore(
		zapcore.NewCore(newGatewayJSONEncoder(), fileSink, level),
		false,
		cfg.Environment,
		resolvedInstanceID,
	)

	cores := []zapcore.Core{fileCore}
	if cfg.ConsoleEnabled {
		consoleCore := newGatewayEnvelopeCore(
			zapcore.NewCore(newGatewayConsoleEncoder(stdoutSupportsColor()), zapcore.Lock(os.Stdout), level),
			true,
			cfg.Environment,
			resolvedInstanceID,
		)
		cores = append(cores, consoleCore)
	}

	logger := zap.New(
		zapcore.NewTee(cores...),
		zap.AddCaller(),
		zap.ErrorOutput(zapcore.Lock(os.Stderr)),
	)
	archiver := newGatewayArchiveCompressor(cfg.FilePath, gatewayArchiveCompressionDelay)
	runtime := &GatewayRuntime{
		Logger:      logger,
		level:       level,
		baseline:    cfg.BaselineLevel,
		environment: cfg.Environment,
		instanceID:  resolvedInstanceID,
		rotator:     rotator,
		archiver:    archiver,
	}
	archiver.Start()
	return runtime, nil
}

// EventFields 将稳定日志分类与 data 字段一起交给 Gateway envelope core。
func EventFields(logType, event string, fields ...zap.Field) []zap.Field {
	out := make([]zap.Field, 0, len(fields)+2)
	out = append(out, zap.String(metaTypeKey, logType), zap.String(metaEventKey, event))
	out = append(out, fields...)
	return out
}

func Debug(logger *zap.Logger, logType, event, message string, fields ...zap.Field) {
	if logger != nil {
		logger.WithOptions(zap.AddCallerSkip(1)).Debug(message, EventFields(logType, event, fields...)...)
	}
}

func Info(logger *zap.Logger, logType, event, message string, fields ...zap.Field) {
	if logger != nil {
		logger.WithOptions(zap.AddCallerSkip(1)).Info(message, EventFields(logType, event, fields...)...)
	}
}

func Warn(logger *zap.Logger, logType, event, message string, fields ...zap.Field) {
	if logger != nil {
		logger.WithOptions(zap.AddCallerSkip(1)).Warn(message, EventFields(logType, event, fields...)...)
	}
}

func Error(logger *zap.Logger, logType, event, message string, fields ...zap.Field) {
	if logger != nil {
		logger.WithOptions(zap.AddCallerSkip(1)).Error(message, EventFields(logType, event, fields...)...)
	}
}

// ApplyDebugSession 热开启 DEBUG，并在 expires_at 到达时由本地 timer 恢复启动基线。
func (r *GatewayRuntime) ApplyDebugSession(session DebugSession, now time.Time) error {
	if r == nil {
		return errors.New("gateway logging runtime is nil")
	}
	if r.baseline == zapcore.DebugLevel {
		return nil
	}
	if err := validateDebugSession(session, now); err != nil {
		return err
	}

	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return errors.New("gateway logging runtime is closed")
	}
	if session.Revision <= r.appliedRevision {
		r.mu.Unlock()
		return nil
	}
	message := "debug logging enabled"
	var previousExpiresAt *time.Time
	if r.active != nil && r.active.SessionID == session.SessionID {
		message = "debug logging extended"
		value := r.active.ExpiresAt
		previousExpiresAt = &value
	}
	if r.expiryTimer != nil {
		r.expiryTimer.Stop()
	}
	copySession := session
	r.active = &copySession
	r.appliedRevision = session.Revision
	r.level.SetLevel(zapcore.DebugLevel)
	delay := time.Until(session.ExpiresAt)
	if !now.Equal(time.Now()) {
		delay = session.ExpiresAt.Sub(now)
	}
	r.expiryTimer = time.AfterFunc(delay, func() { r.expire(session.SessionID, session.Revision) })
	r.mu.Unlock()
	fields := []zap.Field{
		zap.String("debug_session_id", session.SessionID),
		zap.Time("expires_at", session.ExpiresAt),
		zap.Int64("duration_ms", session.ExpiresAt.Sub(now).Milliseconds()),
		zap.String("reason", session.Reason),
		zap.Int64("changed_by_user_id", session.EnabledByUserID),
		zap.Int64("revision", session.Revision),
	}
	if previousExpiresAt != nil {
		fields = append(fields, zap.Time("previous_expires_at", *previousExpiresAt))
	}
	Info(r.Logger, "system", "settings", message, fields...)
	return nil
}

// ClearDebugSession 手动恢复启动基线；旧 revision 不得覆盖较新的已应用状态。
func (r *GatewayRuntime) ClearDebugSession(revision int64, changedByUserID int64) {
	if r == nil {
		return
	}
	r.mu.Lock()
	if revision < r.appliedRevision {
		r.mu.Unlock()
		return
	}
	var sessionID string
	if r.active != nil {
		sessionID = r.active.SessionID
	}
	if r.expiryTimer != nil {
		r.expiryTimer.Stop()
		r.expiryTimer = nil
	}
	r.active = nil
	r.appliedRevision = revision
	r.level.SetLevel(r.baseline)
	r.mu.Unlock()
	if sessionID != "" {
		Info(r.Logger, "system", "settings", "debug logging disabled",
			zap.String("debug_session_id", sessionID),
			zap.String("reason", "manual"),
			zap.Int64("changed_by_user_id", changedByUserID),
			zap.Int64("revision", revision),
		)
	}
}

func (r *GatewayRuntime) expire(sessionID string, revision int64) {
	r.mu.Lock()
	if r.closed || r.active == nil || r.active.SessionID != sessionID || r.appliedRevision != revision {
		r.mu.Unlock()
		return
	}
	expiresAt := r.active.ExpiresAt
	r.active = nil
	r.expiryTimer = nil
	r.level.SetLevel(r.baseline)
	r.mu.Unlock()
	Info(r.Logger, "system", "settings", "debug logging expired",
		zap.String("debug_session_id", sessionID),
		zap.Time("expires_at", expiresAt),
		zap.Int64("revision", revision),
		zap.String("reason", "automatic"),
	)
}

func (r *GatewayRuntime) Status() GatewayStatus {
	if r == nil {
		return GatewayStatus{}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	status := GatewayStatus{
		InstanceID:      r.instanceID,
		Environment:     r.environment,
		BaselineLevel:   gatewayLevelString(r.baseline),
		EffectiveLevel:  gatewayLevelString(r.level.Level()),
		AppliedRevision: r.appliedRevision,
	}
	if r.active != nil {
		status.DebugSessionID = r.active.SessionID
		expiresAt := r.active.ExpiresAt
		status.ExpiresAt = &expiresAt
	}
	return status
}

func (r *GatewayRuntime) Close() error {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return nil
	}
	r.closed = true
	if r.expiryTimer != nil {
		r.expiryTimer.Stop()
	}
	r.mu.Unlock()

	syncErr := r.Logger.Sync()
	closeErr := r.rotator.Close()
	archiveErr := r.archiver.Close()
	return errors.Join(ignoreInvalidSync(syncErr), closeErr, archiveErr)
}

func validateDebugSession(session DebugSession, now time.Time) error {
	if strings.TrimSpace(session.SessionID) == "" {
		return errors.New("debug session_id is required")
	}
	if strings.TrimSpace(session.Reason) == "" || utf8.RuneCountInString(session.Reason) > 200 {
		return errors.New("debug reason must contain 1 to 200 characters")
	}
	if strings.IndexFunc(session.Reason, func(r rune) bool { return r < 0x20 || r == 0x7f }) >= 0 {
		return errors.New("debug reason contains control characters")
	}
	duration := session.ExpiresAt.Sub(now)
	if duration <= 0 || duration > 60*time.Minute {
		return errors.New("debug session duration must be greater than zero and at most 60 minutes")
	}
	return nil
}

type gatewayEnvelopeCore struct {
	inner       zapcore.Core
	fields      []zap.Field
	console     bool
	environment string
	instance    string
}

func newGatewayEnvelopeCore(inner zapcore.Core, console bool, environment, instance string) zapcore.Core {
	return &gatewayEnvelopeCore{
		inner: inner, console: console, environment: environment, instance: instance,
	}
}

func (c *gatewayEnvelopeCore) Enabled(level zapcore.Level) bool { return c.inner.Enabled(level) }

func (c *gatewayEnvelopeCore) With(fields []zap.Field) zapcore.Core {
	clone := *c
	clone.fields = append(append([]zap.Field(nil), c.fields...), fields...)
	return &clone
}

func (c *gatewayEnvelopeCore) Check(entry zapcore.Entry, checked *zapcore.CheckedEntry) *zapcore.CheckedEntry {
	if c.Enabled(entry.Level) {
		return checked.AddCore(entry, c)
	}
	return checked
}

func (c *gatewayEnvelopeCore) Write(entry zapcore.Entry, fields []zap.Field) error {
	all := make([]zap.Field, 0, len(c.fields)+len(fields))
	all = append(all, c.fields...)
	all = append(all, fields...)
	logType, event, data := splitGatewayFields(all)
	if c.console {
		entry.Message = logType + "/" + event + " | " + entry.Message
		return c.inner.Write(entry, []zap.Field{zap.Object("data", zapFieldsObject(data))})
	}
	return c.inner.Write(entry, []zap.Field{
		zap.String("server", gatewayServerName),
		zap.String("environment", c.environment),
		zap.String("instance", c.instance),
		zap.String("type", logType),
		zap.String("event", event),
		zap.Object("data", zapFieldsObject(data)),
	})
}

func (c *gatewayEnvelopeCore) Sync() error { return c.inner.Sync() }

func splitGatewayFields(fields []zap.Field) (string, string, []zap.Field) {
	logType, event := "system", "service"
	data := make([]zap.Field, 0, len(fields))
	for _, field := range fields {
		switch field.Key {
		case metaTypeKey:
			logType = field.String
		case metaEventKey:
			event = field.String
		default:
			data = append(data, field)
		}
	}
	return logType, event, dedupeGatewayDataFields(data)
}

// dedupeGatewayDataFields prevents duplicate JSON object keys when request context and
// event-specific fields describe the same fact. The later, more specific value wins.
func dedupeGatewayDataFields(fields []zap.Field) []zap.Field {
	seen := make(map[string]struct{}, len(fields))
	deduped := make([]zap.Field, 0, len(fields))
	for index := len(fields) - 1; index >= 0; index-- {
		if _, exists := seen[fields[index].Key]; exists {
			continue
		}
		seen[fields[index].Key] = struct{}{}
		deduped = append(deduped, fields[index])
	}
	for left, right := 0, len(deduped)-1; left < right; left, right = left+1, right-1 {
		deduped[left], deduped[right] = deduped[right], deduped[left]
	}
	return deduped
}

type zapFieldsObject []zap.Field

func (f zapFieldsObject) MarshalLogObject(enc zapcore.ObjectEncoder) error {
	for i := range f {
		f[i].AddTo(enc)
	}
	return nil
}

func newGatewayJSONEncoder() zapcore.Encoder {
	return zapcore.NewJSONEncoder(zapcore.EncoderConfig{
		TimeKey:        "timestamp",
		LevelKey:       "level",
		MessageKey:     "message",
		EncodeLevel:    encodeGatewayLevel,
		EncodeTime:     encodeGatewayTime,
		EncodeDuration: zapcore.MillisDurationEncoder,
	})
}

func newGatewayConsoleEncoder(color bool) zapcore.Encoder {
	levelEncoder := zapcore.CapitalLevelEncoder
	if color {
		levelEncoder = zapcore.CapitalColorLevelEncoder
	}
	return zapcore.NewConsoleEncoder(zapcore.EncoderConfig{
		TimeKey:          "timestamp",
		LevelKey:         "level",
		NameKey:          "logger",
		CallerKey:        "caller",
		MessageKey:       "message",
		LineEnding:       zapcore.DefaultLineEnding,
		EncodeLevel:      levelEncoder,
		EncodeTime:       encodeGatewayTime,
		EncodeDuration:   zapcore.MillisDurationEncoder,
		EncodeCaller:     zapcore.ShortCallerEncoder,
		ConsoleSeparator: " | ",
	})
}

func encodeGatewayLevel(level zapcore.Level, enc zapcore.PrimitiveArrayEncoder) {
	enc.AppendString(gatewayLevelString(level))
}

func gatewayLevelString(level zapcore.Level) string {
	if level == zapcore.WarnLevel {
		return "warning"
	}
	return level.String()
}

func encodeGatewayTime(value time.Time, enc zapcore.PrimitiveArrayEncoder) {
	enc.AppendString(value.Format("2006-01-02T15:04:05.000Z07:00"))
}

func stdoutSupportsColor() bool {
	info, err := os.Stdout.Stat()
	return err == nil && info.Mode()&os.ModeCharDevice != 0
}

func resolveInstanceID(configured string) string {
	if value := strings.TrimSpace(configured); value != "" {
		return value
	}
	if hostname, err := os.Hostname(); err == nil && hostname != "" {
		return hostname
	}
	return "unknown"
}

type maxLineWriteSyncer struct {
	zapcore.WriteSyncer
	maxBytes int
}

func (w *maxLineWriteSyncer) Write(line []byte) (int, error) {
	originalSize := len(line)
	if originalSize <= w.maxBytes {
		return w.WriteSyncer.Write(line)
	}

	var record map[string]any
	if err := json.Unmarshal(line, &record); err != nil {
		return 0, fmt.Errorf("gateway log record exceeds %d bytes and cannot be truncated: %w", w.maxBytes, err)
	}
	truncated := map[string]any{
		"record_truncated": true,
		"original_bytes":   originalSize,
	}
	if data, ok := record["data"].(map[string]any); ok {
		for _, key := range []string{"trace_id", "request_id", "attempt_id", "upstream_request_id"} {
			if value, exists := data[key]; exists {
				truncated[key] = value
			}
		}
	}
	record["data"] = truncated
	encoded, err := json.Marshal(record)
	if err != nil {
		return 0, err
	}
	encoded = append(encoded, '\n')
	if len(encoded) > w.maxBytes {
		return 0, fmt.Errorf("truncated gateway log record still exceeds %d bytes", w.maxBytes)
	}
	if _, err := w.WriteSyncer.Write(encoded); err != nil {
		return 0, err
	}
	return originalSize, nil
}

func ignoreInvalidSync(err error) error {
	if errors.Is(err, os.ErrInvalid) {
		return nil
	}
	return err
}
