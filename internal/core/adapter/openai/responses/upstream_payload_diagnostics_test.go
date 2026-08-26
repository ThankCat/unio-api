package responses

import (
	"strings"
	"testing"

	"github.com/ThankCat/unio-gateway/internal/core/adapter"
)

// 上游把错误放在约定之外的字段上时，code 与 message 都取不到，internal_error_detail 只剩一句
// 「upstream responses stream error」。排障的人看着这句话既不知道上游说了什么，也无从判断是不是
// 我们解析错了。原文必须留下来。
func TestUpstreamStreamErrorKeepsUnparsedPayloadForDiagnostics(t *testing.T) {
	raw := []byte(`{"type":"error","error":{"type":"upstream_error","message":"model overloaded"}}`)

	err := newUpstreamStreamErrorWithPayload(
		adapter.UpstreamMetadata{StatusCode: 200},
		"",
		"upstream responses stream error",
		raw,
	)

	if !strings.Contains(err.Error(), "model overloaded") {
		t.Fatalf("upstream payload must survive into the diagnostic detail, got %q", err.Error())
	}
}

// 原文只服务内部排障。面向客户的那条身份信息仍旧是脱敏后的短句，不能把整个 payload 灌进去。
func TestUnparsedPayloadStaysOutOfClientFacingMessage(t *testing.T) {
	raw := []byte(`{"error":{"message":"upstream https://internal.provider.example/v1 exploded"}}`)

	err := newUpstreamStreamErrorWithPayload(
		adapter.UpstreamMetadata{StatusCode: 200},
		"",
		"upstream responses stream error",
		raw,
	)

	meta, ok := adapter.UpstreamMetadataOf(err)
	if !ok {
		t.Fatal("upstream metadata must be attached")
	}
	if strings.Contains(meta.ErrorMessage, "internal.provider.example") {
		t.Fatalf("raw payload leaked into the client-facing identity: %q", meta.ErrorMessage)
	}
	if strings.Contains(meta.ErrorMessage, "exploded") {
		t.Fatalf("raw payload leaked into the client-facing identity: %q", meta.ErrorMessage)
	}
}

// 上游可能发回一整个巨大的 response 对象。诊断详情要有上限，否则一条日志能撑爆整行。
func TestUnparsedPayloadIsTruncated(t *testing.T) {
	raw := []byte(`{"filler":"` + strings.Repeat("x", 4000) + `"}`)

	err := newUpstreamStreamErrorWithPayload(
		adapter.UpstreamMetadata{StatusCode: 200},
		"",
		"upstream responses stream error",
		raw,
	)

	if len(err.Error()) > maxDiagnosticPayloadLen+512 {
		t.Fatalf("diagnostic detail is unbounded: %d bytes", len(err.Error()))
	}
	if !strings.Contains(err.Error(), "…") {
		t.Fatalf("truncation must be visible, got %q", err.Error())
	}
}

// 上游按约定给了 message 时不该再附原文——那只会让日志重复一遍同样的内容。
func TestParsedMessageSuppressesRawPayload(t *testing.T) {
	raw := []byte(`{"type":"error","code":"rate_limit","message":"slow down"}`)

	err := newUpstreamStreamErrorWithPayload(
		adapter.UpstreamMetadata{StatusCode: 200},
		"rate_limit",
		"slow down",
		nil, // 调用方解析成功时不传原文
	)

	if strings.Contains(err.Error(), string(raw)) {
		t.Fatalf("payload should not be appended when the upstream message was parsed: %q", err.Error())
	}
	if !strings.Contains(err.Error(), "slow down") {
		t.Fatalf("parsed message must still be present: %q", err.Error())
	}
}
