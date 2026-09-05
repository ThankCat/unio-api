package httpx

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ThankCat/unio-gateway/internal/platform/failure"
)

// SSEWriterConfig 控制 SSE 写出的内存与行为边界。
type SSEWriterConfig struct {
	// MaxEventBytes 限制单个 event data 的最大字节数，与 sse.Reader 对称，0 表示不限制
	MaxEventBytes int
	// WriteTimeout 是每个 event 写出前刷新的滑动 deadline；0 使用进程级默认。
	WriteTimeout time.Duration
}

// SSEEvent 是 HTTP 层写出的一个 SSE event；形状与 sse.Event 对称但独立，避免 platform 依赖 core。
type SSEEvent struct {
	Type              string  // event 字段；空表示不写 event 行
	Data              []byte  // data payload；含 \n 时按 SSE 规则拆成多行 data:
	ID                *string // id 字段；nil 不写
	RetryMilliseconds *int    // retry 字段；nil 不写
}

// sseKeepaliveIntervalNanos 是进程级默认的下游保活间隔（纳秒），由 settings applier 热更新；
// 0 表示关闭。对齐 Sub2API stream_keepalive_interval（默认 10s）。
var sseKeepaliveIntervalNanos atomic.Int64

// DefaultSSEKeepaliveInterval 与 Sub2API 默认一致。
const DefaultSSEKeepaliveInterval = 10 * time.Second

// SetSSEKeepaliveInterval 设置进程级下游保活间隔；d<=0 关闭。
func SetSSEKeepaliveInterval(d time.Duration) {
	if d < 0 {
		d = 0
	}
	sseKeepaliveIntervalNanos.Store(int64(d))
}

// SSEKeepaliveInterval 返回当前生效的下游保活间隔（0 = 关闭）。
func SSEKeepaliveInterval() time.Duration {
	return time.Duration(sseKeepaliveIntervalNanos.Load())
}

// SSEWriter 把一个支持 flush 的 ResponseWriter 封装成 per-request 的 SSE 写出器。
//
// 写出串行化（mu）：保活 goroutine 与业务事件写出可能并发，SSE 帧不能交错。
type SSEWriter struct {
	ctx     context.Context
	w       http.ResponseWriter
	flusher http.Flusher
	cfg     SSEWriterConfig

	mu          sync.Mutex
	started     bool      // 是否已写出 header（首个 event 或保活注释）
	err         error     // sticky 写出错误，一旦失败后续写出短路
	lastWriteAt time.Time // 最近一次成功写出（event 或注释）的时间，保活据此判定空闲
}

func NewSSEWriter(ctx context.Context, w http.ResponseWriter, cfg SSEWriterConfig) (*SSEWriter, error) {
	if _, ok := w.(http.Flusher); !ok {
		return nil, failure.Wrap(
			failure.CodeHTTPStreamingUnsupported,
			ErrStreamingUnsupported,
			failure.WithMessage(ErrStreamingUnsupported.Error()),
		)
	}

	return &SSEWriter{
		ctx:         ctx,
		w:           w,
		flusher:     w.(http.Flusher),
		cfg:         cfg,
		started:     false,
		err:         nil,
		lastWriteAt: time.Now(),
	}, nil
}

// RunKeepalive 启动下游保活：下游超过 interval 没有任何写出时补一帧 SSE 注释（": \n\n"），
// 防止代理/客户端空闲断开。首字之前也发——上游长思考期间客户端只会收到这些注释。
// interval<=0 不启动。返回的 stop 必须在流结束时调用（可重复调用）。
//
// 保活注释会提交 200 + SSE 响应头（Started 随之为 true），但它不是模型输出：
// lifecycle 的 emitted / 首字 / 结算事实均不因它改变，首字前仍可静默换渠道。
func (s *SSEWriter) RunKeepalive(interval time.Duration) (stop func()) {
	if interval <= 0 {
		return func() {}
	}
	done := make(chan struct{})
	var once sync.Once
	go func() {
		// 检查粒度取间隔的一半，保证空闲判定误差不超过半个间隔。
		ticker := time.NewTicker(interval / 2)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-s.ctx.Done():
				return
			case <-ticker.C:
				s.mu.Lock()
				idle := time.Since(s.lastWriteAt) >= interval && s.err == nil
				s.mu.Unlock()
				if idle {
					_ = s.WriteComment("")
				}
			}
		}
	}()
	return func() { once.Do(func() { close(done) }) }
}

