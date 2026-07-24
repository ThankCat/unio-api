package adapter

import (
	"net/url"
	"strings"

	"github.com/ThankCat/unio-gateway/internal/platform/failure"
)

// P4 §4.6：ProviderOrigin BaseURL 是 adapter root，不包含由标准 adapter 固定追加的 operation 路径。
// 统一用结构化 URL API 拼接，不再散落 strings.TrimRight(base, "/") + path。
//
// 标准 operation 路径：
//
//	OpenAI Chat Completions    -> /v1/chat/completions
//	OpenAI Responses           -> /v1/responses
//	OpenAI Responses Compact   -> /v1/responses/compact
//	Anthropic Messages         -> /v1/messages
//
// provider-specific adapter 可定义自己的相对前缀，但仍只能从 Origin BaseURL 派生。
const (
	OperationPathChatCompletions  = "/v1/chat/completions"
	OperationPathResponses        = "/v1/responses"
	OperationPathResponsesCompact = "/v1/responses/compact"
	OperationPathMessages         = "/v1/messages"
)

// BuildUpstreamURL 从 Origin BaseURL（root）与标准/自定义相对 operation 路径拼接最终上游 URL。
//
// 语义：保留 base 已有 path 段（如 provider-specific 前缀），把 operationPath 追加其后，
// 用 url.JoinPath 归一多余斜杠，避免出现 `//` 或丢段。base 为空、非法或非 http(s) 时返回配置错误。
func BuildUpstreamURL(baseRoot, operationPath string) (string, error) {
	trimmed := strings.TrimSpace(baseRoot)
	if trimmed == "" {
		return "", failure.New(
			failure.CodeConfigInvalid,
			failure.WithMessage("upstream base url is empty"),
		)
	}

	parsed, err := url.Parse(trimmed)
	if err != nil {
		return "", failure.Wrap(
			failure.CodeConfigInvalid,
			err,
			failure.WithMessage("parse upstream base url"),
		)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return "", failure.New(
			failure.CodeConfigInvalid,
			failure.WithMessage("upstream base url must be http or https"),
		)
	}
	if parsed.Host == "" {
		return "", failure.New(
			failure.CodeConfigInvalid,
			failure.WithMessage("upstream base url must include a host"),
		)
	}

	joined, err := url.JoinPath(trimmed, operationPath)
	if err != nil {
		return "", failure.Wrap(
			failure.CodeConfigInvalid,
			err,
			failure.WithMessage("join upstream operation path"),
		)
	}
	return joined, nil
}
