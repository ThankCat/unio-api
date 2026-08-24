package usage

import (
	"time"

	consoleusage "github.com/ThankCat/unio-gateway/internal/service/console/usage"
)

type windowDTO struct {
	RequestCount            int64  `json:"request_count"`
	TokenCount              int64  `json:"token_count"`
	UncachedInputTokenCount int64  `json:"uncached_input_token_count"`
	CacheReadTokenCount     int64  `json:"cache_read_token_count"`
	CacheWriteTokenCount    int64  `json:"cache_write_token_count"`
	OutputTokenCount        int64  `json:"output_token_count"`
	ChargeUSD               string `json:"charge_usd"`
	UncachedInputChargeUSD  string `json:"uncached_input_charge_usd"`
	OutputChargeUSD         string `json:"output_charge_usd"`
	CacheReadChargeUSD      string `json:"cache_read_charge_usd"`
	CacheWriteChargeUSD     string `json:"cache_write_charge_usd"`
	ListChargeUSD           string `json:"list_charge_usd"`
	CacheSavedUSD           string `json:"cache_saved_usd"`
}

type pointDTO struct {
	BucketStart             string `json:"bucket_start"`
	RequestCount            int64  `json:"request_count"`
	TokenCount              int64  `json:"token_count"`
	UncachedInputTokenCount int64  `json:"uncached_input_token_count"`
	CacheReadTokenCount     int64  `json:"cache_read_token_count"`
	CacheWriteTokenCount    int64  `json:"cache_write_token_count"`
	OutputTokenCount        int64  `json:"output_token_count"`
	ChargeUSD               string `json:"charge_usd"`
	UncachedInputChargeUSD  string `json:"uncached_input_charge_usd"`
	OutputChargeUSD         string `json:"output_charge_usd"`
	CacheReadChargeUSD      string `json:"cache_read_charge_usd"`
	CacheWriteChargeUSD     string `json:"cache_write_charge_usd"`
	CacheSavedUSD           string `json:"cache_saved_usd"`
}

type overviewData struct {
	Bucket         string     `json:"bucket"`
	From           string     `json:"from"`
	To             string     `json:"to"`
	PreviousFrom   string     `json:"previous_from"`
	PreviousTo     string     `json:"previous_to"`
	Current        windowDTO  `json:"current"`
	Previous       windowDTO  `json:"previous"`
	Series         []pointDTO `json:"series"`
	PreviousSeries []pointDTO `json:"previous_series"`
}

type trendGroupDTO struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	RequestCount int64  `json:"request_count"`
	TokenCount   int64  `json:"token_count"`
	ChargeUSD    string `json:"charge_usd"`
}

type trendSliceDTO struct {
	GroupID      string `json:"group_id"`
	RequestCount int64  `json:"request_count"`
	TokenCount   int64  `json:"token_count"`
	ChargeUSD    string `json:"charge_usd"`
}

type trendPointDTO struct {
	BucketStart string          `json:"bucket_start"`
	Slices      []trendSliceDTO `json:"slices"`
}

type trendData struct {
	Bucket    string          `json:"bucket"`
	Dimension string          `json:"dimension"`
	From      string          `json:"from"`
	To        string          `json:"to"`
	Groups    []trendGroupDTO `json:"groups"`
	Series    []trendPointDTO `json:"series"`
}

func toTrendData(trend consoleusage.Trend) trendData {
	groups := make([]trendGroupDTO, 0, len(trend.Groups))
	for _, group := range trend.Groups {
		groups = append(groups, trendGroupDTO{
			ChargeUSD:    group.ChargeUSD,
			ID:           group.ID,
			Name:         group.Name,
			RequestCount: group.RequestCount,
			TokenCount:   group.TokenCount,
		})
	}
	series := make([]trendPointDTO, 0, len(trend.Series))
	for _, point := range trend.Series {
		slices := make([]trendSliceDTO, 0, len(point.Slices))
		for _, slice := range point.Slices {
			slices = append(slices, trendSliceDTO{
				ChargeUSD:    slice.ChargeUSD,
				GroupID:      slice.GroupID,
				RequestCount: slice.RequestCount,
				TokenCount:   slice.TokenCount,
			})
		}
		series = append(series, trendPointDTO{
			BucketStart: formatTime(point.BucketStart),
			Slices:      slices,
		})
	}
	return trendData{
		Bucket:    trend.Bucket,
		Dimension: trend.Dimension,
		From:      formatTime(trend.From),
		Groups:    groups,
		Series:    series,
		To:        formatTime(trend.To),
	}
}

