package httpx

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ThankCat/unio-gateway/internal/platform/failure"
)

// decodeJSONTestBody 是 DecodeJSON 测试使用的请求体结构。
type decodeJSONTestBody struct {
	Value string `json:"value"`
}

func TestDecodeJSONDecodesBody(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/test", strings.NewReader(`{"value":"hello"}`))
	rec := httptest.NewRecorder()

	var body decodeJSONTestBody
	if err := DecodeJSON(rec, req, &body); err != nil {
		t.Fatalf("decode json: %v", err)
	}

	if body.Value != "hello" {
		t.Fatalf("expected value %q, got %q", "hello", body.Value)
	}
}

func TestDecodeJSONAcceptsJSONContentTypeWithCharset(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/test", strings.NewReader(`{"value":"hello"}`))
	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	rec := httptest.NewRecorder()

	var body decodeJSONTestBody
	if err := DecodeJSON(rec, req, &body); err != nil {
		t.Fatalf("decode json: %v", err)
	}

	if body.Value != "hello" {
		t.Fatalf("expected value %q, got %q", "hello", body.Value)
	}
}

func TestDecodeJSONReturnsErrorForUnsupportedContentType(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/test", strings.NewReader(`{"value":"hello"}`))
	req.Header.Set("Content-Type", "text/plain")
	rec := httptest.NewRecorder()

	var body decodeJSONTestBody
	err := DecodeJSON(rec, req, &body)
	if !errors.Is(err, ErrUnsupportedContentType) {
		t.Fatalf("expected ErrUnsupportedContentType, got %v", err)
	}
	assertDecodeJSONFailure(t, err, failure.CodeHTTPUnsupportedContentType)
}

func TestDecodeJSONReturnsErrorForEmptyBody(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/test", strings.NewReader(""))
	rec := httptest.NewRecorder()

	var body decodeJSONTestBody
	err := DecodeJSON(rec, req, &body)
	if !errors.Is(err, ErrEmptyJSONBody) {
		t.Fatalf("expected ErrEmptyJSONBody, got %v", err)
	}
	assertDecodeJSONFailure(t, err, failure.CodeHTTPEmptyJSONBody)
}

func TestDecodeJSONReturnsErrorForInvalidJSON(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/test", strings.NewReader(`{`))
	rec := httptest.NewRecorder()

	var body decodeJSONTestBody
	err := DecodeJSON(rec, req, &body)
	if err == nil {
		t.Fatal("expected decode error, got nil")
	}
	assertDecodeJSONFailure(t, err, failure.CodeHTTPInvalidJSONBody)
}

func TestDecodeJSONClassifiesInvalidJSONWithoutBodyValues(t *testing.T) {
	tests := []struct {
		name       string
		body       string
		wantKind   string
		wantField  string
		wantOffset bool
	}{
		{name: "unexpected eof", body: `{"value":"secret`, wantKind: "unexpected_eof"},
		{name: "syntax", body: `{"value":invalid}`, wantKind: "syntax", wantOffset: true},
		{name: "type mismatch", body: `{"value":123}`, wantKind: "type_mismatch", wantField: "value", wantOffset: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/test", strings.NewReader(tt.body))
			rec := httptest.NewRecorder()

			var body decodeJSONTestBody
			err := DecodeJSON(rec, req, &body)
			assertDecodeJSONFailure(t, err, failure.CodeHTTPInvalidJSONBody)

			diagnostic, ok := InvalidJSONDiagnosticOf(err)
			if !ok {
				t.Fatal("expected invalid JSON diagnostic")
			}
			if diagnostic.Kind != tt.wantKind || diagnostic.Field != tt.wantField {
				t.Fatalf("diagnostic kind=%q field=%q, want kind=%q field=%q", diagnostic.Kind, diagnostic.Field, tt.wantKind, tt.wantField)
			}
			if (diagnostic.Offset > 0) != tt.wantOffset {
				t.Fatalf("diagnostic offset=%d, want positive=%v", diagnostic.Offset, tt.wantOffset)
			}
			if diagnostic.BytesRead != int64(len(tt.body)) {
				t.Fatalf("diagnostic bytes_read=%d, want %d", diagnostic.BytesRead, len(tt.body))
			}
			if strings.Contains(diagnostic.Kind, "secret") || strings.Contains(diagnostic.Field, "secret") {
				t.Fatalf("diagnostic leaked body value: %#v", diagnostic)
			}
		})
	}
}

