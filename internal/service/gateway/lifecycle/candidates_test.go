package lifecycle

import (
	"context"
	"errors"
	"math"
	"testing"

	"github.com/ThankCat/unio-gateway/internal/core/channel"
	"github.com/ThankCat/unio-gateway/internal/core/routing"
	"github.com/ThankCat/unio-gateway/internal/platform/breakerstore"
	"github.com/ThankCat/unio-gateway/internal/platform/failure"
	"github.com/ThankCat/unio-gateway/internal/service/gateway/requestadmission"
)

func TestExecutorPrepareCandidatesWithoutRuntimeUsesNeutralSQLOrder(t *testing.T) {
	executor := NewExecutor(candidateCapabilityRegistry{
		allowed: map[int64]bool{1: true, 2: true, 3: true},
	})

	plan, err := executor.PrepareCandidates(context.Background(), PrepareCandidatesParams{
		Protocol: "openai",
		Candidates: []routing.ChatRouteCandidate{
			candidateRoute(1, "openai"),
			candidateRoute(2, "openai"),
			candidateRoute(3, "openai"),
		},
		Capabilities: []AdapterCapability{
			AdapterCapabilityNonStream,
			AdapterCapabilityInputTokenizer,
		},
		EstimateInputTokens: func(_ context.Context, _ routing.ChatRouteCandidate) (int64, error) {
			return 1, nil
		},
		Mode: "balanced",
	})
	if err != nil {
		t.Fatalf("PrepareCandidates returned error: %v", err)
	}

	// 没有 HTTP-owned request session 就没有 Redis SnapshotMany 权威事实；直接调用保持 SQL 顺序，
	// 不得回退到已退役的本机容量/健康评分。
	want := []int64{1, 2, 3}
	if len(plan.Candidates) != len(want) {
		t.Fatalf("expected %d candidates, got %d", len(want), len(plan.Candidates))
	}
	for i, c := range plan.Candidates {
		if c.Route.Channel.ID != want[i] {
			t.Fatalf("balanced order position %d: expected channel %d, got %d", i, want[i], c.Route.Channel.ID)
		}
	}
	for _, candidate := range plan.Candidates {
		if candidate.Balance.FinalScore != 100 || candidate.Balance.AlgorithmVersion != "objective_v1" || !candidate.Balance.CapacityUnknown {
			t.Fatalf("expected neutral unknown runtime score, got %+v", candidate.Balance)
		}
	}
}

// unlimitedConcurrency 是「显式不限并发」的评分输入（§7.3：limit=0 得满分）。
func unlimitedConcurrency() ChannelScoreInputs {
	return ChannelScoreInputs{
		Concurrency:  CapacitySignal{Limit: 0, Known: true},
		RuntimeKnown: true,
	}
}

// TestScoreChannelCostBoundaries 覆盖 §7.2 成本分边界：0% / 60% / 100% 成本占售价。
func TestScoreChannelCostBoundaries(t *testing.T) {
	cases := []struct {
		costRatio float64
		wantCost  float64
	}{
		{0, 100},
		{0.2, 80},
		{0.6, 40},
		{1, 0},
		{2, 0}, // clamp：cost_ratio>1 归零，不产生负分
	}
	for _, tc := range cases {
		score := ScoreChannel(unlimitedConcurrency(), tc.costRatio, 0, BalanceConfig{})
		if math.Abs(score.CostScore-tc.wantCost) > 1e-9 {
			t.Fatalf("cost_ratio=%v cost score=%v want %v", tc.costRatio, score.CostScore, tc.wantCost)
		}
		if score.CostRatio != tc.costRatio {
			t.Fatalf("cost ratio must be reported verbatim: got %v want %v", score.CostRatio, tc.costRatio)
		}
	}
}

