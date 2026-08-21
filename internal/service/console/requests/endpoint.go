package requests

import "strings"

var publicEndpoints = map[string]string{
	"chat_completions": "/chat/completions",
	"messages":         "/messages",
	"responses":        "/responses",
}

var internalEndpoints = map[string]string{
	"/chat/completions":    "chat_completions",
	"/v1/chat/completions": "chat_completions",
	"/messages":            "messages",
	"/v1/messages":         "messages",
	"/responses":           "responses",
	"/v1/responses":        "responses",
}

// PublicEndpoint 把存储的端点枚举转成客户可见路径，不带 /v1 前缀。
func PublicEndpoint(endpoint string) string {
	if mapped, ok := publicEndpoints[endpoint]; ok {
		return mapped
	}
	if mapped, ok := internalEndpoints[endpoint]; ok {
		return publicEndpoints[mapped]
	}
	return stripV1Prefix(endpoint)
}

// KnownPublicEndpoint 判断筛选值是否为客户可见路径；兼容旧的 /v1 前缀。
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

func stripV1Prefix(endpoint string) string {
	if strings.HasPrefix(endpoint, "/v1/") {
		return "/" + strings.TrimPrefix(endpoint, "/v1/")
	}
	return endpoint
}
