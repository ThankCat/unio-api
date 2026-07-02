package tokenest

import (
	"context"
	"encoding/base64"
	"image"
	"io"
	"net/http"
	"strings"

	// 注册标准图片解码器，供 image.DecodeConfig 读取尺寸（只读文件头，不解整张图）。
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"

	// webp 尺寸解码（OpenAI/Anthropic 均支持 webp 图片）。
	_ "golang.org/x/image/webp"
)

// MediaKind 是多模态附件的类别，决定 token 估算方式。
type MediaKind int

const (
	// MediaImage 图片：走 tile/patch 数学（需尺寸）。
	MediaImage MediaKind = iota
	// MediaAudio 音频：固定保守值（对齐 new-api EstimateRequestToken 的 +256）。
	MediaAudio
	// MediaVideo 视频：固定保守值（对齐 new-api 的 4096×2）。
	MediaVideo
	// MediaFile 其它文件：固定保守值（对齐 new-api 的 4096）。
	MediaFile
)

// 音频/文件/视频的固定 token 估算（对齐 new-api EstimateRequestToken 的分支取值）。
const (
	audioFixedTokens = 256
	videoFixedTokens = 4096 * 2
	fileFixedTokens  = 4096
)

// Media 描述一条多模态附件。图片可能来自内联 base64（Base64Data，已去 data URL 前缀）或远程 URL（URL）。
type Media struct {
	Kind       MediaKind
	Detail     string // 图片 detail：low/high/auto（仅图片）
	Base64Data string // 内联 base64 原始数据（无 data: 前缀），仅图片
	URL        string // 远程 http(s) 图片地址，仅图片
}

// ImageFromURL 从 OpenAI 风格的 image_url 构造图片附件：data: URL 拆出内联 base64，其余按远程 URL。
func ImageFromURL(url, detail string) Media {
	url = strings.TrimSpace(url)
	if strings.HasPrefix(strings.ToLower(url), "data:") {
		if i := strings.Index(url, ","); i >= 0 {
			return Media{Kind: MediaImage, Detail: detail, Base64Data: url[i+1:]}
		}
	}
	return Media{Kind: MediaImage, Detail: detail, URL: url}
}

// ImageFromBase64 从 Anthropic 风格的内联 base64 source 构造图片附件。
func ImageFromBase64(data, detail string) Media {
	return Media{Kind: MediaImage, Detail: detail, Base64Data: strings.TrimSpace(data)}
}

// mediaTokens 估算一条附件的 token 数。图片按尺寸走 tile/patch；音频/视频/文件按固定值。
func mediaTokens(model string, m Media, opts Options) int {
	switch m.Kind {
	case MediaImage:
		return imageTokens(model, m.Detail, resolveImageDims(m, opts))
	case MediaAudio:
		return audioFixedTokens
	case MediaVideo:
		return videoFixedTokens
	case MediaFile:
		return fileFixedTokens
	default:
		return fileFixedTokens
	}
}

// resolveImageDims 尝试拿到图片尺寸：媒体计数关闭 → nil（回退 3×base）；内联 base64 → 本地解码；
// 远程 URL → 仅在 FetchRemoteImages 打开时抓取。任何失败都返回 nil（imageTokens 会用 3×base 兜底）。
func resolveImageDims(m Media, opts Options) *imageDims {
	if !opts.CountMedia {
		return nil
	}
	if data := strings.TrimSpace(m.Base64Data); data != "" {
		return decodeBase64Dims(data)
	}
	if url := strings.TrimSpace(m.URL); url != "" && opts.FetchRemoteImages {
		return fetchImageDims(url, opts)
	}
	return nil
}

// decodeBase64Dims 解码内联 base64 的图片头拿到尺寸；失败返回 nil。纯本地、无网络。
func decodeBase64Dims(data string) *imageDims {
	raw, err := base64.StdEncoding.DecodeString(data)
	if err != nil {
		// 部分客户端用 URL-safe 或带换行的 base64，宽松再试一次。
		raw, err = base64.StdEncoding.DecodeString(sanitizeBase64(data))
		if err != nil {
			return nil
		}
	}
	cfg, _, err := image.DecodeConfig(strings.NewReader(string(raw)))
	if err != nil {
		return nil
	}
	return &imageDims{Width: cfg.Width, Height: cfg.Height}
}

// fetchImageDims 抓取远程图片、只读到能解出尺寸即可（带超时 + 体积上限）；失败返回 nil。
//
// 仅在 Options.FetchRemoteImages 显式打开时调用。注意：抓取任意客户 URL 存在 SSRF 风险，
// 默认关闭；启用前应在网络层对出站目标做限制。
func fetchImageDims(url string, opts Options) *imageDims {
	ctx, cancel := context.WithTimeout(context.Background(), opts.FetchTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil
	}

	limited := io.LimitReader(resp.Body, opts.FetchMaxBytes)
	cfg, _, err := image.DecodeConfig(limited)
	if err != nil {
		return nil
	}
	return &imageDims{Width: cfg.Width, Height: cfg.Height}
}

// sanitizeBase64 去掉换行/空白并把 URL-safe 字符还原为标准 base64 字母表。
func sanitizeBase64(s string) string {
	replacer := strings.NewReplacer("\n", "", "\r", "", " ", "", "-", "+", "_", "/")
	return replacer.Replace(s)
}
