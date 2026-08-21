package requests

import "testing"

func TestPublicEndpoint(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		"chat_completions":     "/chat/completions",
		"messages":             "/messages",
		"responses":            "/responses",
		"/v1/chat/completions": "/chat/completions",
		"/chat/completions":    "/chat/completions",
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
	if !KnownPublicEndpoint("/chat/completions") || !KnownPublicEndpoint("/v1/chat/completions") {
		t.Fatal("expected public paths with or without /v1")
	}
	if KnownPublicEndpoint("chat_completions") {
		t.Fatal("storage enums are not public filter values")
	}
}

func TestInternalEndpoints(t *testing.T) {
	t.Parallel()
	got := InternalEndpoints([]string{"/chat/completions", "/v1/messages", "custom"})
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
