package modelops

import "testing"

func TestCacheReadRate(t *testing.T) {
	tests := []struct {
		name      string
		cacheRead int64
		input     int64
		want      float64
	}{
		{name: "read tokens only", cacheRead: 25, input: 100, want: 0.25},
		{name: "zero input", cacheRead: 25, input: 0, want: 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := cacheReadRate(tt.cacheRead, tt.input); got != tt.want {
				t.Fatalf("cacheReadRate(%d, %d) = %v, want %v", tt.cacheRead, tt.input, got, tt.want)
			}
		})
	}
}
