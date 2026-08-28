// Package modelcatalog 把 models.dev 数据源同步为 Unio 能力架构 Layer 1 的模型目录种子。
//
// 它消费 models.dev 的 models.json（canonical 元数据）与 api.json（每 provider 价格基线），
// 按合并规则维护 models 表：source=manual 行永不被覆盖、新模型默认 disabled、上游删除只标记不删除。
// models.dev 仅作种子源，不是运行时事实源；license 与 attribution 见仓库根目录
// THIRD_PARTY_NOTICES.md。
package modelcatalog

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/ThankCat/unio-gateway/internal/core/capability"
	"github.com/ThankCat/unio-gateway/internal/platform/failure"
)

// modelsJSONEntry 是 models.dev models.json 的 canonical 模型元数据（按 lab/model 键控）。
type modelsJSONEntry struct {
	ID               string `json:"id"`
	Name             string `json:"name"`
	Description      string `json:"description"`
	Family           string `json:"family"`
	Attachment       bool   `json:"attachment"`
	Reasoning        bool   `json:"reasoning"`
	ToolCall         bool   `json:"tool_call"`
	StructuredOutput bool   `json:"structured_output"`
	// Knowledge 是知识截止；上游格式不齐（2024-09-30 / 2024-08），原样透传不解析。
	Knowledge   string `json:"knowledge"`
	ReleaseDate string `json:"release_date"`
	LastUpdated string `json:"last_updated"`
	// OpenWeights 用指针区分「上游标注为 false」与「上游未标注」。
	OpenWeights *bool          `json:"open_weights"`
	Modalities  modalitiesJSON `json:"modalities"`
	Limit       limitJSON      `json:"limit"`
}

type modalitiesJSON struct {
	Input  []string `json:"input"`
	Output []string `json:"output"`
}

type limitJSON struct {
	Context *int64 `json:"context"`
	Input   *int64 `json:"input"`
	Output  *int64 `json:"output"`
}

// apiProviderJSON 是 models.dev api.json 的单个 provider（按 provider id 键控）。
type apiProviderJSON struct {
	ID     string                  `json:"id"`
	Models map[string]apiModelJSON `json:"models"`
}

// apiModelJSON 是 api.json 内 provider 模型条目：价格基线 + 推理档位枚举。
type apiModelJSON struct {
	Cost             costJSON              `json:"cost"`
	ReasoningOptions []reasoningOptionJSON `json:"reasoning_options"`
}

// reasoningOptionJSON 是推理配置项；只消费 type=effort 的档位枚举，
// toggle / budget_tokens 等其他类型与我们的 reasoning.effort 能力语义对不上。
type reasoningOptionJSON struct {
	Type   string   `json:"type"`
	Values []string `json:"values"`
}

// costJSON 用 json.Number 承载价格字面量，避免 float 精度损失（价格仅展示，绝不用于计费）。
type costJSON struct {
	Input      json.Number `json:"input"`
	Output     json.Number `json:"output"`
	CacheRead  json.Number `json:"cache_read"`
	CacheWrite json.Number `json:"cache_write"`
}

// CanonicalModel 是 models.dev 一条 canonical 模型合并后的 Layer 1 种子。
type CanonicalModel struct {
	CanonicalID string
	Lab         string
	// Family 是上游给出的模型系列（如 gpt-4、claude-3），仅用于列表分组展示。
	Family      string
	DisplayName string
	// Description 是上游一句话简介，仅展示。
	Description string
	// KnowledgeCutoff 是知识截止字符串（格式不齐，原样保存），空串表示上游未给。
	KnowledgeCutoff string
	ReleaseDate     *time.Time
	LastUpdated     *time.Time
	ContextTokens   *int64
	// InputLimitTokens 是单请求输入上限（上游 limit.input），长上下文阶梯阈值的参考来源。
	InputLimitTokens *int64
	MaxOutputTokens  *int64
	// OpenWeights 是否开源权重；nil 表示上游未标注。
	OpenWeights *bool
	// ModalitiesInput / ModalitiesOutput 是上游原始模态列表，能力声明之外保留原文供展示。
	ModalitiesInput  []string
	ModalitiesOutput []string
	// InputPrice / OutputPrice / CacheReadPrice / CacheCreationPrice 是十进制字符串
	//（USD / 百万 token），nil 表示该模型无此价格基线。
	InputPrice         *string
	OutputPrice        *string
	CacheReadPrice     *string
	CacheCreationPrice *string
	// CoarseCapabilities 是 models.dev 粗能力位映射，落到目录能力提示供采纳预填。
	CoarseCapabilities []capability.Declaration
	// Fingerprint 是本条目内容指纹（元数据 + 排序能力提示规范化 hash），用于采纳追更对比。
	Fingerprint string
}

// Feed 是一次 models.dev 拉取解析后的全部 canonical 模型（按 canonical_id 升序）。
type Feed struct {
	Models []CanonicalModel
	// Fingerprint 是本次源数据指纹，用于 license/版本审计与变更检测。
	Fingerprint string
}

