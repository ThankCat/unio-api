// Package subscriptionaccount 编排订阅账号的管理操作（第九节）：
// 列表与聚合、批量文件导入、OAuth 导入向导、调度参数编辑、启停/归档/恢复、手动令牌刷新、订阅台账。
//
// 两条硬规则贯穿全部写操作：
//  1. 调度参数与状态变更必须经 runtimecontrol.Publisher 与渠道 capacity_revision 同事务推进
//     （配置热更新传播，边界 20）——账号改动渠道并发没变，用 BumpChannelCapacityRevision 推版本；
//  2. 停用/归档池内最后一个可调度账号可能让模型失去最后供给，须经确认门（供给联动，边界 11）。
package subscriptionaccount

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"go.uber.org/zap"

	"github.com/ThankCat/unio-gateway/internal/core/runtimecontrol"
	"github.com/ThankCat/unio-gateway/internal/platform/breakerstore"
	"github.com/ThankCat/unio-gateway/internal/platform/failure"
	"github.com/ThankCat/unio-gateway/internal/platform/store/sqlc"
	channelservice "github.com/ThankCat/unio-gateway/internal/service/admin/channel"
	"github.com/ThankCat/unio-gateway/internal/service/admin/supply"
	"github.com/ThankCat/unio-gateway/internal/service/subscription"
)

// Queries 是账号管理所需的数据库能力（读侧直连 pool；写侧经 Publisher 的 BusinessCommit 事务）。
type Queries interface {
	AdminListSubscriptionAccounts(ctx context.Context, arg sqlc.AdminListSubscriptionAccountsParams) ([]sqlc.AdminListSubscriptionAccountsRow, error)
	AdminGetSubscriptionAccount(ctx context.Context, id int64) (sqlc.SubscriptionAccount, error)
	AdminCountAccountsByChannel(ctx context.Context, channelID int64) (sqlc.AdminCountAccountsByChannelRow, error)
	AdminListSubscriptionLedger(ctx context.Context, accountID int64) ([]sqlc.SubscriptionLedgerEntry, error)
	AdminCreateSubscriptionLedgerEntry(ctx context.Context, arg sqlc.AdminCreateSubscriptionLedgerEntryParams) (sqlc.SubscriptionLedgerEntry, error)
	AdminCreateSubscriptionAccount(ctx context.Context, arg sqlc.AdminCreateSubscriptionAccountParams) (sqlc.SubscriptionAccount, error)
	AdminReauthorizeSubscriptionAccount(ctx context.Context, arg sqlc.AdminReauthorizeSubscriptionAccountParams) (sqlc.SubscriptionAccount, error)
	AdminDeleteSubscriptionAccountCascade(ctx context.Context, id int64) (int64, error)
	GetAccountByPlatformUpstreamID(ctx context.Context, arg sqlc.GetAccountByPlatformUpstreamIDParams) (sqlc.GetAccountByPlatformUpstreamIDRow, error)
	GetChannel(ctx context.Context, id int64) (sqlc.Channel, error)
	CountSchedulableAccountsByChannel(ctx context.Context, channelID int64) (int64, error)
	AdminListPoolChannels(ctx context.Context) ([]sqlc.AdminListPoolChannelsRow, error)
	GetEnabledProxyURL(ctx context.Context, id int64) (string, error)
	AdminChannelAccountsUsage24h(ctx context.Context, channelID int64) ([]sqlc.AdminChannelAccountsUsage24hRow, error)
	AdminChannelAccountsSale24h(ctx context.Context, channelID int64) ([]sqlc.AdminChannelAccountsSale24hRow, error)
	AdminChannelAccountsLastFailure24h(ctx context.Context, channelID int64) ([]sqlc.AdminChannelAccountsLastFailure24hRow, error)
}

// AccountRuntimeReader 读取账号运行态（冷却/隔离/暂停/在途），供列表页展示。
type AccountRuntimeReader interface {
	AccountRuntimeMany(ctx context.Context, accountIDs []int64) ([]breakerstore.AccountRuntime, error)
}

// RuntimeControlStore 提供渠道容量 control 定位（与渠道容量编辑同一条发布路径）。
type RuntimeControlStore interface {
	ChannelCapacityControl(channelID int64) breakerstore.ControlTarget
}

// Publisher 是 runtimecontrol 两阶段发布器（生产实现 *runtimecontrol.Publisher）。
type Publisher interface {
	Publish(ctx context.Context, req runtimecontrol.PublishRequest) (runtimecontrol.PublishResult, error)
}

// Service 编排账号管理。
type Service struct {
	queries   Queries
	runtime   AccountRuntimeReader
	publisher Publisher
	controls  RuntimeControlStore
	outbound  *subscription.Outbound
	tokens    *subscription.TokenClient
	logger    *zap.Logger
	// supplyPreview 供完整供给影响预览（ADR-0020）；nil 时退回简化确认门。
	supplyPreview *sqlc.Queries

	oauthSessions sync.Map // session id -> oauthSession
}

// WithSupplyPreview 接入供给影响预览（bootstrap 注入 *sqlc.Queries 供 supply.ChannelImpact 反查）。
func (s *Service) WithSupplyPreview(q *sqlc.Queries) *Service {
	s.supplyPreview = q
	return s
}

// NewService 创建账号管理服务。
func NewService(
	queries Queries,
	runtime AccountRuntimeReader,
	publisher Publisher,
	controls RuntimeControlStore,
	outbound *subscription.Outbound,
	tokens *subscription.TokenClient,
	logger *zap.Logger,
) *Service {
	if logger == nil {
		logger = zap.NewNop()
	}
	return &Service{
		queries: queries, runtime: runtime, publisher: publisher, controls: controls,
		outbound: outbound, tokens: tokens, logger: logger,
	}
}

