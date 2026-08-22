package requests

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/ThankCat/unio-gateway/internal/platform/listquery"
	consoleservice "github.com/ThankCat/unio-gateway/internal/service/console"
	consolerequests "github.com/ThankCat/unio-gateway/internal/service/console/requests"
)

const (
	defaultPageSize = 20
	maxPageSize     = 100
)

var allowedSortFields = map[string]struct{}{
	"created_at": {},
	"model":      {},
	"reasoning":  {},
	"stream":     {},
	"latency":    {},
	"cost":       {},
	"tokens":     {},
}

var allowedStreamTypes = map[string]struct{}{
	"stream": {},
	"sync":   {},
}

type parsedListQuery struct {
	params   consolerequests.ListParams
	page     int
	pageSize int
}

func parseListQuery(r *http.Request) (parsedListQuery, *consoleservice.Error) {
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
	filters, err := parseSummaryQuery(r)
	if err != nil {
		return parsedListQuery{}, err
	}
	sort, sortErr := listquery.ParseSort(r, allowedSortFields, "created_at", true)
	if sortErr != nil {
		return parsedListQuery{}, consoleservice.InvalidArgument("sort", "sort must be created_at, model, reasoning, stream, latency, cost, or tokens.")
	}
	return parsedListQuery{
		params: consolerequests.ListParams{
			RouteIDs:    filters.RouteIDs,
			APIKeyIDs:   filters.APIKeyIDs,
			Endpoints:   filters.Endpoints,
			StreamTypes: filters.StreamTypes,
			Q:           filters.Q,
			From:        filters.From,
			To:          filters.To,
			SortField:   sort.Field,
			SortDesc:    sort.Desc,
			Limit:       int32(pageSize),
			Offset:      int32((page - 1) * pageSize),
		},
		page:     page,
		pageSize: pageSize,
	}, nil
}

func parseSummaryQuery(r *http.Request) (consolerequests.SummaryParams, *consoleservice.Error) {
	from, err := parseTimeQuery(r, "from")
	if err != nil {
		return consolerequests.SummaryParams{}, err
	}
	to, err := parseTimeQuery(r, "to")
	if err != nil {
		return consolerequests.SummaryParams{}, err
	}
	routeIDs, err := parseInt64Values(r, "route_id")
	if err != nil {
		return consolerequests.SummaryParams{}, err
	}
	apiKeyIDs, err := parseInt64Values(r, "api_key_id")
	if err != nil {
		return consolerequests.SummaryParams{}, err
	}
	endpoints, err := parseEnumValues(r, "endpoint", consolerequests.KnownPublicEndpoint)
	if err != nil {
		return consolerequests.SummaryParams{}, err
	}
	streamTypes, err := parseAllowedValues(r, "stream", allowedStreamTypes)
	if err != nil {
		return consolerequests.SummaryParams{}, err
	}
	return consolerequests.SummaryParams{
		RouteIDs:    routeIDs,
		APIKeyIDs:   apiKeyIDs,
		Endpoints:   endpoints,
		StreamTypes: streamTypes,
		Q:           strings.TrimSpace(r.URL.Query().Get("q")),
		From:        from,
		To:          to,
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

func parseTimeQuery(r *http.Request, key string) (*time.Time, *consoleservice.Error) {
	raw := strings.TrimSpace(r.URL.Query().Get(key))
	if raw == "" {
		return nil, nil
	}
	value, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return nil, consoleservice.InvalidArgument(key, key+" must be an RFC3339 timestamp.")
	}
	return &value, nil
}

func parseInt64Values(r *http.Request, key string) ([]int64, *consoleservice.Error) {
	rawValues := r.URL.Query()[key]
	out := make([]int64, 0, len(rawValues))
	for _, raw := range rawValues {
		for _, part := range strings.Split(raw, ",") {
			part = strings.TrimSpace(part)
			if part == "" {
				continue
			}
			value, err := strconv.ParseInt(part, 10, 64)
			if err != nil || value <= 0 {
				return nil, consoleservice.InvalidArgument(key, key+" must be a positive integer.")
			}
			out = append(out, value)
		}
	}
	return out, nil
}

func parseAllowedValues(r *http.Request, key string, allowed map[string]struct{}) ([]string, *consoleservice.Error) {
	return parseEnumValues(r, key, func(value string) bool {
		_, ok := allowed[value]
		return ok
	})
}

func parseEnumValues(r *http.Request, key string, allowed func(string) bool) ([]string, *consoleservice.Error) {
	rawValues := r.URL.Query()[key]
	out := make([]string, 0, len(rawValues))
	for _, raw := range rawValues {
		for _, part := range strings.Split(raw, ",") {
			part = strings.TrimSpace(part)
			if part == "" {
				continue
			}
			if !allowed(part) {
				return nil, consoleservice.InvalidArgument(key, key+" contains an unsupported value.")
			}
			out = append(out, part)
		}
	}
	return out, nil
}
