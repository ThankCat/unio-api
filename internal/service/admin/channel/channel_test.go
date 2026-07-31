package channel_test

import (
	"testing"

	"github.com/ThankCat/unio-gateway/internal/service/admin/channel"
)

func TestCanonicalCapacityPayloadKeepsNullAndZeroDistinct(t *testing.T) {
	zero := int64(0)
	inherited, err := channel.CanonicalCapacityPayload(channel.ChannelCapacity{})
	if err != nil {
		t.Fatal(err)
	}
	unlimited, err := channel.CanonicalCapacityPayload(channel.ChannelCapacity{Concurrency: &zero})
	if err != nil {
		t.Fatal(err)
	}
	if inherited == unlimited {
		t.Fatalf("inherited and unlimited payloads must differ: %s", inherited)
	}
}

func TestCanonicalCapacityPayloadRejectsNegative(t *testing.T) {
	negative := int64(-1)
	if _, err := channel.CanonicalCapacityPayload(channel.ChannelCapacity{Concurrency: &negative}); err == nil {
		t.Fatal("negative concurrency unexpectedly accepted")
	}
}
