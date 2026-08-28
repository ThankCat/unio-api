package requests

import (
	"time"

	consolerequests "github.com/ThankCat/unio-gateway/internal/service/console/requests"
)

type itemDTO struct {
	ID                          int64    `json:"id"`
	RequestID                   string   `json:"request_id"`
	CreatedAt                   string   `json:"created_at"`
	ClientIP                    string   `json:"client_ip"`
	APIKeyID                    int64    `json:"api_key_id"`
	APIKeyName                  string   `json:"api_key_name"`
	APIKeyPrefix                string   `json:"api_key_prefix"`
	Endpoint                    string   `json:"endpoint"`
	Stream                      bool     `json:"stream"`
	RequestedModelID            string   `json:"requested_model_id"`
	ModelDisplayName            string   `json:"model_display_name"`
	IngressProtocol             string   `json:"ingress_protocol"`
	InputPricePer1M             *string  `json:"input_price_per_1m,omitempty"`
	OutputPricePer1M            *string  `json:"output_price_per_1m,omitempty"`
	CacheReadPricePer1M         *string  `json:"cache_read_price_per_1m,omitempty"`
	CacheCreation5mPricePer1M   *string  `json:"cache_creation_5m_price_per_1m,omitempty"`
	CacheCreation1hPricePer1M   *string  `json:"cache_creation_1h_price_per_1m,omitempty"`
	CacheCreation30mPricePer1M  *string  `json:"cache_creation_30m_price_per_1m,omitempty"`
	ReasoningOutputPricePer1M   *string  `json:"reasoning_output_price_per_1m,omitempty"`
	PriceServiceTier            *string  `json:"price_service_tier,omitempty"`
	ReasoningEffort             *string  `json:"reasoning_effort"`
	UncachedInputTokens         int64    `json:"uncached_input_tokens"`
	CacheReadInputTokens        int64    `json:"cache_read_input_tokens"`
	CacheCreation5mInputTokens  int64    `json:"cache_creation_5m_input_tokens"`
	CacheCreation1hInputTokens  int64    `json:"cache_creation_1h_input_tokens"`
	CacheCreation30mInputTokens int64    `json:"cache_creation_30m_input_tokens"`
	InputTokens                 int64    `json:"input_tokens"`
	OutputTokens                int64    `json:"output_tokens"`
	ReasoningOutputTokens       int64    `json:"reasoning_output_tokens"`
	LatencyMs                   *int64   `json:"latency_ms"`
	FirstTokenMs                *int64   `json:"first_token_ms,omitempty"`
	TPS                         *float64 `json:"tps,omitempty"`
	UserChargeUSD               string   `json:"user_charge_usd"`
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

// summaryWindowDTO 是上一周期的对照值，只带卡片会用到的四个指标。
type summaryWindowDTO struct {
	RequestCount     int64   `json:"request_count"`
	TokenCount       int64   `json:"token_count"`
	ChargeUSD        string  `json:"charge_usd"`
	AverageLatencyMs float64 `json:"average_latency_ms"`
}

// summaryPointDTO 是热力条的一格。
type summaryPointDTO struct {
	BucketStart      string  `json:"bucket_start"`
	RequestCount     int64   `json:"request_count"`
	TokenCount       int64   `json:"token_count"`
	ChargeUSD        string  `json:"charge_usd"`
	AverageLatencyMs float64 `json:"average_latency_ms"`
}

type summaryData struct {
	RequestCount            int64             `json:"request_count"`
	StreamCount             int64             `json:"stream_count"`
	TokenCount              int64             `json:"token_count"`
	InputTokenCount         int64             `json:"input_token_count"`
	OutputTokenCount        int64             `json:"output_token_count"`
	UncachedInputTokenCount int64             `json:"uncached_input_token_count"`
	CacheReadTokenCount     int64             `json:"cache_read_token_count"`
	CacheCreationTokenCount int64             `json:"cache_creation_token_count"`
	ChargeUSD               string            `json:"charge_usd"`
	UncachedInputChargeUSD  string            `json:"uncached_input_charge_usd"`
	OutputChargeUSD         string            `json:"output_charge_usd"`
	CacheReadChargeUSD      string            `json:"cache_read_charge_usd"`
	CacheCreationChargeUSD  string            `json:"cache_creation_charge_usd"`
	ListChargeUSD           string            `json:"list_charge_usd"`
	AverageLatencyMs        float64           `json:"average_latency_ms"`
	AverageFirstTokenMs     float64           `json:"average_first_token_ms"`
	MedianLatencyMs         float64           `json:"median_latency_ms"`
	AverageTPS              float64           `json:"average_tps"`
	TopModels               []summaryModelDTO `json:"top_models"`
	Previous                *summaryWindowDTO `json:"previous,omitempty"`
	Series                  []summaryPointDTO `json:"series,omitempty"`
}

func toSummaryWindowDTO(window *consolerequests.Window) *summaryWindowDTO {
	if window == nil {
		return nil
	}
	return &summaryWindowDTO{
		AverageLatencyMs: window.AverageLatencyMs,
		ChargeUSD:        window.ChargeUSD,
		RequestCount:     window.RequestCount,
		TokenCount:       window.TokenCount,
	}
}

func toSummaryPointDTOs(points []consolerequests.Point) []summaryPointDTO {
	if len(points) == 0 {
		return nil
	}
	out := make([]summaryPointDTO, 0, len(points))
	for _, point := range points {
		bucketStart := ""
		if !point.BucketStart.IsZero() {
			bucketStart = point.BucketStart.UTC().Format(time.RFC3339Nano)
		}
		out = append(out, summaryPointDTO{
			AverageLatencyMs: point.AverageLatencyMs,
			BucketStart:      bucketStart,
			ChargeUSD:        point.ChargeUSD,
			RequestCount:     point.RequestCount,
			TokenCount:       point.TokenCount,
		})
	}
	return out
}

type filtersData struct {
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
		ID:                          item.ID,
		RequestID:                   item.RequestID,
		CreatedAt:                   createdAt,
		ClientIP:                    item.ClientIP,
		APIKeyID:                    item.APIKeyID,
		APIKeyName:                  item.APIKeyName,
		APIKeyPrefix:                item.APIKeyPrefix,
		Endpoint:                    item.Endpoint,
		Stream:                      item.Stream,
		RequestedModelID:            item.RequestedModelID,
		ModelDisplayName:            item.ModelDisplayName,
		IngressProtocol:             item.IngressProtocol,
		InputPricePer1M:             item.InputPricePer1M,
		OutputPricePer1M:            item.OutputPricePer1M,
		CacheReadPricePer1M:         item.CacheReadPricePer1M,
		CacheCreation5mPricePer1M:   item.CacheCreation5mPricePer1M,
		CacheCreation1hPricePer1M:   item.CacheCreation1hPricePer1M,
		CacheCreation30mPricePer1M:  item.CacheCreation30mPricePer1M,
		ReasoningOutputPricePer1M:   item.ReasoningOutputPricePer1M,
		PriceServiceTier:            item.PriceServiceTier,
		ReasoningEffort:             item.ReasoningEffort,
		UncachedInputTokens:         item.UncachedInputTokens,
		CacheReadInputTokens:        item.CacheReadInputTokens,
		CacheCreation5mInputTokens:  item.CacheCreation5mInputTokens,
		CacheCreation1hInputTokens:  item.CacheCreation1hInputTokens,
		CacheCreation30mInputTokens: item.CacheCreation30mInputTokens,
		InputTokens:                 item.InputTokens,
		OutputTokens:                item.OutputTokens,
		ReasoningOutputTokens:       item.ReasoningOutputTokens,
		LatencyMs:                   item.LatencyMs,
		FirstTokenMs:                item.FirstTokenMs,
		TPS:                         item.TPS,
		UserChargeUSD:               item.UserChargeUSD,
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
