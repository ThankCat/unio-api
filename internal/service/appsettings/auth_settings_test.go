package appsettings

import (
	"encoding/json"
	"testing"
)

func TestVerificationRateLimitDefaultsAreRegisteredAndValid(t *testing.T) {
	definition, ok := DefaultRegistry().Get(AuthVerificationRateLimitsKey)
	if !ok {
		t.Fatal("authentication rate-limit setting is not registered")
	}
	if definition.Category != "auth" || !definition.HotReload {
		t.Fatalf("unexpected definition: %+v", definition)
	}
	if err := definition.Validate(definition.Default); err != nil {
		t.Fatalf("default validation failed: %v", err)
	}
}

func TestVerificationRateLimitSettingsRejectInvalidRules(t *testing.T) {
	value := DefaultVerificationRateLimitSettings()
	value.SendEmailPurpose[0].Limit = 0
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DecodeVerificationRateLimitSettings(raw); err == nil {
		t.Fatal("expected invalid zero limit to fail")
	}
}

func TestPasswordLoginRateLimitDefaultsAreRegisteredAndValid(t *testing.T) {
	definition, ok := DefaultRegistry().Get(AuthPasswordLoginRateLimitsKey)
	if !ok {
		t.Fatal("password login rate-limit setting is not registered")
	}
	if definition.Category != "auth" || !definition.HotReload {
		t.Fatalf("unexpected definition: %+v", definition)
	}
	if err := definition.Validate(definition.Default); err != nil {
		t.Fatalf("default validation failed: %v", err)
	}
}

func TestPasswordLoginRateLimitSettingsRejectInvalidRules(t *testing.T) {
	value := DefaultPasswordLoginRateLimitSettings()
	value.EmailIP[0].Limit = 0
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DecodePasswordLoginRateLimitSettings(raw); err == nil {
		t.Fatal("expected invalid zero limit to fail")
	}
}
