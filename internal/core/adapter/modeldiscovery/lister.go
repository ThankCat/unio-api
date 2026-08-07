// Package modeldiscovery 实现上游模型列表的有界、协议感知 HTTP 读取。
package modeldiscovery

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/ThankCat/unio-gateway/internal/core/adapter"
	"github.com/ThankCat/unio-gateway/internal/core/channel"
)

const (
	CodeCredentialInvalid   = "credential_invalid"
	CodePermissionDenied    = "permission_denied"
	CodeUnsupportedEndpoint = "unsupported_endpoint"
	CodeRateLimited         = "rate_limited"
	CodeTimeout             = "timeout"
	CodeUnreachable         = "unreachable"
	CodeProtocolError       = "protocol_error"
	CodeUpstreamError       = "upstream_error"
	CodeCanceled            = "canceled"
)

const (
	defaultMaxResponseBytes = int64(1 << 20)
	defaultMaxModels        = 5000
	maxModelIDBytes         = 512
	maxPages                = 100
	operationPathModels     = "/v1/models"
)

// Error 是模型发现对 Admin/Worker 暴露的稳定失败事实，不携带上游响应正文。
type Error struct {
	Code       string
	HTTPStatus int
	RetryAfter time.Duration
	cause      error
}

func (e *Error) Error() string {
	if e == nil {
		return ""
	}
	return e.Code
}

func (e *Error) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.cause
}

// ErrorOf 从错误链提取稳定发现错误。
func ErrorOf(err error) (*Error, bool) {
	var target *Error
	ok := errors.As(err, &target)
	return target, ok
}

type authStyle int

const (
	authBearer authStyle = iota
	authAnthropic
)

// Lister 使用受限 HTTP 客户端读取上游模型列表。
type Lister struct {
	client           *http.Client
	auth             authStyle
	cursorParam      string
	maxResponseBytes int64
	maxModels        int
}

// NewOpenAICompatible 创建 Bearer `/v1/models` lister；兼容返回 has_more/last_id 的中转分页。
func NewOpenAICompatible(client *http.Client) *Lister {
	return newLister(client, authBearer, "after")
}

// NewAnthropic 创建 x-api-key + anthropic-version `/v1/models` lister。
func NewAnthropic(client *http.Client) *Lister {
	return newLister(client, authAnthropic, "after_id")
}

func newLister(client *http.Client, auth authStyle, cursorParam string) *Lister {
	if client == nil {
		client = http.DefaultClient
	}
	clientCopy := *client
	clientCopy.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &Lister{
		client: &clientCopy, auth: auth, cursorParam: cursorParam,
		maxResponseBytes: defaultMaxResponseBytes, maxModels: defaultMaxModels,
	}
}

type listResponse struct {
	Data []struct {
		ID      string          `json:"id"`
		OwnedBy string          `json:"owned_by"`
		Created json.RawMessage `json:"created"`
	} `json:"data"`
	HasMore bool   `json:"has_more"`
	LastID  string `json:"last_id"`
}

// ListModels 枚举并去重模型。成功响应体和模型数都有硬上限，且不会跟随重定向。
func (l *Lister) ListModels(ctx context.Context, runtime channel.Runtime) (adapter.ModelListResult, error) {
	if l == nil || l.client == nil {
		return adapter.ModelListResult{}, protocolError(errors.New("model lister is not configured"))
	}
	if strings.TrimSpace(runtime.APIKey) == "" {
		return adapter.ModelListResult{}, &Error{Code: CodeCredentialInvalid}
	}

	endpoint, err := adapter.BuildUpstreamURL(runtime.Origin, operationPathModels)
	if err != nil {
		return adapter.ModelListResult{}, protocolError(err)
	}

	seen := make(map[string]adapter.ModelListItem)
	cursor := ""
	for page := 0; page < maxPages; page++ {
		pageURL, err := withCursor(endpoint, l.cursorParam, cursor, l.auth == authAnthropic)
		if err != nil {
			return adapter.ModelListResult{}, protocolError(err)
		}
		payload, err := l.fetchPage(ctx, pageURL, runtime.APIKey)
		if err != nil {
			return adapter.ModelListResult{}, err
		}
		for _, raw := range payload.Data {
			id := strings.TrimSpace(raw.ID)
			if id == "" || len(id) > maxModelIDBytes {
				continue
			}
			if _, exists := seen[id]; exists {
				continue
			}
			if len(seen) >= l.maxModels {
				return adapter.ModelListResult{}, protocolError(errors.New("upstream model count exceeds limit"))
			}
			seen[id] = adapter.ModelListItem{
				ID: id, OwnedBy: strings.TrimSpace(raw.OwnedBy), CreatedAt: parseCreatedAt(raw.Created),
			}
		}
		if !payload.HasMore {
			items := make([]adapter.ModelListItem, 0, len(seen))
			for _, item := range seen {
				items = append(items, item)
			}
			sort.Slice(items, func(i, j int) bool { return items[i].ID < items[j].ID })
			return adapter.ModelListResult{Items: items}, nil
		}
		next := strings.TrimSpace(payload.LastID)
		if next == "" && len(payload.Data) > 0 {
			next = strings.TrimSpace(payload.Data[len(payload.Data)-1].ID)
		}
		if next == "" || next == cursor {
			return adapter.ModelListResult{}, protocolError(errors.New("upstream pagination cursor is missing or repeated"))
		}
		cursor = next
	}
	return adapter.ModelListResult{}, protocolError(errors.New("upstream pagination exceeds page limit"))
}

