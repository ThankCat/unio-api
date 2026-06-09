package modelcatalog

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/ThankCat/unio-api/internal/platform/failure"
)

const (
	defaultFetchTimeout     = 30 * time.Second
	defaultMaxResponseBytes = 16 << 20 // 16 MiB，api.json 约 2.2MB，留足余量。
	modelsResourcePath      = "/models.json"
	apiResourcePath         = "/api.json"
	modelCatalogUserAgent   = "unio-api-model-catalog-sync"
)

// HTTPFetcher 从 models.dev 拉取 models.json（必需）与 api.json（价格，best-effort）。
type HTTPFetcher struct {
	client   *http.Client
	baseURL  string
	maxBytes int64
}

// NewHTTPFetcher 创建 models.dev HTTP 拉取器；baseURL 为空回落到 https://models.dev。
func NewHTTPFetcher(baseURL string, timeout time.Duration, maxBytes int64) *HTTPFetcher {
	if strings.TrimSpace(baseURL) == "" {
		baseURL = "https://models.dev"
	}
	if timeout <= 0 {
		timeout = defaultFetchTimeout
	}
	if maxBytes <= 0 {
		maxBytes = defaultMaxResponseBytes
	}

	return &HTTPFetcher{
		client:   &http.Client{Timeout: timeout},
		baseURL:  strings.TrimRight(baseURL, "/"),
		maxBytes: maxBytes,
	}
}

// Fetch 拉取 models.json（失败即致命）与 api.json（失败仅丢价格，不致命）。
func (f *HTTPFetcher) Fetch(ctx context.Context) (RawFeed, error) {
	modelsJSON, err := f.get(ctx, modelsResourcePath)
	if err != nil {
		return RawFeed{}, err
	}

	// 价格 best-effort：api.json 拉取失败仍以元数据完成同步（价格仅展示，不阻塞）。
	apiJSON, _ := f.get(ctx, apiResourcePath)

	return RawFeed{ModelsJSON: modelsJSON, APIJSON: apiJSON}, nil
}

func (f *HTTPFetcher) get(ctx context.Context, path string) ([]byte, error) {
	url := f.baseURL + path

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, failure.Wrap(failure.CodeModelCatalogStoreFailed, err, failure.WithMessage("build models.dev request"))
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", modelCatalogUserAgent)

	resp, err := f.client.Do(req)
	if err != nil {
		return nil, failure.Wrap(failure.CodeModelCatalogStoreFailed, err, failure.WithMessage("fetch "+path))
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, failure.New(
			failure.CodeModelCatalogStoreFailed,
			failure.WithMessage(fmt.Sprintf("models.dev %s returned status %d", path, resp.StatusCode)),
		)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, f.maxBytes))
	if err != nil {
		return nil, failure.Wrap(failure.CodeModelCatalogStoreFailed, err, failure.WithMessage("read "+path))
	}

	return body, nil
}
