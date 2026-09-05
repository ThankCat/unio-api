package lifecycle

import (
	"context"
	"testing"
	"time"

	"github.com/ThankCat/unio-gateway/internal/core/channel"
	"github.com/ThankCat/unio-gateway/internal/core/requestlog"
	"github.com/ThankCat/unio-gateway/internal/core/routing"
)

type fixedAccountOutbound struct {
	outbound AccountOutbound
}

func (f fixedAccountOutbound) ResolveAccountOutbound(context.Context, int64) (AccountOutbound, error) {
	return f.outbound, nil
}

func durationPtr(d time.Duration) *time.Duration { return &d }

// TestApplyAccountOutboundTimeoutOverrides 冻结账号级超时的三层继承语义（2026-09-05）：
// 账号未配置（nil）继承渠道解析出的值；显式 0 = 不限制；正数覆写。两项相互独立。
func TestApplyAccountOutboundTimeoutOverrides(t *testing.T) {
	base := routing.ChatRouteCandidate{Channel: channel.Runtime{
		ID: 7, ResponseTimeout: 200 * time.Second, FirstTokenTimeout: 60 * time.Second,
	}}

	cases := []struct {
		name              string
		outbound          AccountOutbound
		wantResponse      time.Duration
		wantFirstToken    time.Duration
		wantAccountIDSeen int64
	}{
		{
			name:           "nil inherits the channel values",
			outbound:       AccountOutbound{AccessToken: "at", UpstreamAccountID: "up"},
			wantResponse:   200 * time.Second,
			wantFirstToken: 60 * time.Second,
		},
		{
			name: "explicit zero means unlimited",
			outbound: AccountOutbound{AccessToken: "at", UpstreamAccountID: "up",
				ResponseTimeout: durationPtr(0), FirstTokenTimeout: durationPtr(0)},
			wantResponse:   0,
			wantFirstToken: 0,
		},
		{
			name: "positive values override independently",
			outbound: AccountOutbound{AccessToken: "at", UpstreamAccountID: "up",
				FirstTokenTimeout: durationPtr(300 * time.Second)},
			wantResponse:   200 * time.Second,
			wantFirstToken: 300 * time.Second,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			l := &RequestLifecycle{accountOutbound: fixedAccountOutbound{outbound: tc.outbound}}
			got, err := l.applyAccountOutbound(context.Background(), base, 11)
			if err != nil {
				t.Fatalf("applyAccountOutbound: %v", err)
			}
			if got.Channel.ResponseTimeout != tc.wantResponse || got.Channel.FirstTokenTimeout != tc.wantFirstToken {
				t.Fatalf("timeouts = %v / %v, want %v / %v", got.Channel.ResponseTimeout, got.Channel.FirstTokenTimeout, tc.wantResponse, tc.wantFirstToken)
			}
			if got.Channel.APIKey != "at" || got.Channel.Account.ID != 11 || got.Channel.Account.UpstreamAccountID != "up" {
				t.Fatalf("account identity not applied: %+v", got.Channel.Account)
			}
		})
	}

	// credential 型渠道（accountID=0）原样返回，不碰超时。
	l := &RequestLifecycle{}
	got, err := l.applyAccountOutbound(context.Background(), base, 0)
	if err != nil || got.Channel.ResponseTimeout != 200*time.Second {
		t.Fatalf("credential channel must pass through unchanged: %v %+v", err, got.Channel)
	}
}

// TestCreateAttemptCarriesAccountAttribution 冻结 attempt 级账号归因（2026-09-05）：
// 池型候选在 permit 固化账号后，attempt 创建即写入 account_id——这是账号维度成功率与
// 失败下钻的唯一数据源（request 级 final_account_id 只写成功路径）。credential 型为 0（落库 NULL）。
func TestCreateAttemptCarriesAccountAttribution(t *testing.T) {
	log := &captureCreateAttemptLog{}
	l := &RequestLifecycle{requestLog: log}
	candidate := routing.ChatRouteCandidate{
		ProviderID: 1, ModelDBID: 3, AdapterKey: "codex", UpstreamModel: "gpt-5.5",
		Protocol:       routing.ProtocolOpenAI,
		OriginRevision: 1, ProviderStatusRevision: 1, ChannelConfigRevision: 1,
		Channel: channel.Runtime{ID: 7, Account: channel.AccountIdentity{ID: 11, UpstreamAccountID: "acct"}},
	}
	record := requestlog.RequestRecord{ID: 99, RequestID: "req_x"}

	if _, err := l.CreateAttempt(context.Background(), record, 0, candidate, "permit-1"); err != nil {
		t.Fatalf("CreateAttempt: %v", err)
	}
	if log.params.AccountID != 11 {
		t.Fatalf("attempt account attribution = %d, want 11", log.params.AccountID)
	}

	candidate.Channel.Account = channel.AccountIdentity{}
	if _, err := l.CreateAttempt(context.Background(), record, 1, candidate, "permit-2"); err != nil {
		t.Fatalf("CreateAttempt credential: %v", err)
	}
	if log.params.AccountID != 0 {
		t.Fatalf("credential channel attempt must not carry an account, got %d", log.params.AccountID)
	}
}
