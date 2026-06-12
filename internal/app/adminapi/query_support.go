package adminapi

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/ThankCat/unio-api/internal/platform/failure"
)

// optionalInt64Query 解析可选的正整数 query 过滤项；缺省返回 nil（不过滤），非法返回 admin_invalid_argument。
func optionalInt64Query(r *http.Request, key string) (*int64, error) {
	raw := strings.TrimSpace(r.URL.Query().Get(key))
	if raw == "" {
		return nil, nil
	}
	v, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || v <= 0 {
		return nil, failure.New(
			failure.CodeAdminInvalidArgument,
			failure.WithMessage(key+" must be a positive integer"),
			failure.WithField("field", key),
		)
	}
	return &v, nil
}

// optionalTimeQuery 解析可选的 RFC3339 时间过滤项；缺省返回 nil（不过滤），非法返回 admin_invalid_argument。
func optionalTimeQuery(r *http.Request, key string) (*time.Time, error) {
	raw := strings.TrimSpace(r.URL.Query().Get(key))
	if raw == "" {
		return nil, nil
	}
	t, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return nil, failure.New(
			failure.CodeAdminInvalidArgument,
			failure.WithMessage(key+" must be an RFC3339 timestamp"),
			failure.WithField("field", key),
		)
	}
	return &t, nil
}

// boolQuery 解析布尔开关 query：1/true/yes 为 true，其余（含缺省）为 false。
func boolQuery(r *http.Request, key string) bool {
	switch strings.ToLower(strings.TrimSpace(r.URL.Query().Get(key))) {
	case "1", "true", "yes":
		return true
	default:
		return false
	}
}

// queryString 取 trim 后的 query 值。
func queryString(r *http.Request, key string) string {
	return strings.TrimSpace(r.URL.Query().Get(key))
}

// rfc3339 把时间格式化为 UTC RFC3339 字符串。
func rfc3339(t time.Time) string {
	return t.UTC().Format(time.RFC3339)
}

// rfc3339Ptr 把可空时间格式化为 *string（UTC RFC3339）；nil → nil。
func rfc3339Ptr(t *time.Time) *string {
	if t == nil {
		return nil
	}
	s := t.UTC().Format(time.RFC3339)
	return &s
}
