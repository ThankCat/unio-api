package chatcompletions

import (
	"testing"

	gatewayapi "github.com/ThankCat/unio-gateway/internal/app/gatewayapi/openai/chatcompletions"
)

func TestMapGatewayRequestPreservesOutputLimitPresence(t *testing.T) {
	t.Run("omitted", func(t *testing.T) {
		mapped := mapGatewayRequestToAdapter(gatewayapi.ChatCompletionRequest{}, "upstream-model")
		if mapped.MaxTokens != nil || mapped.MaxCompletionTokens != nil {
			t.Fatalf("omitted output limits were injected: max_tokens=%v max_completion_tokens=%v", mapped.MaxTokens, mapped.MaxCompletionTokens)
		}
	})

	t.Run("explicit max_tokens", func(t *testing.T) {
		value := 257
		mapped := mapGatewayRequestToAdapter(gatewayapi.ChatCompletionRequest{MaxTokens: &value}, "upstream-model")
		if mapped.MaxTokens == nil || *mapped.MaxTokens != value || mapped.MaxCompletionTokens != nil {
			t.Fatalf("explicit max_tokens was not preserved: max_tokens=%v max_completion_tokens=%v", mapped.MaxTokens, mapped.MaxCompletionTokens)
		}
	})

	t.Run("explicit max_completion_tokens", func(t *testing.T) {
		value := 4097
		mapped := mapGatewayRequestToAdapter(gatewayapi.ChatCompletionRequest{MaxCompletionTokens: &value}, "upstream-model")
		if mapped.MaxCompletionTokens == nil || *mapped.MaxCompletionTokens != value || mapped.MaxTokens != nil {
			t.Fatalf("explicit max_completion_tokens was not preserved: max_tokens=%v max_completion_tokens=%v", mapped.MaxTokens, mapped.MaxCompletionTokens)
		}
	})
}
