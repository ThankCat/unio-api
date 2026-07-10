package opsutil

import "testing"

// TestHealthBucketParameterized 验证参数化后与迁移前 0.95/0.80 硬编码口径等价,
// 且阈值可调(batch2:阈值来自运行时配置 admin_backend.channel_health_thresholds)。
func TestHealthBucketParameterized(t *testing.T) {
	const healthy, degraded = 0.95, 0.80
	cases := []struct {
		name             string
		succeeded, total int64
		want             string
	}{
		{"no data", 0, 0, "no_data"},
		{"exactly healthy", 95, 100, "healthy"},
		{"just below healthy", 94, 100, "degraded"},
		{"exactly degraded", 80, 100, "degraded"},
		{"just below degraded", 79, 100, "unhealthy"},
		{"all failed", 0, 10, "unhealthy"},
		{"all succeeded", 10, 10, "healthy"},
	}
	for _, c := range cases {
		if got := HealthBucket(c.succeeded, c.total, healthy, degraded); got != c.want {
			t.Errorf("%s: HealthBucket(%d,%d) = %q, want %q", c.name, c.succeeded, c.total, got, c.want)
		}
	}

	// 阈值热调生效:同一数据在更严阈值下降档。
	if got := HealthBucket(95, 100, 0.99, 0.90); got != "degraded" {
		t.Errorf("stricter healthy threshold: got %q, want degraded", got)
	}
	if got := HealthBucket(85, 100, 0.99, 0.90); got != "unhealthy" {
		t.Errorf("stricter degraded threshold: got %q, want unhealthy", got)
	}
}
