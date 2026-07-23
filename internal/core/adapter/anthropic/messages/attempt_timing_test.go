package messages

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

type messagesTimingRoundTripFunc func(*http.Request) (*http.Response, error)

func (f messagesTimingRoundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

type messagesTransportStartProbe struct {
	starts     atomic.Int32
	firstToken atomic.Int32
	completed  atomic.Int32
}

func (p *messagesTransportStartProbe) TransportStarted() {
	p.starts.Add(1)
}

func (p *messagesTransportStartProbe) FirstTokenEligible() {
	p.firstToken.Add(1)
}

func (p *messagesTransportStartProbe) TransportCompleted() {
	p.completed.Add(1)
}

func TestAdapterMarksTransportStartedImmediatelyBeforeHTTPDo(t *testing.T) {
	maxTokens := 16
	request := MessageRequest{
		Model:     "claude-test",
		MaxTokens: &maxTokens,
		Messages: []Message{
			{Role: "user", Content: json.RawMessage(`"hello"`)},
		},
	}
	tests := []struct {
		name   string
		invoke func(context.Context, *Adapter) error
	}{
		{
			name: "non-stream",
			invoke: func(ctx context.Context, a *Adapter) error {
				_, err := a.Messages(ctx, channel.Runtime{
					BaseURL: "https://example.test",
					APIKey:  "test-secret",
				}, request)
				return err
			},
		},
		{
			name: "stream",
			invoke: func(ctx context.Context, a *Adapter) error {
				_, err := a.StreamMessages(ctx, channel.Runtime{
					BaseURL: "https://example.test",
					APIKey:  "test-secret",
				}, request, func(MessageStreamEvent) error { return nil })
				return err
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			probe := &messagesTransportStartProbe{}
			stop := errors.New("stop after observing transport start")
			client := &http.Client{Transport: messagesTimingRoundTripFunc(func(*http.Request) (*http.Response, error) {
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
