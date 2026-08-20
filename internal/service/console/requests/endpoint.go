package requests

var publicEndpoints = map[string]string{
	"chat_completions": "/v1/chat/completions",
	"messages":         "/v1/messages",
	"responses":        "/v1/responses",
}

var internalEndpoints = map[string]string{
	"/v1/chat/completions": "chat_completions",
	"/v1/messages":         "messages",
	"/v1/responses":        "responses",
}

// PublicEndpoint 把存储的端点枚举转成客户可见路径。
func PublicEndpoint(endpoint string) string {
	if mapped, ok := publicEndpoints[endpoint]; ok {
		return mapped
	}
	return endpoint
}

// KnownPublicEndpoint 判断筛选值是否为客户可见的公网路径。
func KnownPublicEndpoint(endpoint string) bool {
	_, ok := internalEndpoints[endpoint]
	return ok
}

// InternalEndpoints 把客户筛选值转成存储枚举；已是枚举的保持原样。
func InternalEndpoints(values []string) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		if mapped, ok := internalEndpoints[value]; ok {
			out = append(out, mapped)
			continue
		}
		out = append(out, value)
	}
	return out
}
