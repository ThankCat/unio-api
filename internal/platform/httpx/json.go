package httpx

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net"
	"net/http"
	"strings"
	"sync/atomic"

	"github.com/ThankCat/unio-gateway/internal/platform/failure"
)

const (
	// DefaultMaxJSONBodyBytes 是默认 JSON 请求体最大字节数（运行期未配置时的兜底值）。
	DefaultMaxJSONBodyBytes int64 = 256 << 20
	// DefaultTextMaxJSONBodyBytes 是纯文本 Gateway 路由的默认 JSON 请求体上限。
	DefaultTextMaxJSONBodyBytes int64 = 32 << 20
)

// maxJSONBodyBytes 是运行期可配置的 JSON body 上限（字节）；0 表示回退 DefaultMaxJSONBodyBytes。
//
// 由进程启动期 SetMaxJSONBodyBytes 设置一次，gateway / admin server 分别读取自己的 ingress 上限。
// 用 atomic 仅为读写竞态安全；预期 serve 前设置、serve 中只读。
var (
	maxJSONBodyBytes     atomic.Int64
	textMaxJSONBodyBytes atomic.Int64
)

// SetMaxJSONBodyBytes 设置全局 JSON 请求体上限（字节）。n<=0 时回退内置默认值。
func SetMaxJSONBodyBytes(n int64) {
	if n <= 0 {
		maxJSONBodyBytes.Store(0)
		return
	}
	maxJSONBodyBytes.Store(n)
}

// MaxJSONBodyBytes 返回当前生效的 JSON 请求体上限（字节）；未配置时返回 DefaultMaxJSONBodyBytes。
func MaxJSONBodyBytes() int64 {
	if n := maxJSONBodyBytes.Load(); n > 0 {
		return n
	}
	return DefaultMaxJSONBodyBytes
}

// SetTextMaxJSONBodyBytes 设置纯文本 Gateway 路由的 JSON 请求体上限。
func SetTextMaxJSONBodyBytes(n int64) {
	if n <= 0 {
		textMaxJSONBodyBytes.Store(0)
		return
	}
	textMaxJSONBodyBytes.Store(n)
}

// TextMaxJSONBodyBytes 返回纯文本 Gateway 路由当前生效的请求体上限。
func TextMaxJSONBodyBytes() int64 {
	if n := textMaxJSONBodyBytes.Load(); n > 0 {
		return n
	}
	return DefaultTextMaxJSONBodyBytes
}

var (
	// ErrRequestBodyTooLarge 表示 JSON 请求体超过允许大小。
	ErrRequestBodyTooLarge = errors.New("request body too large")

	// ErrUnsupportedContentType 表示请求 Content-Type 不是 JSON。
	ErrUnsupportedContentType = errors.New("unsupported content type")

	// ErrEmptyJSONBody 表示请求体为空。
	ErrEmptyJSONBody = errors.New("empty json body")

	// ErrTrailingJSONToken 表示一个 JSON body 后面还有额外 token。
	ErrTrailingJSONToken = errors.New("trailing json token")

	// ErrRequestBodyTimeout 表示请求体读取期间超过了空闲读取期限。
	ErrRequestBodyTimeout = errors.New("request body read timeout")

	// ErrRequestBodyIncomplete 表示客户端没有发送完整请求体。
	ErrRequestBodyIncomplete = errors.New("request body incomplete")

	// ErrClientDisconnected 表示客户端在请求体读取期间主动断开连接。
	ErrClientDisconnected = errors.New("client disconnected")
)

// RequestBodyDiagnostic 是请求体读取/JSON 解码失败时可安全写入日志的结构化诊断。
// 它不包含请求正文、字段值或原始 decoder 错误文本。
type RequestBodyDiagnostic struct {
	Reason        string
	Kind          string
	Field         string
	Offset        int64
	BytesRead     int64
	ContentLength int64
	Limit         int64
}

type bodyDecodeError struct {
	cause      error
	diagnostic RequestBodyDiagnostic
	message    string
}

func (e *bodyDecodeError) Error() string { return e.message }
func (e *bodyDecodeError) Unwrap() error { return e.cause }

// InvalidJSONDiagnostic 是旧调用方使用的 invalid JSON 诊断类型别名。
type InvalidJSONDiagnostic = RequestBodyDiagnostic

