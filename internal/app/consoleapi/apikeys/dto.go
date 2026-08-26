package apikeys

import (
	"time"

	consoleapikeys "github.com/ThankCat/unio-gateway/internal/service/console/apikeys"
)

// keyDTO 是密钥响应体，只有前缀没有明文。
// 明文单独挂在 createdKeyDTO 上——那是创建接口的 201 响应，也是它唯一一次出现的地方。
type keyDTO struct {
	ID              int64            `json:"id"`
	Name            string           `json:"name"`
	KeyPrefix       string           `json:"key_prefix"`
	Status          string           `json:"status"`
	SpendLimit      *string          `json:"spend_limit"`
	SpentTotal      string           `json:"spent_total"`
	PeriodChargeUSD string           `json:"period_charge_usd"`
	RequestCount    int64            `json:"request_count"`
	LastUsedAt      *string          `json:"last_used_at"`
	ExpiresAt       *string          `json:"expires_at"`
	CreatedAt       string           `json:"created_at"`
	UpdatedAt       string           `json:"updated_at"`
	Trend           []dailyChargeDTO `json:"trend"`
}

// createdKeyDTO 只用于创建接口的 201 响应。
type createdKeyDTO struct {
	keyDTO
	Plaintext string `json:"plaintext"`
}

type dailyChargeDTO struct {
	Day          string `json:"day"`
	RequestCount int64  `json:"request_count"`
	ChargeUSD    string `json:"charge_usd"`
}

type topModelDTO struct {
	ModelID      string `json:"model_id"`
	DisplayName  string `json:"display_name"`
	RequestCount int64  `json:"request_count"`
	ChargeUSD    string `json:"charge_usd"`
}

type detailDTO struct {
	keyDTO
	TopModels []topModelDTO `json:"top_models"`
}

type listData struct {
	Items    []keyDTO `json:"items"`
	Page     int      `json:"page"`
	PageSize int      `json:"page_size"`
	Total    int64    `json:"total"`
}

type summaryData struct {
	KeyTotal     int64  `json:"key_total"`
	KeyActive    int64  `json:"key_active"`
	NearLimit    int64  `json:"near_limit"`
	RequestCount int64  `json:"request_count"`
	ChargeUSD    string `json:"charge_usd"`
}

func toKeyDTO(key consoleapikeys.Key) keyDTO {
	return keyDTO{
		ID:              key.ID,
		Name:            key.Name,
		KeyPrefix:       key.KeyPrefix,
		Status:          key.Status,
		SpendLimit:      key.SpendLimit,
		SpentTotal:      key.SpentTotal,
		PeriodChargeUSD: key.PeriodChargeUSD,
		RequestCount:    key.RequestCount,
		LastUsedAt:      rfc3339Ptr(key.LastUsedAt),
		ExpiresAt:       rfc3339Ptr(key.ExpiresAt),
		CreatedAt:       key.CreatedAt.UTC().Format(time.RFC3339),
		UpdatedAt:       key.UpdatedAt.UTC().Format(time.RFC3339),
		Trend:           toDailyChargeDTOs(key.Trend),
	}
}

func toKeyDTOs(keys []consoleapikeys.Key) []keyDTO {
	out := make([]keyDTO, 0, len(keys))
	for _, key := range keys {
		out = append(out, toKeyDTO(key))
	}
	return out
}

func toDailyChargeDTOs(points []consoleapikeys.DailyCharge) []dailyChargeDTO {
	out := make([]dailyChargeDTO, 0, len(points))
	for _, point := range points {
		out = append(out, dailyChargeDTO{
			Day:          point.Day.UTC().Format(time.RFC3339),
			RequestCount: point.RequestCount,
			ChargeUSD:    point.ChargeUSD,
		})
	}
	return out
}

func toDetailDTO(detail consoleapikeys.Detail) detailDTO {
	models := make([]topModelDTO, 0, len(detail.TopModels))
	for _, model := range detail.TopModels {
		models = append(models, topModelDTO{
			ModelID:      model.ModelID,
			DisplayName:  model.DisplayName,
			RequestCount: model.RequestCount,
			ChargeUSD:    model.ChargeUSD,
		})
	}
	return detailDTO{keyDTO: toKeyDTO(detail.Key), TopModels: models}
}

func rfc3339Ptr(t *time.Time) *string {
	if t == nil {
		return nil
	}
	out := t.UTC().Format(time.RFC3339)
	return &out
}
