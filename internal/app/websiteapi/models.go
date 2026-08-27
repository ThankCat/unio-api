package websiteapi

import (
	"context"
	"net/http"
	"time"

	"go.uber.org/zap"

	"github.com/ThankCat/unio-gateway/internal/platform/httpx"
	"github.com/ThankCat/unio-gateway/internal/service/publicmodels"
)

// ModelsService 定义 website 模型目录所需的最小能力。
type ModelsService interface {
	List(ctx context.Context) ([]publicmodels.Model, error)
	ListDiscountHistory(ctx context.Context, window time.Duration) ([]publicmodels.DiscountHistory, error)
}

// discountHistoryWindow 是折扣走势的回看窗口，与前端「近 48 小时」文案一致。
const discountHistoryWindow = 48 * time.Hour

// modelsCacheControl 是模型目录的公共缓存策略：营销页价格允许最长 5 分钟的陈旧窗口，
// 换取 CDN/浏览器直接命中；与 website 侧 ISR 的 revalidate 周期一致。
const modelsCacheControl = "public, max-age=300"

// priceVectorDTO 是一组按分项展开的单价（十进制字符串）；null 表示该分项未定价。
type priceVectorDTO struct {
	UncachedInput   *string `json:"uncached_input"`
	CacheRead       *string `json:"cache_read"`
	CacheWrite5m    *string `json:"cache_write_5m"`
	CacheWrite1h    *string `json:"cache_write_1h"`
	CacheWrite30m   *string `json:"cache_write_30m"`
	Output          *string `json:"output"`
	ReasoningOutput *string `json:"reasoning_output"`
}

// priceGroupDTO 是某个服务档位的「官方牌价 + 对客售价」对照。
type priceGroupDTO struct {
	List priceVectorDTO `json:"list"`
	Sale priceVectorDTO `json:"sale"`
}

type longContextDTO struct {
	ThresholdTokens  int64  `json:"threshold_tokens"`
	InputMultiplier  string `json:"input_multiplier"`
	OutputMultiplier string `json:"output_multiplier"`
}

type capabilityDTO struct {
	Key          string `json:"key"`
	SupportLevel string `json:"support_level"`
}

type modelDTO struct {
	ModelID             string          `json:"model_id"`
	DisplayName         string          `json:"display_name"`
	Lab                 string          `json:"lab"`
	Family              string          `json:"family"`
	Description         string          `json:"description"`
	KnowledgeCutoff     string          `json:"knowledge_cutoff"`
	Currency            string          `json:"currency"`
	ContextWindowTokens *int64          `json:"context_window_tokens"`
	MaxOutputTokens     *int64          `json:"max_output_tokens"`
	ReleaseDate         *string         `json:"release_date"`
	Standard            priceGroupDTO   `json:"standard"`
	Fast                *priceGroupDTO  `json:"fast"`
	SaleRatio           *string         `json:"sale_ratio"`
	LongContext         *longContextDTO `json:"long_context"`
	Capabilities        []capabilityDTO `json:"capabilities"`
	LabHasLogo          bool            `json:"lab_has_logo"`
	PriceEffectiveFrom  string          `json:"price_effective_from"`
}

type modelsData struct {
	Models []modelDTO `json:"models"`
	// GeneratedAt 供消费方判断数据新鲜度（配合 max-age 的陈旧窗口）。
	GeneratedAt string `json:"generated_at"`
}

type modelsHandler struct {
	service ModelsService
	logger  *zap.Logger
}

func (h *modelsHandler) list(w http.ResponseWriter, r *http.Request) {
	models, err := h.service.List(r.Context())
	if err != nil {
		h.logger.Error("website list models failed", zap.Error(err))
		_ = httpx.WriteError(w, http.StatusInternalServerError, "internal_error", "failed to load models")
		return
	}

	dtos := make([]modelDTO, 0, len(models))
	for _, m := range models {
		dtos = append(dtos, toModelDTO(m))
	}

	w.Header().Set("Cache-Control", modelsCacheControl)
	_ = httpx.WriteJSON(w, http.StatusOK, map[string]modelsData{"data": {
		Models:      dtos,
		GeneratedAt: time.Now().UTC().Format(time.RFC3339),
	}})
}