// TestScoreChannelConcurrencyBoundaries 覆盖 §7.3 并发容量分：不限=100、剩余比例、满载=0、未知=100。
func TestScoreChannelConcurrencyBoundaries(t *testing.T) {
	unlimited := ScoreChannel(unlimitedConcurrency(), 0, 0, BalanceConfig{})
	if unlimited.ConcurrencyScore != 100 {
		t.Fatalf("limit=0 must score 100, got %v", unlimited.ConcurrencyScore)
	}
	partial := ScoreChannel(ChannelScoreInputs{
		Concurrency: CapacitySignal{Used: 2, Limit: 10, Known: true}, RuntimeKnown: true,
	}, 0, 0, BalanceConfig{})
	if math.Abs(partial.ConcurrencyScore-80) > 1e-9 {
		t.Fatalf("remaining 8/10 must score 80, got %v", partial.ConcurrencyScore)
	}
	full := ScoreChannel(ChannelScoreInputs{
		Concurrency: CapacitySignal{Used: 10, Limit: 10, Known: true}, RuntimeKnown: true,
	}, 0, 0, BalanceConfig{})
	if full.ConcurrencyScore != 0 {
		t.Fatalf("saturated concurrency must score 0, got %v", full.ConcurrencyScore)
	}
	unknown := ScoreChannel(ChannelScoreInputs{}, 0, 0, BalanceConfig{})
	if unknown.ConcurrencyScore != 100 || !unknown.CapacityUnknown {
		t.Fatalf("unknown concurrency must stay neutral: %+v", unknown)
	}
}

// TestScoreChannelTTFTPenalty 覆盖 §7.4：每秒扣 2.5 分、40 秒归零、无样本满分。
func TestScoreChannelTTFTPenalty(t *testing.T) {
	cases := []struct {
		name     string
		sumMs    int64
		count    int64
		wantTTFT float64
		wantAvg  float64
	}{
		{"no_sample", 0, 0, 100, 0},
		{"1s", 1000, 1, 97.5, 1000},
		{"10s", 10000, 1, 75, 10000},
		{"40s", 40000, 1, 0, 40000},
		{"60s_clamped", 60000, 1, 0, 60000},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			in := unlimitedConcurrency()
			in.TTFTSumMs, in.TTFTCount = tc.sumMs, tc.count
			score := ScoreChannel(in, 0, 0, BalanceConfig{})
			if math.Abs(score.TTFTScore-tc.wantTTFT) > 1e-9 {
				t.Fatalf("ttft score=%v want %v", score.TTFTScore, tc.wantTTFT)
			}
			if math.Abs(score.AvgTTFTMs-tc.wantAvg) > 1e-9 || score.TTFTSampleCount != tc.count {
				t.Fatalf("ttft facts avg=%v count=%d, want avg=%v count=%d",
					score.AvgTTFTMs, score.TTFTSampleCount, tc.wantAvg, tc.count)
			}
		})
	}
}

// TestScoreChannelTTFTSampleCountDoesNotChangeScore 冻结 §1.7：样本数量不参与得分，只有均值参与。
func TestScoreChannelTTFTSampleCountDoesNotChangeScore(t *testing.T) {
	few := unlimitedConcurrency()
	few.TTFTSumMs, few.TTFTCount = 2000, 1
	many := unlimitedConcurrency()
	many.TTFTSumMs, many.TTFTCount = 200_000, 100 // 同为 2000ms 均值
	fewScore := ScoreChannel(few, 0, 0, BalanceConfig{})
	manyScore := ScoreChannel(many, 0, 0, BalanceConfig{})
	if math.Abs(fewScore.TTFTScore-manyScore.TTFTScore) > 1e-9 || fewScore.FinalScore != manyScore.FinalScore {
		t.Fatalf("equal mean must score equally regardless of sample count: few=%+v many=%+v", fewScore, manyScore)
	}
}

// TestScoreChannelErrorRatePenalty 覆盖 §7.5：每 1% 扣 2.5 分、40% 归零、无样本满分。
func TestScoreChannelErrorRatePenalty(t *testing.T) {
	cases := []struct {
		name      string
		denom     int64
		numerator int64
		wantError float64
		wantPct   float64
	}{
		{"no_sample", 0, 0, 100, 0},
		{"zero_percent", 100, 0, 100, 0},
		{"one_percent", 100, 1, 97.5, 1},
		{"ten_percent", 100, 10, 75, 10},
		{"forty_percent", 100, 40, 0, 40},
		{"all_failed_clamped", 100, 100, 0, 100},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			in := unlimitedConcurrency()
			in.ErrorAttemptCount, in.ErrorCount = tc.denom, tc.numerator
			score := ScoreChannel(in, 0, 0, BalanceConfig{})
			if math.Abs(score.ErrorScore-tc.wantError) > 1e-9 {
				t.Fatalf("error score=%v want %v", score.ErrorScore, tc.wantError)
			}
			if math.Abs(score.ErrorRatePct-tc.wantPct) > 1e-9 || score.ErrorSampleCount != tc.denom {
				t.Fatalf("error facts pct=%v count=%d, want pct=%v count=%d",
					score.ErrorRatePct, score.ErrorSampleCount, tc.wantPct, tc.denom)
			}
		})
	}
}

