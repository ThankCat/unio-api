package httpx

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ThankCat/unio-gateway/internal/platform/failure"
)

// noFlushWriter 是只实现 http.ResponseWriter、不实现 http.Flusher 的测试 writer。
type noFlushWriter struct {
	header http.Header
}

func (n *noFlushWriter) Header() http.Header {
	if n.header == nil {
		n.header = make(http.Header)
	}

	return n.header
}

func (n *noFlushWriter) Write(b []byte) (int, error) { return len(b), nil }

func (n *noFlushWriter) WriteHeader(statusCode int) {}

// failingFlushWriter 实现 http.Flusher 但每次 Write 都失败，用于验证 sticky 短路。
type failingFlushWriter struct {
	header     http.Header
	writeCount int
}

func (f *failingFlushWriter) Header() http.Header {
	if f.header == nil {
		f.header = make(http.Header)
	}

	return f.header
}

func (f *failingFlushWriter) Write(b []byte) (int, error) {
	f.writeCount++
	return 0, errors.New("broken pipe")
}

func (f *failingFlushWriter) WriteHeader(statusCode int) {}

func (f *failingFlushWriter) Flush() {}

func strPtr(v string) *string { return &v }

func intPtr(v int) *int { return &v }

func TestSSEWriterWriteDataWritesDataOnlyEvent(t *testing.T) {
	rec := httptest.NewRecorder()

	sw, err := NewSSEWriter(context.Background(), rec, SSEWriterConfig{})
	if err != nil {
		t.Fatalf("new sse writer: %v", err)
	}

	if sw.Started() {
		t.Fatalf("started before first write: got true, want false")
	}

	if err := sw.WriteData([]byte(`{"param":"value"}`)); err != nil {
		t.Fatalf("write data: %v", err)
	}

	if !sw.Started() {
		t.Fatalf("started after first write: got false, want true")
	}

	if got := rec.Header().Get("Content-Type"); got != ContentTypeSSE {
		t.Fatalf("content-type: got %q, want %q", got, ContentTypeSSE)
	}
	if got := rec.Header().Get("Cache-Control"); got != "no-cache" {
		t.Fatalf("cache-control: got %q, want no-cache", got)
	}

	want := "data: {\"param\":\"value\"}\n\n"
	if got := rec.Body.String(); got != want {
		t.Fatalf("body: got %q, want %q", got, want)
	}

	if !rec.Flushed {
		t.Fatalf("flushed: got false, want true")
	}
}

func TestSSEWriterWriteEventWritesAllFields(t *testing.T) {
	rec := httptest.NewRecorder()

	sw, err := NewSSEWriter(context.Background(), rec, SSEWriterConfig{})
	if err != nil {
		t.Fatalf("new sse writer: %v", err)
	}

	event := SSEEvent{
		Type:              "message",
		Data:              []byte(`{"x":1}`),
		ID:                strPtr("evt-1"),
		RetryMilliseconds: intPtr(1500),
	}
	if err := sw.WriteEvent(event); err != nil {
		t.Fatalf("write event: %v", err)
	}

	want := "event: message\nid: evt-1\nretry: 1500\ndata: {\"x\":1}\n\n"
	if got := rec.Body.String(); got != want {
		t.Fatalf("body: got %q, want %q", got, want)
	}
}

func TestSSEWriterWriteEventSplitsMultiLineData(t *testing.T) {
	rec := httptest.NewRecorder()

	sw, err := NewSSEWriter(context.Background(), rec, SSEWriterConfig{})
	if err != nil {
		t.Fatalf("new sse writer: %v", err)
	}

	if err := sw.WriteData([]byte("line1\nline2\r\nline3")); err != nil {
		t.Fatalf("write data: %v", err)
	}

	want := "data: line1\ndata: line2\ndata: line3\n\n"
	if got := rec.Body.String(); got != want {
		t.Fatalf("body: got %q, want %q", got, want)
	}
}

