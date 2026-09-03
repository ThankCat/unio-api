package subscription

import "testing"

// TestRefreshBodyIsTerminal 冻结刷新终局错误码清单（与 sub2api 生产清单对齐 + 实测样本）。
func TestRefreshBodyIsTerminal(t *testing.T) {
	terminal := []string{
		`{"error":"invalid_grant"}`,
		`{"error":{"code":"refresh_token_reused"}}`,
		`{"error":"refresh_token_invalidated","error_description":"Session ended"}`,
		`{"error":"token_expired"}`,
		`{"error":"app_session_terminated"}`,
		`{"error":"invalid_client"}`,
		`{"error":"unauthorized_client"}`,
		`{"error":"access_denied"}`,
	}
	for _, body := range terminal {
		if !refreshBodyIsTerminal([]byte(body)) {
			t.Fatalf("expected terminal: %s", body)
		}
	}
	transient := []string{
		`{"error":"server_error"}`,
		`{"error":"temporarily_unavailable"}`,
		``,
		`upstream proxy timeout`,
	}
	for _, body := range transient {
		if refreshBodyIsTerminal([]byte(body)) {
			t.Fatalf("expected transient: %s", body)
		}
	}
}