// TestScoreChannelPriorityBoundaries 冻结 §7.6/D7：数值越小越优先，0/30/100 → 100/70/0。
func TestScoreChannelPriorityBoundaries(t *testing.T) {
	for _, tc := range []struct {
		priority int32
		want     float64
	}{{0, 100}, {10, 90}, {30, 70}, {100, 0}} {
		score := ScoreChannel(unlimitedConcurrency(), 0, tc.priority, BalanceConfig{})
		if math.Abs(score.PriorityScore-tc.want) > 1e-9 {
			t.Fatalf("priority=%d score=%v want %v", tc.priority, score.PriorityScore, tc.want)
		}
		if score.Priority != tc.priority {
			t.Fatalf("priority must be reported verbatim: got %d want %d", score.Priority, tc.priority)
		}
	}
}

// TestScoreChannelWeightedTotalMatchesPlanExample 复现 §7.8 的展示样例：
// 成本 80×25% + 并发 50×20% + TTFT 95×25% + 错误率 100×20% + 优先级 80×10% = 81.75。
func TestScoreChannelWeightedTotalMatchesPlanExample(t *testing.T) {
	in := ChannelScoreInputs{
		Concurrency:  CapacitySignal{Used: 5, Limit: 10, Known: true}, // 并发 50
		TTFTSumMs:    2000,
		TTFTCount:    1, // 2s → 100-2*2.5 = 95
		RuntimeKnown: true,
	}
	score := ScoreChannel(in, 0.2 /* 成本 80 */, 20 /* 优先级 80 */, BalanceConfig{Revision: 7})
	if math.Abs(score.CostScore-80) > 1e-9 || math.Abs(score.ConcurrencyScore-50) > 1e-9 ||
		math.Abs(score.TTFTScore-95) > 1e-9 || math.Abs(score.ErrorScore-100) > 1e-9 ||
		math.Abs(score.PriorityScore-80) > 1e-9 {
		t.Fatalf("unexpected component scores: %+v", score)
	}
	if math.Abs(score.FinalScore-81.75) > 1e-9 {
		t.Fatalf("final score=%v want 81.75 (%+v)", score.FinalScore, score)
	}
	if score.AlgorithmVersion != "objective_v1" || score.RoutingBalanceRevision != 7 {
		t.Fatalf("score must carry the single algorithm version and config revision: %+v", score)
	}
	if score.CostWeightPct != 25 || score.ConcurrencyWeightPct != 20 || score.TTFTWeightPct != 25 ||
		score.ErrorRateWeightPct != 20 || score.PriorityWeightPct != 10 {
		t.Fatalf("default weights must be 25/20/25/20/10: %+v", score)
	}
}

// TestScoreChannelInvalidWeightsFallBackToDefaults 保证权重和不为 100 时回退到冻结默认，不产出畸形总分。
func TestScoreChannelInvalidWeightsFallBackToDefaults(t *testing.T) {
	bad := BalanceConfig{CostWeightPct: 50, ConcurrencyWeightPct: 50, TTFTWeightPct: 50}
	score := ScoreChannel(unlimitedConcurrency(), 0, 0, bad)
	if score.CostWeightPct != 25 || score.ConcurrencyWeightPct != 20 || score.TTFTWeightPct != 25 ||
		score.ErrorRateWeightPct != 20 || score.PriorityWeightPct != 10 {
		t.Fatalf("invalid weight sum must fall back to defaults: %+v", score)
	}
	if score.FinalScore != 100 {
		t.Fatalf("all-neutral inputs must total 100, got %v", score.FinalScore)
	}
}

