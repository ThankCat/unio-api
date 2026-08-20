package requests

import "testing"

func TestPublicEndpoint(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		"chat_completions":     "/v1/chat/completions",
		"messages":             "/v1/messages",
		"responses":            "/v1/responses",
		"/v1/chat/completions": "/v1/chat/completions",
		"other":                "other",
	}
	for in, want := range cases {
		if got := PublicEndpoint(in); got != want {
			t.Fatalf("PublicEndpoint(%q)=%q, want %q", in, got, want)
		}
	}
}

func TestKnownPublicEndpoint(t *testing.T) {
	t.Parallel()
	if !KnownPublicEndpoint("/v1/chat/completions") || KnownPublicEndpoint("chat_completions") {
		t.Fatal("expected only public paths to be accepted as filter values")
	}
}

func TestInternalEndpoints(t *testing.T) {
	t.Parallel()
	got := InternalEndpoints([]string{"/v1/chat/completions", "messages", "custom"})
	want := []string{"chat_completions", "messages", "custom"}
	if len(got) != len(want) {
		t.Fatalf("got %#v, want %#v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %#v, want %#v", got, want)
		}
	}
}
