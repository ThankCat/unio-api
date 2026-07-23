package responses

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sync/atomic"
	"testing"

	"github.com/ThankCat/unio-gateway/internal/core/adapter"
	"github.com/ThankCat/unio-gateway/internal/core/channel"
)

type responsesTransportStartProbe struct {
	starts     atomic.Int32
	firstToken atomic.Int32
	completed  atomic.Int32
}

func (p *responsesTransportStartProbe) TransportStarted() {
	p.starts.Add(1)
}

func (p *responsesTransportStartProbe) FirstTokenEligible() {
	p.firstToken.Add(1)
}

func (p *responsesTransportStartProbe) TransportCompleted() {
	p.completed.Add(1)
}

func TestAdapterMarksTransportStartedImmediatelyBeforeHTTPDo(t *testing.T) {
	tests := []struct {
		name   string
		invoke func(context.Context, *Adapter) error
	}{
		{
			name: "create response",
			invoke: func(ctx context.Context, a *Adapter) error {
				_, err := a.CreateResponse(ctx, channel.Runtime{
					BaseURL: "https://example.test",
					APIKey:  "test-secret",
				}, Request{Body: json.RawMessage(`{"model":"gpt-test","stream":false}`)})
				return err
			},
		},
		{
			name: "stream response",
			invoke: func(ctx context.Context, a *Adapter) error {
				_, err := a.StreamResponse(ctx, channel.Runtime{
					BaseURL: "https://example.test",
					APIKey:  "test-secret",
				}, Request{Body: json.RawMessage(`{"model":"gpt-test","stream":true}`)}, func(StreamChunk) error { return nil })
				return err
			},
		},
		{
			name: "compact response",
			invoke: func(ctx context.Context, a *Adapter) error {
				_, err := a.CompactResponse(ctx, channel.Runtime{
					BaseURL: "https://example.test",
					APIKey:  "test-secret",
				}, Request{Body: json.RawMessage(`{"model":"gpt-test"}`)})
				return err
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			probe := &responsesTransportStartProbe{}
			stop := errors.New("stop after observing transport start")
			client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
				if got := probe.starts.Load(); got != 1 {
					t.Errorf("TransportStarted calls before RoundTrip = %d, want 1", got)
				}
				return nil, stop
			})}
			ctx := adapter.WithAttemptTimingObserver(context.Background(), probe)

			if err := tt.invoke(ctx, NewAdapter(client)); err == nil {
				t.Fatal("expected sentinel transport error")
			}
			if got := probe.starts.Load(); got != 1 {
				t.Fatalf("TransportStarted calls = %d, want 1", got)
			}
			if got := probe.firstToken.Load(); got != 0 {
				t.Fatalf("adapter must not classify lifecycle FirstToken events, got %d calls", got)
			}
			if got := probe.completed.Load(); got != 0 {
				t.Fatalf("adapter must not close lifecycle timing, got %d calls", got)
			}
		})
	}
}