// ParseFeed 解析 models.json（必需）与 api.json（价格与推理档位，可空），合并为 canonical 模型种子。
//
// api.json 缺失或解析失败时仍返回元数据（价格留空），由调用方按 best-effort 处理。
func ParseFeed(modelsJSON, apiJSON []byte) (Feed, error) {
	entries := map[string]modelsJSONEntry{}
	if err := json.Unmarshal(modelsJSON, &entries); err != nil {
		return Feed{}, failure.Wrap(failure.CodeModelCatalogStoreFailed, err, failure.WithMessage("parse models.dev models.json"))
	}

	apiModels := parseAPIModels(apiJSON)

	models := make([]CanonicalModel, 0, len(entries))
	for canonicalID, entry := range entries {
		lab, modelKey := splitCanonicalID(canonicalID)

		model := CanonicalModel{
			CanonicalID:      canonicalID,
			Lab:              lab,
			Family:           entry.Family,
			DisplayName:      firstNonEmpty(entry.Name, canonicalID),
			Description:      strings.TrimSpace(entry.Description),
			KnowledgeCutoff:  strings.TrimSpace(entry.Knowledge),
			ReleaseDate:      parseDate(entry.ReleaseDate),
			LastUpdated:      parseDate(entry.LastUpdated),
			ContextTokens:    positiveOrNil(entry.Limit.Context),
			InputLimitTokens: positiveOrNil(entry.Limit.Input),
			MaxOutputTokens:  positiveOrNil(entry.Limit.Output),
			OpenWeights:      entry.OpenWeights,
			ModalitiesInput:  normalizeModalities(entry.Modalities.Input),
			ModalitiesOutput: normalizeModalities(entry.Modalities.Output),
		}
		apiModel, hasAPIModel := apiModels[lab][modelKey]
		if hasAPIModel {
			model.InputPrice = decimalOrNil(apiModel.Cost.Input)
			model.OutputPrice = decimalOrNil(apiModel.Cost.Output)
			model.CacheReadPrice = decimalOrNil(apiModel.Cost.CacheRead)
			model.CacheCreationPrice = decimalOrNil(apiModel.Cost.CacheWrite)
		}
		model.CoarseCapabilities = coarseCapabilities(entry, effortValues(apiModel.ReasoningOptions))
		model.Fingerprint = entryFingerprint(model)
		models = append(models, model)
	}

	sort.Slice(models, func(i, j int) bool { return models[i].CanonicalID < models[j].CanonicalID })

	return Feed{Models: models, Fingerprint: fingerprint(modelsJSON)}, nil
}

// parseAPIModels 解析 api.json 为 lab → modelKey → 条目；解析失败返回空表（best-effort）。
// 只取 lab 同名 provider 的条目：官方 provider 的价格与档位最接近牌价语义。
func parseAPIModels(apiJSON []byte) map[string]map[string]apiModelJSON {
	out := map[string]map[string]apiModelJSON{}
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
		models := make(map[string]apiModelJSON, len(provider.Models))
		for modelKey, model := range provider.Models {
			models[modelKey] = model
		}
		out[key] = models
	}

	return out
}

// effortValues 取 type=effort 的推理档位枚举；其他类型（toggle/budget_tokens）语义对不上，忽略。
func effortValues(options []reasoningOptionJSON) []string {
	for _, option := range options {
		if option.Type != "effort" || len(option.Values) == 0 {
			continue
		}
		values := make([]string, 0, len(option.Values))
		for _, v := range option.Values {
			if trimmed := strings.TrimSpace(v); trimmed != "" {
				values = append(values, trimmed)
			}
		}
		if len(values) > 0 {
			return values
		}
	}
	return nil
}

