// Package quota 实现号池账号的 Codex 用量主动查询与重置卡（rate-limit reset credit）。
//
// 上游是 ChatGPT 后端的用量面（/backend-api/wham/*，Codex 桌面端与 CLI 的 /status 同源）：
//   - GET  /wham/usage：5h/7d 窗口水位（与推理响应头同一口径）+ 重置卡计数；
//   - GET  /wham/rate-limit-reset-credits：每张卡的 id / 状态 / 到期；
//   - POST /wham/rate-limit-reset-credits/consume：消费一张卡，同时重置 5h 与 7d 两个窗口。
//
// 形状以 sandbox/codex/wire/samples/upstream-wham-*.json 的真实样例为准（2026-09-06 实测），
// 不照抄 sub2api 的多别名容错解析。出站身份与推理面同源（codexidentity），按账号代理出站。
package quota

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/ThankCat/unio-gateway/internal/core/codexidentity"
	"github.com/ThankCat/unio-gateway/internal/platform/failure"
)

const (
	usageURL          = "https://chatgpt.com/backend-api/wham/usage"
	resetCreditsURL   = "https://chatgpt.com/backend-api/wham/rate-limit-reset-credits"
	consumeCreditURL  = "https://chatgpt.com/backend-api/wham/rate-limit-reset-credits/consume"
	upstreamTimeout   = 20 * time.Second
	maxResponseBytes  = 256 << 10
	maxErrorBodyBytes = 300

	// creditResetTypeCodex / creditStatusAvailable 是「可用于重置 Codex 窗口」的卡的判据。
	creditResetTypeCodex  = "codex_rate_limits"
	creditStatusAvailable = "available"
)

// Identity 是一次用量面出站所需的账号身份。
type Identity struct {
	AccessToken       string
	UpstreamAccountID string
	ProxyURL          string
}

// Window 是一个用量窗口（primary=5h、secondary=7d）。
type Window struct {
	UsedPercent        float64 `json:"used_percent"`
	LimitWindowSeconds int64   `json:"limit_window_seconds"`
	ResetAfterSeconds  int64   `json:"reset_after_seconds"`
	ResetAt            int64   `json:"reset_at"`
}

// RateLimit 是 /wham/usage 的窗口包。任一窗口可能为 null。
type RateLimit struct {
	Allowed         bool    `json:"allowed"`
	LimitReached    bool    `json:"limit_reached"`
	PrimaryWindow   *Window `json:"primary_window"`
	SecondaryWindow *Window `json:"secondary_window"`
}

// ResetCreditCounts 是 /wham/usage 里的重置卡计数：AvailableCount 是账号持有的可用卡数。
// 上游还会返回 applicable_available_count，但真实测试表明它为 0 时消费照样成功，不代表「此刻能否用卡」，
// 因此不解析、不展示。
type ResetCreditCounts struct {
	AvailableCount int `json:"available_count"`
}

// UsageCredits 是 /wham/usage 里的按量付费 credits（真实样例：has_credits / unlimited / overage_limit_reached / balance）。
type UsageCredits struct {
	HasCredits          bool   `json:"has_credits"`
	Unlimited           bool   `json:"unlimited"`
	OverageLimitReached bool   `json:"overage_limit_reached"`
	Balance             string `json:"balance"`
}

// Usage 是 /wham/usage 的解码结果（只取本项目关心的字段）。
type Usage struct {
	PlanType          string             `json:"plan_type"`
	RateLimit         *RateLimit         `json:"rate_limit"`
	ResetCreditCounts *ResetCreditCounts `json:"rate_limit_reset_credits"`
	Credits           *UsageCredits      `json:"credits,omitempty"`
}

// ResetCredit 是一张重置卡。ID 只在消费时使用：不落库、不进管理端响应。
type ResetCredit struct {
	ID        string
	ResetType string
	Status    string
	GrantedAt time.Time
	ExpiresAt time.Time
	Title     string
}

// Usable 判断这张卡是否可用于重置 Codex 窗口。
func (c ResetCredit) Usable() bool {
	return strings.EqualFold(c.ResetType, creditResetTypeCodex) && strings.EqualFold(c.Status, creditStatusAvailable)
}

// ResetCredits 是 /wham/rate-limit-reset-credits 的解码结果。
type ResetCredits struct {
	AvailableCount int
	Credits        []ResetCredit
}

