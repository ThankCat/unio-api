package gateway

import "encoding/json"

func jsonContent(s string) json.RawMessage {
	b, _ := json.Marshal(s)
	return b
}
