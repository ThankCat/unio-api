package httpx

import (
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"strings"
	"sync/atomic"

	"github.com/ThankCat/unio-gateway/internal/platform/failure"
)

const (
	// DefaultMaxJSONBodyBytes 是默认 JSON 请求体最大字节数（运行期未配置时的兜底值）。
	DefaultMaxJSONBodyBytes int64 = 1 << 20
)

// maxJSONBodyBytes 是运行期可配置的 JSON body 上限（字节）；0 表示回退 DefaultMaxJSONBodyBytes。
//
// 由进程启动期 SetMaxJSONBodyBytes 设置一次，gateway / admin server 分别读取自己的 ingress 上限。
// 用 atomic 仅为读写竞态安全；预期 serve 前设置、serve 中只读。
var maxJSONBodyBytes atomic.Int64

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

var (
	// ErrRequestBodyTooLarge 表示 JSON 请求体超过允许大小。
	ErrRequestBodyTooLarge = errors.New("request body too large")

	// ErrUnsupportedContentType 表示请求 Content-Type 不是 JSON。
	ErrUnsupportedContentType = errors.New("unsupported content type")

	// ErrEmptyJSONBody 表示请求体为空。
	ErrEmptyJSONBody = errors.New("empty json body")

	// ErrTrailingJSONToken 表示一个 JSON body 后面还有额外 token。
	ErrTrailingJSONToken = errors.New("trailing json token")
)

// InvalidJSONDiagnostic 是 JSON 请求体解码失败时可安全写入日志的结构化诊断。
// 它不包含请求正文、字段值或原始 decoder 错误文本。
type InvalidJSONDiagnostic struct {
	Kind      string
	Field     string
	Offset    int64
	BytesRead int64
}

type invalidJSONError struct {
	cause      error
	diagnostic InvalidJSONDiagnostic
}

func (e *invalidJSONError) Error() string { return "invalid json body" }
func (e *invalidJSONError) Unwrap() error { return e.cause }

// InvalidJSONDiagnosticOf 从 DecodeJSON 错误链中读取脱敏诊断。
func InvalidJSONDiagnosticOf(err error) (InvalidJSONDiagnostic, bool) {
	var decodeErr *invalidJSONError
	if !errors.As(err, &decodeErr) {
		return InvalidJSONDiagnostic{}, false
	}
	return decodeErr.diagnostic, true
}

type countingReader struct {
	reader io.Reader
	read   int64
}

func (r *countingReader) Read(p []byte) (int, error) {
	n, err := r.reader.Read(p)
	r.read += int64(n)
	return n, err
}

// DecodeJSON 从 HTTP 请求体读取 JSON，并解码到 dst。
func DecodeJSON(w http.ResponseWriter, r *http.Request, dst any) error {
	if !isJSONContentType(r.Header.Get("Content-Type")) {
		return failure.Wrap(
			failure.CodeHTTPUnsupportedContentType,
			ErrUnsupportedContentType,
			failure.WithMessage("content type must be application/json"),
		)
	}

	r.Body = http.MaxBytesReader(w, r.Body, MaxJSONBodyBytes())

	countedBody := &countingReader{reader: r.Body}
	decoder := json.NewDecoder(countedBody)

	if err := decoder.Decode(dst); err != nil {
		return normalizeJSONDecodeError(err, countedBody.read)
	}

	var trailing any
	if err := decoder.Decode(&trailing); err != nil {
		if errors.Is(err, io.EOF) {
			return nil
		}

		var maxBytes *http.MaxBytesError
		if errors.As(err, &maxBytes) {
			return failure.Wrap(
				failure.CodeHTTPRequestBodyTooLarge,
				ErrRequestBodyTooLarge,
				failure.WithMessage("request body too large"),
			)
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
func normalizeJSONDecodeError(err error, bytesRead int64) error {
	var maxBytesErr *http.MaxBytesError
	if errors.As(err, &maxBytesErr) {
		return failure.Wrap(
			failure.CodeHTTPRequestBodyTooLarge,
			ErrRequestBodyTooLarge,
			failure.WithMessage("request body too large"),
		)
	}

	if errors.Is(err, io.EOF) {
		return failure.Wrap(
			failure.CodeHTTPEmptyJSONBody,
			ErrEmptyJSONBody,
			failure.WithMessage("request body is required"),
		)
	}

	diagnostic := InvalidJSONDiagnostic{
		Kind:      "decode_error",
		BytesRead: bytesRead,
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
		&invalidJSONError{cause: err, diagnostic: diagnostic},
		failure.WithMessage("invalid json body"),
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
