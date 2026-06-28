package responses

import (
	"errors"
	"net/http"

	"github.com/ThankCat/unio-api/internal/core/adapter"
	responsesadapter "github.com/ThankCat/unio-api/internal/core/adapter/openai/responses"
)

// compact_native.go 承载 NativeCompact（原生 /responses/compact 透传）的 service 侧判定辅助。
//
// 上送请求体重放复用 encodeUpstreamResponsesBody（direct_response.go），上游响应原文透传复用
// rewriteResponsesModel（仅改写 model 回显）；本文件只判定「上游是否不支持原生 compact」以决定回落 Synthetic。

// isNativeCompactUnsupported 判断上游是否「确实不提供」原生 /responses/compact endpoint（404/405，无成本），
// 据此决定安全回落 SyntheticCompact（Q2）。
//
// 触发条件：adapter 收敛的 sentinel ErrCompactUnsupported（仅 404/405），或 error 链上的上游 404/405 状态。
// 注意：原生 2xx 但缺 usage 不再归此类（见 isNativeCompactMissingUsage），因为那种情形上游很可能已计费，
// 静默回落会白嫖。其余上游错误（鉴权/限流/超时/5xx）也不视为「不支持」，按正常上游错误处理。
func isNativeCompactUnsupported(err error) bool {
	if errors.Is(err, responsesadapter.ErrCompactUnsupported) {
		return true
	}
	// 2xx 缺 usage 也会带 UpstreamMetadata，但其 StatusCode 为 2xx，不会落入 404/405 判定。
	if meta, ok := adapter.UpstreamMetadataOf(err); ok {
		return meta.StatusCode == http.StatusNotFound || meta.StatusCode == http.StatusMethodNotAllowed
	}
	return false
}

// isNativeCompactMissingUsage 判断原生 compact 是否返回 2xx 却拿不到可计费 usage（上游很可能已产生成本）。
//
// 命中时绝不静默回落 Synthetic（会「双调上游、只收一次费」白嫖），而是上抛交由 lifecycle 记 risk_exposure
// 并报错（P0-3）。与 isNativeCompactUnsupported（真 404/405、无成本）互斥。
func isNativeCompactMissingUsage(err error) bool {
	return errors.Is(err, responsesadapter.ErrCompactMissingUsage)
}