type groupItemDTO struct {
	ID               string  `json:"id"`
	Name             string  `json:"name"`
	RequestCount     int64   `json:"request_count"`
	TokenCount       int64   `json:"token_count"`
	ChargeUSD        string  `json:"charge_usd"`
	IngressProtocol  string  `json:"ingress_protocol"`
	InputPricePer1M  *string `json:"input_price_per_1m"`
	OutputPricePer1M *string `json:"output_price_per_1m"`
}

type groupsData struct {
	By    string         `json:"by"`
	Items []groupItemDTO `json:"items"`
}

type filtersData struct {
	Routes  []consoleusage.FilterOption `json:"routes"`
	APIKeys []consoleusage.FilterOption `json:"api_keys"`
	Models  []consoleusage.ModelOption  `json:"models"`
}

func toWindowDTO(window consoleusage.Window) windowDTO {
	return windowDTO{
		RequestCount:            window.RequestCount,
		TokenCount:              window.TokenCount,
		UncachedInputTokenCount: window.UncachedInputTokenCount,
		CacheReadTokenCount:     window.CacheReadTokenCount,
		CacheWriteTokenCount:    window.CacheWriteTokenCount,
		OutputTokenCount:        window.OutputTokenCount,
		ChargeUSD:               window.ChargeUSD,
		UncachedInputChargeUSD:  window.UncachedInputChargeUSD,
		OutputChargeUSD:         window.OutputChargeUSD,
		CacheReadChargeUSD:      window.CacheReadChargeUSD,
		CacheWriteChargeUSD:     window.CacheWriteChargeUSD,
		ListChargeUSD:           window.ListChargeUSD,
		CacheSavedUSD:           window.CacheSavedUSD,
	}
}

func toPointDTOs(points []consoleusage.Point) []pointDTO {
	out := make([]pointDTO, 0, len(points))
	for _, point := range points {
		out = append(out, pointDTO{
			BucketStart:             formatTime(point.BucketStart),
			RequestCount:            point.RequestCount,
			TokenCount:              point.TokenCount,
			UncachedInputTokenCount: point.UncachedInputTokenCount,
			CacheReadTokenCount:     point.CacheReadTokenCount,
			CacheWriteTokenCount:    point.CacheWriteTokenCount,
			OutputTokenCount:        point.OutputTokenCount,
			ChargeUSD:               point.ChargeUSD,
			UncachedInputChargeUSD:  point.UncachedInputChargeUSD,
			OutputChargeUSD:         point.OutputChargeUSD,
			CacheReadChargeUSD:      point.CacheReadChargeUSD,
			CacheWriteChargeUSD:     point.CacheWriteChargeUSD,
			CacheSavedUSD:           point.CacheSavedUSD,
		})
	}
	return out
}

func toGroupItemDTOs(items []consoleusage.GroupItem) []groupItemDTO {
	out := make([]groupItemDTO, 0, len(items))
	for _, item := range items {
		out = append(out, groupItemDTO{
			ID:               item.ID,
			Name:             item.Name,
			RequestCount:     item.RequestCount,
			TokenCount:       item.TokenCount,
			ChargeUSD:        item.ChargeUSD,
			IngressProtocol:  item.IngressProtocol,
			InputPricePer1M:  item.InputPricePer1M,
			OutputPricePer1M: item.OutputPricePer1M,
		})
	}
	return out
}

func formatTime(value time.Time) string {
	if value.IsZero() {
		return ""
	}
	return value.UTC().Format(time.RFC3339Nano)
}
