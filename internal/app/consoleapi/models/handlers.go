package models

import (
	"net/http"
	"time"

	"github.com/ThankCat/unio-gateway/internal/app/consoleapi/transport"
	consoleservice "github.com/ThankCat/unio-gateway/internal/service/console"
	"github.com/ThankCat/unio-gateway/internal/service/publicmodels"
)

// priceVectorDTO 是一组按分项展开的单价（十进制字符串）；null 表示该分项未定价。
type priceVectorDTO struct {
	UncachedInput    *string `json:"uncached_input"`
	CacheRead        *string `json:"cache_read"`
	CacheCreation5m  *string `json:"cache_creation_5m"`
	CacheCreation1h  *string `json:"cache_creation_1h"`
	CacheCreation30m *string `json:"cache_creation_30m"`
	Output           *string `json:"output"`
	ReasoningOutput  *string `json:"reasoning_output"`
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
	// IconSVG 来自能力字典（24×24 stroke、currentColor），前端固定尺寸内联渲染。
	IconSVG string `json:"icon_svg"`
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

type listData struct {
	Models []modelDTO `json:"models"`
}

func (h *handler) list(w http.ResponseWriter, r *http.Request) {
	items, err := h.service.List(r.Context())
	if err != nil {
		h.errorWriter.Write(w, r, &consoleservice.Error{
			Code:    "internal_error",
			Message: "Failed to load the model catalog.",
			Status:  http.StatusInternalServerError,
		})
		return
	}

	dtos := make([]modelDTO, 0, len(items))
	for _, m := range items {
		dtos = append(dtos, toModelDTO(m))
	}
	_ = transport.WriteData(w, http.StatusOK, listData{Models: dtos})
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
		dto.Capabilities = append(dto.Capabilities, capabilityDTO{
			Key:          c.Key,
			SupportLevel: c.SupportLevel,
			IconSVG:      c.IconSVG,
		})
	}
	return dto
}

func toPriceGroupDTO(g publicmodels.PriceGroup) priceGroupDTO {
	return priceGroupDTO{List: toPriceVectorDTO(g.List), Sale: toPriceVectorDTO(g.Sale)}
}

func toPriceVectorDTO(v publicmodels.PriceVector) priceVectorDTO {
	return priceVectorDTO{
		UncachedInput:    v.UncachedInput,
		CacheRead:        v.CacheRead,
		CacheCreation5m:  v.CacheCreation5m,
		CacheCreation1h:  v.CacheCreation1h,
		CacheCreation30m: v.CacheCreation30m,
		Output:           v.Output,
		ReasoningOutput:  v.ReasoningOutput,
	}
}
