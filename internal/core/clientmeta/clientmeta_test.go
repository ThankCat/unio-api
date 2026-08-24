package clientmeta

import (
	"context"
	"strings"
	"testing"
)

// codexTurnMetadataHeader 是 Codex v0.147 抓包实测的 x-codex-turn-metadata 头值。
const codexTurnMetadataHeader = `{"installation_id":"2935b1f9-83cf-4601-a3e6-c1303fc2ccd4","session_id":"01a03335-7f34-70c2-917e-de98181c4747","thread_id":"01a03335-7f34-70c2-917e-de98181c4747","turn_id":"01a03335-7f63-7aa2-89e1-e19d1fcdef2b","window_id":"01a03335-7f34-70c2-917e-de98181c4747:0","request_kind":"turn","thread_source":"user","sandbox":"none","turn_started_at_unix_ms":1787565539175}`

func TestParseCodexTurnMetadata(t *testing.T) {
	turn := ParseCodexTurnMetadata(codexTurnMetadataHeader)

	if turn.ThreadID != "01a03335-7f34-70c2-917e-de98181c4747" {
		t.Errorf("unexpected thread id: %q", turn.ThreadID)
	}
	if turn.TurnID != "01a03335-7f63-7aa2-89e1-e19d1fcdef2b" {
		t.Errorf("unexpected turn id: %q", turn.TurnID)
	}
	if turn.RequestKind != "turn" {
		t.Errorf("unexpected request kind: %q", turn.RequestKind)
	}
	if turn.IsZero() {
		t.Error("expected non-zero turn")
	}
}

// TestParseCodexTurnMetadataDegradesQuietly 验证畸形输入降级为空值：审计字段可以缺失，
// 但绝不能让客户端可控输入影响请求主链路。
func TestParseCodexTurnMetadataDegradesQuietly(t *testing.T) {
	cases := []struct {
		name   string
		header string
	}{
		{"empty", ""},
		{"blank", "   "},
		{"not json object", "thread_id=abc"},
		{"malformed json", `{"thread_id":`},
		{"unrelated json", `{"other":"x"}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if turn := ParseCodexTurnMetadata(tc.header); !turn.IsZero() {
				t.Errorf("expected zero turn, got %+v", turn)
			}
		})
	}
}

// TestParseCodexTurnMetadataRejectsOversizedField 验证超长字段被丢弃而非落库。
func TestParseCodexTurnMetadataRejectsOversizedField(t *testing.T) {
	oversized := strings.Repeat("x", maxFieldLength+1)
	turn := ParseCodexTurnMetadata(`{"thread_id":"` + oversized + `","turn_id":"ok-turn"}`)

	if turn.ThreadID != "" {
		t.Errorf("oversized thread id should be dropped, got %d chars", len(turn.ThreadID))
	}
	if turn.TurnID != "ok-turn" {
		t.Errorf("well-formed sibling field should survive, got %q", turn.TurnID)
	}
}

func TestTurnContextRoundTrip(t *testing.T) {
	ctx := context.Background()
	if got := TurnFromContext(ctx); !got.IsZero() {
		t.Errorf("expected zero turn from bare context, got %+v", got)
	}

	// 全空不写入，避免在 ctx 上留下无意义的值。
	if WithTurn(ctx, Turn{}) != ctx {
		t.Error("zero turn should not be stored in context")
	}

	want := ParseCodexTurnMetadata(codexTurnMetadataHeader)
	if got := TurnFromContext(WithTurn(ctx, want)); got != want {
		t.Errorf("round trip mismatch: want %+v got %+v", want, got)
	}
}
