package chatcompletions

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type chatDeliveryWriter struct {
	header     http.Header
	writeErr   error
	panicValue any
}

func (w *chatDeliveryWriter) Header() http.Header {
	if w.header == nil {
		w.header = make(http.Header)
	}
	return w.header
}

func (*chatDeliveryWriter) WriteHeader(int) {}

func (w *chatDeliveryWriter) Write(p []byte) (int, error) {
	if w.panicValue != nil {
		panic(w.panicValue)
	}
	if w.writeErr != nil {
		return 0, w.writeErr
	}
	return len(p), nil
}

func chatDeliveryRequest() *http.Request {
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"m","messages":[{"role":"user","content":"hi"}]}`))
	req.Header.Set("Content-Type", "application/json")
	return req
}

func TestChatCompletionHandlerFinalizesNonStreamDelivery(t *testing.T) {
	t.Run("write success completes", func(t *testing.T) {
		service := &fakeChatCompletionService{createResp: &ChatCompletionResponse{Object: "chat.completion", Model: "m"}}
		NewChatCompletionsHandler(service).ServeHTTP(&chatDeliveryWriter{}, chatDeliveryRequest())
		if service.deliveryCompleted != 1 || service.deliveryInterrupted != 0 {
			t.Fatalf("completed=%d interrupted=%d, want 1/0", service.deliveryCompleted, service.deliveryInterrupted)
		}
	})

	t.Run("write error interrupts", func(t *testing.T) {
		service := &fakeChatCompletionService{createResp: &ChatCompletionResponse{Object: "chat.completion", Model: "m"}}
		NewChatCompletionsHandler(service).ServeHTTP(&chatDeliveryWriter{writeErr: errors.New("client disconnected")}, chatDeliveryRequest())
		if service.deliveryCompleted != 0 || service.deliveryInterrupted != 1 {
			t.Fatalf("completed=%d interrupted=%d, want 0/1", service.deliveryCompleted, service.deliveryInterrupted)
		}
	})

	t.Run("write panic interrupts and repanics", func(t *testing.T) {
		service := &fakeChatCompletionService{createResp: &ChatCompletionResponse{Object: "chat.completion", Model: "m"}}
		const panicValue = "chat writer panic"
		func() {
			defer func() {
				if recovered := recover(); recovered != panicValue {
					t.Fatalf("recovered = %#v, want %q", recovered, panicValue)
				}
			}()
			NewChatCompletionsHandler(service).ServeHTTP(&chatDeliveryWriter{panicValue: panicValue}, chatDeliveryRequest())
		}()
		if service.deliveryCompleted != 0 || service.deliveryInterrupted != 1 {
			t.Fatalf("completed=%d interrupted=%d, want 0/1", service.deliveryCompleted, service.deliveryInterrupted)
		}
	})
}