// TestScoreChannelCustomPenaltyConfig 确认惩罚参数可配置（窗口/单位/每单位扣分）。
func TestScoreChannelCustomPenaltyConfig(t *testing.T) {
	in := unlimitedConcurrency()
	in.TTFTSumMs, in.TTFTCount = 2000, 1
	in.ErrorAttemptCount, in.ErrorCount = 10, 1 // 10%
	score := ScoreChannel(in, 0, 0, BalanceConfig{
		CostWeightPct: 25, ConcurrencyWeightPct: 20, TTFTWeightPct: 25,
		ErrorRateWeightPct: 20, PriorityWeightPct: 10,
		TTFTPenaltyUnitMs: 500, TTFTPenaltyPointsPerUnit: 1, // 2000ms/500ms=4 单位 → 扣 4
		ErrorPenaltyPointsPerPercent: 5, // 10% → 扣 50
	})
	if math.Abs(score.TTFTScore-96) > 1e-9 {
		t.Fatalf("custom ttft penalty score=%v want 96", score.TTFTScore)
	}
	if math.Abs(score.ErrorScore-50) > 1e-9 {
		t.Fatalf("custom error penalty score=%v want 50", score.ErrorScore)
	}
}

func TestBalancedPrefersCheaperCandidateWhenOtherMetricsEqual(t *testing.T) {
	expensive := candidateRoute(1, "openai")
	expensive.CostRatio = 1
	cheap := candidateRoute(2, "openai")
	cheap.CostRatio = 0.2
	reader := func(context.Context, routing.ChatRouteCandidate) (ChannelScoreInputs, error) {
		return unlimitedConcurrency(), nil
	}
	out, scores, _ := orderBalancedCandidates(
		context.Background(), []routing.ChatRouteCandidate{expensive, cheap}, "balanced", reader, BalanceConfig{},
	)
	if math.Abs(scores[1].CostScore-0) > 1e-9 || math.Abs(scores[2].CostScore-80) > 1e-9 {
		t.Fatalf("unexpected cost scores: expensive=%+v cheap=%+v", scores[1], scores[2])
	}
	// expensive: 75, cheap: 95
	if math.Abs(scores[1].FinalScore-75) > 1e-9 || math.Abs(scores[2].FinalScore-95) > 1e-9 {
		t.Fatalf("unexpected totals: expensive=%v cheap=%v", scores[1].FinalScore, scores[2].FinalScore)
	}
	if out[0].Channel.ID != cheap.Channel.ID {
		t.Fatalf("cheaper candidate must lead: order=%v", candidateIDs(out))
	}
}

// TestBalancedErrorRateOutweighsCostWhenSevere 验证严重错误率可以压过成本优势（20% vs 25% 权重下的真实取舍）。
func TestBalancedErrorRateOutweighsCostWhenSevere(t *testing.T) {
	cheapBroken := candidateRoute(1, "openai")
	cheapBroken.CostRatio = 0
	pricierHealthy := candidateRoute(2, "openai")
	pricierHealthy.CostRatio = 0.5

	out, scores, _ := orderBalancedCandidates(
		context.Background(), []routing.ChatRouteCandidate{cheapBroken, pricierHealthy}, "balanced",
		func(_ context.Context, candidate routing.ChatRouteCandidate) (ChannelScoreInputs, error) {
			in := unlimitedConcurrency()
			if candidate.Channel.ID == cheapBroken.Channel.ID {
				in.ErrorAttemptCount, in.ErrorCount = 100, 40 // 40% → 错误率分 0
			}
			return in, nil
		},
		BalanceConfig{},
	)
	// cheapBroken: cost100*25 + conc100*20 + ttft100*25 + err0*20 + prio100*10 = 80
	// pricierHealthy: cost50*25 + 20 + 25 + 20 + 10 → 87.5
	if math.Abs(scores[1].FinalScore-80) > 1e-9 || math.Abs(scores[2].FinalScore-87.5) > 1e-9 {
		t.Fatalf("unexpected totals: broken=%v healthy=%v", scores[1].FinalScore, scores[2].FinalScore)
	}
	if out[0].Channel.ID != pricierHealthy.Channel.ID {
		t.Fatalf("severe error rate must lose to a pricier healthy channel: order=%v", candidateIDs(out))
	}
}

func TestFixedModeKeepsSQLOrderWhileExposingScores(t *testing.T) {
	first := candidateRoute(1, "openai")
	first.CostRatio = 1
	second := candidateRoute(2, "openai")
	second.CostRatio = 0
	out, scores, _ := orderBalancedCandidates(
		context.Background(), []routing.ChatRouteCandidate{first, second}, "fixed", nil, BalanceConfig{},
	)
	if out[0].Channel.ID != first.Channel.ID || out[1].Channel.ID != second.Channel.ID {
		t.Fatalf("fixed order changed: %v", candidateIDs(out))
	}
	// fixed 不重排，但仍暴露完整评分事实供 Admin/trace 解释。
	if scores[first.Channel.ID].CostScore != 0 || math.Abs(scores[first.Channel.ID].FinalScore-75) > 1e-9 {
		t.Fatalf("fixed mode must expose objective facts: %+v", scores[first.Channel.ID])
	}
}