// RequestBodyDiagnosticOf 从 DecodeJSON 错误链中读取脱敏诊断。
func RequestBodyDiagnosticOf(err error) (RequestBodyDiagnostic, bool) {
	var decodeErr *bodyDecodeError
	if !errors.As(err, &decodeErr) {
		return RequestBodyDiagnostic{}, false
	}
	return decodeErr.diagnostic, true
}

// InvalidJSONDiagnosticOf 从 DecodeJSON 错误链中读取脱敏诊断。
func InvalidJSONDiagnosticOf(err error) (InvalidJSONDiagnostic, bool) {
	diagnostic, ok := RequestBodyDiagnosticOf(err)
	if !ok || diagnostic.Reason != "invalid_json" {
		return InvalidJSONDiagnostic{}, false
	}
	return diagnostic, true
}

type countingReader struct {
	reader io.Reader
	read   int64
	err    error
}

func (r *countingReader) Read(p []byte) (int, error) {
	n, err := r.reader.Read(p)
	r.read += int64(n)
	if err != nil {
		r.err = err
	}
	return n, err
}

// DecodeJSON 从 HTTP 请求体读取 JSON，并解码到 dst。
func DecodeJSON(w http.ResponseWriter, r *http.Request, dst any) error {
	return DecodeJSONWithLimit(w, r, dst, MaxJSONBodyBytes())
}

// DecodeTextJSON 使用纯文本 Gateway 路由的独立请求体上限解码 JSON。
func DecodeTextJSON(w http.ResponseWriter, r *http.Request, dst any) error {
	return DecodeJSONWithLimit(w, r, dst, TextMaxJSONBodyBytes())
}

// DecodeJSONWithLimit 使用显式请求体上限读取并解码 JSON。
// 纯文本接口可使用较小的路由级上限，多模态接口使用 Gateway 的绝对上限。
func DecodeJSONWithLimit(w http.ResponseWriter, r *http.Request, dst any, maxBytes int64) error {
	if !isJSONContentType(r.Header.Get("Content-Type")) {
		return failure.Wrap(
			failure.CodeHTTPUnsupportedContentType,
			ErrUnsupportedContentType,
			failure.WithMessage("content type must be application/json"),
		)
	}
	if maxBytes <= 0 {
		maxBytes = MaxJSONBodyBytes()
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxBytes)

	countedBody := &countingReader{reader: r.Body}
	decoder := json.NewDecoder(countedBody)

	if err := decoder.Decode(dst); err != nil {
		return normalizeJSONDecodeError(err, countedBody.read, r.ContentLength, countedBody.err, r.Context().Err(), maxBytes)
	}

	var trailing any
	if err := decoder.Decode(&trailing); err != nil {
		if errors.Is(err, io.EOF) {
			return nil
		}

		var maxBytesErr *http.MaxBytesError
		if errors.As(err, &maxBytesErr) {
			return requestBodyFailure(
				failure.CodeHTTPRequestBodyTooLarge,
				ErrRequestBodyTooLarge,
				"request_body_too_large",
				"body_too_large",
				"request body too large",
				maxBytesErr.Limit,
				countedBody.read,
				r.ContentLength,
				maxBytesErr,
			)
		}
		if readFailure := normalizeBodyReadFailure(err, countedBody.read, r.ContentLength, countedBody.err, r.Context().Err(), maxBytes); readFailure != nil {
			return readFailure
		}

		return failure.Wrap(
			failure.CodeHTTPTrailingJSONToken,
			ErrTrailingJSONToken,
			failure.WithMessage("request body must contain a single JSON object"),
		)
	}

	return failure.Wrap(
		failure.CodeHTTPTrailingJSONToken,
		ErrTrailingJSONToken,
		failure.WithMessage("request body must contain a single JSON object"),
	)
}