// UsableCredits 返回可用的卡，按到期时刻升序（最早到期的先用）。
func (r ResetCredits) UsableCredits() []ResetCredit {
	out := make([]ResetCredit, 0, len(r.Credits))
	for _, credit := range r.Credits {
		if credit.Usable() {
			out = append(out, credit)
		}
	}
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j].ExpiresAt.Before(out[j-1].ExpiresAt); j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}

// ConsumeResult 是消费一张卡的响应：code（如 success / no_credit）、被消费的卡与重置的窗口数。
type ConsumeResult struct {
	Code         string         `json:"code"`
	WindowsReset int            `json:"windows_reset"`
	Credit       *ConsumeCredit `json:"credit,omitempty"`
}

// ConsumeCredit 是消费响应里的卡元数据（不含 id，避免上游标识外泄）。
type ConsumeCredit struct {
	ResetType  string `json:"reset_type,omitempty"`
	Status     string `json:"status,omitempty"`
	GrantedAt  string `json:"granted_at,omitempty"`
	ExpiresAt  string `json:"expires_at,omitempty"`
	RedeemedAt string `json:"redeemed_at,omitempty"`
}

// NoCredit 表示上游确认没有可用卡（消费未发生）。
func (r ConsumeResult) NoCredit() bool {
	return strings.EqualFold(strings.TrimSpace(r.Code), "no_credit")
}

// UpstreamError 是用量面返回非 2xx 的结构化错误：状态码 + 截断正文，供管理端与日志排障。
type UpstreamError struct {
	Operation  string
	StatusCode int
	Body       string
}

func (e *UpstreamError) Error() string {
	return fmt.Sprintf("codex %s: upstream status %d", e.Operation, e.StatusCode)
}

// Client 按账号身份调用用量面。clientFor 为按账号代理解析器（nil 直连），version 为客户端版本来源。
type Client struct {
	clientFor func(proxyURL string) *http.Client
	version   codexidentity.VersionSource
	usageURL  string
	creditURL string
	redeemURL string
	checkURL  string
	meURL     string
}

// NewClient 创建用量面客户端。
func NewClient(clientFor func(proxyURL string) *http.Client, version codexidentity.VersionSource) *Client {
	if clientFor == nil {
		clientFor = func(string) *http.Client { return http.DefaultClient }
	}
	return &Client{
		clientFor: clientFor, version: version,
		usageURL: usageURL, creditURL: resetCreditsURL, redeemURL: consumeCreditURL,
		checkURL: accountsCheckURL, meURL: meURL,
	}
}

// WithBaseURL 把全部端点改到指定前缀（单测用 httptest 服务器）。
func (c *Client) WithBaseURL(base string) *Client {
	base = strings.TrimRight(base, "/")
	c.usageURL = base + "/backend-api/wham/usage"
	c.creditURL = base + "/backend-api/wham/rate-limit-reset-credits"
	c.redeemURL = base + "/backend-api/wham/rate-limit-reset-credits/consume"
	c.checkURL = base + "/backend-api/accounts/check/v4-2023-04-27"
	c.meURL = base + "/backend-api/me"
	return c
}

// FetchUsage 读取账号当前用量窗口与重置卡计数。
func (c *Client) FetchUsage(ctx context.Context, identity Identity) (Usage, error) {
	var usage Usage
	if err := c.do(ctx, identity, http.MethodGet, c.usageURL, nil, "usage", &usage); err != nil {
		return Usage{}, err
	}
	return usage, nil
}

// creditPayload / creditsPayload 是重置卡明细端点的原始形状（真实样例：credits[] + available_count）。
type creditPayload struct {
	ID        string `json:"id"`
	ResetType string `json:"reset_type"`
	Status    string `json:"status"`
	GrantedAt string `json:"granted_at"`
	ExpiresAt string `json:"expires_at"`
	Title     string `json:"title"`
}

type creditsPayload struct {
	Credits        []creditPayload `json:"credits"`
	AvailableCount int             `json:"available_count"`
}

// FetchResetCredits 读取账号持有的重置卡明细。
func (c *Client) FetchResetCredits(ctx context.Context, identity Identity) (ResetCredits, error) {
	var payload creditsPayload
	if err := c.do(ctx, identity, http.MethodGet, c.creditURL, nil, "reset credits", &payload); err != nil {
		return ResetCredits{}, err
	}
	out := ResetCredits{AvailableCount: payload.AvailableCount, Credits: make([]ResetCredit, 0, len(payload.Credits))}
	for _, raw := range payload.Credits {
		credit := ResetCredit{
			ID: strings.TrimSpace(raw.ID), ResetType: raw.ResetType, Status: raw.Status, Title: raw.Title,
		}
		credit.GrantedAt = parseUpstreamTime(raw.GrantedAt)
		credit.ExpiresAt = parseUpstreamTime(raw.ExpiresAt)
		out.Credits = append(out.Credits, credit)
	}
	return out, nil
}

