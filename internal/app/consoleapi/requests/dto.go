package requests

import (
	"time"

	consolerequests "github.com/ThankCat/unio-gateway/internal/service/console/requests"
)

type itemDTO struct {
	ID                        int64    `json:"id"`
	RequestID                 string   `json:"request_id"`
	CreatedAt                 string   `json:"created_at"`
	ClientIP                  string   `json:"client_ip"`
	RouteID                   *int64   `json:"route_id"`
	RouteName                 string   `json:"route_name"`
	APIKeyID                  int64    `json:"api_key_id"`
	APIKeyName                string   `json:"api_key_name"`
	APIKeyPrefix              string   `json:"api_key_prefix"`
	APIKeyPlaintext           *string  `json:"api_key_plaintext,omitempty"`
	Endpoint                  string   `json:"endpoint"`
	Stream                    bool     `json:"stream"`
	RequestedModelID          string   `json:"requested_model_id"`
	ModelDisplayName          string   `json:"model_display_name"`
	IngressProtocol           string   `json:"ingress_protocol"`
	InputPricePer1M           *string  `json:"input_price_per_1m,omitempty"`
	OutputPricePer1M          *string  `json:"output_price_per_1m,omitempty"`
	CacheReadPricePer1M       *string  `json:"cache_read_price_per_1m,omitempty"`
	CacheWrite5mPricePer1M    *string  `json:"cache_write_5m_price_per_1m,omitempty"`
	CacheWrite1hPricePer1M    *string  `json:"cache_write_1h_price_per_1m,omitempty"`
	CacheWrite30mPricePer1M   *string  `json:"cache_write_30m_price_per_1m,omitempty"`
	ReasoningOutputPricePer1M *string  `json:"reasoning_output_price_per_1m,omitempty"`
	PriceServiceTier          *string  `json:"price_service_tier,omitempty"`
	ReasoningEffort           *string  `json:"reasoning_effort"`
	UncachedInputTokens       int64    `json:"uncached_input_tokens"`
	CacheReadInputTokens      int64    `json:"cache_read_input_tokens"`
	CacheWrite5mInputTokens   int64    `json:"cache_write_5m_input_tokens"`
	CacheWrite1hInputTokens   int64    `json:"cache_write_1h_input_tokens"`
	CacheWrite30mInputTokens  int64    `json:"cache_write_30m_input_tokens"`
	InputTokens               int64    `json:"input_tokens"`
	OutputTokens              int64    `json:"output_tokens"`
	ReasoningOutputTokens     int64    `json:"reasoning_output_tokens"`
	LatencyMs                 *int64   `json:"latency_ms"`
	FirstTokenMs              *int64   `json:"first_token_ms,omitempty"`
	TPS                       *float64 `json:"tps,omitempty"`
	UserChargeUSD             string   `json:"user_charge_usd"`
}

type listData struct {
	Items    []itemDTO `json:"items"`
	Page     int       `json:"page"`
	PageSize int       `json:"page_size"`
	Total    int64     `json:"total"`
}

type summaryModelDTO struct {
	ModelID          string  `json:"model_id"`
	DisplayName      string  `json:"display_name"`
	RequestCount     int64   `json:"request_count"`
	IngressProtocol  string  `json:"ingress_protocol"`
	InputPricePer1M  *string `json:"input_price_per_1m"`
	OutputPricePer1M *string `json:"output_price_per_1m"`
}

type summaryData struct {
	RequestCount            int64             `json:"request_count"`
	StreamCount             int64             `json:"stream_count"`
	TokenCount              int64             `json:"token_count"`
	InputTokenCount         int64             `json:"input_token_count"`
	OutputTokenCount        int64             `json:"output_token_count"`
	UncachedInputTokenCount int64             `json:"uncached_input_token_count"`
	CacheReadTokenCount     int64             `json:"cache_read_token_count"`
	CacheWriteTokenCount    int64             `json:"cache_write_token_count"`
	ChargeUSD               string            `json:"charge_usd"`
	UncachedInputChargeUSD  string            `json:"uncached_input_charge_usd"`
	OutputChargeUSD         string            `json:"output_charge_usd"`
	CacheReadChargeUSD      string            `json:"cache_read_charge_usd"`
	CacheWriteChargeUSD     string            `json:"cache_write_charge_usd"`
	ListChargeUSD           string            `json:"list_charge_usd"`
	AverageLatencyMs        float64           `json:"average_latency_ms"`
	AverageFirstTokenMs     float64           `json:"average_first_token_ms"`
	MedianLatencyMs         float64           `json:"median_latency_ms"`
	AverageTPS              float64           `json:"average_tps"`
	TopModels               []summaryModelDTO `json:"top_models"`
}

