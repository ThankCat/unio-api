package httpx

import (
	"encoding/json"
	"net/http"
)

const (
	// ContentTypeJSON 是 JSON 响应使用的 Content-Type。
	ContentTypeJSON = "application/json"
)

// ErrorResponse 是 API 错误响应的外层结构。
type ErrorResponse struct {
	Error ErrorBody `json:"error"`
}

// ErrorBody 描述 API 错误的业务码和可读消息。
type ErrorBody struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// WriteJSON 将 v 以 JSON 格式写入响应，并设置 HTTP 状态码。
func WriteJSON(w http.ResponseWriter, status int, v any) error {
	w.Header().Set("Content-Type", ContentTypeJSON)
	w.WriteHeader(status)
	return json.NewEncoder(w).Encode(v)
}

// WriteError 写入统一格式的 JSON 错误响应。
func WriteError(w http.ResponseWriter, status int, code string, message string) error {
	errbody := ErrorResponse{
		Error: ErrorBody{
			Code:    code,
			Message: message,
		},
	}
	return WriteJSON(w, status, errbody)
}
