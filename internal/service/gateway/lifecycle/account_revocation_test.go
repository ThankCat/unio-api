package lifecycle

import "testing"

// TestUpstreamBodyConfirmsRevocation 冻结请求路径的明确吊销码判定（实测样本 token_revoked）。
func TestUpstreamBodyConfirmsRevocation(t *testing.T) {
	confirmed := []string{
		`{"error":{"code":"token_revoked","message":"..."}}`,
		`{"detail":"token_invalidated"}`,
	}
	for _, snippet := range confirmed {
		if !upstreamBodyConfirmsRevocation(snippet) {
			t.Fatalf("expected revocation confirmed: %s", snippet)
		}
	}
	notConfirmed := []string{
		`{"detail":"Unauthorized"}`, // 语义不明确：走隔离+刷新确认，不直接禁用
		`{"error":{"code":"invalid_api_key"}}`,
		``,
	}
	for _, snippet := range notConfirmed {
		if upstreamBodyConfirmsRevocation(snippet) {
			t.Fatalf("expected not confirmed: %s", snippet)
		}
	}
}
