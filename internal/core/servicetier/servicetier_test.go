package servicetier

import "testing"

func TestNormalizeOpenAIRequest(t *testing.T) {
	tests := []struct {
		name         string
		raw          *string
		wantTier     Tier
		wantUpstream string
		wantErr      bool
	}{
		{name: "missing", wantTier: TierStandard, wantUpstream: "default"},
		{name: "auto", raw: tierString("auto"), wantTier: TierStandard, wantUpstream: "default"},
		{name: "default", raw: tierString("default"), wantTier: TierStandard, wantUpstream: "default"},
		{name: "fast", raw: tierString("fast"), wantTier: TierFast, wantUpstream: "priority"},
		{name: "priority", raw: tierString("priority"), wantTier: TierFast, wantUpstream: "priority"},
		{name: "internal standard is not public", raw: tierString("standard"), wantErr: true},
		{name: "case sensitive", raw: tierString("Fast"), wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := NormalizeOpenAIRequest(tt.raw)
			if (err != nil) != tt.wantErr {
				t.Fatalf("NormalizeOpenAIRequest() error = %v, wantErr %v", err, tt.wantErr)
			}
			if got.Tier != tt.wantTier || got.UpstreamRaw != tt.wantUpstream {
				t.Fatalf("NormalizeOpenAIRequest() = %+v, want tier=%q upstream=%q", got, tt.wantTier, tt.wantUpstream)
			}
		})
	}
}

func TestResolveOpenAIForwardRequest(t *testing.T) {
	tests := []struct {
		name         string
		requested    Tier
		supportsFast bool
		wantTier     Tier
		wantUpstream string
	}{
		{name: "standard unsupported", requested: TierStandard, wantTier: TierStandard, wantUpstream: "default"},
		{name: "standard supported", requested: TierStandard, supportsFast: true, wantTier: TierStandard, wantUpstream: "default"},
		{name: "fast unsupported", requested: TierFast, wantTier: TierStandard, wantUpstream: "default"},
		{name: "fast supported", requested: TierFast, supportsFast: true, wantTier: TierFast, wantUpstream: "priority"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ResolveOpenAIForwardRequest(tt.requested, tt.supportsFast)
			if got.Tier != tt.wantTier || got.UpstreamRaw != tt.wantUpstream {
				t.Fatalf("ResolveOpenAIForwardRequest() = %+v, want tier=%q upstream=%q", got, tt.wantTier, tt.wantUpstream)
			}
		})
	}
}

func TestResolveOpenAIResponse(t *testing.T) {
	tests := []struct {
		name           string
		raw            *string
		wantActual     Tier
		wantSettled    Tier
		wantResolution Resolution
	}{
		{name: "missing", wantSettled: TierStandard, wantResolution: ResolutionStandardFallbackMissing},
		{name: "default", raw: tierString("default"), wantActual: TierStandard, wantSettled: TierStandard, wantResolution: ResolutionUpstreamResponse},
		{name: "priority", raw: tierString("priority"), wantActual: TierFast, wantSettled: TierFast, wantResolution: ResolutionUpstreamResponse},
		{name: "unknown", raw: tierString("future"), wantSettled: TierStandard, wantResolution: ResolutionStandardFallbackUnknown},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ResolveOpenAIResponse(tt.raw)
			if got.Actual != tt.wantActual || got.Settled != tt.wantSettled || got.Resolution != tt.wantResolution {
				t.Fatalf("ResolveOpenAIResponse() = %+v", got)
			}
		})
	}
}

func tierString(value string) *string { return &value }
