package codexresponses

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"sort"
	"strings"

	"github.com/ThankCat/unio-gateway/internal/core/adapter"
	"github.com/ThankCat/unio-gateway/internal/core/adapter/modeldiscovery"
	"github.com/ThankCat/unio-gateway/internal/core/channel"
)

// maxModelsResponseBytes 限制模型清单响应体（清单含完整 instructions 模板，实测约 300KB）。
const maxModelsResponseBytes = int64(4 << 20)

// ModelLister 读取 Codex 订阅后端的模型清单（GET /backend-api/codex/models?client_version=<ver>）。
//
// 与官方 /v1/models 的差异：无分页、模型在 models[] 下、标识字段是 slug、需要账号身份头。
// 失败语义复用 modeldiscovery.Error 的稳定错误码，Admin/Worker 的发现流程零改动。
type ModelLister struct {
	client    *http.Client
	clientFor func(proxyURL string) *http.Client
}

// NewModelLister 创建 Codex 模型清单 lister；clientFor 为按账号代理解析器（可为 nil）。
func NewModelLister(client *http.Client, clientFor func(proxyURL string) *http.Client) *ModelLister {
	if client == nil {
		client = http.DefaultClient
	}
	return &ModelLister{client: client, clientFor: clientFor}
}

var _ adapter.ModelLister = (*ModelLister)(nil)

// codexModelsResponse 是清单端点的最小解码形态（upstream-models.json 对照）。
type codexModelsResponse struct {
	Models []struct {
		Slug        string `json:"slug"`
		DisplayName string `json:"display_name"`
		Visibility  string `json:"visibility"`
	} `json:"models"`
}

// ListModels 枚举可用模型 slug。visibility=list 之外的（hidden 等）仍然返回——
// 发现流程的职责是「上游有什么」，展示口径由 Admin 决定。
func (l *ModelLister) ListModels(ctx context.Context, runtime channel.Runtime) (adapter.ModelListResult, error) {
	if strings.TrimSpace(runtime.APIKey) == "" {
		return adapter.ModelListResult{}, &modeldiscovery.Error{Code: modeldiscovery.CodeCredentialInvalid}
	}
	endpoint, err := adapter.BuildUpstreamURL(runtime.Origin, modelsPath)
	if err != nil {
		return adapter.ModelListResult{}, &modeldiscovery.Error{Code: modeldiscovery.CodeProtocolError}
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint+"?client_version="+clientVersion, nil)
	if err != nil {
		return adapter.ModelListResult{}, &modeldiscovery.Error{Code: modeldiscovery.CodeProtocolError}
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+runtime.APIKey)
	decorateCodexRequest(req, runtime)

	client := l.client
	if l.clientFor != nil {
		if resolved := l.clientFor(runtime.Account.ProxyURL); resolved != nil {
			client = resolved
		}
	}
	resp, err := client.Do(req)
	if err != nil {
		return adapter.ModelListResult{}, codexDiscoverySendError(ctx, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 8<<10))
		return adapter.ModelListResult{}, codexDiscoveryStatusError(resp)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxModelsResponseBytes+1))
	if err != nil || int64(len(body)) > maxModelsResponseBytes {
		return adapter.ModelListResult{}, &modeldiscovery.Error{Code: modeldiscovery.CodeProtocolError}
	}
	var payload codexModelsResponse
	if err := json.Unmarshal(body, &payload); err != nil || payload.Models == nil {
		return adapter.ModelListResult{}, &modeldiscovery.Error{Code: modeldiscovery.CodeProtocolError}
	}

	seen := make(map[string]struct{}, len(payload.Models))
	items := make([]adapter.ModelListItem, 0, len(payload.Models))
	for _, model := range payload.Models {
		slug := strings.TrimSpace(model.Slug)
		if slug == "" {
			continue
		}
		if _, dup := seen[slug]; dup {
			continue
		}
		seen[slug] = struct{}{}
		items = append(items, adapter.ModelListItem{ID: slug, OwnedBy: "openai"})
	}
	sort.Slice(items, func(i, j int) bool { return items[i].ID < items[j].ID })
	return adapter.ModelListResult{Items: items}, nil
}

func codexDiscoverySendError(ctx context.Context, err error) error {
	switch {
	case errors.Is(context.Cause(ctx), context.Canceled), errors.Is(err, context.Canceled):
		return &modeldiscovery.Error{Code: modeldiscovery.CodeCanceled}
	case errors.Is(context.Cause(ctx), context.DeadlineExceeded), errors.Is(err, context.DeadlineExceeded):
		return &modeldiscovery.Error{Code: modeldiscovery.CodeTimeout}
	default:
		return &modeldiscovery.Error{Code: modeldiscovery.CodeUnreachable}
	}
}

func codexDiscoveryStatusError(resp *http.Response) error {
	code := modeldiscovery.CodeUpstreamError
	switch resp.StatusCode {
	case http.StatusUnauthorized:
		code = modeldiscovery.CodeCredentialInvalid
	case http.StatusForbidden:
		code = modeldiscovery.CodePermissionDenied
	case http.StatusNotFound, http.StatusMethodNotAllowed:
		code = modeldiscovery.CodeUnsupportedEndpoint
	case http.StatusTooManyRequests:
		code = modeldiscovery.CodeRateLimited
	}
	return &modeldiscovery.Error{
		Code: code, HTTPStatus: resp.StatusCode,
		RetryAfter: adapter.ParseRetryAfterHeader(resp.Header),
	}
}
