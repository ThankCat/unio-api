package lifecycle

import (
	"context"
	"errors"

	"github.com/ThankCat/unio-gateway/internal/core/adapter"
	"github.com/ThankCat/unio-gateway/internal/platform/failure"
)

// stickyFailureVerdict 是一次 attempt 失败对 sticky 绑定的处置（§10.7/§10.8）。
type stickyFailureVerdict struct {
	// clear 为 true 时按「已确认的永久渠道故障」CAS 清除绑定。
	clear bool
	// reason 是写入审计的稳定原因；clear=false 时也可能有值（临时绕行）。
	reason string
	// temporaryBypass 为 true 表示原绑定只是暂时不可用：保留绑定且不刷新 TTL。
	temporaryBypass bool
}

// classifyStickyFailure 判定一次 attempt 失败是否应清除 sticky 绑定。
//
// 原则（§10.7）：永久失去基础池或候选资格才清绑定。临时状态（并发满、上游 429）保留绑定；
// 客户取消、普通 4xx、以及 gateway 自身的数据库/Redis/结算/平台错误一律不动绑定（§10.8）——
// 否则一次网关内部故障会把所有会话的 prompt cache 亲和性一次性打散。
func classifyStickyFailure(err error) stickyFailureVerdict {
	if err == nil {
		return stickyFailureVerdict{}
	}

	// Gateway 自身故障不归咎渠道：绝不因为我们自己的存储/结算问题清客户的绑定。
	switch failure.CodeOf(err) {
	case failure.CodeGatewayBreakerStoreUnavailable,
		failure.CodeGatewayRuntimeSyncRequired,
		failure.CodeGatewayRuntimeStateLost,
		failure.CodeRoutingChannelCapacityExhausted:
		return stickyFailureVerdict{reason: "gateway_runtime_fault"}
	case failure.CodeGatewayChannelRateLimited:
		return stickyFailureVerdict{reason: "cooldown", temporaryBypass: true}
	}
	if errors.Is(err, ErrAttemptRuntimeFeedback) || errors.Is(err, errAttemptPermitFinish) {
		return stickyFailureVerdict{reason: "gateway_runtime_fault"}
	}

	category, classified := adapter.UpstreamCategoryOf(err)
	if !classified {
		// 未分类错误保守处理：不清绑定，也不声称是临时绕行。
		return stickyFailureVerdict{reason: "unclassified"}
	}
	switch category {
	case adapter.UpstreamErrorRateLimit:
		// 上游真实 429：临时保留绑定（§10.6），冷却本身会让本次绕行。
		return stickyFailureVerdict{reason: "upstream_rate_limit", temporaryBypass: true}
	case adapter.UpstreamErrorCanceled:
		// 客户取消不归咎渠道，也不刷新 TTL（§10.8/§10.10）。
		return stickyFailureVerdict{reason: "client_canceled"}
	case adapter.UpstreamErrorBadRequest:
		// 客户请求本身非法：换渠道也一样失败，绑定与渠道健康无关。
		return stickyFailureVerdict{reason: "client_bad_request"}
	case adapter.UpstreamErrorAuth:
		// 401 凭据失效是已确认的渠道故障（§10.7）。
		return stickyFailureVerdict{clear: true, reason: "upstream_credential_invalid"}
	case adapter.UpstreamErrorPermission:
		// 403 模型权限失效同样是渠道级永久失格。
		return stickyFailureVerdict{clear: true, reason: "upstream_model_permission_revoked"}
	case adapter.UpstreamErrorTimeout:
		return stickyFailureVerdict{clear: true, reason: "upstream_timeout"}
	case adapter.UpstreamErrorServer:
		return stickyFailureVerdict{clear: true, reason: "upstream_server_error"}
	default:
		return stickyFailureVerdict{clear: true, reason: "upstream_" + string(category)}
	}
}

// applyStickyAttemptFailure 按 §10.7/§10.8 处置一次 attempt 失败对绑定的影响。
// 只有失败渠道恰为当前绑定渠道时才有动作。
func applyStickyAttemptFailure(ctx context.Context, session *StickySession, channelID int64, err error) {
	if session.BoundChannelID() != channelID {
		return
	}
	verdict := classifyStickyFailure(err)
	switch {
	case verdict.clear:
		session.ClearOnPermanentFailure(ctx, verdict.reason)
	case verdict.temporaryBypass:
		session.PreserveOnTemporaryBypass(ctx, channelID, verdict.reason)
	}
}
