// Package ingresslog 记录公开 Gateway 协议入口在持久 Request 创建前的脱敏诊断。
package ingresslog

import (
	"net/http"
	"strings"

	"github.com/ThankCat/unio-gateway/internal/platform/failure"
	"github.com/ThankCat/unio-gateway/internal/platform/httpx"
	"github.com/ThankCat/unio-gateway/internal/platform/observability/logfields"
)

const maxLogHeaderRunes = 256

// RecordRequestBodyFailure 把公开请求的请求体读取/JSON 解码原因写入现有请求完成日志。
// 请求正文、字段值和原始 decoder 错误文本不会进入日志。
func RecordRequestBodyFailure(r *http.Request, err error) {
	if r == nil {
		return
	}
	code := failure.CodeOf(err)
	switch code {
	case failure.CodeHTTPInvalidJSONBody,
		failure.CodeHTTPRequestBodyTooLarge,
		failure.CodeHTTPRequestBodyTimeout,
		failure.CodeHTTPRequestBodyIncomplete,
		failure.CodeHTTPClientDisconnected:
	default:
		return
	}

	diagnostic, ok := httpx.RequestBodyDiagnosticOf(err)
	if !ok {
		return
	}

	logfields.SetJSONDecodeSummary(r.Context(), logfields.JSONDecodeSummary{
		Reason:           diagnostic.Reason,
		Kind:             diagnostic.Kind,
		Field:            diagnostic.Field,
		Offset:           diagnostic.Offset,
		BytesRead:        diagnostic.BytesRead,
		ContentLength:    r.ContentLength,
		BodyLimit:        diagnostic.Limit,
		CompletionStatus: bodyCompletionStatus(diagnostic.BytesRead, r.ContentLength),
		ContentEncoding:  boundedLogHeader(r.Header.Get("Content-Encoding")),
		TransferEncoding: boundedLogHeader(strings.Join(r.TransferEncoding, ",")),
		HTTPVersion:      boundedLogHeader(r.Proto),
		UserAgent:        boundedLogHeader(r.UserAgent()),
	})
	logfields.SetCompletion(r.Context(), "warning", string(code))
}

// RecordInvalidJSON 保留旧调用语义，仅记录真实 invalid JSON。
func RecordInvalidJSON(r *http.Request, err error) {
	if failure.CodeOf(err) == failure.CodeHTTPInvalidJSONBody {
		RecordRequestBodyFailure(r, err)
	}
}

func bodyCompletionStatus(bytesRead int64, contentLength int64) string {
	if contentLength < 0 {
		return "unknown"
	}
	if bytesRead < contentLength {
		return "incomplete"
	}
	return "complete"
}

func boundedLogHeader(value string) string {
	value = strings.TrimSpace(strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return ' '
		}
		return r
	}, value))
	runes := []rune(value)
	if len(runes) > maxLogHeaderRunes {
		runes = runes[:maxLogHeaderRunes]
	}
	return string(runes)
}
