//go:build !billing_e2e

package lifecycle

import (
	"os"

	"go.uber.org/zap"
)

// 本文件是「生产构建」下的故障注入开关（P2-6）。
//
// 默认（不带 -tags billing_e2e）编译时，故障注入恒为关闭：结算热路径不再读任何 env，
// 杜绝「生产误设 BILLING_E2E_INJECT_SETTLEMENT_FAIL 导致每次结算都失败」。账单 E2E 需要
// 注入故障时，用 `go build/test -tags billing_e2e` 构建（见 fault_inject_e2e.go）。

// faultInjectSettlementAlways 生产构建恒为 false。
func faultInjectSettlementAlways() bool { return false }

// faultInjectSettlementOnce 生产构建恒为 false。
func faultInjectSettlementOnce() bool { return false }

// WarnIfSettlementFaultInjectionConfigured 在生产构建启动时检测到误设的故障注入 env 即告警。
//
// 即便生产构建不读取该 env 生效，运维误设仍可能意味着「以为开了注入其实没开」或配置漂移，
// 启动期显式告警有助于尽早发现。logger 为 nil 时 no-op。
func WarnIfSettlementFaultInjectionConfigured(logger *zap.Logger) {
	if logger == nil {
		return
	}
	if v := os.Getenv("BILLING_E2E_INJECT_SETTLEMENT_FAIL"); v != "" {
		logger.Warn(
			"BILLING_E2E_INJECT_SETTLEMENT_FAIL is set but ignored in this build; fault injection requires -tags billing_e2e",
			zap.String("value", v),
		)
	}
}
