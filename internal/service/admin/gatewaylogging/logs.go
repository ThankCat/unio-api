package gatewaylogging

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/ThankCat/unio-gateway/internal/platform/failure"
)

const (
	defaultLogWindow     = "1h"
	defaultLogLimit      = 100
	maxLogLimit          = 200
	maxLogSearchRunes    = 200
	maxLogRelatedIDRunes = 128
	maxLokiResponseBytes = 32 << 20
	lokiLogQueryTimeout  = 5 * time.Second
)

var (
	ErrLogQueryInvalid = errors.New("gateway log query is invalid")
	logTokenPattern    = regexp.MustCompile(`^[a-z0-9_]+$`)
	logWindows         = map[string]time.Duration{
		"15m": 15 * time.Minute,
		"1h":  time.Hour,
		"6h":  6 * time.Hour,
		"24h": 24 * time.Hour,
		"7d":  7 * 24 * time.Hour,
	}
	logLevels = map[string]struct{}{
		"debug": {}, "info": {}, "warning": {}, "error": {},
	}
)

type LogQuery struct {
	Window    string
	Level     string
	Type      string
	Event     string
	RelatedID string
	Search    string
	Limit     int
}

type LogList struct {
	Items     []LogEntry `json:"items"`
	From      time.Time  `json:"from"`
	To        time.Time  `json:"to"`
	Limit     int        `json:"limit"`
	Truncated bool       `json:"truncated"`
}

type LogEntry struct {
	ID          string         `json:"id"`
	Timestamp   time.Time      `json:"timestamp"`
	Level       string         `json:"level"`
	Type        string         `json:"type"`
	Event       string         `json:"event"`
	Message     string         `json:"message"`
	Environment string         `json:"environment"`
	Instance    string         `json:"instance"`
	Data        map[string]any `json:"data"`
}

type normalizedLogQuery struct {
	LogQuery
	windowDuration time.Duration
}

type lokiQueryResponse struct {
	Status string `json:"status"`
	Data   struct {
		Result []struct {
			Stream map[string]string   `json:"stream"`
			Values [][]json.RawMessage `json:"values"`
		} `json:"result"`
	} `json:"data"`
	Error string `json:"error"`
}

type gatewayLogEnvelope struct {
	Level       string         `json:"level"`
	Timestamp   time.Time      `json:"timestamp"`
	Message     string         `json:"message"`
	Server      string         `json:"server"`
	Environment string         `json:"environment"`
	Instance    string         `json:"instance"`
	Type        string         `json:"type"`
	Event       string         `json:"event"`
	Data        map[string]any `json:"data"`
}

func (s *Service) ListLogs(ctx context.Context, query LogQuery) (LogList, error) {
	if s == nil || strings.TrimSpace(s.lokiURL) == "" {
		return LogList{}, lokiUnavailable(errors.New("LOKI_URL is not configured"))
	}
	normalized, err := normalizeLogQuery(query)
	if err != nil {
		return LogList{}, err
	}

	to := s.now().UTC()
	from := to.Add(-normalized.windowDuration)
	logQL := buildLogQL(normalized.LogQuery)
	endpoint, err := url.Parse(s.lokiURL + "/loki/api/v1/query_range")
	if err != nil {
		return LogList{}, lokiUnavailable(err)
	}
	parameters := endpoint.Query()
	parameters.Set("query", logQL)
	parameters.Set("start", strconv.FormatInt(from.UnixNano(), 10))
	parameters.Set("end", strconv.FormatInt(to.UnixNano(), 10))
	// Fetch one extra entry so truncated reports whether more data actually exists.
	parameters.Set("limit", strconv.Itoa(normalized.Limit+1))
	parameters.Set("direction", "backward")
	endpoint.RawQuery = parameters.Encode()

	requestCtx, cancel := context.WithTimeout(ctx, lokiLogQueryTimeout)
	defer cancel()
	request, err := http.NewRequestWithContext(requestCtx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return LogList{}, lokiUnavailable(err)
	}
	response, err := s.client.Do(request)
	if err != nil {
		return LogList{}, lokiUnavailable(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 64<<10))
		return LogList{}, lokiUnavailable(fmt.Errorf("HTTP %d", response.StatusCode))
	}

	var payload lokiQueryResponse
	if err := json.NewDecoder(io.LimitReader(response.Body, maxLokiResponseBytes)).Decode(&payload); err != nil {
		return LogList{}, lokiUnavailable(err)
	}
	if payload.Status != "success" {
		return LogList{}, lokiUnavailable(errors.New("Loki query did not succeed"))
	}

	items := parseLokiLogEntries(payload)
	truncated := len(items) > normalized.Limit
	if truncated {
		items = items[:normalized.Limit]
	}
	return LogList{
		Items:     items,
		From:      from,
		To:        to,
		Limit:     normalized.Limit,
		Truncated: truncated,
	}, nil
}

