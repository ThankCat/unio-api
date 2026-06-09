// Package modelcatalog 把 models.dev 数据源同步为 Unio 能力架构 Layer 1 的模型目录种子。
//
// 它消费 models.dev 的 models.json（canonical 元数据）与 api.json（每 provider 价格基线），
// 按合并规则维护 models 表：source=manual 行永不被覆盖、新模型默认 disabled、上游删除只标记不删除。
// models.dev 仅作种子源，不是运行时事实源（DEC-015）；license 与 attribution 见
// docs/datasources/MODELS_DEV_LICENSE.md。
package modelcatalog

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"
	"strings"
	"time"

	"github.com/ThankCat/unio-api/internal/core/capability"
	"github.com/ThankCat/unio-api/internal/platform/failure"
)

// modelsJSONEntry 是 models.dev models.json 的 canonical 模型元数据（按 lab/model 键控）。
type modelsJSONEntry struct {
	ID               string         `json:"id"`
	Name             string         `json:"name"`
	Family           string         `json:"family"`
	Attachment       bool           `json:"attachment"`
	Reasoning        bool           `json:"reasoning"`
	ToolCall         bool           `json:"tool_call"`
	StructuredOutput bool           `json:"structured_output"`
	ReleaseDate      string         `json:"release_date"`
	Modalities       modalitiesJSON `json:"modalities"`
	Limit            limitJSON      `json:"limit"`
}

type modalitiesJSON struct {
	Input  []string `json:"input"`
	Output []string `json:"output"`
}

type limitJSON struct {
	Context *int64 `json:"context"`
	Output  *int64 `json:"output"`
}

// apiProviderJSON 是 models.dev api.json 的单个 provider（按 provider id 键控）。
type apiProviderJSON struct {
	ID     string                  `json:"id"`
	Models map[string]apiModelJSON `json:"models"`
}

// apiModelJSON 是 api.json 内 provider 模型条目，仅取价格基线。
type apiModelJSON struct {
	Cost costJSON `json:"cost"`
}

// costJSON 用 json.Number 承载价格字面量，避免 float 精度损失（价格仅展示，绝不用于计费）。
type costJSON struct {
	Input  json.Number `json:"input"`
	Output json.Number `json:"output"`
}

// CanonicalModel 是 models.dev 一条 canonical 模型合并后的 Layer 1 种子。
type CanonicalModel struct {
	CanonicalID     string
	Lab             string
	DisplayName     string
	ReleaseDate     *time.Time
	ContextTokens   *int64
	MaxOutputTokens *int64
	// InputPrice / OutputPrice 是十进制字符串（USD / 百万 token），nil 表示该模型无价格基线。
	InputPrice  *string
	OutputPrice *string
	// CoarseCapabilities 是 models.dev 粗能力位映射，仅在模型首次入库时写入 source=models_dev。
	CoarseCapabilities []capability.Declaration
}

// Feed 是一次 models.dev 拉取解析后的全部 canonical 模型（按 canonical_id 升序）。
type Feed struct {
	Models []CanonicalModel
	// Fingerprint 是本次源数据指纹，用于 license/版本审计与变更检测。
	Fingerprint string
}

// ParseFeed 解析 models.json（必需）与 api.json（价格，可空），合并为 canonical 模型种子。
//
// api.json 缺失或解析失败时仍返回元数据（价格留空），由调用方按 best-effort 处理。
func ParseFeed(modelsJSON, apiJSON []byte) (Feed, error) {
	entries := map[string]modelsJSONEntry{}
	if err := json.Unmarshal(modelsJSON, &entries); err != nil {
		return Feed{}, failure.Wrap(failure.CodeModelCatalogStoreFailed, err, failure.WithMessage("parse models.dev models.json"))
	}

	prices := parseAPIPrices(apiJSON)

	models := make([]CanonicalModel, 0, len(entries))
	for canonicalID, entry := range entries {
		lab, modelKey := splitCanonicalID(canonicalID)

		model := CanonicalModel{
			CanonicalID:        canonicalID,
			Lab:                lab,
			DisplayName:        firstNonEmpty(entry.Name, canonicalID),
			ReleaseDate:        parseDate(entry.ReleaseDate),
			ContextTokens:      positiveOrNil(entry.Limit.Context),
			MaxOutputTokens:    positiveOrNil(entry.Limit.Output),
			CoarseCapabilities: coarseCapabilities(entry),
		}
		if price, ok := prices[lab][modelKey]; ok {
			model.InputPrice = decimalOrNil(price.Input)
			model.OutputPrice = decimalOrNil(price.Output)
		}
		models = append(models, model)
	}

	sort.Slice(models, func(i, j int) bool { return models[i].CanonicalID < models[j].CanonicalID })

	return Feed{Models: models, Fingerprint: fingerprint(modelsJSON)}, nil
}

