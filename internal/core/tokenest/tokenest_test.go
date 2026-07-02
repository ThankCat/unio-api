package tokenest

import (
	"bytes"
	"encoding/base64"
	"image"
	"image/color"
	"image/gif"
	"image/jpeg"
	"image/png"
	"strings"
	"testing"
)

// 1×1 透明 PNG 的标准 base64（用于验证图片走尺寸解码 + tile 数学，而非按字符数计）。
const onePixelPNGBase64 = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mNk+M9QDwADhgGAWjR9awAAAABJRU5ErkJggg=="

func TestCountTextKnownAndUnknownModels(t *testing.T) {
	for _, model := range []string{"gpt-5.5", "gpt-4o", "claude-sonnet-4", "", "some-brand-new-model"} {
		if got := CountText(model, "hello world, this is a token test"); got <= 0 {
			t.Fatalf("CountText(%q) = %d, want positive", model, got)
		}
	}
	if got := CountText("gpt-5", ""); got != 0 {
		t.Fatalf("CountText empty text = %d, want 0", got)
	}
}

func TestImageTokensTileModel(t *testing.T) {
	// gpt-5 base: base=70 tile=140；1024×1024 high → 缩到 768×768 → 2×2=4 tile → 70+140×4=630。
	if got := imageTokens("gpt-5", "high", &imageDims{Width: 1024, Height: 1024}); got != 630 {
		t.Fatalf("gpt-5 1024x1024 high = %d, want 630", got)
	}
	// detail=low → 固定 base。
	if got := imageTokens("gpt-5", "low", &imageDims{Width: 1024, Height: 1024}); got != 70 {
		t.Fatalf("gpt-5 low = %d, want 70 (base)", got)
	}
	// 尺寸未知 → 3×base 兜底。
	if got := imageTokens("gpt-5", "high", nil); got != 210 {
		t.Fatalf("gpt-5 unknown dims = %d, want 210 (3x base)", got)
	}
	// GLM-4 固定值。
	if got := imageTokens("glm-4v", "high", &imageDims{Width: 512, Height: 512}); got != 1047 {
		t.Fatalf("glm-4 = %d, want 1047", got)
	}
}

func TestImageTokensPatchModel(t *testing.T) {
	// gpt-5-nano 为 patch 制：应产出有界正值（远小于把 base64 当文本）。
	got := imageTokens("gpt-5-nano", "high", &imageDims{Width: 1024, Height: 1024})
	if got <= 0 || got > 4000 {
		t.Fatalf("gpt-5-nano patch tokens = %d, want bounded positive", got)
	}
}

func TestClaudeImageTokensPixelFormula(t *testing.T) {
	// claude 1000×1000 → ceil(1e6/750)=1334（未封顶）。
	if got := imageTokens("claude-sonnet-4", "", &imageDims{Width: 1000, Height: 1000}); got != 1334 {
		t.Fatalf("claude 1000x1000 = %d, want 1334", got)
	}
	// 巨图封顶 1600。
	if got := imageTokens("claude-sonnet-4", "", &imageDims{Width: 4000, Height: 4000}); got != 1600 {
		t.Fatalf("claude huge = %d, want 1600 (capped)", got)
	}
	// 尺寸未知 → 保守兜底。
	if got := imageTokens("claude-sonnet-4", "", nil); got != claudeImageFallbackTokens {
		t.Fatalf("claude unknown dims = %d, want %d", got, claudeImageFallbackTokens)
	}
}

func TestDecodeBase64DimsReadsHeader(t *testing.T) {
	dims := decodeBase64Dims(onePixelPNGBase64)
	if dims == nil || dims.Width != 1 || dims.Height != 1 {
		t.Fatalf("decodeBase64Dims = %+v, want 1x1", dims)
	}
	if got := decodeBase64Dims("not-a-real-image-payload"); got != nil {
		t.Fatalf("decodeBase64Dims(garbage) = %+v, want nil", got)
	}
}

