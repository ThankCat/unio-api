//go:build billing_e2e

package lifecycle

import (
	"log/slog"
	"os"
)

// 本文件是「账单 E2E 构建」（-tags billing_e2e）下的故障注入开关（P2-6）。
//
// 只有显式带 billing_e2e 构建标签编译的二进制才会读取 BILLING_E2E_INJECT_SETTLEMENT_FAIL，
// 用于驱动 recovery 重试/dead/风险敞口收口的端到端验证。生产二进制不含本文件，零影响。

// faultInjectSettlementAlways 在 env=always 时让每次 raw settlement 失败（REC-02）。
func faultInjectSettlementAlways() bool {
	return os.Getenv("BILLING_E2E_INJECT_SETTLEMENT_FAIL") == "always"
}

// faultInjectSettlementOnce 在 env=once 时让内联首次结算失败、保留 pending job（REC-01）。
func faultInjectSettlementOnce() bool {
	return os.Getenv("BILLING_E2E_INJECT_SETTLEMENT_FAIL") == "once"
}

// WarnIfSettlementFaultInjectionConfigured 在 E2E 构建里提示当前注入模式（便于排查）。logger 为 nil 时 no-op。
func WarnIfSettlementFaultInjectionConfigured(logger *slog.Logger) {
	if logger == nil {
		return
	}
	if v := os.Getenv("BILLING_E2E_INJECT_SETTLEMENT_FAIL"); v != "" {
		logger.Warn("billing_e2e build: settlement fault injection ACTIVE", "mode", v)
	}
}