// ConsumeResetCredit 消费一张卡。redeemRequestID 是上游幂等键（重试必须复用同一个）；
// creditID 为空表示由上游挑卡，非空则定向消费（自动用卡用它锁定最早到期的那张）。
func (c *Client) ConsumeResetCredit(ctx context.Context, identity Identity, creditID, redeemRequestID string) (ConsumeResult, error) {
	redeemRequestID = strings.TrimSpace(redeemRequestID)
	if redeemRequestID == "" {
		return ConsumeResult{}, failure.New(failure.CodeConfigInvalid, failure.WithMessage("redeem request id is required"))
	}
	body := map[string]string{"redeem_request_id": redeemRequestID}
	if creditID = strings.TrimSpace(creditID); creditID != "" {
		body["credit_id"] = creditID
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return ConsumeResult{}, failure.Wrap(failure.CodeConfigInvalid, err, failure.WithMessage("encode consume body"))
	}
	var result ConsumeResult
	if err := c.do(ctx, identity, http.MethodPost, c.redeemURL, raw, "consume reset credit", &result); err != nil {
		return ConsumeResult{}, err
	}
	return result, nil
}

// do 发起一次用量面调用并解码 2xx 正文。非 2xx 返回 *UpstreamError；网络错误按 adapter 发送失败归类。
func (c *Client) do(ctx context.Context, identity Identity, method, endpoint string, body []byte, operation string, out any) error {
	return c.doWithHeaders(ctx, identity, method, endpoint, body, operation, out, nil)
}

// doWithHeaders 在 do 的基础上附加额外请求头（accounts/check 需要 ChatGPT 网页同源的 Origin/Referer）。
func (c *Client) doWithHeaders(ctx context.Context, identity Identity, method, endpoint string, body []byte, operation string, out any, extra http.Header) error {
	if strings.TrimSpace(identity.AccessToken) == "" {
		return failure.New(failure.CodeConfigInvalid, failure.WithMessage("account access token is empty"))
	}
	callCtx, cancel := context.WithTimeout(ctx, upstreamTimeout)
	defer cancel()

	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(callCtx, method, endpoint, reader)
	if err != nil {
		return failure.Wrap(failure.CodeConfigInvalid, err, failure.WithMessage("create codex "+operation+" request"))
	}
	req.Header.Set("Authorization", "Bearer "+identity.AccessToken)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if identity.UpstreamAccountID != "" {
		req.Header.Set("chatgpt-account-id", identity.UpstreamAccountID)
	}
	codexidentity.Resolve(c.version).ApplyUsageHeaders(req.Header)
	for key, values := range extra {
		for _, value := range values {
			req.Header.Set(key, value)
		}
	}

	resp, err := c.clientFor(identity.ProxyURL).Do(req)
	if err != nil {
		return failure.Wrap(
			failure.CodeAdapterSendRequestFailed, err,
			failure.WithMessage("codex "+operation+" endpoint unreachable"),
		)
	}
	defer resp.Body.Close()
	payload, readErr := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if readErr != nil {
		return failure.Wrap(failure.CodeAdapterSendRequestFailed, readErr, failure.WithMessage("read codex "+operation+" response"))
	}
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return &UpstreamError{Operation: operation, StatusCode: resp.StatusCode, Body: truncate(string(payload), maxErrorBodyBytes)}
	}
	if err := json.Unmarshal(payload, out); err != nil {
		return failure.Wrap(failure.CodeAdapterSendRequestFailed, err, failure.WithMessage("decode codex "+operation+" response"))
	}
	return nil
}

// parseUpstreamTime 解析上游 RFC3339（含微秒）时间；解析失败返回零值，调用方按「未知」处理。
func parseUpstreamTime(raw string) time.Time {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return time.Time{}
	}
	if t, err := time.Parse(time.RFC3339Nano, raw); err == nil {
		return t.UTC()
	}
	return time.Time{}
}

func truncate(s string, limit int) string {
	if len(s) <= limit {
		return s
	}
	return s[:limit]
}
