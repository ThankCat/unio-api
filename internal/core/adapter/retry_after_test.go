package adapter

import (
	"net/http"
	"testing"
	"time"
)

func TestParseRetryAfterValueDeltaSeconds(t *testing.T) {
	now := time.Date(2026, 6, 28, 12, 0, 0, 0, time.UTC)

	if got := parseRetryAfterValue("30", now); got != 30*time.Second {
		t.Fatalf("delta-seconds: want 30s, got %s", got)
	}
	if got := parseRetryAfterValue("1", now); got != time.Second {
		t.Fatalf("delta-seconds: want 1s, got %s", got)
	}
}

func TestParseRetryAfterValueNonPositiveOrInvalid(t *testing.T) {
	now := time.Date(2026, 6, 28, 12, 0, 0, 0, time.UTC)

	cases := []string{"", "0", "-5", "soon", "abc", "3.5"}
	for _, c := range cases {
		if got := parseRetryAfterValue(c, now); got != 0 {
			t.Fatalf("value %q: want 0, got %s", c, got)
		}
	}
}

func TestParseRetryAfterValueHTTPDate(t *testing.T) {
	now := time.Date(2026, 6, 28, 12, 0, 0, 0, time.UTC)
	future := now.Add(90 * time.Second).UTC().Format(http.TimeFormat)

	got := parseRetryAfterValue(future, now)
	// HTTP-date 精度到秒，允许 1s 容差。
	if got < 89*time.Second || got > 90*time.Second {
		t.Fatalf("http-date: want ~90s, got %s", got)
	}

	past := now.Add(-90 * time.Second).UTC().Format(http.TimeFormat)
	if got := parseRetryAfterValue(past, now); got != 0 {
		t.Fatalf("http-date in past: want 0, got %s", got)
	}
}

func TestParseRetryAfterValueClampsToMax(t *testing.T) {
	now := time.Date(2026, 6, 28, 12, 0, 0, 0, time.UTC)
	huge := int(((48 * time.Hour) / time.Second))

	got := parseRetryAfterValue(itoa(huge), now)
	if got != maxParsedRetryAfter {
		t.Fatalf("clamp: want %s, got %s", maxParsedRetryAfter, got)
	}
}

func TestParseRetryAfterHeaderNilAndMissing(t *testing.T) {
	if got := ParseRetryAfterHeader(nil); got != 0 {
		t.Fatalf("nil header: want 0, got %s", got)
	}
	if got := ParseRetryAfterHeader(http.Header{}); got != 0 {
		t.Fatalf("missing header: want 0, got %s", got)
	}

	h := http.Header{}
	h.Set("Retry-After", "12")
	if got := ParseRetryAfterHeader(h); got != 12*time.Second {
		t.Fatalf("header 12: want 12s, got %s", got)
	}
}

// itoa 避免在测试里引入 strconv，仅用于本文件构造大整数字符串。
func itoa(v int) string {
	if v == 0 {
		return "0"
	}
	neg := v < 0
	if neg {
		v = -v
	}
	var buf [20]byte
	i := len(buf)
	for v > 0 {
		i--
		buf[i] = byte('0' + v%10)
		v /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
