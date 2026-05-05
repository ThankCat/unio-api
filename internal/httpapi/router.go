package httpapi

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/ThankCat/unio-api/internal/httpx"
	"github.com/ThankCat/unio-api/internal/middleware"
)

type RouterDeps struct {
	Logger              *slog.Logger
	APIKeyAuthenticator middleware.APIKeyAuthenticator
}

// NewRouter 创建 API server 使用的 HTTP handler。
func NewRouter(deps RouterDeps) http.Handler {
	r := chi.NewRouter()

	r.Use(middleware.RequestID)
	r.Use(middleware.Logger(deps.Logger))
	r.Use(middleware.Recoverer(deps.Logger))

	r.NotFound(func(w http.ResponseWriter, r *http.Request) {
		_ = httpx.WriteError(
			w,
			http.StatusNotFound,
			"not_found",
			"route not found",
		)
	})
	r.MethodNotAllowed(func(w http.ResponseWriter, r *http.Request) {
		_ = httpx.WriteError(
			w,
			http.StatusMethodNotAllowed,
			"method_not_allowed",
			"method not allowed",
		)
	})

	r.Get("/healthz", func(w http.ResponseWriter, r *http.Request) {
		_ = httpx.WriteJSON(w, http.StatusOK, map[string]string{
			"status": "ok",
		})
	})

	r.Route("/v1", func(r chi.Router) {
		r.Use(middleware.APIKeyAuth(deps.APIKeyAuthenticator))

		r.Get("/models", func(w http.ResponseWriter, r *http.Request) {
			_ = httpx.WriteJSON(w, http.StatusOK, map[string]any{
				"object": "list",
				"data":   []any{},
			})
		})

		r.Post("/chat/completions", func(w http.ResponseWriter, r *http.Request) {
			var req ChatCompletionRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				_ = httpx.WriteError(w, http.StatusBadRequest, "invalid_request", "invalid json body")
				return
			}

			if req.Model == "" {
				_ = httpx.WriteError(w, http.StatusBadRequest, "invalid_request", "model is required")
				return
			}

			if len(req.Messages) == 0 {
				_ = httpx.WriteError(w, http.StatusBadRequest, "invalid_request", "messages is required")
				return
			}

			_ = httpx.WriteJSON(w, http.StatusOK, ChatCompletionResponse{
				ID:      "chatcmpl_mock",
				Object:  "chat.completion",
				Created: time.Now().Unix(),
				Model:   req.Model,
				Choices: []ChatCompletionChoice{
					{
						Index: 0,
						Message: ChatMessage{
							Role:    "assistant",
							Content: "mock response",
						},
						FinishReason: "stop",
					},
				},
				Usage: ChatCompletionUsage{
					PromptTokens:     0,
					CompletionTokens: 0,
					TotalTokens:      0,
				},
			})
		})
	})

	return r
}