func TestScoreOrderStillAllowsStickyPostOrderPin(t *testing.T) {
	expensive := candidateRoute(1, "openai")
	expensive.CostRatio = 1
	cheap := candidateRoute(2, "openai")
	cheap.CostRatio = 0
	ordered, scores, _ := orderBalancedCandidates(
		context.Background(), []routing.ChatRouteCandidate{expensive, cheap}, "balanced", nil, BalanceConfig{},
	)
	if ordered[0].Channel.ID != cheap.Channel.ID {
		t.Fatalf("test setup expected cheap candidate first, got %d", ordered[0].Channel.ID)
	}
	candidates := []Candidate{
		{Route: ordered[0], Balance: scores[ordered[0].Channel.ID]},
		{Route: ordered[1], Balance: scores[ordered[1].Channel.ID]},
	}
	pinned, found, reordered := pinStickyCandidate(candidates, expensive.Channel.ID)
	if !found || !reordered || pinned[0].Route.Channel.ID != expensive.Channel.ID {
		t.Fatalf("sticky must remain the final ordering step: found=%v reordered=%v order=%v",
			found, reordered, []int64{pinned[0].Route.Channel.ID, pinned[1].Route.Channel.ID})
	}
}

func TestBalancedHalfOpenStaysBehindObjectiveCandidates(t *testing.T) {
	in := []routing.ChatRouteCandidate{candidateRoute(1, "openai"), candidateRoute(2, "openai"), candidateRoute(3, "openai")}
	out, scores, _ := orderBalancedCandidates(context.Background(), in, "balanced",
		func(_ context.Context, candidate routing.ChatRouteCandidate) (ChannelScoreInputs, error) {
			inputs := unlimitedConcurrency()
			inputs.HalfOpen = candidate.Channel.ID == 1
			return inputs, nil
		}, BalanceConfig{})
	if len(out) != 3 || out[2].Channel.ID != 1 || scores[1].FinalScore != 0 || !scores[1].HalfOpen {
		t.Fatalf("half-open candidate must stay behind objective candidates: order=%v score=%+v",
			candidateIDs(out), scores[1])
	}
}

func TestBalancedAllConcurrencyZeroStillUsesDeterministicTieBreak(t *testing.T) {
	in := []routing.ChatRouteCandidate{candidateRoute(1, "openai"), candidateRoute(2, "openai")}
	out, _, allZero := orderBalancedCandidates(context.Background(), in, "balanced",
		func(_ context.Context, _ routing.ChatRouteCandidate) (ChannelScoreInputs, error) {
			return ChannelScoreInputs{
				Concurrency: CapacitySignal{Used: 10, Limit: 10, Known: true}, RuntimeKnown: true,
			}, nil
		}, BalanceConfig{})
	if !allZero || len(out) != 2 || out[0].Channel.ID != 1 {
		t.Fatalf("expected deterministic channel-ID tie break with full fallback, allZero=%v order=%v",
			allZero, candidateIDs(out))
	}
}

// TestBalancedTPMNoLongerAffectsScoring 冻结 §1.2/§19.1：TPM 已彻底移出评分输入。
// ChannelScoreInputs 不再有 TPM 字段，这里通过「相同输入必得相同分」证明观测量无法影响排序。
func TestBalancedTPMNoLongerAffectsScoring(t *testing.T) {
	in := ChannelScoreInputs{
		Concurrency: CapacitySignal{Used: 1, Limit: 10, Known: true}, RuntimeKnown: true,
	}
	first := ScoreChannel(in, 0.3, 10, BalanceConfig{})
	second := ScoreChannel(in, 0.3, 10, BalanceConfig{})
	if first.FinalScore != second.FinalScore || first.CostScore != second.CostScore ||
		first.ConcurrencyScore != second.ConcurrencyScore || first.TTFTScore != second.TTFTScore ||
		first.ErrorScore != second.ErrorScore || first.PriorityScore != second.PriorityScore {
		t.Fatalf("scoring must be a deterministic pure function: %+v vs %+v", first, second)
	}
}

