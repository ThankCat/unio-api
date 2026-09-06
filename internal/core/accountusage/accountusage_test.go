package accountusage

import (
	"testing"
	"time"
)

func int32Ptr(v int32) *int32 { return &v }

func TestResolveThresholdInheritance(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name        string
		account     *int32
		channel     *int32
		global      int32
		wantPercent int32
		wantSource  ThresholdSource
	}{
		{name: "账号覆写优先", account: int32Ptr(70), channel: int32Ptr(80), global: 90, wantPercent: 70, wantSource: ThresholdSourceAccount},
		{name: "账号 NULL 继承渠道", channel: int32Ptr(80), global: 90, wantPercent: 80, wantSource: ThresholdSourceChannel},
		{name: "账号与渠道都 NULL 继承全局", global: 90, wantPercent: 90, wantSource: ThresholdSourceGlobal},
		{name: "非法账号值按 NULL 处理", account: int32Ptr(0), channel: int32Ptr(85), global: 90, wantPercent: 85, wantSource: ThresholdSourceChannel},
		{name: "非法渠道值按 NULL 处理", channel: int32Ptr(101), global: 95, wantPercent: 95, wantSource: ThresholdSourceGlobal},
		{name: "全局非法回落 100", global: 0, wantPercent: 100, wantSource: ThresholdSourceGlobal},
		{name: "边界 1 与 100 合法", account: int32Ptr(1), channel: int32Ptr(100), global: 90, wantPercent: 1, wantSource: ThresholdSourceAccount},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := ResolveThreshold(tc.account, tc.channel, tc.global)
			if got.Percent != tc.wantPercent || got.Source != tc.wantSource {
				t.Fatalf("ResolveThreshold = %+v, want percent=%d source=%s", got, tc.wantPercent, tc.wantSource)
			}
		})
	}
}

func TestParseSnapshot(t *testing.T) {
	t.Parallel()
	if _, ok := ParseSnapshot(nil); ok {
		t.Fatal("empty snapshot should not parse")
	}
	if _, ok := ParseSnapshot([]byte("{broken")); ok {
		t.Fatal("broken snapshot should not parse")
	}
	raw := []byte(`{"primary":{"used_percent":90,"window_minutes":300,"reset_at":1788688116},"secondary":{"used_percent":69,"window_minutes":10080,"reset_at":1789000000},"plan_type":"plus","captured_at":"2026-09-06T07:12:31Z"}`)
	snapshot, ok := ParseSnapshot(raw)
	if !ok {
		t.Fatal("valid snapshot should parse")
	}
	if snapshot.Primary == nil || snapshot.Primary.UsedPercent != 90 || snapshot.Primary.ResetAt != 1788688116 {
		t.Fatalf("primary = %+v", snapshot.Primary)
	}
	if snapshot.Secondary == nil || snapshot.Secondary.WindowMinutes != 10080 {
		t.Fatalf("secondary = %+v", snapshot.Secondary)
	}
	if snapshot.PlanType != "plus" || snapshot.CapturedAt.IsZero() {
		t.Fatalf("plan_type/captured_at = %q/%v", snapshot.PlanType, snapshot.CapturedAt)
	}
}

func TestEvaluate(t *testing.T) {
	t.Parallel()
	now := time.Unix(1_700_000_000, 0)
	future := now.Unix() + 3600
	past := now.Unix() - 1

	cases := []struct {
		name      string
		snapshot  Snapshot
		threshold int32
		want      Decision
	}{
		{
			name:      "无观测不暂停",
			snapshot:  Snapshot{},
			threshold: 90,
		},
		{
			name:      "主窗口低于阈值不暂停",
			snapshot:  Snapshot{Primary: &Window{UsedPercent: 89.9, ResetAt: future}},
			threshold: 90,
		},
		{
			name:      "主窗口达阈值暂停到重置时刻",
			snapshot:  Snapshot{Primary: &Window{UsedPercent: 90, ResetAt: future}},
			threshold: 90,
			want:      Decision{Paused: true, Window: WindowPrimary, UsedPercent: 90, ResetAtUnix: future},
		},
		{
			name:      "阈值提高到 100 后 90% 不再暂停",
			snapshot:  Snapshot{Primary: &Window{UsedPercent: 90, ResetAt: future}},
			threshold: 100,
		},
		{
			name:      "阈值 100 时 100% 暂停",
			snapshot:  Snapshot{Primary: &Window{UsedPercent: 100, ResetAt: future}},
			threshold: 100,
			want:      Decision{Paused: true, Window: WindowPrimary, UsedPercent: 100, ResetAtUnix: future},
		},
		{
			name:      "主窗口已重置只看次窗口",
			snapshot:  Snapshot{Primary: &Window{UsedPercent: 95, ResetAt: past}, Secondary: &Window{UsedPercent: 92, ResetAt: future}},
			threshold: 90,
			want:      Decision{Paused: true, Window: WindowSecondary, UsedPercent: 92, ResetAtUnix: future},
		},
		{
			name:      "主窗口优先于次窗口",
			snapshot:  Snapshot{Primary: &Window{UsedPercent: 95, ResetAt: future}, Secondary: &Window{UsedPercent: 99, ResetAt: future + 10}},
			threshold: 90,
			want:      Decision{Paused: true, Window: WindowPrimary, UsedPercent: 95, ResetAtUnix: future},
		},
		{
			name:      "缺失重置时刻不暂停",
			snapshot:  Snapshot{Primary: &Window{UsedPercent: 95}},
			threshold: 90,
		},
		{
			name:      "非法阈值按 100 判定",
			snapshot:  Snapshot{Primary: &Window{UsedPercent: 95, ResetAt: future}},
			threshold: 0,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := Evaluate(tc.snapshot, tc.threshold, now)
			if got != tc.want {
				t.Fatalf("Evaluate = %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestDecisionRemainingMs(t *testing.T) {
	t.Parallel()
	now := time.Unix(1_700_000_000, 0)
	paused := Decision{Paused: true, ResetAtUnix: now.Unix() + 90}
	if got := paused.RemainingMs(now); got != 90_000 {
		t.Fatalf("RemainingMs = %d, want 90000", got)
	}
	if got := (Decision{}).RemainingMs(now); got != 0 {
		t.Fatalf("unpaused RemainingMs = %d, want 0", got)
	}
	expired := Decision{Paused: true, ResetAtUnix: now.Unix() - 1}
	if got := expired.RemainingMs(now); got != 0 {
		t.Fatalf("expired RemainingMs = %d, want 0", got)
	}
}
