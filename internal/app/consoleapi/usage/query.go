package usage

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	consoleservice "github.com/ThankCat/unio-gateway/internal/service/console"
	consolerequests "github.com/ThankCat/unio-gateway/internal/service/console/requests"
	consoleusage "github.com/ThankCat/unio-gateway/internal/service/console/usage"
)

var allowedStreamTypes = map[string]struct{}{
	"stream": {},
	"sync":   {},
}

// parseFilters 解析与请求中心口径相同的筛选条件，外加用量统计特有的模型筛选。
func parseFilters(r *http.Request) (consoleusage.Filters, *consoleservice.Error) {
	apiKeyIDs, err := parseInt64Values(r, "api_key_id")
	if err != nil {
		return consoleusage.Filters{}, err
	}
	endpoints, err := parseEnumValues(r, "endpoint", consolerequests.KnownPublicEndpoint)
	if err != nil {
		return consoleusage.Filters{}, err
	}
	streamTypes, err := parseAllowedValues(r, "stream", allowedStreamTypes)
	if err != nil {
		return consoleusage.Filters{}, err
	}
	return consoleusage.Filters{
		APIKeyIDs:   apiKeyIDs,
		ModelIDs:    parseStringValues(r, "model_id"),
		Endpoints:   endpoints,
		StreamTypes: streamTypes,
		Q:           strings.TrimSpace(r.URL.Query().Get("q")),
	}, nil
}

func parseOverviewQuery(r *http.Request) (consoleusage.OverviewParams, *consoleservice.Error) {
	from, err := parseRequiredTimeQuery(r, "from")
	if err != nil {
		return consoleusage.OverviewParams{}, err
	}
	to, err := parseRequiredTimeQuery(r, "to")
	if err != nil {
		return consoleusage.OverviewParams{}, err
	}
	filters, err := parseFilters(r)
	if err != nil {
		return consoleusage.OverviewParams{}, err
	}
	return consoleusage.OverviewParams{
		From:    from,
		To:      to,
		Bucket:  strings.TrimSpace(r.URL.Query().Get("bucket")),
		TZ:      strings.TrimSpace(r.URL.Query().Get("tz")),
		Filters: filters,
	}, nil
}

func parseTrendQuery(r *http.Request) (consoleusage.TrendParams, *consoleservice.Error) {
	from, err := parseRequiredTimeQuery(r, "from")
	if err != nil {
		return consoleusage.TrendParams{}, err
	}
	to, err := parseRequiredTimeQuery(r, "to")
	if err != nil {
		return consoleusage.TrendParams{}, err
	}
	filters, err := parseFilters(r)
	if err != nil {
		return consoleusage.TrendParams{}, err
	}
	return consoleusage.TrendParams{
		Bucket:    strings.TrimSpace(r.URL.Query().Get("bucket")),
		Dimension: strings.TrimSpace(r.URL.Query().Get("by")),
		Filters:   filters,
		From:      from,
		TZ:        strings.TrimSpace(r.URL.Query().Get("tz")),
		To:        to,
	}, nil
}

func parseGroupsQuery(r *http.Request) (consoleusage.GroupParams, *consoleservice.Error) {
	from, err := parseRequiredTimeQuery(r, "from")
	if err != nil {
		return consoleusage.GroupParams{}, err
	}
	to, err := parseRequiredTimeQuery(r, "to")
	if err != nil {
		return consoleusage.GroupParams{}, err
	}
	filters, err := parseFilters(r)
	if err != nil {
		return consoleusage.GroupParams{}, err
	}
	by := strings.TrimSpace(r.URL.Query().Get("by"))
	if by == "" {
		by = consoleusage.GroupByModel
	}
	return consoleusage.GroupParams{
		By:      by,
		From:    from,
		To:      to,
		Filters: filters,
	}, nil
}

// parseRequiredTimeQuery 要求显式时间窗：环比与分桶都依赖它，缺省会让口径变得不可解释。
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

func parseInt64Values(r *http.Request, key string) ([]int64, *consoleservice.Error) {
	out := make([]int64, 0)
	for _, part := range splitValues(r, key) {
		value, err := strconv.ParseInt(part, 10, 64)
		if err != nil || value <= 0 {
			return nil, consoleservice.InvalidArgument(key, key+" must be a positive integer.")
		}
		out = append(out, value)
	}
	return out, nil
}

func parseStringValues(r *http.Request, key string) []string {
	return splitValues(r, key)
}

func parseAllowedValues(r *http.Request, key string, allowed map[string]struct{}) ([]string, *consoleservice.Error) {
	return parseEnumValues(r, key, func(value string) bool {
		_, ok := allowed[value]
		return ok
	})
}

func parseEnumValues(r *http.Request, key string, allowed func(string) bool) ([]string, *consoleservice.Error) {
	out := make([]string, 0)
	for _, part := range splitValues(r, key) {
		if !allowed(part) {
			return nil, consoleservice.InvalidArgument(key, key+" contains an unsupported value.")
		}
		out = append(out, part)
	}
	return out, nil
}

func splitValues(r *http.Request, key string) []string {
	rawValues := r.URL.Query()[key]
	out := make([]string, 0, len(rawValues))
	for _, raw := range rawValues {
		for _, part := range strings.Split(raw, ",") {
			if part = strings.TrimSpace(part); part != "" {
				out = append(out, part)
			}
		}
	}
	return out
}
