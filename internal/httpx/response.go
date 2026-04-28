package httpx

import (
	"encoding/json"
	"net/http"
)

const (
	ContentTypeJSON = "application/json"
)

type ErrorResponse struct {
	Error ErrorBody `json:"error"`
}

type ErrorBody struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func WriteJSON(w http.ResponseWriter, status int, v any) error {
	w.Header().Set("Content-Type", ContentTypeJSON)
	w.WriteHeader(status)
	return json.NewEncoder(w).Encode(v)
}

func WriteError(w http.ResponseWriter, status int, code string, message string) error {
	errbody := ErrorResponse{
		Error: ErrorBody{
			Code:    code,
			Message: message,
		},
	}
	return WriteJSON(w, status, errbody)
}