func TestSSEWriterWriteComment(t *testing.T) {
	rec := httptest.NewRecorder()

	sw, err := NewSSEWriter(context.Background(), rec, SSEWriterConfig{})
	if err != nil {
		t.Fatalf("new sse writer: %v", err)
	}

	if err := sw.WriteComment("ping"); err != nil {
		t.Fatalf("write comment: %v", err)
	}

	want := ": ping\n\n"
	if got := rec.Body.String(); got != want {
		t.Fatalf("body: got %q, want %q", got, want)
	}
}

func TestSSEWriterWriteDataDoneSentinel(t *testing.T) {
	rec := httptest.NewRecorder()

	sw, err := NewSSEWriter(context.Background(), rec, SSEWriterConfig{})
	if err != nil {
		t.Fatalf("new sse writer: %v", err)
	}

	if err := sw.WriteData([]byte("[DONE]")); err != nil {
		t.Fatalf("write data: %v", err)
	}

	want := "data: [DONE]\n\n"
	if got := rec.Body.String(); got != want {
		t.Fatalf("body: got %q, want %q", got, want)
	}
}

func TestNewSSEWriterRejectsNonFlusher(t *testing.T) {
	_, err := NewSSEWriter(context.Background(), &noFlushWriter{}, SSEWriterConfig{})
	if err == nil {
		t.Fatalf("new sse writer: got nil error, want streaming unsupported")
	}

	if !errors.Is(err, ErrStreamingUnsupported) {
		t.Fatalf("new sse writer: got %v, want ErrStreamingUnsupported", err)
	}
	if got := failure.CodeOf(err); got != failure.CodeHTTPStreamingUnsupported {
		t.Fatalf("new sse writer code: got %q, want %q", got, failure.CodeHTTPStreamingUnsupported)
	}
}

func TestSSEWriterContextCanceledShortCircuits(t *testing.T) {
	rec := httptest.NewRecorder()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	sw, err := NewSSEWriter(ctx, rec, SSEWriterConfig{})
	if err != nil {
		t.Fatalf("new sse writer: %v", err)
	}

	writeErr := sw.WriteData([]byte(`{"x":1}`))
	if writeErr == nil {
		t.Fatalf("write data: got nil error, want client disconnected")
	}
	if got := failure.CodeOf(writeErr); got != failure.CodeHTTPClientDisconnected {
		t.Fatalf("write data code: got %q, want %q", got, failure.CodeHTTPClientDisconnected)
	}

	if sw.Started() {
		t.Fatalf("started after canceled write: got true, want false")
	}
	if rec.Body.Len() != 0 {
		t.Fatalf("body after canceled write: got %q, want empty", rec.Body.String())
	}
	if sw.Err() == nil {
		t.Fatalf("sticky err after cancel: got nil, want set")
	}
}

func TestSSEWriterStickyErrorAfterWriteFailure(t *testing.T) {
	writer := &failingFlushWriter{}

	sw, err := NewSSEWriter(context.Background(), writer, SSEWriterConfig{})
	if err != nil {
		t.Fatalf("new sse writer: %v", err)
	}

	firstErr := sw.WriteData([]byte(`{"x":1}`))
	if firstErr == nil {
		t.Fatalf("first write: got nil error, want write failed")
	}
	if got := failure.CodeOf(firstErr); got != failure.CodeHTTPResponseWriteFailed {
		t.Fatalf("first write code: got %q, want %q", got, failure.CodeHTTPResponseWriteFailed)
	}

	writesAfterFirst := writer.writeCount

	secondErr := sw.WriteData([]byte(`{"y":2}`))
	if secondErr == nil {
		t.Fatalf("second write: got nil error, want sticky error")
	}
	if !errors.Is(secondErr, firstErr) {
		t.Fatalf("second write: got %v, want same sticky error as first", secondErr)
	}

	if writer.writeCount != writesAfterFirst {
		t.Fatalf("second write should short-circuit: write count went %d -> %d", writesAfterFirst, writer.writeCount)
	}
}
