package tokenest

import (
	"math"
	"strings"
)

// imageDims 是一张图片的像素尺寸；nil 表示尺寸未知（未开启媒体计数 / URL 未抓取 / 解码失败）。
type imageDims struct {
	Width  int
	Height int
}

// Claude 图片 token：Anthropic 文档口径 tokens ≈ (w×h)/750，实际封顶约 1600；尺寸未知时用保守兜底。
const (
	claudeImageDivisor        = 750
	claudeImageMaxTokens      = 1600
	claudeImageFallbackTokens = 1600
)

// claudeImageTokens 按 Anthropic 像素公式估算 Claude 图片 token（封顶 claudeImageMaxTokens）。
func claudeImageTokens(width, height int) int {
	tokens := (width*height + claudeImageDivisor - 1) / claudeImageDivisor
	if tokens > claudeImageMaxTokens {
		tokens = claudeImageMaxTokens
	}
	if tokens < 1 {
		tokens = 1
	}
	return tokens
}

// imageClass 描述某模型族的图片 token 计价方式（tile 制或 patch 制）。
type imageClass struct {
	baseTokens int
	tileTokens int
	patchBased bool
	multiplier float64
}

// classifyImageModel 按模型名判定图片计价方式与基数，忠实移植 new-api getImageToken 的分类分支。
func classifyImageModel(model string) imageClass {
	lower := strings.ToLower(model)

	// 默认按 4o/4.1/4.5 族的 tile 基数。
	c := imageClass{baseTokens: 85, tileTokens: 170, multiplier: 1.0}

	// patch 制模型族（32×32 patch，封顶 1536，× 系数）。
	switch {
	case strings.Contains(lower, "gpt-4.1-mini"):
		c.patchBased, c.multiplier = true, 1.62
		return c
	case strings.Contains(lower, "gpt-4.1-nano"):
		c.patchBased, c.multiplier = true, 2.46
		return c
	case strings.HasPrefix(lower, "o4-mini"):
		c.patchBased, c.multiplier = true, 1.72
		return c
	case strings.HasPrefix(lower, "gpt-5-mini"):
		c.patchBased, c.multiplier = true, 1.62
		return c
	case strings.HasPrefix(lower, "gpt-5-nano"):
		c.patchBased, c.multiplier = true, 2.46
		return c
	}

	// tile 制各族基数。
	switch {
	case strings.HasPrefix(lower, "gpt-4o-mini"):
		c.baseTokens, c.tileTokens = 2833, 5667
	case strings.HasPrefix(lower, "gpt-5-chat-latest"),
		strings.HasPrefix(lower, "gpt-5") && !strings.Contains(lower, "mini") && !strings.Contains(lower, "nano"):
		c.baseTokens, c.tileTokens = 70, 140
	case strings.HasPrefix(lower, "o1"), strings.HasPrefix(lower, "o3"), strings.HasPrefix(lower, "o1-pro"):
		c.baseTokens, c.tileTokens = 75, 150
	case strings.Contains(lower, "computer-use-preview"):
		c.baseTokens, c.tileTokens = 65, 129
	case strings.Contains(lower, "4.1"), strings.Contains(lower, "4o"), strings.Contains(lower, "4.5"):
		c.baseTokens, c.tileTokens = 85, 170
	}
	return c
}

// imageTokens 估算一张图片的输入 token 数（忠实移植 new-api getImageToken 的 tile/patch 数学）。
//
// dims 为 nil（尺寸未知）时回退到 new-api 的 flag-off 兜底 3×base；detail=low 且非 patch 制返回 base。
func imageTokens(model, detail string, dims *imageDims) int {
	lower := strings.ToLower(model)
	// GLM-4 系列固定值（new-api 特例）。
	if strings.HasPrefix(lower, "glm-4") {
		return 1047
	}

	// Claude 族：按 Anthropic 文档的像素公式 tokens ≈ (w×h)/750（封顶 ~1600），比 OpenAI tile 更贴近。
	if strings.HasPrefix(lower, "claude") {
		if dims == nil || dims.Width <= 0 || dims.Height <= 0 {
			return claudeImageFallbackTokens
		}
		return claudeImageTokens(dims.Width, dims.Height)
	}

	c := classifyImageModel(model)

	// detail=low 且 tile 制：固定 base（patch 制无 low 概念，继续按 patch 数学）。
	if detail == "low" && !c.patchBased {
		return c.baseTokens
	}

	// 尺寸未知：与 new-api「不抓取/不解码」分支一致，回退 3×base 保守值。
	if dims == nil || dims.Width <= 0 || dims.Height <= 0 {
		return 3 * c.baseTokens
	}

	width, height := dims.Width, dims.Height

	if c.patchBased {
		return patchTokens(width, height, c.multiplier)
	}
	return tileTokens(width, height, c.baseTokens, c.tileTokens)
}

// patchTokens 实现 32×32 patch 制：patch 数封顶 1536，再 × 模型系数（new-api getImageToken patch 分支）。
func patchTokens(width, height int, multiplier float64) int {
	ceilDiv := func(a, b int) int { return (a + b - 1) / b }
	rawPatches := ceilDiv(width, 32) * ceilDiv(height, 32)

	if rawPatches <= 1536 {
		return int(math.Round(float64(rawPatches) * multiplier))
	}

	// 超过 1536：等比缩放到恰好塞进 1536 个 patch。
	area := float64(width * height)
	r := math.Sqrt(float64(32*32*1536) / area)
	wScaled := float64(width) * r
	hScaled := float64(height) * r
	adjW := math.Floor(wScaled/32.0) / (wScaled / 32.0)
	adjH := math.Floor(hScaled/32.0) / (hScaled / 32.0)
	adj := math.Min(adjW, adjH)
	if !math.IsNaN(adj) && adj > 0 {
		r *= adj
	}
	wScaled = float64(width) * r
	hScaled = float64(height) * r
	patches := int(math.Ceil(wScaled/32.0) * math.Ceil(hScaled/32.0))
	if patches > 1536 {
		patches = 1536
	}
	return int(math.Round(float64(patches) * multiplier))
}

// tileTokens 实现 512px tile 制：先塞进 2048×2048，再把最短边缩到 768，数 512 tile（new-api tile 分支）。
func tileTokens(width, height, baseTokens, tileToken int) int {
	maxSide := math.Max(float64(width), float64(height))
	fitScale := 1.0
	if maxSide > 2048 {
		fitScale = maxSide / 2048.0
	}
	fitW := int(math.Round(float64(width) / fitScale))
	fitH := int(math.Round(float64(height) / fitScale))

	minSide := math.Min(float64(fitW), float64(fitH))
	if minSide == 0 {
		return baseTokens
	}
	shortScale := 768.0 / minSide
	finalW := int(math.Round(float64(fitW) * shortScale))
	finalH := int(math.Round(float64(fitH) * shortScale))

	tilesW := (finalW + 511) / 512
	tilesH := (finalH + 511) / 512
	return tilesW*tilesH*tileToken + baseTokens
}