// WriteEvent 写出一个完整 event：检查 ctx → 装 header → 写各字段行 → 空行 → flush。
func (s *SSEWriter) WriteEvent(ev SSEEvent) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.guard(); err != nil {
		return err
	}

	// 单 event data 体积上限，避免异常调用方一次写爆内存（与 Reader 对称）。
	if s.cfg.MaxEventBytes > 0 && len(ev.Data) > s.cfg.MaxEventBytes {
		return failure.Wrap(
			failure.CodeSSEEventTooLarge,
			errors.New("sse event too large"),
			failure.WithMessage("sse event too large"),
		)
	}

	s.ensureStarted()

	if ev.Type != "" {
		if err := s.writeRaw("event: " + ev.Type + "\n"); err != nil {
			return err
		}
	}

	if ev.ID != nil {
		if err := s.writeRaw("id: " + *ev.ID + "\n"); err != nil {
			return err
		}
	}

	if ev.RetryMilliseconds != nil {
		if err := s.writeRaw("retry: " + strconv.Itoa(*ev.RetryMilliseconds) + "\n"); err != nil {
			return err
		}
	}

	for _, line := range splitSSEDataLines(ev.Data) {
		if err := s.writeRaw("data: " + line + "\n"); err != nil {
			return err
		}
	}

	// event 以空行结束。
	if err := s.writeRaw("\n"); err != nil {
		return err
	}

	s.flusher.Flush()
	s.lastWriteAt = time.Now()

	return nil
}

// WriteData 是 OpenAI-compatible data-only 便捷写法。
func (s *SSEWriter) WriteData(data []byte) error {
	return s.WriteEvent(SSEEvent{Data: data})
}

// WriteComment 写出 SSE comment 行用于 heartbeat 保活。
func (s *SSEWriter) WriteComment(text string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.guard(); err != nil {
		return err
	}

	s.ensureStarted()

	if err := s.writeRaw(": " + text + "\n\n"); err != nil {
		return err
	}

	s.flusher.Flush()
	s.lastWriteAt = time.Now()
	return nil
}

// Started 返回是否已提交 SSE 响应头（首个 event 或保活注释）；为 true 后不能再改写成 JSON 错误响应。
func (s *SSEWriter) Started() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.started
}

// guard 在每次写出前检查 sticky error 和客户端是否已断开。
func (s *SSEWriter) guard() error {
	if s.err != nil {
		return s.err
	}

	if err := s.ctx.Err(); err != nil {
		// 客户端断开/请求取消：记成 sticky error，后续写出直接短路。
		s.err = failure.Wrap(
			failure.CodeHTTPClientDisconnected,
			err,
			failure.WithMessage("client disconnected"))

		return s.err
	}
	if err := RefreshResponseWriteDeadline(s.w, s.cfg.WriteTimeout); err != nil {
		s.err = failure.Wrap(
			failure.CodeHTTPResponseWriteFailed,
			err,
			failure.WithMessage("set sse write deadline"),
		)
		return s.err
	}

	return nil
}

// Err 返回 sticky 写出错误。
func (s *SSEWriter) Err() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.err
}

// ensureStarted 在首个 event 写出前安装 SSE header（只装一次）。
func (s *SSEWriter) ensureStarted() {
	if s.started {
		return
	}

	h := s.w.Header()
	h.Set("Content-Type", ContentTypeSSE)
	h.Set("Cache-Control", "no-cache")
	h.Set("X-Accel-Buffering", "no")
	s.w.WriteHeader(http.StatusOK)

	s.started = true
}

// writeRaw 写一段字符串，失败时记录 sticky error 并返回稳定错误。
func (s *SSEWriter) writeRaw(text string) error {
	if _, err := io.WriteString(s.w, text); err != nil {
		s.err = failure.Wrap(
			failure.CodeHTTPResponseWriteFailed,
			err,
			failure.WithMessage("write sse"),
		)

		return s.err
	}

	return nil
}

// splitSSEDataLines 把 data 按换行拆成多行 data 内容；空 data 也返回一行（空字符串）。
func splitSSEDataLines(data []byte) []string {
	if len(data) == 0 {
		return []string{""}
	}

	normalized := strings.ReplaceAll(string(data), "\r\n", "\n")
	normalized = strings.ReplaceAll(normalized, "\r", "\n")

	return strings.Split(normalized, "\n")
}