// normalizeModalities 去空白去空项，保持上游顺序。
func normalizeModalities(values []string) []string {
	out := make([]string, 0, len(values))
	for _, v := range values {
		if trimmed := strings.TrimSpace(v); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}

// coarseCapabilities 把 models.dev 模型布尔位/模态映射为粗能力声明（全 full，仅首次入库默认值）。
//
// effortValues 非空时挂到 reasoning.effort 提示的 limits（{"effort":[...]}）：目录提示表
// 不受「limited 才允许 limits」的运营约束，这里只是把上游档位枚举留给采纳预填参考。
func coarseCapabilities(entry modelsJSONEntry, effortValues []string) []capability.Declaration {
	decls := []capability.Declaration{
		{Key: "text.input", SupportLevel: capability.SupportLevelFull},
		{Key: "text.output", SupportLevel: capability.SupportLevelFull},
	}
	if entry.ToolCall {
		decls = append(decls, capability.Declaration{Key: "tools.function", SupportLevel: capability.SupportLevelFull})
	}
	if entry.Reasoning {
		decl := capability.Declaration{Key: "reasoning.effort", SupportLevel: capability.SupportLevelFull}
		if len(effortValues) > 0 {
			if limits, err := json.Marshal(map[string][]string{"effort": effortValues}); err == nil {
				decl.Limits = limits
			}
		}
		decls = append(decls, decl)
	}
	if entry.StructuredOutput {
		decls = append(decls, capability.Declaration{Key: "response_format.json_schema", SupportLevel: capability.SupportLevelFull})
	}
	if entry.Attachment {
		decls = append(decls, capability.Declaration{Key: "file.input", SupportLevel: capability.SupportLevelFull})
	}
	if containsFold(entry.Modalities.Input, "image") {
		decls = append(decls, capability.Declaration{Key: "image.input", SupportLevel: capability.SupportLevelFull})
	}
	if containsFold(entry.Modalities.Input, "audio") {
		decls = append(decls, capability.Declaration{Key: "audio.input", SupportLevel: capability.SupportLevelFull})
	}
	if containsFold(entry.Modalities.Output, "image") {
		decls = append(decls, capability.Declaration{Key: "image.output", SupportLevel: capability.SupportLevelFull})
	}
	if containsFold(entry.Modalities.Output, "audio") {
		decls = append(decls, capability.Declaration{Key: "audio.output", SupportLevel: capability.SupportLevelFull})
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
// 价格基线仅展示（绝不用于计费），统一四舍五入到三位小数并去尾零后入库，
// 使目录成为「三位小数」唯一真源——采纳/追更刷新/展示均由此继承一致精度。
func decimalOrNil(number json.Number) *string {
	literal := strings.TrimSpace(number.String())
	if literal == "" {
		return nil
	}
	value, err := number.Float64()
	if err != nil || value < 0 {
		return nil
	}
	rounded := trimTrailingZeros(strconv.FormatFloat(value, 'f', 3, 64))
	return &rounded
}

// trimTrailingZeros 去掉十进制字符串多余的尾零："2.500" → "2.5"，"15.000" → "15"。
func trimTrailingZeros(s string) string {
	if !strings.Contains(s, ".") {
		return s
	}
	return strings.TrimRight(strings.TrimRight(s, "0"), ".")
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

// entryFingerprint 计算单条目录条目的内容指纹（规范化元数据 + 排序后能力提示）。
// 用于采纳后对比「目录最新 vs 采纳基线」是否有差异；字段顺序/空值/数值格式固定，避免误报。
func entryFingerprint(m CanonicalModel) string {
	var b strings.Builder
	b.WriteString(m.CanonicalID)
	b.WriteByte('\n')
	b.WriteString(m.Lab)
	b.WriteByte('\n')
	b.WriteString(m.Family)
	b.WriteByte('\n')
	b.WriteString(m.DisplayName)
	b.WriteByte('\n')
	b.WriteString(m.Description)
	b.WriteByte('\n')
	b.WriteString(m.KnowledgeCutoff)
	b.WriteByte('\n')
	b.WriteString(fingerprintInt(m.ContextTokens))
	b.WriteByte('\n')
	b.WriteString(fingerprintInt(m.InputLimitTokens))
	b.WriteByte('\n')
	b.WriteString(fingerprintInt(m.MaxOutputTokens))
	b.WriteByte('\n')
	b.WriteString(fingerprintBool(m.OpenWeights))
	b.WriteByte('\n')
	b.WriteString(strings.Join(m.ModalitiesInput, ","))
	b.WriteByte('\n')
	b.WriteString(strings.Join(m.ModalitiesOutput, ","))
	b.WriteByte('\n')
	b.WriteString(fingerprintStr(m.InputPrice))
	b.WriteByte('\n')
	b.WriteString(fingerprintStr(m.OutputPrice))
	b.WriteByte('\n')
	b.WriteString(fingerprintStr(m.CacheReadPrice))
	b.WriteByte('\n')
	b.WriteString(fingerprintStr(m.CacheCreationPrice))
	b.WriteByte('\n')
	if m.ReleaseDate != nil {
		b.WriteString(m.ReleaseDate.Format("2006-01-02"))
	}
	b.WriteByte('\n')
	if m.LastUpdated != nil {
		b.WriteString(m.LastUpdated.Format("2006-01-02"))
	}
	b.WriteByte('\n')

	caps := make([]string, 0, len(m.CoarseCapabilities))
	for _, d := range m.CoarseCapabilities {
		caps = append(caps, string(d.Key)+"="+string(d.SupportLevel)+"@"+string(d.Limits))
	}
	sort.Strings(caps)
	for _, c := range caps {
		b.WriteString(c)
		b.WriteByte(';')
	}

	sum := sha256.Sum256([]byte(b.String()))
	return hex.EncodeToString(sum[:])
}

func fingerprintBool(value *bool) string {
	if value == nil {
		return ""
	}
	return strconv.FormatBool(*value)
}

func fingerprintInt(value *int64) string {
	if value == nil {
		return ""
	}
	return strconv.FormatInt(*value, 10)
}

func fingerprintStr(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}
