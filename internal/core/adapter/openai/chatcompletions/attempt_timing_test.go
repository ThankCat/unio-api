package chatcompletions

import (
	"context"
	"errors"
	"net/http"
	"sync/atomic"
	"testing"

	coreadapter "github.com/ThankCat/unio-gateway/internal/core/adapter"
	"github.com/ThankCat/unio-gateway/internal/core/channel"
)

type timingRoundTripFunc func(*http.Request) (*http.Response, error)

func (f timingRoundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

type transportStartProbe struct {
	starts     atomic.Int32
	firstToken atomic.Int32
	completed  atomic.Int32
}

func (p *transportStartProbe) TransportStarted() {
	p.starts.Add(1)
}

func (p *transportStartProbe) FirstTokenEligible() {
	p.firstToken.Add(1)
}

func (p *transportStartProbe) TransportCompleted() {
	p.completed.Add(1)
}

func TestAdapterMarksTransportStartedImmediatelyBeforeHTTPDo(t *testing.T) {
	tests := []struct {
		name   string
		invoke func(context.Context, *Adapter) error
	}{
		{
			name: "non-stream",
			invoke: func(ctx context.Context, a *Adapter) error {
				_, err := a.ChatCompletions(ctx, channel.Runtime{
					BaseURL: "https://example.test",
					APIKey:  "test-secret",
				}, ChatRequest{Model: "gpt-test"})
				return err
			},
		},
		{
			name: "stream",
			invoke: func(ctx context.Context, a *Adapter) error {
				_, err := a.StreamChatCompletions(ctx, channel.Runtime{
					BaseURL: "https://example.test",
					APIKey:  "test-secret",
				}, ChatRequest{Model: "gpt-test"}, func(ChatStreamChunk) error { return nil })
				return err
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			probe := &transportStartProbe{}
			stop := errors.New("stop after observing transport start")
			client := &http.Client{Transport: timingRoundTripFunc(func(*http.Request) (*http.Response, error) {
				if got := probe.starts.Load(); got != 1 {
					t.Errorf("TransportStarted calls before RoundTrip = %d, want 1", got)
				}
				return nil, stop
			})}
			ctx := coreadapter.WithAttemptTimingObserver(context.Background(), probe)

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
