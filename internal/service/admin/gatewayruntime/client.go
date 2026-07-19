// Package gatewayruntime 供 admin-server 拉取 gateway 进程内只读运行时状态（熔断快照等）。
package gatewayruntime

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"

	"go.uber.org/zap"

	"github.com/ThankCat/unio-gateway/internal/service/gateway/lifecycle"
)

// ChannelStatus 是按 channel 合并后的熔断视图（供渠道列表徽章）。
type ChannelStatus struct {
	State            lifecycle.CircuitStateName `json:"state"`
	Failures         int                        `json:"failures"`
	Successes        int                        `json:"successes"`
	WindowStart      *time.Time                 `json:"window_start,omitempty"`
	OpenedAt         *time.Time                 `json:"opened_at,omitempty"`
	OpenRemainingMs  *int64                     `json:"open_remaining_ms,omitempty"`
	HalfOpenInFlight bool                       `json:"half_open_in_flight"`
	HealthScore      float64                    `json:"health_score"`
	ErrorRate        float64                    `json:"error_rate"`
	LatencyEWMAMs    float64                    `json:"latency_ewma_ms"`
	ObservedAt       time.Time                  `json:"observed_at"`
	Instances        []InstanceStatus           `json:"instances,omitempty"`
}

// InstanceStatus 是单个 gateway 实例上的熔断状态。
type InstanceStatus struct {
	ID               string                     `json:"id"`
	State            lifecycle.CircuitStateName `json:"state"`
	OpenRemainingMs  *int64                     `json:"open_remaining_ms,omitempty"`
	HalfOpenInFlight bool                       `json:"half_open_in_flight"`
	Failures         int                        `json:"failures"`
	Successes        int                        `json:"successes"`
	HealthScore      float64                    `json:"health_score"`
	ErrorRate        float64                    `json:"error_rate"`
	LatencyEWMAMs    float64                    `json:"latency_ewma_ms"`
	ObservedAt       time.Time                  `json:"observed_at"`
}

type SourceStatus struct {
	ID         string    `json:"id"`
	Available  bool      `json:"available"`
	ObservedAt time.Time `json:"observed_at,omitempty"`
	Error      string    `json:"error,omitempty"`
}

type Snapshot struct {
	Channels   map[int64]ChannelStatus `json:"channels"`
	Sources    []SourceStatus          `json:"sources"`
	ObservedAt time.Time               `json:"observed_at"`
	Available  bool                    `json:"available"`
}

// Client 并发拉取多个 gateway 的熔断快照并按 worst-wins 合并。
type Client struct {
	URLs   []string
	Token  string
	HTTP   *http.Client
	Logger *zap.Logger
}

// NewClient 构造客户端；urls/token 为空时 Statuses 恒返回空 map。
func NewClient(urls []string, token string, logger *zap.Logger) *Client {
	if len(urls) == 0 || token == "" {
		return &Client{Logger: logger}
	}
	return &Client{
		URLs:  urls,
		Token: token,
		HTTP: &http.Client{
			Timeout: 400 * time.Millisecond,
		},
		Logger: logger,
	}
}

// Statuses 返回 channel_id → 合并后的熔断状态（含 closed，供列表常驻徽章）。
func (c *Client) Statuses(ctx context.Context) map[int64]ChannelStatus {
	return c.Snapshot(ctx).Channels
}

