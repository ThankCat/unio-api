package messages

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type messagesDeliveryWriter struct {
	header     http.Header
	writeErr   error
	panicValue any
}

func (w *messagesDeliveryWriter) Header() http.Header {
	if w.header == nil {
		w.header = make(http.Header)
	}
	return w.header
}

func (*messagesDeliveryWriter) WriteHeader(int) {}

func (w *messagesDeliveryWriter) Write(p []byte) (int, error) {
	if w.panicValue != nil {
		panic(w.panicValue)
	}
	if w.writeErr != nil {
		return 0, w.writeErr
	}
	return len(p), nil
}

func messagesDeliveryRequest() *http.Request {
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{"model":"m","max_tokens":16,"messages":[{"role":"user","content":"hi"}]}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(anthropicVersionHeader, "2023-06-01")
	return req
}

func TestMessagesHandlerFinalizesNonStreamDelivery(t *testing.T) {
	t.Run("write success completes", func(t *testing.T) {
		service := &fakeMessagesService{}
		NewMessagesHandler(service, nil).ServeHTTP(&messagesDeliveryWriter{}, messagesDeliveryRequest())
		if service.deliveryCompleted != 1 || service.deliveryInterrupted != 0 {
			t.Fatalf("completed=%d interrupted=%d, want 1/0", service.deliveryCompleted, service.deliveryInterrupted)
		}
	})

	t.Run("write error interrupts", func(t *testing.T) {
		service := &fakeMessagesService{}
		NewMessagesHandler(service, nil).ServeHTTP(&messagesDeliveryWriter{writeErr: errors.New("client disconnected")}, messagesDeliveryRequest())
		if service.deliveryCompleted != 0 || service.deliveryInterrupted != 1 {
			t.Fatalf("completed=%d interrupted=%d, want 0/1", service.deliveryCompleted, service.deliveryInterrupted)
		}
	})

	t.Run("write panic interrupts and repanics", func(t *testing.T) {
		service := &fakeMessagesService{}
		const panicValue = "messages writer panic"
		func() {
			defer func() {
				if recovered := recover(); recovered != panicValue {
					t.Fatalf("recovered = %#v, want %q", recovered, panicValue)
				}
			}()
			NewMessagesHandler(service, nil).ServeHTTP(&messagesDeliveryWriter{panicValue: panicValue}, messagesDeliveryRequest())
		}()
		if service.deliveryCompleted != 0 || service.deliveryInterrupted != 1 {
			t.Fatalf("completed=%d interrupted=%d, want 0/1", service.deliveryCompleted, service.deliveryInterrupted)
		}
	})
}