// Account 是账号管理视图（凭据只暴露摘要，不回令牌明文）。
type Account struct {
	ID                    int64      `json:"id"`
	ChannelID             int64      `json:"channel_id"`
	Platform              string     `json:"platform"`
	CredentialType        string     `json:"credential_type"`
	UpstreamAccountID     string     `json:"upstream_account_id"`
	DisplayName           string     `json:"display_name"`
	PlanType              string     `json:"plan_type,omitempty"`
	ProxyURL              string     `json:"proxy_url,omitempty"`
	ProxyID               *int64     `json:"proxy_id,omitempty"`
	ProxyName             string     `json:"proxy_name,omitempty"`
	ProxyStatus           string     `json:"proxy_status,omitempty"`
	Email                 string     `json:"email,omitempty"`
	ConcurrencyLimit      *int64     `json:"concurrency_limit"`
	Priority              int32      `json:"priority"`
	Status                string     `json:"status"`
	DisabledReason        string     `json:"disabled_reason,omitempty"`
	SubscriptionExpiresAt *time.Time `json:"subscription_expires_at,omitempty"`
	UsageSnapshot         any        `json:"usage_snapshot,omitempty"`
	LastSuccessAt         *time.Time `json:"last_success_at,omitempty"`
	ConfigRevision        int64      `json:"config_revision"`
	TokenExpiresAt        string     `json:"token_expires_at,omitempty"`
	HasRefreshToken       bool       `json:"has_refresh_token"`
	CreatedAt             time.Time  `json:"created_at"`
	UpdatedAt             time.Time  `json:"updated_at"`

	// Runtime 是 Redis 运行态（冷却/临时不可调度/用量暂停/在途），列表读取时批量拉取。
	Runtime *AccountRuntimeView `json:"runtime,omitempty"`
	// Usage24h 是近 24 小时按账号聚合的产出（请求/成功率/token/售卖额），列表批量拉取。
	Usage24h *AccountUsage24hView `json:"usage_24h,omitempty"`
}

// AccountUsage24hView 是「24H」列的数据（request_records + usage_records + ledger_entries 聚合）。
// TotalRequests 包含已归属账号的全部终态记录（含 canceled）；进行中请求由 Runtime.InFlight 单独提供。
type AccountUsage24hView struct {
	TotalRequests     int64 `json:"total_requests"`
	SucceededRequests int64 `json:"succeeded_requests"`
	FailedRequests    int64 `json:"failed_requests"`
	CanceledRequests  int64 `json:"canceled_requests"`
	TotalTokens       int64 `json:"total_tokens"`
	// SaleAmounts 按币种的净扣费串（"8.4021 CNY"），多币种逗号相连；金额走十进制字符串，不经 float。
	SaleAmounts     string `json:"sale_amounts,omitempty"`
	AvgLatencyMs    int64  `json:"avg_latency_ms"`
	AvgFirstTokenMs int64  `json:"avg_first_token_ms"`
	LastFailureAt   string `json:"last_failure_at,omitempty"`
	LastFailureCode string `json:"last_failure_code,omitempty"`
}

// AccountRuntimeView 是运行态的展示形态。
type AccountRuntimeView struct {
	CooldownRemainingMs      int64  `json:"cooldown_remaining_ms"`
	CooldownWindow           string `json:"cooldown_window,omitempty"`
	UnschedulableRemainingMs int64  `json:"unschedulable_remaining_ms"`
	UnschedulableReason      string `json:"unschedulable_reason,omitempty"`
	UsagePauseRemainingMs    int64  `json:"usage_pause_remaining_ms"`
	UsagePauseWindow         string `json:"usage_pause_window,omitempty"`
	InFlight                 int64  `json:"in_flight"`
}

// Aggregates 是渠道账号聚合（概览区 + 池空 ≠ 熔断的区分展示）。
type Aggregates struct {
	Total        int64 `json:"total"`
	Enabled      int64 `json:"enabled"`
	Disabled     int64 `json:"disabled"`
	Archived     int64 `json:"archived"`
	ExpiringSoon int64 `json:"expiring_soon"`
	// InFlight/CoolingDown/UsagePaused 来自 Redis 运行态聚合。
	InFlight    int64 `json:"in_flight"`
	CoolingDown int64 `json:"cooling_down"`
	UsagePaused int64 `json:"usage_paused"`
}

// ListResult 是账号页签的完整读模型。
type ListResult struct {
	Accounts   []Account  `json:"accounts"`
	Aggregates Aggregates `json:"aggregates"`
}

// List 列出渠道下全部账号并合并 Redis 运行态。
func (s *Service) List(ctx context.Context, channelID int64, status string) (ListResult, error) {
	if channelID <= 0 {
		return ListResult{}, invalidArgument("channel_id", "channel id must be positive")
	}
	var statusFilter pgtype.Text
	if status != "" {
		statusFilter = pgtype.Text{String: status, Valid: true}
	}
	rows, err := s.queries.AdminListSubscriptionAccounts(ctx, sqlc.AdminListSubscriptionAccountsParams{
		ChannelID: channelID, Status: statusFilter,
	})
	if err != nil {
		return ListResult{}, storeFailed(err, "list subscription accounts")
	}
	counts, err := s.queries.AdminCountAccountsByChannel(ctx, channelID)
	if err != nil {
		return ListResult{}, storeFailed(err, "count subscription accounts")
	}

	result := ListResult{
		Accounts: make([]Account, 0, len(rows)),
		Aggregates: Aggregates{
			Total: counts.Total, Enabled: counts.Enabled,
			Disabled: counts.Disabled, Archived: counts.Archived,
			ExpiringSoon: counts.ExpiringSoon,
		},
	}
	ids := make([]int64, 0, len(rows))
	for _, row := range rows {
		result.Accounts = append(result.Accounts, accountFromListRow(row))
		if row.Status != "archived" {
			ids = append(ids, row.ID)
		}
	}
	s.attachUsage24h(ctx, channelID, result.Accounts)
	if s.runtime != nil && len(ids) > 0 {
		runtimes, runtimeErr := s.runtime.AccountRuntimeMany(ctx, ids)
		if runtimeErr == nil {
			byID := make(map[int64]breakerstore.AccountRuntime, len(runtimes))
			for _, rt := range runtimes {
				byID[rt.AccountID] = rt
			}
			for index := range result.Accounts {
				rt, ok := byID[result.Accounts[index].ID]
				if !ok {
					continue
				}
				view := &AccountRuntimeView{
					CooldownRemainingMs:      rt.CooldownRemainingMs,
					CooldownWindow:           string(rt.CooldownWindow),
					UnschedulableRemainingMs: rt.UnschedulableRemainingMs,
					UnschedulableReason:      string(rt.UnschedulableReason),
					UsagePauseRemainingMs:    rt.UsagePauseRemainingMs,
					UsagePauseWindow:         string(rt.UsagePauseWindow),
					InFlight:                 rt.InFlight,
				}
				result.Accounts[index].Runtime = view
				result.Aggregates.InFlight += rt.InFlight
				if rt.CooldownRemainingMs > 0 {
					result.Aggregates.CoolingDown++
				}
				if rt.UsagePauseRemainingMs > 0 {
					result.Aggregates.UsagePaused++
				}
			}
		}
	}
	return result, nil
}

