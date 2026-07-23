package httpx

import (
	"net/http/httptest"
	"testing"
	"time"
)

func TestSetRetryAfterRoundsUpAndCaps(t *testing.T) {
	tests := []struct {
		duration time.Duration
		want     string
	}{
		{duration: 0, want: ""},
		{duration: time.Millisecond, want: "1"},
		{duration: 1001 * time.Millisecond, want: "2"},
		{duration: 10 * time.Minute, want: "300"},
	}
	for _, test := range tests {
		recorder := httptest.NewRecorder()
		SetRetryAfter(recorder, test.duration)
		if got := recorder.Header().Get("Retry-After"); got != test.want {
			t.Fatalf("duration %v: Retry-After=%q want %q", test.duration, got, test.want)
		}
	}
}
