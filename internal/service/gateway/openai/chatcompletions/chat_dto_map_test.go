package chatcompletions

import (
	"testing"

	"github.com/ThankCat/unio-gateway/internal/service/gateway/openai/chatcompletions/dto"
)

func TestMapGatewayRequestPreservesOutputLimitPresence(t *testing.T) {
	t.Run("omitted", func(t *testing.T) {
		mapped := mapGatewayRequestToAdapter(dto.ChatCompletionRequest{}, "upstream-model")
		if mapped.MaxTokens != nil || mapped.MaxCompletionTokens != nil {
			t.Fatalf("omitted output limits were injected: max_tokens=%v max_completion_tokens=%v", mapped.MaxTokens, mapped.MaxCompletionTokens)
		}
	})

	t.Run("explicit max_tokens", func(t *testing.T) {
		value := 257
		mapped := mapGatewayRequestToAdapter(dto.ChatCompletionRequest{MaxTokens: &value}, "upstream-model")
		if mapped.MaxTokens == nil || *mapped.MaxTokens != value || mapped.MaxCompletionTokens != nil {
			t.Fatalf("explicit max_tokens was not preserved: max_tokens=%v max_completion_tokens=%v", mapped.MaxTokens, mapped.MaxCompletionTokens)
		}
	})

	t.Run("explicit max_completion_tokens", func(t *testing.T) {
		value := 4097
		mapped := mapGatewayRequestToAdapter(dto.ChatCompletionRequest{MaxCompletionTokens: &value}, "upstream-model")
		if mapped.MaxCompletionTokens == nil || *mapped.MaxCompletionTokens != value || mapped.MaxTokens != nil {
			t.Fatalf("explicit max_completion_tokens was not preserved: max_tokens=%v max_completion_tokens=%v", mapped.MaxTokens, mapped.MaxCompletionTokens)
		}
	})
}
