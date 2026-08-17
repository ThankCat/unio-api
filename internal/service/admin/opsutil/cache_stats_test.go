package opsutil

import "testing"

func TestCacheStatsFrom(t *testing.T) {
	tests := []struct {
		name          string
		aggregate     CacheAggregate
		wantStatus    CacheStatus
		wantReadRate  *float64
		wantWriteRate *float64
	}{
		{
			name: "available keeps read and write separate",
			aggregate: CacheAggregate{
				UncachedInput:      380,
				CacheReadInput:     100,
				CacheWrite30mInput: 20,
				UsageRecords:       3,
				EvaluableRecords:   3,
			},
			wantStatus:    CacheStatusAvailable,
			wantReadRate:  float64Ptr(0.2),
			wantWriteRate: float64Ptr(0.04),
		},
		{
			name:       "no usage is no data",
			aggregate:  CacheAggregate{},
			wantStatus: CacheStatusNoData,
		},
		{
			name: "all read dimensions not applicable",
			aggregate: CacheAggregate{
				UsageRecords:             2,
				ReadNotApplicableRecords: 2,
			},
			wantStatus: CacheStatusNotApplicable,
		},
		{
			name: "usage without evaluable input is unknown",
			aggregate: CacheAggregate{
				UsageRecords: 2,
			},
			wantStatus: CacheStatusUnknown,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := CacheStatsFrom(tt.aggregate)
			if got.Status != tt.wantStatus {
				t.Fatalf("status = %q, want %q", got.Status, tt.wantStatus)
			}
			assertOptionalRate(t, "read", got.ReadRate, tt.wantReadRate)
			assertOptionalRate(t, "write", got.WriteRate, tt.wantWriteRate)
		})
	}
}

func float64Ptr(v float64) *float64 {
	return &v
}

func assertOptionalRate(t *testing.T, name string, got, want *float64) {
	t.Helper()
	if got == nil || want == nil {
		if got != nil || want != nil {
			t.Fatalf("%s rate = %v, want %v", name, got, want)
		}
		return
	}
	if *got != *want {
		t.Fatalf("%s rate = %v, want %v", name, *got, *want)
	}
}