// attachUsage24h 批量拉近 24h 聚合并挂到账号视图。聚合只是运维观测，
// 查询失败不阻断列表主体（该列显示为空，问题在日志里暴露）。
func (s *Service) attachUsage24h(ctx context.Context, channelID int64, accounts []Account) {
	usageRows, err := s.queries.AdminChannelAccountsUsage24h(ctx, channelID)
	if err != nil {
		return
	}
	views := make(map[int64]*AccountUsage24hView, len(usageRows))
	for _, row := range usageRows {
		if !row.AccountID.Valid {
			continue
		}
		views[row.AccountID.Int64] = &AccountUsage24hView{
			TotalRequests:     row.TotalRequests,
			SucceededRequests: row.SucceededRequests,
			FailedRequests:    row.FailedRequests,
			CanceledRequests:  row.CanceledRequests,
			TotalTokens:       row.TotalTokens,
			AvgLatencyMs:      row.AvgLatencyMs,
			AvgFirstTokenMs:   row.AvgFirstTokenMs,
		}
	}
	if saleRows, saleErr := s.queries.AdminChannelAccountsSale24h(ctx, channelID); saleErr == nil {
		for _, row := range saleRows {
			if !row.AccountID.Valid {
				continue
			}
			view, ok := views[row.AccountID.Int64]
			if !ok {
				continue
			}
			amount := numericToDecimalString(row.SaleAmount)
			if amount == "" || amount == "0" {
				continue
			}
			part := amount + " " + row.Currency
			if view.SaleAmounts != "" {
				view.SaleAmounts += ", "
			}
			view.SaleAmounts += part
		}
	}
	if failRows, failErr := s.queries.AdminChannelAccountsLastFailure24h(ctx, channelID); failErr == nil {
		for _, row := range failRows {
			if !row.AccountID.Valid {
				continue
			}
			view, ok := views[row.AccountID.Int64]
			if !ok {
				continue
			}
			view.LastFailureAt = row.FailedAt.Time.UTC().Format(time.RFC3339)
			view.LastFailureCode = row.ErrorCode
		}
	}
	for index := range accounts {
		if view, ok := views[accounts[index].ID]; ok {
			accounts[index].Usage24h = view
		}
	}
}

// numericToDecimalString 把 pgtype.Numeric 转十进制字符串（金额不经 float，与全库口径一致）。
func numericToDecimalString(v pgtype.Numeric) string {
	if !v.Valid {
		return ""
	}
	raw, err := v.MarshalJSON()
	if err != nil {
		return ""
	}
	return strings.Trim(string(raw), "\"")
}

// PoolOverview 是号池并发监测的一个池（实时监控页区块，Provider → 池 → 账号钻取）。
type PoolOverview struct {
	ChannelID                 int64      `json:"channel_id"`
	ChannelName               string     `json:"channel_name"`
	ChannelStatus             string     `json:"channel_status"`
	ProviderID                int64      `json:"provider_id"`
	ProviderName              string     `json:"provider_name"`
	AccountDefaultConcurrency *int64     `json:"account_default_concurrency"`
	Aggregates                Aggregates `json:"aggregates"`
	Accounts                  []Account  `json:"accounts"`
}

// PoolsOverview 返回全部未归档池型渠道及其账号运行态（数据源与账号页签同一条读路径：
// Postgres 账号事实 + Redis 运行态批量读；不进 DB 经营聚合）。
func (s *Service) PoolsOverview(ctx context.Context) ([]PoolOverview, error) {
	channels, err := s.queries.AdminListPoolChannels(ctx)
	if err != nil {
		return nil, storeFailed(err, "list pool channels")
	}
	out := make([]PoolOverview, 0, len(channels))
	for _, ch := range channels {
		list, listErr := s.List(ctx, ch.ID, "")
		if listErr != nil {
			return nil, listErr
		}
		var defaultConcurrency *int64
		if ch.AccountDefaultConcurrency.Valid {
			v := int64(ch.AccountDefaultConcurrency.Int32)
			defaultConcurrency = &v
		}
		out = append(out, PoolOverview{
			ChannelID:                 ch.ID,
			ChannelName:               ch.Name,
			ChannelStatus:             ch.Status,
			ProviderID:                ch.ProviderID,
			ProviderName:              ch.ProviderName,
			AccountDefaultConcurrency: defaultConcurrency,
			Aggregates:                list.Aggregates,
			Accounts:                  list.Accounts,
		})
	}
	return out, nil
}

// ImportFile 解析并导入一个 sub2api-data v1 文件到指定池型渠道。
func (s *Service) ImportFile(ctx context.Context, channelID int64, raw []byte) ([]subscription.ImportResultItem, error) {
	if err := s.requirePoolChannel(ctx, channelID); err != nil {
		return nil, err
	}
	accounts, err := subscription.ParseSub2APIData(raw)
	if err != nil {
		// 解析失败是客户端提交了坏文件（格式/字段/JSON），归 400 而非 500——
		// ParseSub2APIData 用 CodeConfigInvalid，不在 admin 状态映射里会漏成 internal error。
		return nil, invalidArgument("file", "导入文件不是合法的 sub2api-data v1（检查 type/version/accounts 字段与 JSON 格式）")
	}
	results, err := subscription.ImportAccounts(ctx, importerQueries{s.queries}, channelID, accounts)
	if err != nil {
		return results, err
	}
	return results, nil
}

