package sse

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"strconv"

	"github.com/ThankCat/unio-api/internal/platform/failure"
)

const (
	// DefaultMaxEventBytes 是单个 SSE event data 的默认内存上限。
	DefaultMaxEventBytes = 4 * 1024 * 1024

	// DefaultMaxLineBytes 是单行 SSE field 的默认读取上限。
	DefaultMaxLineBytes = DefaultMaxEventBytes
)

var (
	utf8BOM = []byte{0xEF, 0xBB, 0xBF}

	// ErrLineTooLong 表示单行 SSE field 超过配置上限。
	ErrLineTooLong = errors.New("sse: line too long")

	// ErrEventTooLarge 表示单个 SSE event data 超过配置上限。
	ErrEventTooLarge = errors.New("sse: event too large")

	// ErrMalformedStream 表示上游 SSE 流在 event 完整结束前中断或底层读取失败。
	ErrMalformedStream = errors.New("sse: malformed stream")
)

// Config 定义 SSE reader 的内存边界与活动回调。
type Config struct {
	// MaxLineBytes 限制单行 field 的最大字节数，避免异常上游发送无限长行。
	MaxLineBytes int

	// MaxEventBytes 限制单个 event 的 data 聚合大小，避免多行 data 无限增长。
	MaxEventBytes int

	// OnActivity 在每读到一整行（含 event/data/心跳注释行）后被调用，用于驱动上层 idle 看门狗复位。
	// 为 nil 时不回调。心跳注释行（":"）也算活动，避免长任务期间被 idle 误杀。
	OnActivity func()
}

// Event 表示一个已经按 SSE 规则聚合完成的 event。
type Event struct {
	// Type 是 event 字段原始值；为空表示上游未显式设置 event type。
	Type string

	// Data 是多行 data 字段按 SSE 规则用换行符合并后的 payload。
	Data []byte

	// ID 是本 event 显式携带的 id 字段；nil 表示上游未发送 id 字段。
	ID *string

	// RetryMilliseconds 是本 event 显式携带的 retry 字段；nil 表示上游未发送合法 retry。
	RetryMilliseconds *int
}

// Reader 从 io.Reader 中按 SSE event 边界增量读取事件。
type Reader struct {
	reader *bufio.Reader
	config Config

	event Event
	err   error

	eventType string
	eventData []byte
	eventID   *string
	retry     *int
	hasData   bool
	seenLine  bool
}

// NewReader 创建一个 SSE event reader。
func NewReader(r io.Reader, cfg Config) *Reader {
	return &Reader{
		reader: bufio.NewReader(r),
		config: normalizeConfig(cfg),
	}
}

// Next 读取下一个包含 data 字段的完整 SSE event。
func (r *Reader) Next() bool {
	if r.err != nil {
		return false
	}

	for {
		line, terminated, err := r.readLine()
		if err != nil {
			r.err = err
			return false
		}
		if line == nil && !terminated {
			if r.hasData {
				r.err = failure.Wrap(
					failure.CodeSSEMalformedStream,
					ErrMalformedStream,
					failure.WithMessage(ErrMalformedStream.Error()),
				)
			}
			r.resetPendingEvent()
			return false
		}

		dispatched, err := r.processLine(r.normalizeLine(line))
		if err != nil {
			r.err = err
			return false
		}
		if dispatched {
			return true
		}

		if !terminated {
			if r.hasData {
				r.err = failure.Wrap(
					failure.CodeSSEMalformedStream,
					ErrMalformedStream,
					failure.WithMessage(ErrMalformedStream.Error()),
				)
			}
			r.resetPendingEvent()
			return false
		}
	}
}

func (r *Reader) normalizeLine(line []byte) []byte {
	if r.seenLine {
		return line
	}

	r.seenLine = true
	return bytes.TrimPrefix(line, utf8BOM)
}

// Event 返回最近一次 Next 读取到的 SSE event。
func (r *Reader) Event() Event {
	return r.event
}

// Err 返回 reader 遇到的稳定解析错误。
func (r *Reader) Err() error {
	return r.err
}