func TestBalancedOrderUsesDescendingObjectiveScore(t *testing.T) {
	in := []routing.ChatRouteCandidate{candidateRoute(3, "openai"), candidateRoute(1, "openai"), candidateRoute(2, "openai")}
	reader := func(_ context.Context, candidate routing.ChatRouteCandidate) (ChannelScoreInputs, error) {
		return ChannelScoreInputs{
			Concurrency:  CapacitySignal{Used: candidate.Channel.ID, Limit: 10, Known: true},
			RuntimeKnown: true,
		}, nil
	}
	ordered, _, _ := orderBalancedCandidates(context.Background(), in, "balanced", reader, BalanceConfig{})
	want := []int64{1, 2, 3}
	for index, channelID := range want {
		if ordered[index].Channel.ID != channelID {
			t.Fatalf("unexpected objective order: got=%v want=%v", candidateIDs(ordered), want)
		}
	}
}

func TestBalancedTieBreakUsesPriorityThenChannelID(t *testing.T) {
	highPriorityLargeID := candidateRoute(9, "openai")
	highPriorityLargeID.Priority = 10
	lowPrioritySmallID := candidateRoute(1, "openai")
	lowPrioritySmallID.Priority = 20
	highPrioritySmallID := candidateRoute(2, "openai")
	highPrioritySmallID.Priority = 10

	out, _, _ := orderBalancedCandidates(
		context.Background(),
		[]routing.ChatRouteCandidate{highPriorityLargeID, lowPrioritySmallID, highPrioritySmallID},
		"balanced",
		nil,
		BalanceConfig{},
	)
	// Priority 10 的两条同分（99），按 Channel ID 升序；Priority 20 的一条（98）在后。
	want := []int64{2, 9, 1}
	for index, channelID := range want {
		if out[index].Channel.ID != channelID {
			t.Fatalf("unexpected deterministic tie break: got=%v want=%v", candidateIDs(out), want)
		}
	}
}

// TestBalancedOrderIsRepeatable 冻结 §7.7：不得加入随机抖动，重复调用必须完全一致。
func TestBalancedOrderIsRepeatable(t *testing.T) {
	in := []routing.ChatRouteCandidate{
		candidateRoute(5, "openai"), candidateRoute(3, "openai"), candidateRoute(7, "openai"),
	}
	reader := func(_ context.Context, candidate routing.ChatRouteCandidate) (ChannelScoreInputs, error) {
		return ChannelScoreInputs{
			Concurrency:  CapacitySignal{Used: candidate.Channel.ID % 3, Limit: 10, Known: true},
			RuntimeKnown: true,
		}, nil
	}
	first, _, _ := orderBalancedCandidates(context.Background(), in, "balanced", reader, BalanceConfig{})
	for i := 0; i < 5; i++ {
		again, _, _ := orderBalancedCandidates(context.Background(), in, "balanced", reader, BalanceConfig{})
		if len(again) != len(first) {
			t.Fatalf("unstable candidate count: %d vs %d", len(again), len(first))
		}
		for index := range first {
			if first[index].Channel.ID != again[index].Channel.ID {
				t.Fatalf("order must be repeatable: first=%v again=%v", candidateIDs(first), candidateIDs(again))
			}
		}
	}
}

func candidateIDs(candidates []routing.ChatRouteCandidate) []int64 {
	ids := make([]int64, len(candidates))
	for index, candidate := range candidates {
		ids[index] = candidate.Channel.ID
	}
	return ids
}

type candidateCapabilityRegistry struct {
	allowed map[int64]bool
}

func (r candidateCapabilityRegistry) FilterCandidates(_ string, candidates []routing.ChatRouteCandidate, _ ...AdapterCapability) []routing.ChatRouteCandidate {
	filtered := make([]routing.ChatRouteCandidate, 0, len(candidates))
	for _, candidate := range candidates {
		if r.allowed[candidate.Channel.ID] {
			filtered = append(filtered, candidate)
		}
	}
	return filtered
}