// importerQueries 适配 subscription.ImporterQueries（同一 Queries 集合的子集）。
type importerQueries struct{ q Queries }

func (a importerQueries) AdminCreateSubscriptionAccount(ctx context.Context, arg sqlc.AdminCreateSubscriptionAccountParams) (sqlc.SubscriptionAccount, error) {
	return a.q.AdminCreateSubscriptionAccount(ctx, arg)
}

func (a importerQueries) GetAccountByPlatformUpstreamID(ctx context.Context, arg sqlc.GetAccountByPlatformUpstreamIDParams) (sqlc.GetAccountByPlatformUpstreamIDRow, error) {
	return a.q.GetAccountByPlatformUpstreamID(ctx, arg)
}

// oauthSession 是一次 OAuth 导入向导的服务端会话（verifier/state 不出服务端）。
type oauthSession struct {
	Challenge subscription.PKCEChallenge
	ChannelID int64
	ProxyURL  string
	ProxyID   int64
	ExpiresAt time.Time
}

// StartOAuth 生成授权链接。代理实体（proxyID）优先；proxyURL 为裸串回退（API 兼容）。
// 该账号将绑定这个出口：换码请求就从它发出，保证会话从诞生起固定出口。
func (s *Service) StartOAuth(ctx context.Context, channelID int64, proxyURL string, proxyID int64) (sessionID, authorizationURL string, err error) {
	if err := s.requirePoolChannel(ctx, channelID); err != nil {
		return "", "", err
	}
	if proxyID > 0 {
		entityURL, proxyErr := s.queries.GetEnabledProxyURL(ctx, proxyID)
		if proxyErr != nil {
			return "", "", invalidArgument("proxy_id", "代理不存在或已停用")
		}
		proxyURL = entityURL
	}
	s.sweepExpiredOAuthSessions()
	challenge, err := subscription.NewPKCEChallenge()
	if err != nil {
		return "", "", err
	}
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", "", failure.Wrap(failure.CodeConfigInvalid, err, failure.WithMessage("generate oauth session id"))
	}
	sessionID = hex.EncodeToString(buf)
	s.oauthSessions.Store(sessionID, oauthSession{
		Challenge: challenge, ChannelID: channelID, ProxyURL: proxyURL, ProxyID: proxyID,
		ExpiresAt: time.Now().Add(15 * time.Minute),
	})
	return sessionID, challenge.AuthorizationURL(""), nil
}

// sweepExpiredOAuthSessions 清理超时未完成的向导会话。
// 会话只在 complete 时删除，弃用的会话（管理员关掉弹窗）会永久留在内存——在每次 start 时顺手清扫。
func (s *Service) sweepExpiredOAuthSessions() {
	now := time.Now()
	s.oauthSessions.Range(func(key, value any) bool {
		if session, ok := value.(oauthSession); ok && now.After(session.ExpiresAt) {
			s.oauthSessions.Delete(key)
		}
		return true
	})
}

// CompleteOAuthResult 是 OAuth 向导完成的结果：新导入或对既有账号的重新授权。
type CompleteOAuthResult struct {
	Account      Account `json:"account"`
	Reauthorized bool    `json:"reauthorized"`
}

// CompleteOAuth 回填 code 完成导入。
// 同渠道已存在该上游账号时执行重新授权（覆盖凭据、保留调度参数与台账）——吊销后续命是高频操作，
// 不应走删除重建；在其它渠道时仍拒绝（一号一池不变量）。新导入落库 disabled。
func (s *Service) CompleteOAuth(ctx context.Context, sessionID, code, state string) (CompleteOAuthResult, error) {
	value, ok := s.oauthSessions.LoadAndDelete(sessionID)
	if !ok {
		return CompleteOAuthResult{}, invalidArgument("session_id", "oauth session not found or already used")
	}
	session := value.(oauthSession)
	if time.Now().After(session.ExpiresAt) {
		return CompleteOAuthResult{}, invalidArgument("session_id", "oauth session expired")
	}
	// 裸 code 粘贴（管理员只复制了 code 参数）：session_id 已把本次会话与 PKCE verifier 绑定，
	// 缺省 state 视为同会话放行；带 state 时仍严格校验，防止两个并行向导互相串码。
	if strings.TrimSpace(state) == "" {
		state = session.Challenge.State
	}
	imported, err := subscription.CompleteAuthorization(ctx, s.tokens, session.Challenge, code, state, "", session.ProxyURL)
	if err != nil {
		return CompleteOAuthResult{}, err
	}
	if session.ProxyID > 0 {
		proxyID := session.ProxyID
		imported.ProxyID = &proxyID
	}

	// 重授权分支：同渠道同上游账号 → 覆盖凭据（调度参数/状态/台账不动，状态由管理员随后启用）。
	existing, err := s.queries.GetAccountByPlatformUpstreamID(ctx, sqlc.GetAccountByPlatformUpstreamIDParams{
		Platform: imported.Platform, UpstreamAccountID: imported.UpstreamAccountID,
	})
	switch {
	case err == nil && existing.ChannelID == session.ChannelID:
		raw, encodeErr := imported.Credentials.Encode()
		if encodeErr != nil {
			return CompleteOAuthResult{}, encodeErr
		}
		var subscriptionUntil pgtype.Timestamptz
		if !imported.SubscriptionUntil.IsZero() {
			subscriptionUntil = pgtype.Timestamptz{Time: imported.SubscriptionUntil, Valid: true}
		}
		row, reauthErr := s.queries.AdminReauthorizeSubscriptionAccount(ctx, sqlc.AdminReauthorizeSubscriptionAccountParams{
			Platform:              imported.Platform,
			UpstreamAccountID:     imported.UpstreamAccountID,
			Credentials:           raw,
			PlanType:              optionalText(imported.PlanType),
			SubscriptionExpiresAt: subscriptionUntil,
		})
		if reauthErr != nil {
			return CompleteOAuthResult{}, storeFailed(reauthErr, "reauthorize subscription account")
		}
		return CompleteOAuthResult{Account: accountFromRow(row), Reauthorized: true}, nil
	case err == nil:
		return CompleteOAuthResult{}, conflict(
			"该上游账号已存在于其它渠道（channel_id=" + strconv.FormatInt(existing.ChannelID, 10) + "）；一号一池，先在原渠道归档后再导入",
		)
	case !errors.Is(err, pgx.ErrNoRows):
		return CompleteOAuthResult{}, storeFailed(err, "check existing account")
	}

	results, err := subscription.ImportAccounts(ctx, importerQueries{s.queries}, session.ChannelID, []subscription.ImportAccount{imported})
	if err != nil {
		return CompleteOAuthResult{}, err
	}
	if len(results) != 1 || !results[0].Imported {
		reason := "import rejected"
		if len(results) == 1 && results[0].Reason != "" {
			reason = results[0].Reason
		}
		return CompleteOAuthResult{}, conflict(reason)
	}
	row, err := s.queries.AdminGetSubscriptionAccount(ctx, results[0].AccountID)
	if err != nil {
		return CompleteOAuthResult{}, storeFailed(err, "load imported account")
	}
	return CompleteOAuthResult{Account: accountFromRow(row)}, nil
}

