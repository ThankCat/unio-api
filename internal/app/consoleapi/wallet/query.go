package wallet

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	consoleservice "github.com/ThankCat/unio-gateway/internal/service/console"
	consolewallet "github.com/ThankCat/unio-gateway/internal/service/console/wallet"
)

const (
	defaultPageSize = 20
	maxPageSize     = 100
)

type parsedListQuery struct {
	params   consolewallet.ListParams
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
	entryTypes, err := parseEntryTypes(r)
	if err != nil {
		return parsedListQuery{}, err
	}
	from, err := parseTimeQuery(r, "from")
	if err != nil {
		return parsedListQuery{}, err
	}
	to, err := parseTimeQuery(r, "to")
	if err != nil {
		return parsedListQuery{}, err
	}
	return parsedListQuery{
		params: consolewallet.ListParams{
			EntryTypes: entryTypes,
			From:       from,
			To:         to,
			Limit:      int32(pageSize),
			Offset:     int32((page - 1) * pageSize),
		},
		page:     page,
		pageSize: pageSize,
	}, nil
}

func parseEntryTypes(r *http.Request) ([]string, *consoleservice.Error) {
	rawValues := r.URL.Query()["type"]
	out := make([]string, 0, len(rawValues))
	for _, raw := range rawValues {
		for _, part := range strings.Split(raw, ",") {
			part = strings.TrimSpace(part)
			if part == "" {
				continue
			}
			if _, ok := consolewallet.EntryTypes[part]; !ok {
				return nil, consoleservice.InvalidArgument("type", "type contains an unsupported value.")
			}
			out = append(out, part)
		}
	}
	return out, nil
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
