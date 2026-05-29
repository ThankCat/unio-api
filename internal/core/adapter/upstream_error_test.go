package adapter

import (
	"errors"
	"fmt"
	"testing"

	"github.com/ThankCat/unio-api/internal/platform/failure"
)

// TestUpstreamErrorPreservesFailureChain 验证 UpstreamError 不破坏 failure.CodeOf 和 errors.Is。
// 这是边界设计的核心约束：新增上游分类维度不能影响既有 error_code 写入和 sentinel 匹配。
func TestUpstreamErrorPreservesFailureChain(t *testing.T) {
	sentinel := errors.New("boom")
	cause := failure.Wrap(failure.CodeAdapterUpstreamStatus, sentinel)

	err := NewUpstreamError(
		UpstreamErrorRateLimit,
		UpstreamMetadata{StatusCode: 429, RequestID: "req-123"},
		cause,
	)

	if got := failure.CodeOf(err); got != failure.CodeAdapterUpstreamStatus {
		t.Fatalf("failure.CodeOf: got %q, want %q", got, failure.CodeAdapterUpstreamStatus)
	}
	if !errors.Is(err, sentinel) {
		t.Fatal("errors.Is should still match the wrapped sentinel through UpstreamError")
	}
}

// TestUpstreamCategoryOf 验证沿 error 链提取上游分类和元信息。
func TestUpstreamCategoryOf(t *testing.T) {
	meta := UpstreamMetadata{StatusCode: 401, RequestID: "req-auth"}
	err := NewUpstreamError(UpstreamErrorAuth, meta, failure.New(failure.CodeAdapterUpstreamStatus))

	// 即使被外层 failure 再包一次，也要能沿链取回上游分类。
	wrapped := fmt.Errorf("gateway context: %w", err)

	category, ok := UpstreamCategoryOf(wrapped)
	if !ok {
		t.Fatal("UpstreamCategoryOf should find UpstreamError in the chain")
	}
	if category != UpstreamErrorAuth {
		t.Fatalf("category: got %q, want %q", category, UpstreamErrorAuth)
	}

	gotMeta, ok := UpstreamMetadataOf(wrapped)
	if !ok {
		t.Fatal("UpstreamMetadataOf should find UpstreamError in the chain")
	}
	if gotMeta.StatusCode != 401 || gotMeta.RequestID != "req-auth" {
		t.Fatalf("metadata: got %+v, want %+v", gotMeta, meta)
	}
}

// TestUpstreamCategoryOfMissing 验证非上游错误返回保守的 (unknown, false)。
func TestUpstreamCategoryOfMissing(t *testing.T) {
	plain := failure.New(failure.CodeGatewayAdapterNotRegistered)

	category, ok := UpstreamCategoryOf(plain)
	if ok {
		t.Fatal("UpstreamCategoryOf should not match a non-upstream error")
	}
	if category != UpstreamErrorUnknown {
		t.Fatalf("category: got %q, want %q", category, UpstreamErrorUnknown)
	}

	if _, ok := UpstreamMetadataOf(nil); ok {
		t.Fatal("UpstreamMetadataOf(nil) should be false")
	}
}