// UpdateConfigInput 是调度参数编辑入参。
type UpdateConfigInput struct {
	AccountID   int64
	DisplayName string
	ProxyURL    string
	// ProxyID 是出站代理实体引用（>0 时生效并清空 legacy ProxyURL；0 表示不用实体）。
	ProxyID          int64
	ConcurrencyLimit *int64
	Priority         int32
	// SubscriptionExpiresAt 是订阅到期时间（到期预警数据源）。上游不提供机读到期时间，
	// 运营在这里录入/更正；nil 表示清除（未知）。
	SubscriptionExpiresAt *time.Time
}

// UpdateConfig 修改调度参数并把变更传播到运行态围栏（账号 config_revision +1、
// 渠道 capacity_revision +1、Redis capacity control 两阶段重发布）。
func (s *Service) UpdateConfig(ctx context.Context, in UpdateConfigInput) (Account, error) {
	if in.AccountID <= 0 {
		return Account{}, invalidArgument("id", "account id must be positive")
	}
	if strings.TrimSpace(in.DisplayName) == "" {
		return Account{}, invalidArgument("display_name", "display name is required")
	}
	if in.Priority < 0 {
		return Account{}, invalidArgument("priority", "priority must be >= 0")
	}
	if in.ConcurrencyLimit != nil && *in.ConcurrencyLimit < 0 {
		return Account{}, invalidArgument("concurrency_limit", "concurrency limit must be >= 0")
	}
	account, err := s.queries.AdminGetSubscriptionAccount(ctx, in.AccountID)
	if err != nil {
		return Account{}, accountLoadError(err)
	}

	var expiresAt pgtype.Timestamptz
	if in.SubscriptionExpiresAt != nil {
		expiresAt = pgtype.Timestamptz{Time: *in.SubscriptionExpiresAt, Valid: true}
	}
	var proxyID pgtype.Int8
	proxyURL := in.ProxyURL
	if in.ProxyID > 0 {
		if _, proxyErr := s.queries.GetEnabledProxyURL(ctx, in.ProxyID); proxyErr != nil {
			return Account{}, invalidArgument("proxy_id", "代理不存在或已停用")
		}
		proxyID = pgtype.Int8{Int64: in.ProxyID, Valid: true}
		// 实体引用是单一真相：绑定实体时清空 legacy 裸 URL，避免两处口径漂移。
		proxyURL = ""
	}
	var updated sqlc.SubscriptionAccount
	err = s.publishAccountChange(ctx, account.ChannelID, func(ctx context.Context, qtx *sqlc.Queries) error {
		row, updateErr := qtx.AdminUpdateSubscriptionAccountConfig(ctx, sqlc.AdminUpdateSubscriptionAccountConfigParams{
			ID:                    in.AccountID,
			DisplayName:           strings.TrimSpace(in.DisplayName),
			ProxyUrl:              optionalText(proxyURL),
			ProxyID:               proxyID,
			ConcurrencyLimit:      optionalInt4(in.ConcurrencyLimit),
			Priority:              in.Priority,
			SubscriptionExpiresAt: expiresAt,
		})
		if updateErr != nil {
			return storeFailed(updateErr, "update subscription account config")
		}
		updated = row
		return nil
	})
	if err != nil {
		return Account{}, err
	}
	return accountFromRow(updated), nil
}

// Delete 物理删除账号，用于清理录错/试错的脏数据。
//
// 闸门与渠道删除同构：只允许删除已归档账号（归档已保证不可调度且经过停用确认）。
// 台账随账号级联删除；一旦账号被请求历史（request_records.final_account_id）引用，
// DB 报 23503，降级为 conflict 提示保持归档——保住账务归因链路。
// 运行态 Redis 键（冷却/在途等）不显式清理：键都带 TTL，账号删除后自然过期，不会误伤他号。
func (s *Service) Delete(ctx context.Context, accountID int64) error {
	if accountID <= 0 {
		return invalidArgument("id", "account id must be positive")
	}
	account, err := s.queries.AdminGetSubscriptionAccount(ctx, accountID)
	if err != nil {
		return accountLoadError(err)
	}
	if account.Status != "archived" {
		return conflict("先归档账号再删除（归档保证它已退出调度）；有历史请求的账号应保持归档而非删除")
	}
	affected, err := s.queries.AdminDeleteSubscriptionAccountCascade(ctx, accountID)
	if err != nil {
		if isForeignKeyViolation(err) {
			return conflict("该账号已有请求/账务历史引用，不可物理删除；保持归档即可")
		}
		return storeFailed(err, "delete subscription account")
	}
	if affected == 0 {
		return failure.New(failure.CodeAdminNotFound, failure.WithMessage("account not found"))
	}
	return nil
}

// isForeignKeyViolation 判断 PostgreSQL 外键约束（23503）。
func isForeignKeyViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23503"
}

// SetStatusInput 是状态流转入参。
type SetStatusInput struct {
	AccountID int64
	// Action: enable / disable / archive / restore。
	Action string
	// DisabledReason 仅 disable 有意义（缺省 manual）。
	DisabledReason string
	// ConfirmSupplyImpact 确认「停用/归档最后一个可调度账号」的供给影响（边界 11）。
	ConfirmSupplyImpact bool
	// ExpectedImpactFingerprint 是影响预览指纹（ADR-0020 完整预览）：预览后影响集合若被并发
	// 写入改变，指纹失配将再次要求确认。接入 supply 预览后必带；简化门路径忽略。
	ExpectedImpactFingerprint string
}

