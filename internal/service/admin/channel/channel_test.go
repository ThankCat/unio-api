package channel_test

import (
	"testing"

	"github.com/ThankCat/unio-gateway/internal/service/admin/channel"
)

func TestCanonicalAdmissionLimitsPayloadKeepsNullAndZeroDistinct(t *testing.T) {
	zero := int64(0)
	inherited, err := channel.CanonicalAdmissionLimitsPayload(channel.AdmissionLimits{})
	if err != nil {
		t.Fatal(err)
	}
	unlimited, err := channel.CanonicalAdmissionLimitsPayload(channel.AdmissionLimits{RPM: &zero})
	if err != nil {
		t.Fatal(err)
	}
	if inherited == unlimited {
		t.Fatalf("inherited and unlimited payloads must differ: %s", inherited)
	}
}

func TestCanonicalAdmissionLimitsPayloadRejectsNegative(t *testing.T) {
	negative := int64(-1)
	if _, err := channel.CanonicalAdmissionLimitsPayload(channel.AdmissionLimits{Concurrency: &negative}); err == nil {
		t.Fatal("negative concurrency unexpectedly accepted")
	}
}
