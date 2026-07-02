package lifecycle

import (
	"testing"

	"github.com/ThankCat/unio-api/internal/core/routing"
	coreusage "github.com/ThankCat/unio-api/internal/core/usage"
)

// TestBuildPartialStreamFactsSplitsInputByAssumedCacheRatio 锁定临时口径：无真实 usage 的 partial 结算
// 按固定假定缓存率把估算输入拆成 cache_read(60%) / uncached(40%)，二者之和恒等于输入、输出原样透传。
func TestBuildPartialStreamFactsSplitsInputByAssumedCacheRatio(t *testing.T) {
	SetPartialAssumedCacheReadRatio(0.60)
	t.Cleanup(func() { SetPartialAssumedCacheReadRatio(defaultPartialAssumedCacheReadRatio) })

	facts := BuildPartialStreamFacts(PartialStreamFactsParams{
		Candidate:       routing.ChatRouteCandidate{Protocol: "openai", UpstreamModel: "gpt-5.5"},
		RequestRecordID: 1,
		InputTokens:     1000,
		OutputTokens:    20,
		Reason:          PartialReasonInterrupted,
	})

	unc, uncOK := facts.Usage.UncachedInputTokens.BillableValue()
	cr, crOK := facts.Usage.CacheReadInputTokens.BillableValue()
	out, _ := facts.Usage.OutputTokensTotal.BillableValue()

	if !crOK || !uncOK {
		t.Fatal("uncached / cache_read must be known (billable) values, not not_applicable")
	}
	if cr != 600 || unc != 400 {
		t.Fatalf("expected 600 cache_read / 400 uncached (60/40 of 1000), got cr=%d unc=%d", cr, unc)
	}
	if cr+unc != 1000 {
		t.Fatalf("split must sum to input tokens, got %d", cr+unc)
	}
	if out != 20 {
		t.Fatalf("output tokens should pass through unchanged, got %d", out)
	}
	if facts.UsageSource != coreusage.SourcePartialStreamEstimate {
		t.Fatalf("usage source must be partial_stream_estimate, got %q", facts.UsageSource)
	}
}

// TestBuildPartialStreamFactsZeroInput 验证零输入不会产生负数或非零拆分。
func TestBuildPartialStreamFactsZeroInput(t *testing.T) {
	facts := BuildPartialStreamFacts(PartialStreamFactsParams{
		Candidate:       routing.ChatRouteCandidate{Protocol: "openai", UpstreamModel: "gpt-5.5"},
		RequestRecordID: 1,
		InputTokens:     0,
		OutputTokens:    0,
		Reason:          PartialReasonInterrupted,
	})
	cr, _ := facts.Usage.CacheReadInputTokens.BillableValue()
	unc, _ := facts.Usage.UncachedInputTokens.BillableValue()
	if cr != 0 || unc != 0 {
		t.Fatalf("zero input must split to 0/0, got cr=%d unc=%d", cr, unc)
	}
}