// SetStatus 执行启停/归档/恢复。归档不可逆到 enabled：恢复统一落 disabled。
func (s *Service) SetStatus(ctx context.Context, in SetStatusInput) (Account, error) {
	if in.AccountID <= 0 {
		return Account{}, invalidArgument("id", "account id must be positive")
	}
	account, err := s.queries.AdminGetSubscriptionAccount(ctx, in.AccountID)
	if err != nil {
		return Account{}, accountLoadError(err)
	}

	var nextStatus, disabledReason string
	switch in.Action {
	case "enable":
		if account.Status == "archived" {
			return Account{}, conflict("archived account must be restored to disabled first")
		}
		nextStatus = "enabled"
	case "disable":
		nextStatus = "disabled"
		disabledReason = in.DisabledReason
		if disabledReason == "" {
			disabledReason = "manual"
		}
	case "archive":
		if account.Status == "enabled" {
			return Account{}, conflict("enabled account must be disabled before archiving")
		}
		nextStatus = "archived"
	case "restore":
		if account.Status != "archived" {
			return Account{}, conflict("only archived accounts can be restored")
		}
		nextStatus = "disabled"
	default:
		return Account{}, invalidArgument("action", "action must be enable/disable/archive/restore")
	}

	// 供给联动（边界 11 / ADR-0020）：停用最后一个可调度账号会让整条池失去候选资格。
	// 渠道仍 enabled 时给出完整影响预览（哪些模型会失去最后运行供给 + 指纹确认）；
	// 预览依赖未注入时退回简化确认门，不静默放行。
	if account.Status == "enabled" && nextStatus != "enabled" {
		schedulable, countErr := s.queries.CountSchedulableAccountsByChannel(ctx, account.ChannelID)
		if countErr != nil {
			return Account{}, storeFailed(countErr, "count schedulable accounts")
		}
		if schedulable <= 1 {
			channelRow, chErr := s.queries.GetChannel(ctx, account.ChannelID)
			if chErr == nil && channelRow.Status == "enabled" {
				if err := s.authorizeLastSupplyChange(ctx, account.ChannelID, in); err != nil {
					return Account{}, err
				}
			}
		}
	}

	var updated sqlc.SubscriptionAccount
	err = s.publishAccountChange(ctx, account.ChannelID, func(ctx context.Context, qtx *sqlc.Queries) error {
		row, statusErr := qtx.AdminSetSubscriptionAccountStatus(ctx, sqlc.AdminSetSubscriptionAccountStatusParams{
			ID:             in.AccountID,
			Status:         nextStatus,
			DisabledReason: optionalText(disabledReason),
		})
		if statusErr != nil {
			return storeFailed(statusErr, "set subscription account status")
		}
		updated = row
		return nil
	})
	if err != nil {
		return Account{}, err
	}
	return accountFromRow(updated), nil
}

// authorizeLastSupplyChange 对「移出最后一个可调度账号」执行供给影响确认。
//
// 接入 supply 预览时（生产路径）：复用渠道停用的 ChannelImpact 语义——受影响集合 =
// 以本渠道为最后一条运行候选的模型。集合为空（其它渠道还能服务这些模型）直接放行：
// 没有客户影响就不打断操作；有影响则要求「确认 + 指纹一致」（ConfirmationRequired → 409 预览）。
// 未接入预览（测试/降级）时退回简化确认门：只看 confirm 布尔。
func (s *Service) authorizeLastSupplyChange(ctx context.Context, channelID int64, in SetStatusInput) error {
	if s.supplyPreview == nil {
		if in.ConfirmSupplyImpact {
			return nil
		}
		return failure.New(
			failure.CodeAdminConflict,
			failure.WithMessage("这是该池最后一个可调度账号：停用后渠道将失去全部供给，绑定模型的请求会得到 503。确认请携带 confirm_supply_impact=true 重试"),
			failure.WithField("reason_code", "account_last_supply_confirmation_required"),
		)
	}
	impact, err := supply.ChannelImpact(ctx, s.supplyPreview, channelID)
	if err != nil {
		return storeFailed(err, "compute account supply impact")
	}
	// 与渠道停用预览区分指纹域：同一影响集合不允许跨操作复用确认。
	impact.Kind = "account_last_supply"
	return supply.Authorize(
		impact,
		"account_last_supply_confirmation_required",
		"这是该池最后一个可调度账号：停用后以下模型将失去最后一条可用供给，新请求会得到 503，直到重新启用账号",
		supply.Confirmation{Confirm: in.ConfirmSupplyImpact, ExpectedFingerprint: in.ExpectedImpactFingerprint},
	)
}

// RefreshToken 手动触发一次令牌刷新（与后台保活同一条代码路径）。
func (s *Service) RefreshToken(ctx context.Context, accountID int64) (Account, error) {
	if accountID <= 0 {
		return Account{}, invalidArgument("id", "account id must be positive")
	}
	account, err := s.queries.AdminGetSubscriptionAccount(ctx, accountID)
	if err != nil {
		return Account{}, accountLoadError(err)
	}
	// 归档是终局状态：不再持有调度资格，也不该继续动上游会话（刷新会轮换 refresh_token）。
	if account.Status == "archived" {
		return Account{}, conflict("归档账号不刷新令牌；如需续用请先恢复为停用")
	}
	creds, err := subscription.DecodeCredentials(account.Credentials)
	if err != nil {
		return Account{}, err
	}
	proxy := ""
	if account.ProxyUrl.Valid {
		proxy = account.ProxyUrl.String
	}
	if _, err := s.outbound.RefreshAccount(ctx, accountID, creds, proxy); err != nil {
		return Account{}, refreshError(err)
	}
	row, err := s.queries.AdminGetSubscriptionAccount(ctx, accountID)
	if err != nil {
		return Account{}, accountLoadError(err)
	}
	return accountFromRow(row), nil
}