func TestInvalidJSONDiagnosticOfRejectsOtherDecodeFailures(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/test", strings.NewReader(""))
	var body decodeJSONTestBody
	err := DecodeJSON(httptest.NewRecorder(), req, &body)
	if _, ok := InvalidJSONDiagnosticOf(err); ok {
		t.Fatal("empty body must not be classified as invalid JSON")
	}
}

func TestDecodeJSONReturnsErrorForTrailingJSONToken(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/test", strings.NewReader(`{"value":"hello"} {"value":"second"}`))
	rec := httptest.NewRecorder()

	var body decodeJSONTestBody
	err := DecodeJSON(rec, req, &body)
	if !errors.Is(err, ErrTrailingJSONToken) {
		t.Fatalf("expected ErrTrailingJSONToken, got %v", err)
	}
	assertDecodeJSONFailure(t, err, failure.CodeHTTPTrailingJSONToken)
}

func TestDecodeJSONReturnsErrorForTooLargeBody(t *testing.T) {
	largeValue := strings.Repeat("a", int(DefaultMaxJSONBodyBytes)+1)
	reqBody := `{"value":"` + largeValue + `"}`
	req := httptest.NewRequest(http.MethodPost, "/test", strings.NewReader(reqBody))
	rec := httptest.NewRecorder()

	var body decodeJSONTestBody
	err := DecodeJSON(rec, req, &body)
	if !errors.Is(err, ErrRequestBodyTooLarge) {
		t.Fatalf("expected ErrRequestBodyTooLarge, got %v", err)
	}
	assertDecodeJSONFailure(t, err, failure.CodeHTTPRequestBodyTooLarge)
}

func TestMaxJSONBodyBytesDefaultsWhenUnset(t *testing.T) {
	SetMaxJSONBodyBytes(0)
	if got := MaxJSONBodyBytes(); got != DefaultMaxJSONBodyBytes {
		t.Fatalf("expected default %d, got %d", DefaultMaxJSONBodyBytes, got)
	}

	// 负值同样回退默认。
	SetMaxJSONBodyBytes(-1)
	if got := MaxJSONBodyBytes(); got != DefaultMaxJSONBodyBytes {
		t.Fatalf("expected default %d for negative limit, got %d", DefaultMaxJSONBodyBytes, got)
	}
}

func TestDecodeJSONHonorsConfiguredLimit(t *testing.T) {
	t.Cleanup(func() { SetMaxJSONBodyBytes(0) })

	// 抬高上限到 4MB：原本超过默认 1MB 的 body 现在应能正常解码。
	SetMaxJSONBodyBytes(4 << 20)
	largeValue := strings.Repeat("a", int(DefaultMaxJSONBodyBytes)+1024)
	reqBody := `{"value":"` + largeValue + `"}`
	req := httptest.NewRequest(http.MethodPost, "/test", strings.NewReader(reqBody))
	rec := httptest.NewRecorder()

	var body decodeJSONTestBody
	if err := DecodeJSON(rec, req, &body); err != nil {
		t.Fatalf("expected decode under raised limit to succeed, got %v", err)
	}

	// 收紧上限到 16 字节：正常 body 也应 413。
	SetMaxJSONBodyBytes(16)
	req = httptest.NewRequest(http.MethodPost, "/test", strings.NewReader(`{"value":"hello"}`))
	rec = httptest.NewRecorder()
	err := DecodeJSON(rec, req, &body)
	if !errors.Is(err, ErrRequestBodyTooLarge) {
		t.Fatalf("expected ErrRequestBodyTooLarge under tightened limit, got %v", err)
	}
	assertDecodeJSONFailure(t, err, failure.CodeHTTPRequestBodyTooLarge)
}

func assertDecodeJSONFailure(t *testing.T, err error, wantCode failure.Code) {
	t.Helper()

	if failure.CodeOf(err) != wantCode {
		t.Fatalf("expected failure code %q, got %q", wantCode, failure.CodeOf(err))
	}
	if failure.CategoryOf(err) != failure.CategoryHTTP {
		t.Fatalf("expected failure category %q, got %q", failure.CategoryHTTP, failure.CategoryOf(err))
	}
	if fields := failure.FieldsOf(err); len(fields) != 0 {
		t.Fatalf("expected no failure fields, got %#v", fields)
	}
}
