package channelmodelinventory

import "testing"

func TestNormalizeBatchBindings(t *testing.T) {
	got, err := normalizeBatchBindings([]BatchBindingInput{
		{ModelID: 2, UpstreamModel: "  provider-a  "},
		{ModelID: 2, UpstreamModel: "provider-a"},
		{ModelID: 3, UpstreamModel: "provider-b"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].UpstreamModel != "provider-a" {
		t.Fatalf("normalized=%+v", got)
	}
}

func TestNormalizeBatchBindingsRejectsConflictingDuplicate(t *testing.T) {
	_, err := normalizeBatchBindings([]BatchBindingInput{
		{ModelID: 2, UpstreamModel: "provider-a"},
		{ModelID: 2, UpstreamModel: "provider-b"},
	})
	if err == nil {
		t.Fatal("expected conflicting duplicate to fail")
	}
}