// TestBuilderDoesNotCountBase64AsText 是本次改造的核心回归防线：
// 即使内联图片 base64 极长，估算也走 tile/兜底数学（有界），绝不按字符数放大。
func TestBuilderDoesNotCountBase64AsText(t *testing.T) {
	// 有效 1×1 PNG data URL → 解码出尺寸 → gpt-5 tile 数学 = 630。
	valid := NewBuilder("gpt-5")
	valid.AddMessage()
	valid.AddText("here is an image")
	valid.AddMedia(ImageFromURL("data:image/png;base64,"+onePixelPNGBase64, "high"))
	validCount := valid.Count()
	if validCount < 600 || validCount > 700 {
		t.Fatalf("valid image estimate = %d, want ~630 (tile math)", validCount)
	}

	// 超长但无法解码的 base64（~120k 字符）→ 尺寸未知 → 3×base=210 兜底，绝不 ~4 万。
	huge := "data:image/png;base64," + strings.Repeat("QUJD", 30000)
	exploded := NewBuilder("gpt-5")
	exploded.AddMessage()
	exploded.AddMedia(ImageFromURL(huge, "high"))
	if got := exploded.Count(); got > 1000 {
		t.Fatalf("huge base64 estimate = %d, want bounded (<=1000); base64 must not be counted as text", got)
	}
}

// encodeImageBase64 用标准库现编一张 w×h 的真实图片并返回其 base64（png/jpeg/gif）。
func encodeImageBase64(t *testing.T, format string, w, h int) string {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, color.RGBA{R: uint8(x % 256), G: uint8(y % 256), B: 128, A: 255})
		}
	}
	var buf bytes.Buffer
	var err error
	switch format {
	case "png":
		err = png.Encode(&buf, img)
	case "jpeg":
		err = jpeg.Encode(&buf, img, nil)
	case "gif":
		err = gif.Encode(&buf, img, nil)
	default:
		t.Fatalf("unknown format %q", format)
	}
	if err != nil {
		t.Fatalf("encode %s: %v", format, err)
	}
	return base64.StdEncoding.EncodeToString(buf.Bytes())
}

// realWebP75x100Base64 是 golang.org/x/image 自带的真实 75×100 lossless webp（用于验证 webp 解码已接线）。
const realWebP75x100Base64 = "UklGRrIBAABXRUJQVlA4TKUBAAAvSsAYAA8w//M///MfeJAkbXvaSG7m8Q3GfYSBJekwQztm/IcZlgwnmWImn2BK7aFmBtnVir6q//8VOkFE/xm4baTIu8c48ArEo6+B3zFKYln3pqClSCKX0begFTAXFOLXHSyF8cCNcZEG4OywuA4KVVfJCiArU7GAgJI8+lJP/OKMT/fBAjevg1cYB7YVkFuWga2lyPi5I0HFy5YTpWIHg0RZpkniRVW9odHAKOwosWuOGdxIyn2OvaCDvhg/we6TwadPBPbqBV58MsLmMJ8yZnOWk8SRz4N+QoyPL+MnamzMvcE1rHNEr91F9GKZPVUcS9w7PhhH36suB9qPeYb/oLk6cuTiJ0wOK3m5h1cKjW6EVZCYMK7dxcKCBdgP9HkKr9gkAO2P8GKZGWVdIAatQa+1IDpt6qyorVwdy01xdW8Jkfk6xjEXmVQQ+HQdFr6OKhIN34dXWq0+0qr6EJSCeeVLH9+gvGTLyqM65PQ44ihzlTXxQKjKbAvshXgir7Lil9w4L2bvMycmjQcqXaMCO6BlY28i+FOLzbfI1vEqxAhotocAAA=="

