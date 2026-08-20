package requests

import (
	"time"

	consolerequests "github.com/ThankCat/unio-gateway/internal/service/console/requests"
)

type itemDTO struct {
	ID               int64   `json:"id"`
	RequestID        string  `json:"request_id"`
	CreatedAt        string  `json:"created_at"`
	ClientIP         string  `json:"client_ip"`
	RouteID          *int64  `json:"route_id"`
	RouteName        string  `json:"route_name"`
	APIKeyID         int64   `json:"api_key_id"`
	APIKeyName       string  `json:"api_key_name"`
	Endpoint         string  `json:"endpoint"`
	Stream           bool    `json:"stream"`
	RequestedModelID string  `json:"requested_model_id"`
	ReasoningEffort  *string `json:"reasoning_effort"`
	InputTokens      int64   `json:"input_tokens"`
	OutputTokens     int64   `json:"output_tokens"`
	LatencyMs        *int64  `json:"latency_ms"`
	UserChargeUSD    string  `json:"user_charge_usd"`
}

type listData struct {
	Items    []itemDTO `json:"items"`
	Page     int       `json:"page"`
	PageSize int       `json:"page_size"`
	Total    int64     `json:"total"`
}

type summaryData struct {
	RequestCount     int64   `json:"request_count"`
	TokenCount       int64   `json:"token_count"`
	InputTokenCount  int64   `json:"input_token_count"`
	OutputTokenCount int64   `json:"output_token_count"`
	ChargeUSD        string  `json:"charge_usd"`
	AverageLatencyMs float64 `json:"average_latency_ms"`
}

type filtersData struct {
	Routes    []consolerequests.FilterOption `json:"routes"`
	APIKeys   []consolerequests.FilterOption `json:"api_keys"`
	Endpoints []string                       `json:"endpoints"`
}

func toItemDTO(item consolerequests.Item) itemDTO {
	createdAt := ""
	if !item.CreatedAt.IsZero() {
		createdAt = item.CreatedAt.UTC().Format(time.RFC3339Nano)
	}
	return itemDTO{
		ID:               item.ID,
		RequestID:        item.RequestID,
		CreatedAt:        createdAt,
		ClientIP:         item.ClientIP,
		RouteID:          item.RouteID,
		RouteName:        item.RouteName,
		APIKeyID:         item.APIKeyID,
		APIKeyName:       item.APIKeyName,
		Endpoint:         item.Endpoint,
		Stream:           item.Stream,
		RequestedModelID: item.RequestedModelID,
		ReasoningEffort:  item.ReasoningEffort,
		InputTokens:      item.InputTokens,
		OutputTokens:     item.OutputTokens,
		LatencyMs:        item.LatencyMs,
		UserChargeUSD:    item.UserChargeUSD,
	}
}

func toItemDTOs(items []consolerequests.Item) []itemDTO {
	out := make([]itemDTO, 0, len(items))
	for _, item := range items {
		out = append(out, toItemDTO(item))
	}
	return out
}