// discountPointDTO 是一个采样点；ratio 为 null 表示该时刻尚无生效价格，前端应断开折线。
type discountPointDTO struct {
	At    string   `json:"at"`
	Ratio *float64 `json:"ratio"`
}

type discountHistoryDTO struct {
	ModelID string             `json:"model_id"`
	Points  []discountPointDTO `json:"points"`
	Current *float64           `json:"current"`
	Average *float64           `json:"average"`
	Min     *float64           `json:"min"`
}

type discountHistoryData struct {
	WindowHours int                  `json:"window_hours"`
	Histories   []discountHistoryDTO `json:"histories"`
	GeneratedAt string               `json:"generated_at"`
}

// discountHistory 返回各模型近 48 小时的折扣走势（由 model_prices 生效窗口回放而来）。
func (h *modelsHandler) discountHistory(w http.ResponseWriter, r *http.Request) {
	histories, err := h.service.ListDiscountHistory(r.Context(), discountHistoryWindow)
	if err != nil {
		h.logger.Error("website list discount history failed", zap.Error(err))
		_ = httpx.WriteError(w, http.StatusInternalServerError, "internal_error", "failed to load discount history")
		return
	}

	dtos := make([]discountHistoryDTO, 0, len(histories))
	for _, item := range histories {
		points := make([]discountPointDTO, 0, len(item.Points))
		for _, p := range item.Points {
			points = append(points, discountPointDTO{At: p.At.UTC().Format(time.RFC3339), Ratio: p.Ratio})
		}
		dtos = append(dtos, discountHistoryDTO{
			ModelID: item.ModelID,
			Points:  points,
			Current: item.Current,
			Average: item.Average,
			Min:     item.Min,
		})
	}

	w.Header().Set("Cache-Control", modelsCacheControl)
	_ = httpx.WriteJSON(w, http.StatusOK, map[string]discountHistoryData{"data": {
		WindowHours: int(discountHistoryWindow / time.Hour),
		Histories:   dtos,
		GeneratedAt: time.Now().UTC().Format(time.RFC3339),
	}})
}

func toModelDTO(m publicmodels.Model) modelDTO {
	dto := modelDTO{
		ModelID:             m.ModelID,
		DisplayName:         m.DisplayName,
		Lab:                 m.Lab,
		Family:              m.Family,
		Description:         m.Description,
		KnowledgeCutoff:     m.KnowledgeCutoff,
		Currency:            m.Currency,
		ContextWindowTokens: m.ContextWindowTokens,
		MaxOutputTokens:     m.MaxOutputTokens,
		Standard:            toPriceGroupDTO(m.Standard),
		SaleRatio:           m.SaleRatio,
		LabHasLogo:          m.LabHasLogo,
		PriceEffectiveFrom:  m.PriceEffectiveFrom.UTC().Format(time.RFC3339),
	}
	if m.ReleaseDate != nil {
		formatted := m.ReleaseDate.Format("2006-01-02")
		dto.ReleaseDate = &formatted
	}
	if m.Fast != nil {
		fast := toPriceGroupDTO(*m.Fast)
		dto.Fast = &fast
	}
	if m.LongContext != nil {
		dto.LongContext = &longContextDTO{
			ThresholdTokens:  m.LongContext.ThresholdTokens,
			InputMultiplier:  m.LongContext.InputMultiplier,
			OutputMultiplier: m.LongContext.OutputMultiplier,
		}
	}
	dto.Capabilities = make([]capabilityDTO, 0, len(m.Capabilities))
	for _, c := range m.Capabilities {
		dto.Capabilities = append(dto.Capabilities, capabilityDTO{Key: c.Key, SupportLevel: c.SupportLevel})
	}
	return dto
}

func toPriceGroupDTO(g publicmodels.PriceGroup) priceGroupDTO {
	return priceGroupDTO{List: toPriceVectorDTO(g.List), Sale: toPriceVectorDTO(g.Sale)}
}

func toPriceVectorDTO(v publicmodels.PriceVector) priceVectorDTO {
	return priceVectorDTO{
		UncachedInput:   v.UncachedInput,
		CacheRead:       v.CacheRead,
		CacheWrite5m:    v.CacheWrite5m,
		CacheWrite1h:    v.CacheWrite1h,
		CacheWrite30m:   v.CacheWrite30m,
		Output:          v.Output,
		ReasoningOutput: v.ReasoningOutput,
	}
}
