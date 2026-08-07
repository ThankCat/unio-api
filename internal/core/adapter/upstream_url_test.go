package adapter

import "testing"

func TestBuildUpstreamURLAcceptsRootWithOrWithoutV1(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		base      string
		operation string
		want      string
	}{
		{name: "root", base: "https://api.example.com", operation: "/v1/models", want: "https://api.example.com/v1/models"},
		{name: "root trailing slash", base: "https://api.example.com/", operation: "/v1/models", want: "https://api.example.com/v1/models"},
		{name: "root already versioned", base: "https://api.example.com/v1", operation: "/v1/models", want: "https://api.example.com/v1/models"},
		{name: "root versioned trailing slash", base: "https://api.example.com/v1/", operation: "/v1/models", want: "https://api.example.com/v1/models"},
		{name: "provider prefix", base: "https://proxy.example.com/openai", operation: "/v1/models", want: "https://proxy.example.com/openai/v1/models"},
		{name: "provider prefix already versioned", base: "https://proxy.example.com/openai/v1", operation: "/v1/models", want: "https://proxy.example.com/openai/v1/models"},
		{name: "non version suffix", base: "https://proxy.example.com/v10", operation: "/v1/models", want: "https://proxy.example.com/v10/v1/models"},
		{name: "chat operation", base: "https://api.example.com/v1", operation: OperationPathChatCompletions, want: "https://api.example.com/v1/chat/completions"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := BuildUpstreamURL(tt.base, tt.operation)
			if err != nil {
				t.Fatalf("BuildUpstreamURL() error = %v", err)
			}
			if got != tt.want {
				t.Fatalf("BuildUpstreamURL() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestBuildUpstreamURLRejectsInvalidRoot(t *testing.T) {
	t.Parallel()

	for _, base := range []string{"", "localhost:8080", "ftp://api.example.com"} {
		if _, err := BuildUpstreamURL(base, "/v1/models"); err == nil {
			t.Fatalf("BuildUpstreamURL(%q) expected error", base)
		}
	}
}
