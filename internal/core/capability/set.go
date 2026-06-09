package capability

import "sort"

// Set 是去重的 capability key 集合，Keys 返回稳定升序序列。
//
// 零值 Set 可直接用于读取（Has/Len/Keys 返回空集语义）；写入请用 NewSet 或 Add。
// Set 内部持有 map，按值传递会共享底层 map，约定用法是「构造一次、只读消费」。
type Set struct {
	members map[Key]struct{}
}

// NewSet 用给定 key 构造集合，重复 key 自动去重，空 key 忽略。
func NewSet(keys ...Key) Set {
	s := Set{}
	for _, key := range keys {
		s.Add(key)
	}

	return s
}

// Add 向集合加入一个 key；空 key 忽略，重复 key 幂等。
func (s *Set) Add(key Key) {
	if key == "" {
		return
	}
	if s.members == nil {
		s.members = make(map[Key]struct{})
	}

	s.members[key] = struct{}{}
}

// Has 判断集合是否包含某 key。
func (s Set) Has(key Key) bool {
	_, ok := s.members[key]
	return ok
}

// Len 返回集合元素数量。
func (s Set) Len() int {
	return len(s.members)
}

// Keys 返回升序排序的全部 key，保证调用稳定可比较。
func (s Set) Keys() []Key {
	keys := make([]Key, 0, len(s.members))
	for key := range s.members {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i] < keys[j] })

	return keys
}

// StringKeys 返回升序排序的全部 key 的字符串形式，供持久化（如 TEXT[] 审计列）使用。
func (s Set) StringKeys() []string {
	keys := s.Keys()
	out := make([]string, len(keys))
	for i, key := range keys {
		out[i] = string(key)
	}

	return out
}

// Equal 判断两个集合成员是否完全相同。
func (s Set) Equal(other Set) bool {
	if len(s.members) != len(other.members) {
		return false
	}
	for key := range s.members {
		if _, ok := other.members[key]; !ok {
			return false
		}
	}

	return true
}