// Snapshot returns merged channel health and per-instance source availability.
func (c *Client) Snapshot(ctx context.Context) Snapshot {
	if c == nil || c.Token == "" || len(c.URLs) == 0 {
		return Snapshot{Channels: map[int64]ChannelStatus{}, Sources: []SourceStatus{}}
	}

	type result struct {
		snap lifecycle.ChannelBreakerSnapshot
		err  error
		url  string
	}
	results := make([]result, len(c.URLs))
	var wg sync.WaitGroup
	for i, base := range c.URLs {
		wg.Add(1)
		go func(i int, base string) {
			defer wg.Done()
			snap, err := c.fetchOne(ctx, base)
			results[i] = result{snap: snap, err: err, url: base}
		}(i, base)
	}
	wg.Wait()

	merged := make(map[int64]*ChannelStatus)
	sources := make([]SourceStatus, 0, len(results))
	latestObservedAt := time.Time{}
	allAvailable := true
	for _, res := range results {
		if res.err != nil {
			allAvailable = false
			sources = append(sources, SourceStatus{ID: fmt.Sprintf("gateway-%d", len(sources)+1), Available: false, Error: truncate(res.err.Error(), 200)})
			if c.Logger != nil {
				c.Logger.Warn("gateway circuit-breaker snapshot failed", zap.String("url", res.url), zap.Error(res.err))
			}
			continue
		}
		instanceID := res.snap.Instance
		if instanceID == "" {
			instanceID = res.url
		}
		sources = append(sources, SourceStatus{ID: instanceID, Available: true, ObservedAt: res.snap.ObservedAt})
		if res.snap.ObservedAt.After(latestObservedAt) {
			latestObservedAt = res.snap.ObservedAt
		}
		if !res.snap.Enabled {
			continue
		}
		for _, ch := range res.snap.Channels {
			inst := InstanceStatus{
				ID:               instanceID,
				State:            ch.State,
				OpenRemainingMs:  ch.OpenRemainingMs,
				HalfOpenInFlight: ch.HalfOpenInFlight,
				Failures:         ch.Failures,
				Successes:        ch.Successes,
				HealthScore:      ch.HealthScore,
				ErrorRate:        ch.ErrorRate,
				LatencyEWMAMs:    ch.LatencyEWMAMs,
				ObservedAt:       res.snap.ObservedAt,
			}
			cur, ok := merged[ch.ChannelID]
			if !ok {
				ws := ch.WindowStart
				entry := &ChannelStatus{
					State:            ch.State,
					Failures:         ch.Failures,
					Successes:        ch.Successes,
					WindowStart:      &ws,
					OpenedAt:         ch.OpenedAt,
					OpenRemainingMs:  ch.OpenRemainingMs,
					HalfOpenInFlight: ch.HalfOpenInFlight,
					HealthScore:      ch.HealthScore,
					ErrorRate:        ch.ErrorRate,
					LatencyEWMAMs:    ch.LatencyEWMAMs,
					ObservedAt:       res.snap.ObservedAt,
					Instances:        []InstanceStatus{inst},
				}
				merged[ch.ChannelID] = entry
				continue
			}
			cur.Instances = append(cur.Instances, inst)
			if stateRank(ch.State) > stateRank(cur.State) {
				cur.State = ch.State
				cur.Failures = ch.Failures
				cur.Successes = ch.Successes
				ws := ch.WindowStart
				cur.WindowStart = &ws
				cur.OpenedAt = ch.OpenedAt
				cur.OpenRemainingMs = ch.OpenRemainingMs
				cur.HalfOpenInFlight = ch.HalfOpenInFlight
				cur.HealthScore = ch.HealthScore
				cur.ErrorRate = ch.ErrorRate
				cur.LatencyEWMAMs = ch.LatencyEWMAMs
				cur.ObservedAt = res.snap.ObservedAt
			} else if ch.State == lifecycle.CircuitStateOpen && cur.State == lifecycle.CircuitStateOpen {
				// 同为 open：取更长的剩余打开时长，避免徽章过早消失。
				if remainingMs(ch.OpenRemainingMs) > remainingMs(cur.OpenRemainingMs) {
					cur.OpenRemainingMs = ch.OpenRemainingMs
					cur.OpenedAt = ch.OpenedAt
					cur.ObservedAt = res.snap.ObservedAt
				}
				cur.HalfOpenInFlight = cur.HalfOpenInFlight || ch.HalfOpenInFlight
			} else if ch.State == lifecycle.CircuitStateClosed && cur.State == lifecycle.CircuitStateClosed {
				// 同为闭合：窗口样本求和，健康分取最差实例，时间取最新快照。
				cur.Failures += ch.Failures
				cur.Successes += ch.Successes
				if ch.HealthScore > cur.HealthScore {
					cur.HealthScore = ch.HealthScore
					cur.ErrorRate = ch.ErrorRate
					cur.LatencyEWMAMs = ch.LatencyEWMAMs
				}
				if res.snap.ObservedAt.After(cur.ObservedAt) {
					cur.ObservedAt = res.snap.ObservedAt
					ws := ch.WindowStart
					cur.WindowStart = &ws
				}
			} else if ch.State == lifecycle.CircuitStateHalfOpen && cur.State == lifecycle.CircuitStateHalfOpen {
				cur.HalfOpenInFlight = cur.HalfOpenInFlight || ch.HalfOpenInFlight
				if ch.HealthScore > cur.HealthScore {
					cur.HealthScore = ch.HealthScore
					cur.ErrorRate = ch.ErrorRate
					cur.LatencyEWMAMs = ch.LatencyEWMAMs
				}
			}
		}
	}

	out := make(map[int64]ChannelStatus, len(merged))
	for id, st := range merged {
		out[id] = *st
	}
	return Snapshot{Channels: out, Sources: sources, ObservedAt: latestObservedAt, Available: allAvailable && len(sources) > 0}
}

func (c *Client) fetchOne(ctx context.Context, base string) (lifecycle.ChannelBreakerSnapshot, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/internal/v1/circuit-breaker", nil)
	if err != nil {
		return lifecycle.ChannelBreakerSnapshot{}, err
	}
	req.Header.Set("X-Unio-Internal-Token", c.Token)
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return lifecycle.ChannelBreakerSnapshot{}, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return lifecycle.ChannelBreakerSnapshot{}, err
	}
	if resp.StatusCode != http.StatusOK {
		return lifecycle.ChannelBreakerSnapshot{}, fmt.Errorf("status %d: %s", resp.StatusCode, truncate(string(body), 200))
	}
	var snap lifecycle.ChannelBreakerSnapshot
	if err := json.Unmarshal(body, &snap); err != nil {
		return lifecycle.ChannelBreakerSnapshot{}, err
	}
	return snap, nil
}

func stateRank(s lifecycle.CircuitStateName) int {
	switch s {
	case lifecycle.CircuitStateOpen:
		return 2
	case lifecycle.CircuitStateHalfOpen:
		return 1
	default:
		return 0
	}
}

func remainingMs(v *int64) int64 {
	if v == nil {
		return 0
	}
	return *v
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