func TestExecutorPrepareCandidatesFiltersAndUsesConservativeEstimate(t *testing.T) {
	executor := NewExecutor(candidateCapabilityRegistry{
		allowed: map[int64]bool{1: true, 3: true},
	})
	var estimated []int64

	plan, err := executor.PrepareCandidates(context.Background(), PrepareCandidatesParams{
		Protocol: "openai",
		Candidates: []routing.ChatRouteCandidate{
			candidateRoute(1, "first"),
			candidateRoute(2, "second"),
			candidateRoute(3, "third"),
			candidateRoute(4, "filtered-by-capability"),
		},
		Capabilities: []AdapterCapability{
			AdapterCapabilityNonStream,
			AdapterCapabilityInputTokenizer,
		},
		EstimateInputTokens: func(_ context.Context, candidate routing.ChatRouteCandidate) (int64, error) {
			estimated = append(estimated, candidate.Channel.ID)
			switch candidate.Channel.ID {
			case 1:
				return 20, nil
			case 3:
				return 80, nil
			default:
				return 0, nil
			}
		},
	})
	if err != nil {
		t.Fatalf("PrepareCandidates returned error: %v", err)
	}

	if len(plan.Candidates) != 2 {
		t.Fatalf("expected 2 candidates, got %d", len(plan.Candidates))
	}
	if plan.Candidates[0].Route.Channel.ID != 1 || plan.Candidates[0].RouteIndex != 0 {
		t.Fatalf("unexpected first candidate: %#v", plan.Candidates[0])
	}
	if plan.Candidates[1].Route.Channel.ID != 3 || plan.Candidates[1].RouteIndex != 2 {
		t.Fatalf("unexpected second candidate: %#v", plan.Candidates[1])
	}
	if plan.ConservativeInputTokens != 80 {
		t.Fatalf("expected conservative estimate %d, got %d", int64(80), plan.ConservativeInputTokens)
	}
	if len(estimated) != 2 || estimated[0] != 1 || estimated[1] != 3 {
		t.Fatalf("unexpected estimated candidates: %#v", estimated)
	}
}

func TestExecutorPrepareCandidatesReturnsNoAvailableChannelAfterFiltering(t *testing.T) {
	executor := NewExecutor(candidateCapabilityRegistry{
		allowed: map[int64]bool{},
	})

	_, err := executor.PrepareCandidates(context.Background(), PrepareCandidatesParams{
		Protocol:   "openai",
		Candidates: []routing.ChatRouteCandidate{candidateRoute(1, "first")},
		EstimateInputTokens: func(context.Context, routing.ChatRouteCandidate) (int64, error) {
			return 10, nil
		},
	})
	if !errors.Is(err, routing.ErrNoAvailableChannel) {
		t.Fatalf("expected ErrNoAvailableChannel, got %v", err)
	}
	if got := failure.CodeOf(err); got != failure.CodeRoutingNoAvailableChannel {
		t.Fatalf("expected code %q, got %q", failure.CodeRoutingNoAvailableChannel, got)
	}
}

func TestExecutorPrepareCandidatesAggregatesCooldownOnlyAsRateLimit(t *testing.T) {
	executor := NewExecutor(candidateCapabilityRegistry{allowed: map[int64]bool{1: true, 2: true}})
	ctx := requestadmission.ContextWithRequestSession(context.Background(), &candidateSnapshotSession{
		result: breakerstore.SnapshotManyResult{Candidates: []breakerstore.CandidateSnapshot{
			{Status: breakerstore.CandidateSnapshotRateLimited, CooldownRemainingMs: 2_001},
			{Status: breakerstore.CandidateSnapshotRateLimited, CooldownRemainingMs: 1_001},
		}},
	})

	plan, err := executor.PrepareCandidates(ctx, PrepareCandidatesParams{
		Protocol:   "openai",
		Candidates: []routing.ChatRouteCandidate{candidateRoute(1, "first"), candidateRoute(2, "second")},
		EstimateInputTokens: func(context.Context, routing.ChatRouteCandidate) (int64, error) {
			return 10, nil
		},
	})
	if failure.CodeOf(err) != failure.CodeGatewayChannelRateLimited {
		t.Fatalf("expected all-cooldown rate limit, got %v", err)
	}
	if got := failureInt64Field(err, "retry_after_ms"); got != 1_001 {
		t.Fatalf("expected earliest provable cooldown 1001ms, got %d", got)
	}
	if len(plan.Candidates) != 0 || len(plan.Excluded) != 2 {
		t.Fatalf("all-cooldown plan must retain both exclusions: %#v", plan)
	}
	for _, excluded := range plan.Excluded {
		if excluded.Reason != string(breakerstore.CandidateSnapshotRateLimited) || excluded.Balance.CooldownRemainingMs <= 0 {
			t.Fatalf("all-cooldown exclusion lost runtime facts: %#v", excluded)
		}
	}
}

