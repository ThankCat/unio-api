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

// isNativeCompactUnsupported 判断上游是否不提供可用的原生 /responses/compact，据此决定回落 SyntheticCompact（Q2）。
//
// 触发条件：adapter 收敛的 sentinel ErrCompactUnsupported（404/405、响应无可计费 usage、无法解析），
// 或 error 链上的上游 404/405 状态。其余上游错误（鉴权/限流/超时/5xx）不视为「不支持」，按正常上游错误处理。
func isNativeCompactUnsupported(err error) bool {
	if errors.Is(err, responsesadapter.ErrCompactUnsupported) {
		return true
	}
	if meta, ok := adapter.UpstreamMetadataOf(err); ok {
		return meta.StatusCode == http.StatusNotFound || meta.StatusCode == http.StatusMethodNotAllowed
	}
	return false
}
