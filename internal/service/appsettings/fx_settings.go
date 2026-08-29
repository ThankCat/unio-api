package appsettings

import (
	"context"
	"encoding/json"
	"strings"
)

// GatewayExchangeRateAPIKeyKey 是 ExchangeRate-API（主汇率源）API Key 的运行时配置项。
// 双层解析（多货币 PLAN 5.1）：设置表值优先，空值回退环境变量 EXCHANGE_RATE_API_KEY——
// 管理员可在系统设置页运行时换 key，配合「验证 API Key」按钮当场校验，无需重启。
const GatewayExchangeRateAPIKeyKey = "gateway.exchange_rate_api_key"

func exchangeRateAPIKeyDefinition() Definition {
	return Definition{
		Key:      GatewayExchangeRateAPIKeyKey,
		Category: "gateway",
		Label:    "汇率源 API Key",
		Description: "ExchangeRate-API（主汇率源）的 API Key。留空时回退环境变量 EXCHANGE_RATE_API_KEY；" +
			"热改生效，改后可用汇率页「验证 API Key」按钮当场校验。备源（er-api / Frankfurter）免 key，不受影响。",
		HotReload: true,
		Default:   json.RawMessage(`""`),
		Validate: func(raw json.RawMessage) error {
			var s string
			return json.Unmarshal(raw, &s)
		},
	}
}

// GatewayExchangeRateAPIKey 读取当前生效的汇率源 API Key：设置表非空值优先，否则回退 envFallback。
func GatewayExchangeRateAPIKey(ctx context.Context, store *SettingsStore, envFallback string) string {
	var key string
	if err := json.Unmarshal(store.Raw(ctx, GatewayExchangeRateAPIKeyKey), &key); err == nil {
		if key = strings.TrimSpace(key); key != "" {
			return key
		}
	}
	return envFallback
}
