package route

import "testing"

func TestValidateRateLimitsIncludesConcurrency(t *testing.T) {
	zero := int64(0)
	positive := int64(4)
	negative := int64(-1)

	if err := validateRateLimits(nil, nil, nil, nil); err != nil {
		t.Fatalf("inherit limits: %v", err)
	}
	if err := validateRateLimits(&zero, &zero, &zero, &positive); err != nil {
		t.Fatalf("valid limits: %v", err)
	}
	if err := validateRateLimits(nil, nil, nil, &negative); err == nil {
		t.Fatal("negative concurrency limit unexpectedly accepted")
	}
}
