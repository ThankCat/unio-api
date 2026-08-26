package apikeys

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	consoleservice "github.com/ThankCat/unio-gateway/internal/service/console"
	consoleapikeys "github.com/ThankCat/unio-gateway/internal/service/console/apikeys"
)

const (
	defaultPageSize = 50
	maxPageSize     = 200
)

var allowedStatuses = map[string]struct{}{
	consoleapikeys.StatusActive:   {},
	consoleapikeys.StatusDisabled: {},
	consoleapikeys.StatusExpired:  {},
	consoleapikeys.StatusRevoked:  {},
}

type parsedListQuery struct {
	params   consoleapikeys.ListParams
	page     int
	pageSize int
}

func parseWindow(r *http.Request) (consoleapikeys.Window, *consoleservice.Error) {
	from, err := parseRequiredTimeQuery(r, "from")
	if err != nil {
		return consoleapikeys.Window{}, err
	}
	to, err := parseRequiredTimeQuery(r, "to")
	if err != nil {
		return consoleapikeys.Window{}, err
	}
	return consoleapikeys.Window{
		From: from,
		To:   to,
		TZ:   strings.TrimSpace(r.URL.Query().Get("tz")),
	}, nil
}

func parseListQuery(r *http.Request) (parsedListQuery, *consoleservice.Error) {
	window, err := parseWindow(r)
	if err != nil {
		return parsedListQuery{}, err
	}
	page, err := parsePositiveIntQuery(r, "page", 1)
	if err != nil {
		return parsedListQuery{}, err
	}
	pageSize, err := parsePositiveIntQuery(r, "page_size", defaultPageSize)
	if err != nil {
		return parsedListQuery{}, err
	}
	if pageSize > maxPageSize {
		pageSize = maxPageSize
	}
	status := strings.TrimSpace(r.URL.Query().Get("status"))
	if status != "" {
		if _, ok := allowedStatuses[status]; !ok {
			return parsedListQuery{}, consoleservice.InvalidArgument(
				"status",
				"status must be active, disabled, expired, or revoked.",
			)
		}
	}
	return parsedListQuery{
		params: consoleapikeys.ListParams{
			Window: window,
			Search: strings.TrimSpace(r.URL.Query().Get("q")),
			Status: status,
			Limit:  int32(pageSize),
			Offset: int32((page - 1) * pageSize),
		},
		page:     page,
		pageSize: pageSize,
	}, nil
}

func parsePositiveIntQuery(r *http.Request, key string, fallback int) (int, *consoleservice.Error) {
	raw := strings.TrimSpace(r.URL.Query().Get(key))
	if raw == "" {
		return fallback, nil
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value <= 0 {
		return 0, consoleservice.InvalidArgument(key, key+" must be a positive integer.")
	}
	return value, nil
}

func parseRequiredTimeQuery(r *http.Request, key string) (time.Time, *consoleservice.Error) {
	raw := strings.TrimSpace(r.URL.Query().Get(key))
	if raw == "" {
		return time.Time{}, consoleservice.InvalidArgument(key, key+" is required.")
	}
	value, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return time.Time{}, consoleservice.InvalidArgument(key, key+" must be an RFC3339 timestamp.")
	}
	return value, nil
}

func parsePathID(r *http.Request, raw string) (int64, *consoleservice.Error) {
	value, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
	if err != nil || value <= 0 {
		return 0, consoleservice.InvalidArgument("id", "The api key id must be a positive integer.")
	}
	return value, nil
}

// createRequest 是创建密钥的请求体。
type createRequest struct {
	Name       string  `json:"name"`
	SpendLimit *string `json:"spend_limit"`
	ExpiresAt  *string `json:"expires_at"`
}

func (req createRequest) toParams() (consoleapikeys.CreateParams, *consoleservice.Error) {
	params := consoleapikeys.CreateParams{
		Name:       req.Name,
		SpendLimit: req.SpendLimit,
	}
	if req.ExpiresAt != nil && strings.TrimSpace(*req.ExpiresAt) != "" {
		value, err := time.Parse(time.RFC3339, strings.TrimSpace(*req.ExpiresAt))
		if err != nil {
			return consoleapikeys.CreateParams{}, consoleservice.InvalidArgument(
				"expires_at",
				"expires_at must be an RFC3339 timestamp.",
			)
		}
		params.ExpiresAt = &value
	}
	return params, nil
}

// updateRequest 是更新密钥的请求体。
//
// 用 json.RawMessage 而不是 *string / *time.Time，是为了区分三种意图：
// 字段缺省=不改，字段为 null=清空（不限额 / 永不过期），字段有值=设成该值。
// 少了这层区分，「清除额度上限」和「不动额度上限」在 JSON 里长得一模一样。
type updateRequest struct {
	Name       json.RawMessage `json:"name"`
	SpendLimit json.RawMessage `json:"spend_limit"`
	ExpiresAt  json.RawMessage `json:"expires_at"`
	Disabled   json.RawMessage `json:"disabled"`
}

func (req updateRequest) toParams() (consoleapikeys.UpdateParams, *consoleservice.Error) {
	var params consoleapikeys.UpdateParams

	if len(req.Name) > 0 {
		var name string
		if err := json.Unmarshal(req.Name, &name); err != nil {
			return params, consoleservice.InvalidArgument("name", "name must be a string.")
		}
		params.NameProvided = true
		params.Name = &name
	}

	if len(req.SpendLimit) > 0 {
		params.SpendLimitProvided = true
		if !isJSONNull(req.SpendLimit) {
			var limit string
			if err := json.Unmarshal(req.SpendLimit, &limit); err != nil {
				return params, consoleservice.InvalidArgument(
					"spend_limit",
					"spend_limit must be a decimal string or null.",
				)
			}
			params.SpendLimit = &limit
		}
	}

	if len(req.ExpiresAt) > 0 {
		params.ExpiresProvided = true
		if !isJSONNull(req.ExpiresAt) {
			var raw string
			if err := json.Unmarshal(req.ExpiresAt, &raw); err != nil {
				return params, consoleservice.InvalidArgument(
					"expires_at",
					"expires_at must be an RFC3339 string or null.",
				)
			}
			value, parseErr := time.Parse(time.RFC3339, strings.TrimSpace(raw))
			if parseErr != nil {
				return params, consoleservice.InvalidArgument(
					"expires_at",
					"expires_at must be an RFC3339 string or null.",
				)
			}
			params.ExpiresAt = &value
		}
	}

	if len(req.Disabled) > 0 {
		var disabled bool
		if err := json.Unmarshal(req.Disabled, &disabled); err != nil {
			return params, consoleservice.InvalidArgument("disabled", "disabled must be a boolean.")
		}
		params.DisabledProvided = true
		params.Disabled = &disabled
	}

	return params, nil
}

func isJSONNull(raw json.RawMessage) bool {
	return string(raw) == "null"
}
