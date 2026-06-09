package capability

import (
	"reflect"
	"testing"
)

// TestSetZeroValueReadable 验证零值 Set 可安全读取。
func TestSetZeroValueReadable(t *testing.T) {
	var s Set

	if s.Len() != 0 {
		t.Fatalf("zero set len = %d, want 0", s.Len())
	}
	if s.Has(KeyStream) {
		t.Fatal("zero set should not contain any key")
	}
	if len(s.Keys()) != 0 {
		t.Fatalf("zero set keys = %v, want empty", s.Keys())
	}
}

// TestSetAddDedupAndIgnoreEmpty 验证 Add 去重且忽略空 key。
func TestSetAddDedupAndIgnoreEmpty(t *testing.T) {
	var s Set
	s.Add(KeyStream)
	s.Add(KeyStream)
	s.Add("")

	if s.Len() != 1 {
		t.Fatalf("len = %d, want 1", s.Len())
	}
	if !s.Has(KeyStream) {
		t.Fatalf("expected %s present", KeyStream)
	}
}

// TestSetKeysSorted 验证 Keys 返回升序稳定序列。
func TestSetKeysSorted(t *testing.T) {
	s := NewSet(KeyToolsFunction, KeyTextOutput, KeyStream, KeyImageInput)

	got := s.Keys()
	want := []Key{KeyImageInput, KeyStream, KeyTextOutput, KeyToolsFunction}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Keys = %v, want %v", got, want)
	}
}

// TestSetEqual 验证集合相等比较与顺序无关。
func TestSetEqual(t *testing.T) {
	a := NewSet(KeyStream, KeyTextOutput)
	b := NewSet(KeyTextOutput, KeyStream)
	c := NewSet(KeyStream)

	if !a.Equal(b) {
		t.Fatal("a and b should be equal regardless of insertion order")
	}
	if a.Equal(c) {
		t.Fatal("a and c should not be equal")
	}
}
