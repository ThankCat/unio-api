package codexidentity

import (
	"net/http"
	"regexp"
	"strings"
	"testing"
)

// TestUserAgentShapeMatchesWireCapture 冻结 UA 形态与 0.152.1 抓包一致：
// <originator>/<ver> (<OS>; <arch>) <terminal> (<originator>; <ver>)，且不含任何网关自有标记。
func TestUserAgentShapeMatchesWireCapture(t *testing.T) {
	ua := Default().UserAgent()
	if ua != "codex-tui/0.152.1 (Mac OS 15.2.0; arm64) tmux/3.7c (codex-tui; 0.152.1)" {
		t.Fatalf("user agent = %q", ua)
	}
	shape := regexp.MustCompile(`^([a-z_-]+)/([0-9][0-9A-Za-z.\-]*) \(Mac OS [0-9.]+; arm64\) [A-Za-z_.]+/[0-9A-Za-z.]+ \(([a-z_-]+); ([0-9][0-9A-Za-z.\-]*)\)$`)
	m := shape.FindStringSubmatch(ua)
	if m == nil {
		t.Fatalf("user agent %q does not match the captured wire shape", ua)
	}
	if m[1] != Originator || m[3] != Originator {
		t.Fatalf("originator segments %q/%q must equal %q", m[1], m[3], Originator)
	}
	if m[2] != m[4] || m[2] != Default().Version {
		t.Fatalf("version segments %q/%q must equal %q", m[2], m[4], Default().Version)
	}
	if strings.Contains(strings.ToLower(ua), "unio") {
		t.Fatal("user agent must not carry a gateway marker")
	}
}

// TestHeadersAreSelfConsistent 冻结三个头同源：originator == UA 首段，version == UA 版本段；
// 凭据面不发 version。
func TestHeadersAreSelfConsistent(t *testing.T) {
	id := WithVersion("0.153.0-alpha.5")
	inference := http.Header{}
	id.ApplyInferenceHeaders(inference)
	ua := inference.Get("User-Agent")
	if !strings.HasPrefix(ua, inference.Get("originator")+"/"+inference.Get("version")+" ") {
		t.Fatalf("inference headers drift: originator=%q version=%q ua=%q", inference.Get("originator"), inference.Get("version"), ua)
	}
	if inference.Get("version") != "0.153.0-alpha.5" {
		t.Fatalf("version header = %q, want 0.153.0-alpha.5", inference.Get("version"))
	}

	credential := http.Header{}
	id.ApplyCredentialHeaders(credential)
	if credential.Get("User-Agent") != ua || credential.Get("originator") != Originator {
		t.Fatalf("credential face must reuse the same identity pair, got %v", credential)
	}
	if credential.Get("version") != "" {
		t.Fatalf("credential face must not send version, got %q", credential.Get("version"))
	}
}

func TestVersionNormalizationAndFloor(t *testing.T) {
	for _, valid := range []string{"0.152.1", " 0.160.0 ", "0.153.0-alpha.5", "1.2", "1.2.3.4"} {
		if NormalizeVersion(valid) == "" {
			t.Fatalf("%q should be a valid version", valid)
		}
	}
	for _, invalid := range []string{"", "v0.152.1", "0.152.1; drop", "abc", "0.152.1 rc", strings.Repeat("1.", 40) + "1"} {
		if got := NormalizeVersion(invalid); got != "" {
			t.Fatalf("%q should be rejected, got %q", invalid, got)
		}
	}
	if got := FloorVersion("0.144.0"); got != BaselineVersion {
		t.Fatalf("below-baseline version must floor to baseline, got %q", got)
	}
	if got := FloorVersion("garbage"); got != BaselineVersion {
		t.Fatalf("invalid version must floor to baseline, got %q", got)
	}
	if got := FloorVersion("0.160.2"); got != "0.160.2" {
		t.Fatalf("newer version must pass through, got %q", got)
	}
}

func TestCompareVersions(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"0.152.1", "0.152.1", 0},
		{"0.152.1", "0.153.0", -1},
		{"0.160.0", "0.152.1", 1},
		{"0.153.0-alpha.5", "0.153.0", -1},
		{"0.153.0", "0.153.0-alpha.5", 1},
		{"0.153.0-alpha.5", "0.153.0-alpha.6", -1},
		{"1.0", "1.0.0", 0},
		{"0.153.0-alpha.5", "0.152.1", 1},
	}
	for _, tc := range cases {
		if got := CompareVersions(tc.a, tc.b); got != tc.want {
			t.Fatalf("CompareVersions(%q, %q) = %d, want %d", tc.a, tc.b, got, tc.want)
		}
	}
}

// TestEffectiveVersionPrecedence 冻结生效优先级：Admin 覆写 → 自动同步（开启时）→ 基线；全部施加下限。
func TestEffectiveVersionPrecedence(t *testing.T) {
	if got := EffectiveVersion("0.155.0", true, "0.160.0"); got != "0.155.0" {
		t.Fatalf("override must win, got %q", got)
	}
	if got := EffectiveVersion("", true, "0.160.0"); got != "0.160.0" {
		t.Fatalf("synced must apply when auto sync is on, got %q", got)
	}
	if got := EffectiveVersion("", false, "0.160.0"); got != BaselineVersion {
		t.Fatalf("synced must be ignored when auto sync is off, got %q", got)
	}
	if got := EffectiveVersion("", true, ""); got != BaselineVersion {
		t.Fatalf("no signal must fall back to baseline, got %q", got)
	}
	if got := EffectiveVersion("0.100.0", true, "0.101.0"); got != BaselineVersion {
		t.Fatalf("values below baseline must floor, got %q", got)
	}
	if got := EffectiveVersion("not-a-version", true, "0.160.0"); got != "0.160.0" {
		t.Fatalf("invalid override must be ignored in favour of synced, got %q", got)
	}
}

func TestResolveNilSourceIsBaseline(t *testing.T) {
	if Resolve(nil) != Default() {
		t.Fatal("nil source must resolve to baseline identity")
	}
	if got := Resolve(func() string { return "0.170.0" }).Version; got != "0.170.0" {
		t.Fatalf("source version must be honoured, got %q", got)
	}
}