// TestDecodeRealWebP 验证 webp 解码器已通过 blank import 接线，能解出真实 webp 的尺寸。
func TestDecodeRealWebP(t *testing.T) {
	dims := decodeBase64Dims(realWebP75x100Base64)
	if dims == nil || dims.Width != 75 || dims.Height != 100 {
		t.Fatalf("webp decode = %+v, want 75x100 (webp decoder must be wired)", dims)
	}
	if got := imageTokens("gpt-5", "high", dims); got <= 0 {
		t.Fatalf("webp tile tokens = %d, want positive", got)
	}
}

// TestDecodeRealImagesAcrossFormats 验证真实 png/jpeg/gif 的 base64 能被解出准确尺寸。
func TestDecodeRealImagesAcrossFormats(t *testing.T) {
	cases := []struct {
		format string
		w, h   int
	}{
		{"png", 64, 64},
		{"jpeg", 800, 600},
		{"gif", 1600, 400},
		{"png", 2048, 1024},
	}
	for _, c := range cases {
		b64 := encodeImageBase64(t, c.format, c.w, c.h)
		dims := decodeBase64Dims(b64)
		if dims == nil || dims.Width != c.w || dims.Height != c.h {
			t.Fatalf("%s %dx%d decode = %+v, want %dx%d", c.format, c.w, c.h, dims, c.w, c.h)
		}
	}
}

// TestImageTileTokensForRealDims 验证 tile 数学对若干尺寸给出与 OpenAI 文档一致的手算值（gpt-5 base70/tile140）。
func TestImageTileTokensForRealDims(t *testing.T) {
	cases := []struct {
		w, h, want int
	}{
		{64, 64, 630},       // 放大到 768x768 → 2x2 tile
		{800, 600, 630},     // → 1024x768 → 2x2
		{1600, 400, 1750},   // → 3072x768 → 6x2 = 12 tile
		{2048, 1024, 910},   // → 1536x768 → 3x2 = 6 tile
		{1024, 1024, 630},   // → 768x768 → 2x2
		{4096, 4096, 630},   // 先塞进 2048 再缩到 768 → 2x2
	}
	for _, c := range cases {
		if got := imageTokens("gpt-5", "high", &imageDims{Width: c.w, Height: c.h}); got != c.want {
			t.Fatalf("imageTokens gpt-5 %dx%d = %d, want %d", c.w, c.h, got, c.want)
		}
	}
}

// TestDecodeThenTileEndToEnd 串起「真实图片 → 解码尺寸 → tile 数学」的完整链路（非兜底）。
func TestDecodeThenTileEndToEnd(t *testing.T) {
	b64 := encodeImageBase64(t, "png", 1024, 1024)
	b := NewBuilder("gpt-5.5")
	b.AddMessage()
	b.AddText("describe this image")
	b.AddMedia(ImageFromURL("data:image/png;base64,"+b64, "high"))
	got := b.Count()
	// 1024x1024 → 630 tile token + 少量文本/框架；确认走的是 tile（~630+）而非 3×base 兜底(210)或按字符计。
	if got < 630 || got > 720 {
		t.Fatalf("decode+tile end-to-end = %d, want ~630 (tile math, not fallback/text)", got)
	}
}

func TestBuilderFramingAndMediaIncreaseCount(t *testing.T) {
	base := NewBuilder("gpt-4o")
	base.AddMessage()
	base.AddText("hello")
	baseCount := base.Count()

	withTool := NewBuilder("gpt-4o")
	withTool.AddMessage()
	withTool.AddText("hello")
	withTool.AddTool()
	withTool.AddText("search_docs")
	if withTool.Count() <= baseCount {
		t.Fatalf("tool should increase estimate: base=%d withTool=%d", baseCount, withTool.Count())
	}

	withAudio := NewBuilder("gpt-4o")
	withAudio.AddMessage()
	withAudio.AddText("hello")
	withAudio.AddMedia(Media{Kind: MediaAudio})
	if withAudio.Count() != baseCount+audioFixedTokens {
		t.Fatalf("audio estimate = %d, want base(%d)+%d", withAudio.Count(), baseCount, audioFixedTokens)
	}
}