func (l *Lister) fetchPage(ctx context.Context, endpoint, apiKey string) (listResponse, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return listResponse{}, protocolError(err)
	}
	req.Header.Set("Accept", "application/json")
	if l.auth == authAnthropic {
		req.Header.Set("x-api-key", apiKey)
		req.Header.Set("anthropic-version", "2023-06-01")
	} else {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}

	resp, err := l.client.Do(req)
	if err != nil {
		return listResponse{}, classifySendError(ctx, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 8<<10))
		return listResponse{}, statusError(resp)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, l.maxResponseBytes+1))
	if err != nil {
		return listResponse{}, protocolError(err)
	}
	if int64(len(body)) > l.maxResponseBytes {
		return listResponse{}, protocolError(errors.New("upstream model response exceeds byte limit"))
	}
	var payload listResponse
	if err := json.Unmarshal(body, &payload); err != nil {
		return listResponse{}, protocolError(err)
	}
	if payload.Data == nil {
		return listResponse{}, protocolError(errors.New("upstream model response is missing data"))
	}
	return payload, nil
}

func withCursor(endpoint, cursorParam, cursor string, includeLimit bool) (string, error) {
	parsed, err := url.Parse(endpoint)
	if err != nil {
		return "", err
	}
	query := parsed.Query()
	if cursor != "" {
		query.Set(cursorParam, cursor)
	}
	if includeLimit {
		query.Set("limit", "100")
	}
	parsed.RawQuery = query.Encode()
	return parsed.String(), nil
}

func parseCreatedAt(raw json.RawMessage) *time.Time {
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	var unix int64
	if err := json.Unmarshal(raw, &unix); err != nil || unix <= 0 {
		return nil
	}
	t := time.Unix(unix, 0).UTC()
	return &t
}

func classifySendError(ctx context.Context, err error) error {
	switch {
	case errors.Is(context.Cause(ctx), context.Canceled), errors.Is(err, context.Canceled):
		return &Error{Code: CodeCanceled, cause: err}
	case errors.Is(context.Cause(ctx), context.DeadlineExceeded), errors.Is(err, context.DeadlineExceeded):
		return &Error{Code: CodeTimeout, cause: err}
	default:
		var netErr net.Error
		if errors.As(err, &netErr) && netErr.Timeout() {
			return &Error{Code: CodeTimeout, cause: err}
		}
		return &Error{Code: CodeUnreachable, cause: err}
	}
}

func statusError(resp *http.Response) error {
	code := CodeUpstreamError
	switch resp.StatusCode {
	case http.StatusUnauthorized:
		code = CodeCredentialInvalid
	case http.StatusForbidden:
		code = CodePermissionDenied
	case http.StatusNotFound, http.StatusMethodNotAllowed:
		code = CodeUnsupportedEndpoint
	case http.StatusTooManyRequests:
		code = CodeRateLimited
	default:
		if resp.StatusCode >= 400 && resp.StatusCode < 500 {
			code = CodeProtocolError
		}
	}
	return &Error{
		Code: code, HTTPStatus: resp.StatusCode, RetryAfter: adapter.ParseRetryAfterHeader(resp.Header),
		cause: fmt.Errorf("upstream models status %d", resp.StatusCode),
	}
}

func protocolError(err error) error {
	return &Error{Code: CodeProtocolError, cause: err}
}

var _ adapter.ModelLister = (*Lister)(nil)