// refreshError 把 outbound 刷新失败翻成 admin 可读的错误，而不是让内部 routing 错误码漏成 500。
//   - 确认吊销（RefreshRejectedError）：账号已被本次刷新置为 disabled(token_revoked)，回 409 说明；
//   - 其余（网络/上游不可用）：回 502，提示可稍后重试，账号已临时隔离等下一轮保活。
func refreshError(err error) error {
	var rejected *subscription.RefreshRejectedError
	if errors.As(err, &rejected) {
		return failure.New(
			failure.CodeAdminConflict,
			failure.WithMessage("刷新令牌已被上游确认吊销，账号已自动停用（token_revoked）；需重新授权该账号"),
		)
	}
	return failure.Wrap(
		failure.CodeAdminUpstreamUnavailable,
		err,
		failure.WithMessage("令牌刷新失败：上游不可达或返回异常，账号已临时移出调度，可稍后重试"),
	)
}

// LedgerEntry 是订阅台账条目视图。
type LedgerEntry struct {
	ID          int64     `json:"id"`
	AccountID   int64     `json:"account_id"`
	Amount      string    `json:"amount"`
	Currency    string    `json:"currency"`
	PeriodStart time.Time `json:"period_start"`
	PeriodEnd   time.Time `json:"period_end"`
	Note        string    `json:"note,omitempty"`
	CreatedBy   string    `json:"created_by,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
}

// ListLedger 列出账号台账。
func (s *Service) ListLedger(ctx context.Context, accountID int64) ([]LedgerEntry, error) {
	rows, err := s.queries.AdminListSubscriptionLedger(ctx, accountID)
	if err != nil {
		return nil, storeFailed(err, "list subscription ledger")
	}
	entries := make([]LedgerEntry, 0, len(rows))
	for _, row := range rows {
		entries = append(entries, ledgerFromRow(row))
	}
	return entries, nil
}

// CreateLedgerInput 是台账录入入参。
type CreateLedgerInput struct {
	AccountID   int64
	Amount      pgtype.Numeric
	Currency    string
	PeriodStart time.Time
	PeriodEnd   time.Time
	Note        string
	CreatedBy   string
}

// CreateLedger 录入一期订阅费用。
func (s *Service) CreateLedger(ctx context.Context, in CreateLedgerInput) (LedgerEntry, error) {
	if in.AccountID <= 0 {
		return LedgerEntry{}, invalidArgument("id", "account id must be positive")
	}
	if strings.TrimSpace(in.Currency) == "" {
		return LedgerEntry{}, invalidArgument("currency", "currency is required")
	}
	if !in.PeriodEnd.After(in.PeriodStart) {
		return LedgerEntry{}, invalidArgument("period_end", "period_end must be after period_start")
	}
	row, err := s.queries.AdminCreateSubscriptionLedgerEntry(ctx, sqlc.AdminCreateSubscriptionLedgerEntryParams{
		AccountID:   in.AccountID,
		Amount:      in.Amount,
		Currency:    strings.ToUpper(strings.TrimSpace(in.Currency)),
		PeriodStart: pgtype.Timestamptz{Time: in.PeriodStart, Valid: true},
		PeriodEnd:   pgtype.Timestamptz{Time: in.PeriodEnd, Valid: true},
		Note:        optionalText(in.Note),
		CreatedBy:   optionalText(in.CreatedBy),
	})
	if err != nil {
		return LedgerEntry{}, storeFailed(err, "create subscription ledger entry")
	}
	return ledgerFromRow(row), nil
}

// publishAccountChange 用渠道容量 control 的两阶段发布承载一次账号写入：
// BusinessCommit 事务内先执行账号变更（config_revision +1 由查询自身完成），
// 再 BumpChannelCapacityRevision 推进渠道 capacity_revision——运行态围栏据此立即失效旧候选快照。
func (s *Service) publishAccountChange(ctx context.Context, channelID int64, mutate func(context.Context, *sqlc.Queries) error) error {
	if s.publisher == nil || s.controls == nil {
		return failure.New(
			failure.CodeGatewayBreakerStoreUnavailable,
			failure.WithMessage("subscription account: runtime-control publisher unavailable"),
		)
	}
	channelRow, err := s.queries.GetChannel(ctx, channelID)
	if err != nil {
		return storeFailed(err, "load channel for account change")
	}
	payload, err := channelservice.CanonicalCapacityPayloadFromChannel(channelRow)
	if err != nil {
		return err
	}
	token, err := newControlToken()
	if err != nil {
		return err
	}
	nextRevision := channelRow.CapacityRevision + 1
	_, err = s.publisher.Publish(ctx, runtimecontrol.PublishRequest{
		Kind:            runtimecontrol.KindChannelCapacity,
		Target:          s.controls.ChannelCapacityControl(channelID),
		Token:           token,
		Payload:         payload,
		CurrentRevision: channelRow.CapacityRevision,
		NextRevision:    nextRevision,
		ChannelID:       &channelRow.ID,
		BusinessCommit: func(ctx context.Context, tx pgx.Tx) error {
			qtx := sqlc.New(tx)
			if mutateErr := mutate(ctx, qtx); mutateErr != nil {
				return mutateErr
			}
			if _, bumpErr := qtx.BumpChannelCapacityRevision(ctx, sqlc.BumpChannelCapacityRevisionParams{
				NextRevision:    nextRevision,
				ID:              channelID,
				CurrentRevision: channelRow.CapacityRevision,
			}); bumpErr != nil {
				if errors.Is(bumpErr, pgx.ErrNoRows) {
					return conflict("channel capacity changed during publish; retry")
				}
				return storeFailed(bumpErr, "bump channel capacity revision")
			}
			return nil
		},
	})
	return err
}

func (s *Service) requirePoolChannel(ctx context.Context, channelID int64) error {
	if channelID <= 0 {
		return invalidArgument("channel_id", "channel id must be positive")
	}
	row, err := s.queries.GetChannel(ctx, channelID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return invalidArgument("channel_id", "channel not found")
		}
		return storeFailed(err, "load channel")
	}
	if row.SupplyForm != "pool" {
		return invalidArgument("channel_id", "channel is not a subscription pool channel")
	}
	return nil
}

func newControlToken() (string, error) {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", failure.Wrap(failure.CodeConfigInvalid, err, failure.WithMessage("generate runtime-control token"))
	}
	return "acct-" + hex.EncodeToString(buf), nil
}

func accountFromListRow(row sqlc.AdminListSubscriptionAccountsRow) Account {
	account := Account{
		ID: row.ID, ChannelID: row.ChannelID, Platform: row.Platform,
		CredentialType: row.CredentialType, UpstreamAccountID: row.UpstreamAccountID,
		DisplayName: row.DisplayName, Priority: row.Priority, Status: row.Status,
		ConfigRevision: row.ConfigRevision,
		CreatedAt:      row.CreatedAt.Time, UpdatedAt: row.UpdatedAt.Time,
	}
	if has, ok := row.HasRefreshToken.(bool); ok {
		account.HasRefreshToken = has
	}
	if row.PlanType.Valid {
		account.PlanType = row.PlanType.String
	}
	if row.ProxyUrl.Valid {
		account.ProxyURL = row.ProxyUrl.String
	}
	if row.ProxyID.Valid {
		proxyID := row.ProxyID.Int64
		account.ProxyID = &proxyID
	}
	if row.ProxyName.Valid {
		account.ProxyName = row.ProxyName.String
	}
	if row.ProxyStatus.Valid {
		account.ProxyStatus = row.ProxyStatus.String
	}
	if v, ok := row.Email.(string); ok {
		account.Email = v
	}
	if row.ConcurrencyLimit.Valid {
		v := int64(row.ConcurrencyLimit.Int32)
		account.ConcurrencyLimit = &v
	}
	if row.DisabledReason.Valid {
		account.DisabledReason = row.DisabledReason.String
	}
	if row.SubscriptionExpiresAt.Valid {
		t := row.SubscriptionExpiresAt.Time
		account.SubscriptionExpiresAt = &t
	}
	if row.LastSuccessAt.Valid {
		t := row.LastSuccessAt.Time
		account.LastSuccessAt = &t
	}
	if len(row.UsageSnapshot) > 0 {
		account.UsageSnapshot = rawJSON(row.UsageSnapshot)
	}
	if v, ok := row.TokenExpiresAt.(string); ok {
		account.TokenExpiresAt = v
	}
	return account
}

func accountFromRow(row sqlc.SubscriptionAccount) Account {
	creds, credsErr := subscription.DecodeCredentials(row.Credentials)
	account := Account{
		ID: row.ID, ChannelID: row.ChannelID, Platform: row.Platform,
		CredentialType: row.CredentialType, UpstreamAccountID: row.UpstreamAccountID,
		DisplayName: row.DisplayName, Priority: row.Priority, Status: row.Status,
		ConfigRevision: row.ConfigRevision,
		CreatedAt:      row.CreatedAt.Time, UpdatedAt: row.UpdatedAt.Time,
	}
	if credsErr == nil {
		account.HasRefreshToken = creds.RefreshToken != ""
		account.Email = creds.Email
		if !creds.ExpiresAt.IsZero() {
			account.TokenExpiresAt = creds.ExpiresAt.UTC().Format(time.RFC3339)
		}
	}
	if row.PlanType.Valid {
		account.PlanType = row.PlanType.String
	}
	if row.ProxyUrl.Valid {
		account.ProxyURL = row.ProxyUrl.String
	}
	if row.ProxyID.Valid {
		proxyID := row.ProxyID.Int64
		account.ProxyID = &proxyID
	}
	if row.ConcurrencyLimit.Valid {
		v := int64(row.ConcurrencyLimit.Int32)
		account.ConcurrencyLimit = &v
	}
	if row.DisabledReason.Valid {
		account.DisabledReason = row.DisabledReason.String
	}
	if row.SubscriptionExpiresAt.Valid {
		t := row.SubscriptionExpiresAt.Time
		account.SubscriptionExpiresAt = &t
	}
	if row.LastSuccessAt.Valid {
		t := row.LastSuccessAt.Time
		account.LastSuccessAt = &t
	}
	if len(row.UsageSnapshot) > 0 {
		account.UsageSnapshot = rawJSON(row.UsageSnapshot)
	}
	return account
}

func ledgerFromRow(row sqlc.SubscriptionLedgerEntry) LedgerEntry {
	entry := LedgerEntry{
		ID: row.ID, AccountID: row.AccountID, Currency: row.Currency,
		PeriodStart: row.PeriodStart.Time, PeriodEnd: row.PeriodEnd.Time,
		CreatedAt: row.CreatedAt.Time,
	}
	if row.Amount.Valid {
		if v, err := row.Amount.Value(); err == nil {
			if text, ok := v.(string); ok {
				entry.Amount = text
			}
		}
	}
	if row.Note.Valid {
		entry.Note = row.Note.String
	}
	if row.CreatedBy.Valid {
		entry.CreatedBy = row.CreatedBy.String
	}
	return entry
}

// rawJSON 让 jsonb 原样通过（避免二次转义）。
type rawJSON []byte

func (r rawJSON) MarshalJSON() ([]byte, error) { return r, nil }

func optionalText(v string) pgtype.Text {
	v = strings.TrimSpace(v)
	if v == "" {
		return pgtype.Text{}
	}
	return pgtype.Text{String: v, Valid: true}
}

func optionalInt4(v *int64) pgtype.Int4 {
	if v == nil {
		return pgtype.Int4{}
	}
	return pgtype.Int4{Int32: int32(*v), Valid: true}
}

func accountLoadError(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return failure.New(failure.CodeAdminNotFound, failure.WithMessage("subscription account not found"))
	}
	return storeFailed(err, "load subscription account")
}

func invalidArgument(field, message string) error {
	return failure.New(
		failure.CodeAdminInvalidArgument,
		failure.WithMessage(message),
		failure.WithField("field", field),
	)
}

func conflict(message string) error {
	return failure.New(failure.CodeAdminConflict, failure.WithMessage(message))
}

func storeFailed(err error, operation string) error {
	return failure.Wrap(failure.CodeAdminStoreFailed, err, failure.WithMessage(operation))
}
