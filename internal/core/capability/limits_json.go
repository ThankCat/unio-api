package capability

import (
	"encoding/json"
	"strings"
)

// LimitsJSONPresent 判断 JSON limits 是否携带有效内容。
// encoding/json 会把 JSON null 解成字面量 []byte("null")，不能当作有限制。
func LimitsJSONPresent(raw json.RawMessage) bool {
	s := strings.TrimSpace(string(raw))
	return s != "" && s != "null"
}

// NormalizeLimitsJSON 把空白/null 归一为 nil，供写库时落 NULL。
func NormalizeLimitsJSON(raw json.RawMessage) json.RawMessage {
	if !LimitsJSONPresent(raw) {
		return nil
	}
	return raw
}