// parseAPIPrices 解析 api.json 为 lab → modelKey → cost；解析失败返回空表（价格 best-effort）。
func parseAPIPrices(apiJSON []byte) map[string]map[string]costJSON {
	out := map[string]map[string]costJSON{}
	if len(apiJSON) == 0 {
		return out
	}

	providers := map[string]apiProviderJSON{}
	if err := json.Unmarshal(apiJSON, &providers); err != nil {
		return out
	}

	for providerID, provider := range providers {
		key := provider.ID
		if key == "" {
			key = providerID
		}
		if len(provider.Models) == 0 {
			continue
		}
		costs := make(map[string]costJSON, len(provider.Models))
		for modelKey, model := range provider.Models {
			costs[modelKey] = model.Cost
		}
		out[key] = costs
	}

	return out
}

// coarseCapabilities 把 models.dev 模型布尔位/模态映射为粗能力声明（全 full，仅首次入库默认值）。
func coarseCapabilities(entry modelsJSONEntry) []capability.Declaration {
	decls := []capability.Declaration{
		{Key: capability.KeyTextInput, SupportLevel: capability.SupportLevelFull},
		{Key: capability.KeyTextOutput, SupportLevel: capability.SupportLevelFull},
	}
	if entry.ToolCall {
		decls = append(decls, capability.Declaration{Key: capability.KeyToolsFunction, SupportLevel: capability.SupportLevelFull})
	}
	if entry.Reasoning {
		decls = append(decls, capability.Declaration{Key: capability.KeyReasoningEffort, SupportLevel: capability.SupportLevelFull})
	}
	if entry.StructuredOutput {
		decls = append(decls, capability.Declaration{Key: capability.KeyResponseFormatJSONSchema, SupportLevel: capability.SupportLevelFull})
	}
	if entry.Attachment {
		decls = append(decls, capability.Declaration{Key: capability.KeyFileInput, SupportLevel: capability.SupportLevelFull})
	}
	if containsFold(entry.Modalities.Input, "image") {
		decls = append(decls, capability.Declaration{Key: capability.KeyImageInput, SupportLevel: capability.SupportLevelFull})
	}
	if containsFold(entry.Modalities.Input, "audio") {
		decls = append(decls, capability.Declaration{Key: capability.KeyAudioInput, SupportLevel: capability.SupportLevelFull})
	}
	if containsFold(entry.Modalities.Output, "image") {
		decls = append(decls, capability.Declaration{Key: capability.KeyImageOutput, SupportLevel: capability.SupportLevelFull})
	}
	if containsFold(entry.Modalities.Output, "audio") {
		decls = append(decls, capability.Declaration{Key: capability.KeyAudioOutput, SupportLevel: capability.SupportLevelFull})
	}

	return decls
}

// splitCanonicalID 把 canonical_id（lab/model）拆为 lab 与 provider 内模型 key。
func splitCanonicalID(canonicalID string) (lab string, modelKey string) {
	if idx := strings.Index(canonicalID, "/"); idx >= 0 {
		return canonicalID[:idx], canonicalID[idx+1:]
	}
	return "", canonicalID
}

func parseDate(value string) *time.Time {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	parsed, err := time.Parse("2006-01-02", value)
	if err != nil {
		return nil
	}
	return &parsed
}

func positiveOrNil(value *int64) *int64 {
	if value == nil || *value <= 0 {
		return nil
	}
	out := *value
	return &out
}

// decimalOrNil 把 json.Number 价格字面量转成十进制字符串，非法/空/负值返回 nil。
func decimalOrNil(number json.Number) *string {
	literal := strings.TrimSpace(number.String())
	if literal == "" {
		return nil
	}
	value, err := number.Float64()
	if err != nil || value < 0 {
		return nil
	}
	return &literal
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func containsFold(values []string, target string) bool {
	for _, value := range values {
		if strings.EqualFold(strings.TrimSpace(value), target) {
			return true
		}
	}
	return false
}

func fingerprint(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