func normalizeConfig(cfg Config) Config {
	if cfg.MaxEventBytes <= 0 {
		cfg.MaxEventBytes = DefaultMaxEventBytes
	}
	if cfg.MaxLineBytes <= 0 {
		cfg.MaxLineBytes = DefaultMaxLineBytes
	}

	return cfg
}

func (r *Reader) signalActivity() {
	if r.config.OnActivity != nil {
		r.config.OnActivity()
	}
}

func (r *Reader) readLine() ([]byte, bool, error) {
	line := make([]byte, 0, 256)

	for {
		b, err := r.reader.ReadByte()
		if err != nil {
			if errors.Is(err, io.EOF) {
				if len(line) == 0 {
					return nil, false, nil
				}

				r.signalActivity()
				return line, false, nil
			}

			return nil, false, failure.Wrap(
				failure.CodeSSEMalformedStream,
				fmt.Errorf("%w: %v", ErrMalformedStream, err),
				failure.WithMessage(ErrMalformedStream.Error()),
			)
		}

		switch b {
		case '\n':
			r.signalActivity()
			return line, true, nil
		case '\r':
			next, err := r.reader.ReadByte()
			if err == nil {
				if next != '\n' {
					_ = r.reader.UnreadByte()
				}
			} else if !errors.Is(err, io.EOF) {
				return nil, false, failure.Wrap(
					failure.CodeSSEMalformedStream,
					fmt.Errorf("%w: %v", ErrMalformedStream, err),
					failure.WithMessage(ErrMalformedStream.Error()),
				)
			}

			r.signalActivity()
			return line, true, nil
		default:
			if len(line)+1 > r.config.MaxLineBytes {
				return nil, false, failure.Wrap(
					failure.CodeSSELineTooLong,
					ErrLineTooLong,
					failure.WithMessage(ErrLineTooLong.Error()),
				)
			}
			line = append(line, b)
		}
	}
}

func (r *Reader) processLine(line []byte) (bool, error) {
	if len(line) == 0 {
		if !r.hasData {
			r.resetPendingEvent()
			return false, nil
		}

		r.dispatchEvent()
		return true, nil
	}

	if line[0] == ':' {
		return false, nil
	}

	name, value, ok := bytes.Cut(line, []byte(":"))
	if !ok {
		value = nil
	}
	if len(value) > 0 && value[0] == ' ' {
		value = value[1:]
	}

	switch string(name) {
	case "data":
		if len(r.eventData)+len(value)+1 > r.config.MaxEventBytes {
			return false, failure.Wrap(
				failure.CodeSSEEventTooLarge,
				ErrEventTooLarge,
				failure.WithMessage(ErrEventTooLarge.Error()),
			)
		}
		r.eventData = append(r.eventData, value...)
		r.eventData = append(r.eventData, '\n')
		r.hasData = true
	case "event":
		r.eventType = string(value)
	case "id":
		if !bytes.Contains(value, []byte{0}) {
			id := string(value)
			r.eventID = &id
		}
	case "retry":
		if retry, ok := parseRetryMilliseconds(value); ok {
			r.retry = &retry
		}
	}

	return false, nil
}

func (r *Reader) dispatchEvent() {
	data := append([]byte(nil), r.eventData...)
	if len(data) > 0 && data[len(data)-1] == '\n' {
		data = data[:len(data)-1]
	}

	r.event = Event{
		Type:              r.eventType,
		Data:              data,
		ID:                cloneStringPtr(r.eventID),
		RetryMilliseconds: cloneIntPtr(r.retry),
	}
	r.resetPendingEvent()
}

func (r *Reader) resetPendingEvent() {
	r.eventType = ""
	r.eventData = nil
	r.eventID = nil
	r.retry = nil
	r.hasData = false
}

func parseRetryMilliseconds(value []byte) (int, bool) {
	if len(value) == 0 {
		return 0, false
	}
	for _, b := range value {
		if b < '0' || b > '9' {
			return 0, false
		}
	}

	retry, err := strconv.Atoi(string(value))
	if err != nil {
		return 0, false
	}

	return retry, true
}

func cloneStringPtr(value *string) *string {
	if value == nil {
		return nil
	}

	cloned := *value
	return &cloned
}

func cloneIntPtr(value *int) *int {
	if value == nil {
		return nil
	}

	cloned := *value
	return &cloned
}
