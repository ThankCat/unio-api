package lifecycle

import (
	"context"
	"errors"
	"fmt"
	"net"
	"testing"
	"time"

	"github.com/ThankCat/unio-gateway/internal/core/adapter"
)

type fakeNetTimeout struct{}

func (fakeNetTimeout) Error() string   { return "i/o timeout" }
func (fakeNetTimeout) Timeout() bool   { return true }
func (fakeNetTimeout) Temporary() bool { return true }

var _ net.Error = fakeNetTimeout{}

func timingAt(headers bool, firstToken bool) AttemptTimingFacts {
	started := time.Unix(100, 0)
	facts := AttemptTimingFacts{UpstreamStartedAt: &started, ResponseHeadersSeen: headers}
	if firstToken {
		at := started.Add(200 * time.Millisecond)
		facts.UpstreamFirstTokenAt = &at
	}
	return facts
}

// TestTimeoutPhaseOf 冻结 §11.4 的四个稳定阶段与 §19.4 的判定矩阵。
func TestTimeoutPhaseOf(t *testing.T) {
	upstreamTimeout := adapter.NewUpstreamError(
		adapter.UpstreamErrorTimeout, adapter.UpstreamMetadata{}, errors.New("upstream timeout"),
	)
	tests := []struct {
		name   string
		err    error
		stream bool
		timing AttemptTimingFacts
		want   adapter.UpstreamTimeoutPhase
	}{
		{name: "no error", err: nil, want: ""},
		{
			name: "non-stream fast headers slow body",
			err:  context.DeadlineExceeded, stream: false, timing: timingAt(true, false),
			want: adapter.TimeoutPhaseResponseBody,
		},
		{
			name: "non-stream headers never arrived",
			err:  context.DeadlineExceeded, stream: false, timing: timingAt(false, false),
			want: adapter.TimeoutPhaseResponseHeader,
		},
		{
			name: "stream slow headers",
			err:  context.DeadlineExceeded, stream: true, timing: timingAt(false, false),
			want: adapter.TimeoutPhaseResponseHeader,
		},
		{
			name: "stream fast headers but no valid event",
			err:  context.DeadlineExceeded, stream: true, timing: timingAt(true, false),
			want: adapter.TimeoutPhaseFirstToken,
		},
		{
			name: "stream stalls after the first event",
			err:  context.DeadlineExceeded, stream: true, timing: timingAt(true, true),
			want: adapter.TimeoutPhaseStreamIdle,
		},
		{
			name:   "first token sentinel wins regardless of progress",
			err:    fmt.Errorf("wrapped: %w", adapter.ErrFirstTokenTimeout),
			stream: true, timing: timingAt(true, true),
			want: adapter.TimeoutPhaseFirstToken,
		},
		{
			name:   "idle sentinel wins regardless of progress",
			err:    fmt.Errorf("wrapped: %w", adapter.ErrStreamIdleTimeout),
			stream: true, timing: timingAt(true, false),
			want: adapter.TimeoutPhaseStreamIdle,
		},
		{
			name: "net timeout counts as a timeout",
			err:  fmt.Errorf("dial: %w", fakeNetTimeout{}), stream: false, timing: timingAt(false, false),
			want: adapter.TimeoutPhaseResponseHeader,
		},
		{
			name: "adapter timeout category counts as a timeout",
			err:  upstreamTimeout, stream: true, timing: timingAt(true, false),
			want: adapter.TimeoutPhaseFirstToken,
		},
		{
			name: "client cancel is not a timeout",
			err:  context.Canceled, stream: true, timing: timingAt(true, false),
			want: "",
		},
		{
			name: "upstream 5xx is not a timeout",
			err: adapter.NewUpstreamError(
				adapter.UpstreamErrorServer, adapter.UpstreamMetadata{}, errors.New("boom"),
			),
			stream: true, timing: timingAt(true, true),
			want: "",
		},
		{name: "plain error is not a timeout", err: errors.New("plain"), want: ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := TimeoutPhaseOf(tc.err, tc.stream, tc.timing); got != tc.want {
				t.Fatalf("phase = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestTimeoutPhaseStaysConsistentAcrossConsumers 冻结 §19.4 最后一条：
// attempt 落库、Sticky 清绑原因与错误率样本必须消费同一个阶段判定。
func TestTimeoutPhaseStaysConsistentAcrossConsumers(t *testing.T) {
	// 首字超时既要落 first_token 阶段，也要按永久故障清 Sticky（§10.7）。
	firstTokenErr := adapter.NewUpstreamError(
		adapter.UpstreamErrorTimeout, adapter.UpstreamMetadata{},
		fmt.Errorf("wrapped: %w", adapter.ErrFirstTokenTimeout),
	)
	if got := TimeoutPhaseOf(firstTokenErr, true, timingAt(true, false)); got != adapter.TimeoutPhaseFirstToken {
		t.Fatalf("attempt phase = %q, want first_token", got)
	}
	verdict := classifyStickyFailure(firstTokenErr)
	if !verdict.clear || verdict.reason != "upstream_timeout" {
		t.Fatalf("first token timeout must clear the sticky binding: %+v", verdict)
	}
}