// normalizeJSONDecodeError 将底层 JSON decode 错误收敛成 HTTP 层可稳定识别的错误。
func normalizeJSONDecodeError(err error, bytesRead int64, contentLength int64, readerErr error, contextErr error, maxBytes int64) error {
	var maxBytesErr *http.MaxBytesError
	if errors.As(err, &maxBytesErr) {
		return requestBodyFailure(
			failure.CodeHTTPRequestBodyTooLarge,
			ErrRequestBodyTooLarge,
			"request_body_too_large",
			"body_too_large",
			"request body too large",
			maxBytesErr.Limit,
			bytesRead,
			contentLength,
			maxBytesErr,
		)
	}

	if readFailure := normalizeBodyReadFailure(err, bytesRead, contentLength, readerErr, contextErr, maxBytes); readFailure != nil {
		return readFailure
	}

	if errors.Is(err, io.EOF) {
		return failure.Wrap(
			failure.CodeHTTPEmptyJSONBody,
			ErrEmptyJSONBody,
			failure.WithMessage("request body is required"),
		)
	}

	diagnostic := RequestBodyDiagnostic{
		Reason:        "invalid_json",
		Kind:          "decode_error",
		BytesRead:     bytesRead,
		ContentLength: contentLength,
		Limit:         maxBytes,
	}
	var syntaxErr *json.SyntaxError
	var typeErr *json.UnmarshalTypeError
	switch {
	case errors.Is(err, io.ErrUnexpectedEOF):
		diagnostic.Kind = "unexpected_eof"
	case errors.As(err, &syntaxErr):
		diagnostic.Kind = "syntax"
		diagnostic.Offset = syntaxErr.Offset
	case errors.As(err, &typeErr):
		diagnostic.Kind = "type_mismatch"
		diagnostic.Field = typeErr.Field
		diagnostic.Offset = typeErr.Offset
	}

	return failure.Wrap(
		failure.CodeHTTPInvalidJSONBody,
		&bodyDecodeError{cause: err, diagnostic: diagnostic, message: "invalid json body"},
		failure.WithMessage("invalid json body"),
	)
}

func normalizeBodyReadFailure(err error, bytesRead int64, contentLength int64, readerErr error, contextErr error, maxBytes int64) error {
	var netErr net.Error
	if (errors.As(readerErr, &netErr) || errors.As(err, &netErr)) && netErr.Timeout() {
		return requestBodyFailure(
			failure.CodeHTTPRequestBodyTimeout,
			ErrRequestBodyTimeout,
			"request_body_timeout",
			"read_timeout",
			"request body read timed out",
			maxBytes,
			bytesRead,
			contentLength,
			readerErr,
		)
	}

	if errors.Is(readerErr, context.Canceled) || errors.Is(err, context.Canceled) ||
		errors.Is(readerErr, net.ErrClosed) || errors.Is(err, net.ErrClosed) ||
		errors.Is(contextErr, context.Canceled) {
		return requestBodyFailure(
			failure.CodeHTTPClientDisconnected,
			ErrClientDisconnected,
			"client_disconnected",
			"client_disconnected",
			"client disconnected while reading request body",
			maxBytes,
			bytesRead,
			contentLength,
			readerErr,
		)
	}

	if errors.Is(readerErr, io.ErrUnexpectedEOF) ||
		(contentLength >= 0 && bytesRead < contentLength && errors.Is(err, io.ErrUnexpectedEOF)) ||
		(readerErr != nil && !errors.Is(readerErr, io.EOF)) {
		return requestBodyFailure(
			failure.CodeHTTPRequestBodyIncomplete,
			ErrRequestBodyIncomplete,
			"request_body_incomplete",
			"incomplete_body",
			"request body is incomplete",
			maxBytes,
			bytesRead,
			contentLength,
			readerErr,
		)
	}

	return nil
}

func requestBodyFailure(code failure.Code, sentinel error, reason string, kind string, message string, limit int64, bytesRead int64, contentLength int64, cause error) error {
	if sentinel != nil && cause != nil {
		cause = errors.Join(sentinel, cause)
	} else if sentinel != nil {
		cause = sentinel
	}
	return failure.Wrap(
		code,
		&bodyDecodeError{
			cause: cause,
			diagnostic: RequestBodyDiagnostic{
				Reason:        reason,
				Kind:          kind,
				BytesRead:     bytesRead,
				ContentLength: contentLength,
				Limit:         limit,
			},
			message: message,
		},
		failure.WithMessage(message),
	)
}

// isJSONContentType 判断 contentType 是否是 "application/json" 类型。
func isJSONContentType(contentType string) bool {
	if contentType == "" {
		return true
	}

	mediaType, _, err := mime.ParseMediaType(contentType)
	if err != nil {
		return false
	}

	return strings.EqualFold(mediaType, "application/json")
}
