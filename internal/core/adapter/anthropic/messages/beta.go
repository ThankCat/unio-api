package messages

// supportedAnthropicBetas 是官方 1P adapter 允许转发到 upstream anthropic-beta 的白名单。
//
// ingress 按 DEC-013 宽进接受任意 beta；本表决定哪些值真正 Pass 到 Anthropic（未登记的不透传，
// 避免对客户做假承诺）。接入时可随官方文档扩展，见 providers/anthropic/upgrade-plan N1。
var supportedAnthropicBetas = map[string]struct{}{
	"prompt-caching-2024-07-31":              {},
	"fine-grained-tool-streaming-2025-05-14": {},
	"structured-outputs-2025-11-13":          {},
	"files-api-2025-04-14":                   {},
	"mcp-client-2025-04-04":                  {},
}

// filterSupportedBetas 只保留白名单内的 beta token，保持相对顺序、去重。
func filterSupportedBetas(betas []string) []string {
	if len(betas) == 0 {
		return nil
	}

	seen := make(map[string]struct{}, len(betas))
	out := make([]string, 0, len(betas))
	for _, beta := range betas {
		if _, ok := supportedAnthropicBetas[beta]; !ok {
			continue
		}
		if _, dup := seen[beta]; dup {
			continue
		}
		seen[beta] = struct{}{}
		out = append(out, beta)
	}

	if len(out) == 0 {
		return nil
	}

	return out
}

// droppedBetas 返回 ingress beta 中未命中白名单、因而不会转发 upstream 的 token（供脱敏审计）。
func droppedBetas(betas []string) []string {
	if len(betas) == 0 {
		return nil
	}

	var out []string
	for _, beta := range betas {
		if _, ok := supportedAnthropicBetas[beta]; ok {
			continue
		}
		out = append(out, beta)
	}

	return out
}