func normalizeLogQuery(query LogQuery) (normalizedLogQuery, error) {
	query.Window = strings.TrimSpace(query.Window)
	if query.Window == "" {
		query.Window = defaultLogWindow
	}
	windowDuration, ok := logWindows[query.Window]
	if !ok {
		return normalizedLogQuery{}, invalidLogQuery("range must be one of 15m, 1h, 6h, 24h, or 7d")
	}

	query.Level = strings.ToLower(strings.TrimSpace(query.Level))
	if query.Level != "" {
		if _, ok := logLevels[query.Level]; !ok {
			return normalizedLogQuery{}, invalidLogQuery("level must be debug, info, warning, or error")
		}
	}
	query.Type = strings.ToLower(strings.TrimSpace(query.Type))
	query.Event = strings.ToLower(strings.TrimSpace(query.Event))
	if query.Type != "" && !logTokenPattern.MatchString(query.Type) {
		return normalizedLogQuery{}, invalidLogQuery("type contains unsupported characters")
	}
	if query.Event != "" && !logTokenPattern.MatchString(query.Event) {
		return normalizedLogQuery{}, invalidLogQuery("event contains unsupported characters")
	}

	query.RelatedID = strings.TrimSpace(query.RelatedID)
	query.Search = strings.TrimSpace(query.Search)
	if err := validateLogSearchValue(query.RelatedID, maxLogRelatedIDRunes, "related_id"); err != nil {
		return normalizedLogQuery{}, err
	}
	if err := validateLogSearchValue(query.Search, maxLogSearchRunes, "search"); err != nil {
		return normalizedLogQuery{}, err
	}
	if query.Limit == 0 {
		query.Limit = defaultLogLimit
	}
	if query.Limit < 1 || query.Limit > maxLogLimit {
		return normalizedLogQuery{}, invalidLogQuery("limit must be between 1 and 200")
	}
	return normalizedLogQuery{LogQuery: query, windowDuration: windowDuration}, nil
}

func validateLogSearchValue(value string, maxRunes int, field string) error {
	if len([]rune(value)) > maxRunes {
		return invalidLogQuery(field + " is too long")
	}
	if strings.IndexFunc(value, func(r rune) bool { return r < 0x20 || r == 0x7f }) >= 0 {
		return invalidLogQuery(field + " contains control characters")
	}
	return nil
}

func buildLogQL(query LogQuery) string {
	selectors := []string{`server="gateway"`}
	if query.Level != "" {
		selectors = append(selectors, `level=`+strconv.Quote(query.Level))
	}
	if query.Type != "" {
		selectors = append(selectors, `type=`+strconv.Quote(query.Type))
	}
	if query.Event != "" {
		selectors = append(selectors, `event=`+strconv.Quote(query.Event))
	}
	result := "{" + strings.Join(selectors, ",") + "}"
	if query.RelatedID != "" {
		result += " |= " + strconv.Quote(query.RelatedID)
	}
	if query.Search != "" {
		result += " |= " + strconv.Quote(query.Search)
	}
	return result
}

func parseLokiLogEntries(payload lokiQueryResponse) []LogEntry {
	items := make([]LogEntry, 0)
	for _, result := range payload.Data.Result {
		for _, value := range result.Values {
			if len(value) != 2 {
				continue
			}
			var timestampRaw, line string
			if err := json.Unmarshal(value[0], &timestampRaw); err != nil {
				continue
			}
			if err := json.Unmarshal(value[1], &line); err != nil {
				continue
			}
			timestampNanos, err := strconv.ParseInt(timestampRaw, 10, 64)
			if err != nil {
				continue
			}
			var envelope gatewayLogEnvelope
			if err := json.Unmarshal([]byte(line), &envelope); err != nil || envelope.Server != "gateway" {
				continue
			}
			timestamp := envelope.Timestamp
			if timestamp.IsZero() {
				timestamp = time.Unix(0, timestampNanos).UTC()
			}
			if envelope.Data == nil {
				envelope.Data = map[string]any{}
			}
			environment := envelope.Environment
			if environment == "" {
				environment = result.Stream["environment"]
			}
			instance := envelope.Instance
			if instance == "" {
				instance = result.Stream["instance"]
			}
			hash := sha256.Sum256([]byte(timestampRaw + "\x00" + line))
			items = append(items, LogEntry{
				ID:          hex.EncodeToString(hash[:12]),
				Timestamp:   timestamp,
				Level:       envelope.Level,
				Type:        envelope.Type,
				Event:       envelope.Event,
				Message:     envelope.Message,
				Environment: environment,
				Instance:    instance,
				Data:        envelope.Data,
			})
		}
	}
	sort.SliceStable(items, func(left, right int) bool {
		if items[left].Timestamp.Equal(items[right].Timestamp) {
			return false
		}
		return items[left].Timestamp.After(items[right].Timestamp)
	})
	return items
}

func invalidLogQuery(message string) error {
	return fmt.Errorf("%w: %s", ErrLogQueryInvalid, message)
}

func lokiUnavailable(cause error) error {
	return failure.Wrap(
		failure.CodeDependencyLokiUnavailable,
		cause,
		failure.WithMessage("log storage is unavailable"),
	)
}
