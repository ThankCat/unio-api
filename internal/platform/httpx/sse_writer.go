package httpx

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/ThankCat/unio-gateway/internal/platform/failure"
)

// SSEWriterConfig 控制 SSE 写出的内存与行为边界。
type SSEWriterConfig struct {
	// MaxEventBytes 限制单个 event data 的最大字节数，与 sse.Reader 对称，0 表示不限制
	MaxEventBytes int
}

// SSEEvent 是 HTTP 层写出的一个 SSE event；形状与 sse.Event 对称但独立，避免 platform 依赖 core。
type SSEEvent struct {
	Type              string  // event 字段；空表示不写 event 行
	Data              []byte  // data payload；含 \n 时按 SSE 规则拆成多行 data:
	ID                *string // id 字段；nil 不写
	RetryMilliseconds *int    // retry 字段；nil 不写
}

// SSEWriter 把一个支持 flush 的 ResponseWriter 封装成 per-request 的 SSE 写出器。
type SSEWriter struct {
	ctx     context.Context
	w       http.ResponseWriter
	flusher http.Flusher
	cfg     SSEWriterConfig
	started bool  // 是否已写出 header + 首个 event
	err     error // sticky 写出错误，一旦失败后续写出短路
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
		ctx:     ctx,
		w:       w,
		flusher: w.(http.Flusher),
		cfg:     cfg,
		started: false,
		err:     nil,
	}, nil
}

// WriteEvent 写出一个完整 event：检查 ctx → 装 header → 写各字段行 → 空行 → flush。
func (s *SSEWriter) WriteEvent(ev SSEEvent) error {
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

	return nil
}

// WriteData 是 OpenAI-compatible data-only 便捷写法。
func (s *SSEWriter) WriteData(data []byte) error {
	return s.WriteEvent(SSEEvent{Data: data})
}

// WriteComment 写出 SSE comment 行用于 heartbeat 保活。
func (s *SSEWriter) WriteComment(text string) error {
	if err := s.guard(); err != nil {
		return err
	}

	s.ensureStarted()

	if err := s.writeRaw(": " + text + "\n\n"); err != nil {
		return err
	}

	s.flusher.Flush()
	return nil
}

// Started 返回是否已写出首个 event。
func (s *SSEWriter) Started() bool {
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

	return nil
}

// Err 返回 sticky 写出错误。
func (s *SSEWriter) Err() error {
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
