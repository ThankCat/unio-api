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

// RecordInvalidJSON 把公开请求的 invalid JSON 原因写入现有请求完成日志。
// 请求正文、字段值和原始 decoder 错误文本不会进入日志。
func RecordInvalidJSON(r *http.Request, err error) {
	if r == nil || failure.CodeOf(err) != failure.CodeHTTPInvalidJSONBody {
		return
	}
	diagnostic, ok := httpx.InvalidJSONDiagnosticOf(err)
	if !ok {
		return
	}

	logfields.SetJSONDecodeSummary(r.Context(), logfields.JSONDecodeSummary{
		Kind:             diagnostic.Kind,
		Field:            diagnostic.Field,
		Offset:           diagnostic.Offset,
		BytesRead:        diagnostic.BytesRead,
		ContentLength:    r.ContentLength,
		ContentEncoding:  boundedLogHeader(r.Header.Get("Content-Encoding")),
		TransferEncoding: boundedLogHeader(strings.Join(r.TransferEncoding, ",")),
		UserAgent:        boundedLogHeader(r.UserAgent()),
	})
	logfields.SetCompletion(r.Context(), "warning", string(failure.CodeHTTPInvalidJSONBody))
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