type filtersData struct {
	Routes      []consolerequests.FilterOption `json:"routes"`
	APIKeys     []consolerequests.FilterOption `json:"api_keys"`
	Endpoints   []string                       `json:"endpoints"`
	StreamTypes []string                       `json:"stream_types"`
}

func toItemDTO(item consolerequests.Item) itemDTO {
	createdAt := ""
	if !item.CreatedAt.IsZero() {
		createdAt = item.CreatedAt.UTC().Format(time.RFC3339Nano)
	}
	return itemDTO{
		ID:                        item.ID,
		RequestID:                 item.RequestID,
		CreatedAt:                 createdAt,
		ClientIP:                  item.ClientIP,
		RouteID:                   item.RouteID,
		RouteName:                 item.RouteName,
		APIKeyID:                  item.APIKeyID,
		APIKeyName:                item.APIKeyName,
		APIKeyPrefix:              item.APIKeyPrefix,
		APIKeyPlaintext:           item.APIKeyPlaintext,
		Endpoint:                  item.Endpoint,
		Stream:                    item.Stream,
		RequestedModelID:          item.RequestedModelID,
		ModelDisplayName:          item.ModelDisplayName,
		IngressProtocol:           item.IngressProtocol,
		InputPricePer1M:           item.InputPricePer1M,
		OutputPricePer1M:          item.OutputPricePer1M,
		CacheReadPricePer1M:       item.CacheReadPricePer1M,
		CacheWrite5mPricePer1M:    item.CacheWrite5mPricePer1M,
		CacheWrite1hPricePer1M:    item.CacheWrite1hPricePer1M,
		CacheWrite30mPricePer1M:   item.CacheWrite30mPricePer1M,
		ReasoningOutputPricePer1M: item.ReasoningOutputPricePer1M,
		PriceServiceTier:          item.PriceServiceTier,
		ReasoningEffort:           item.ReasoningEffort,
		UncachedInputTokens:       item.UncachedInputTokens,
		CacheReadInputTokens:      item.CacheReadInputTokens,
		CacheWrite5mInputTokens:   item.CacheWrite5mInputTokens,
		CacheWrite1hInputTokens:   item.CacheWrite1hInputTokens,
		CacheWrite30mInputTokens:  item.CacheWrite30mInputTokens,
		InputTokens:               item.InputTokens,
		OutputTokens:              item.OutputTokens,
		ReasoningOutputTokens:     item.ReasoningOutputTokens,
		LatencyMs:                 item.LatencyMs,
		FirstTokenMs:              item.FirstTokenMs,
		TPS:                       item.TPS,
		UserChargeUSD:             item.UserChargeUSD,
	}
}

func toSummaryModelDTOs(models []consolerequests.SummaryModel) []summaryModelDTO {
	out := make([]summaryModelDTO, 0, len(models))
	for _, model := range models {
		out = append(out, summaryModelDTO{
			ModelID:          model.ModelID,
			DisplayName:      model.DisplayName,
			RequestCount:     model.RequestCount,
			IngressProtocol:  model.IngressProtocol,
			InputPricePer1M:  model.InputPricePer1M,
			OutputPricePer1M: model.OutputPricePer1M,
		})
	}
	return out
}

func toItemDTOs(items []consolerequests.Item) []itemDTO {
	out := make([]itemDTO, 0, len(items))
	for _, item := range items {
		out = append(out, toItemDTO(item))
	}
	return out
}