func TestExecutorPrepareCandidatesKeepsMixedExclusionsUnavailable(t *testing.T) {
	executor := NewExecutor(candidateCapabilityRegistry{allowed: map[int64]bool{1: true, 2: true}})
	ctx := requestadmission.ContextWithRequestSession(context.Background(), &candidateSnapshotSession{
		result: breakerstore.SnapshotManyResult{Candidates: []breakerstore.CandidateSnapshot{
			{Status: breakerstore.CandidateSnapshotRateLimited, CooldownRemainingMs: 1_000},
			{Status: breakerstore.CandidateSnapshotOpen},
		}},
	})

	_, err := executor.PrepareCandidates(ctx, PrepareCandidatesParams{
		Protocol:   "openai",
		Candidates: []routing.ChatRouteCandidate{candidateRoute(1, "first"), candidateRoute(2, "second")},
		EstimateInputTokens: func(context.Context, routing.ChatRouteCandidate) (int64, error) {
			return 10, nil
		},
	})
	if failure.CodeOf(err) != failure.CodeRoutingNoAvailableChannel {
		t.Fatalf("mixed cooldown/breaker reasons must map to unavailable, got %v", err)
	}
}

type candidateSnapshotSession struct {
	result  breakerstore.SnapshotManyResult
	windows map[int64]breakerstore.ChannelSampleWindow
}

func (*candidateSnapshotSession) BindAttempt(*breakerstore.AcquireAttemptInput) error { return nil }

func (s *candidateSnapshotSession) SnapshotMany(context.Context, int64, []breakerstore.SnapshotCandidateInput) (breakerstore.SnapshotManyResult, error) {
	return s.result, nil
}

func (s *candidateSnapshotSession) AggregateChannelSamples(context.Context, []int64) (map[int64]breakerstore.ChannelSampleWindow, error) {
	return s.windows, nil
}

func failureInt64Field(err error, key string) int64 {
	for _, field := range failure.FieldsOf(err) {
		if field.Key == key {
			value, _ := field.Value.(int64)
			return value
		}
	}
	return 0
}

func TestExecutorPrepareCandidatesWrapsEstimatorError(t *testing.T) {
	estimateErr := errors.New("tokenizer unavailable")
	executor := NewExecutor(candidateCapabilityRegistry{
		allowed: map[int64]bool{1: true},
	})

	_, err := executor.PrepareCandidates(context.Background(), PrepareCandidatesParams{
		Protocol:   "openai",
		Candidates: []routing.ChatRouteCandidate{candidateRoute(1, "first")},
		EstimateInputTokens: func(context.Context, routing.ChatRouteCandidate) (int64, error) {
			return 0, estimateErr
		},
	})
	if !errors.Is(err, estimateErr) {
		t.Fatalf("expected estimator error cause, got %v", err)
	}
	if got := failure.CodeOf(err); got != failure.CodeGatewayInputTokenEstimateFailed {
		t.Fatalf("expected code %q, got %q", failure.CodeGatewayInputTokenEstimateFailed, got)
	}
}

func TestExecutorPrepareCandidatesRejectsNegativeEstimate(t *testing.T) {
	executor := NewExecutor(candidateCapabilityRegistry{
		allowed: map[int64]bool{1: true},
	})

	_, err := executor.PrepareCandidates(context.Background(), PrepareCandidatesParams{
		Protocol:   "openai",
		Candidates: []routing.ChatRouteCandidate{candidateRoute(1, "first")},
		EstimateInputTokens: func(context.Context, routing.ChatRouteCandidate) (int64, error) {
			return -1, nil
		},
	})
	if !errors.Is(err, ErrCandidateInputTokenEstimateInvalid) {
		t.Fatalf("expected ErrCandidateInputTokenEstimateInvalid, got %v", err)
	}
	if got := failure.CodeOf(err); got != failure.CodeGatewayInputTokenEstimateFailed {
		t.Fatalf("expected code %q, got %q", failure.CodeGatewayInputTokenEstimateFailed, got)
	}
}

func candidateRoute(channelID int64, adapterKey string) routing.ChatRouteCandidate {
	return routing.ChatRouteCandidate{
		AdapterKey: adapterKey,
		Channel:    channel.Runtime{ID: channelID},
	}
}
