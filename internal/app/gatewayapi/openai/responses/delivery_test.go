package responses

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type responsesDeliveryWriter struct {
	header     http.Header
	writeErr   error
	panicValue any
}

func (w *responsesDeliveryWriter) Header() http.Header {
	if w.header == nil {
		w.header = make(http.Header)
	}
	return w.header
}

func (*responsesDeliveryWriter) WriteHeader(int) {}

func (w *responsesDeliveryWriter) Write(p []byte) (int, error) {
	if w.panicValue != nil {
		panic(w.panicValue)
	}
	if w.writeErr != nil {
		return 0, w.writeErr
	}
	return len(p), nil
}

func responsesDeliveryRequest(path string) *http.Request {
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{"model":"m","input":"hi"}`))
	req.Header.Set("Content-Type", "application/json")
	return req
}

func TestResponsesHandlersFinalizeNonStreamDelivery(t *testing.T) {
	handlers := []struct {
		name    string
		path    string
		service func() *fakeResponsesService
		handler func(*fakeResponsesService) http.Handler
	}{
		{
			name: "create",
			path: "/v1/responses",
			service: func() *fakeResponsesService {
				return &fakeResponsesService{resp: &ResponsesResponse{Object: "response", Model: "m", Status: "completed"}}
			},
			handler: func(service *fakeResponsesService) http.Handler { return NewResponsesHandler(service) },
		},
		{
			name: "compact",
			path: "/v1/responses/compact",
			service: func() *fakeResponsesService {
				return &fakeResponsesService{compact: &CompactHistoryResponse{}}
			},
			handler: func(service *fakeResponsesService) http.Handler { return NewResponsesCompactHandler(service) },
		},
	}

	for _, origin := range handlers {
		origin := origin
		t.Run(origin.name, func(t *testing.T) {
			t.Run("write success completes", func(t *testing.T) {
				service := origin.service()
				origin.handler(service).ServeHTTP(&responsesDeliveryWriter{}, responsesDeliveryRequest(origin.path))
				if service.deliveryCompleted != 1 || service.deliveryInterrupted != 0 {
					t.Fatalf("completed=%d interrupted=%d, want 1/0", service.deliveryCompleted, service.deliveryInterrupted)
				}
			})

			t.Run("write error interrupts", func(t *testing.T) {
				service := origin.service()
				origin.handler(service).ServeHTTP(&responsesDeliveryWriter{writeErr: errors.New("client disconnected")}, responsesDeliveryRequest(origin.path))
				if service.deliveryCompleted != 0 || service.deliveryInterrupted != 1 {
					t.Fatalf("completed=%d interrupted=%d, want 0/1", service.deliveryCompleted, service.deliveryInterrupted)
				}
			})

			t.Run("write panic interrupts and repanics", func(t *testing.T) {
				service := origin.service()
				const panicValue = "responses writer panic"
				func() {
					defer func() {
						if recovered := recover(); recovered != panicValue {
							t.Fatalf("recovered = %#v, want %q", recovered, panicValue)
						}
					}()
					origin.handler(service).ServeHTTP(&responsesDeliveryWriter{panicValue: panicValue}, responsesDeliveryRequest(origin.path))
				}()
				if service.deliveryCompleted != 0 || service.deliveryInterrupted != 1 {
					t.Fatalf("completed=%d interrupted=%d, want 0/1", service.deliveryCompleted, service.deliveryInterrupted)
				}
			})
		})
	}
}
