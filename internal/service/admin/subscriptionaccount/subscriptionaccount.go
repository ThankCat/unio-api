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
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"go.uber.org/zap"

	"github.com/ThankCat/unio-gateway/internal/core/runtimecontrol"
	"github.com/ThankCat/unio-gateway/internal/platform/breakerstore"
	"github.com/ThankCat/unio-gateway/internal/platform/failure"
	"github.com/ThankCat/unio-gateway/internal/platform/store/sqlc"
	channelservice "github.com/ThankCat/unio-gateway/internal/service/admin/channel"
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
	GetAccountByPlatformUpstreamID(ctx context.Context, arg sqlc.GetAccountByPlatformUpstreamIDParams) (sqlc.GetAccountByPlatformUpstreamIDRow, error)
	GetChannel(ctx context.Context, id int64) (sqlc.Channel, error)
	CountSchedulableAccountsByChannel(ctx context.Context, channelID int64) (int64, error)
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

	oauthSessions sync.Map // session id -> oauthSession
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

// ImportFile 解析并导入一个 sub2api-data v1 文件到指定池型渠道。
func (s *Service) ImportFile(ctx context.Context, channelID int64, raw []byte) ([]subscription.ImportResultItem, error) {
	if err := s.requirePoolChannel(ctx, channelID); err != nil {
		return nil, err
	}
	accounts, err := subscription.ParseSub2APIData(raw)
	if err != nil {
		return nil, err
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
	ExpiresAt time.Time
}

// StartOAuth 生成授权链接。proxyURL 是该账号将绑定的出口（换码同样走它）。
func (s *Service) StartOAuth(ctx context.Context, channelID int64, proxyURL string) (sessionID, authorizationURL string, err error) {
	if err := s.requirePoolChannel(ctx, channelID); err != nil {
		return "", "", err
	}
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
		Challenge: challenge, ChannelID: channelID, ProxyURL: proxyURL,
		ExpiresAt: time.Now().Add(15 * time.Minute),
	})
	return sessionID, challenge.AuthorizationURL(""), nil
}

// CompleteOAuth 回填 code 完成导入（落库 disabled）。
func (s *Service) CompleteOAuth(ctx context.Context, sessionID, code, state string) (Account, error) {
	value, ok := s.oauthSessions.LoadAndDelete(sessionID)
	if !ok {
		return Account{}, invalidArgument("session_id", "oauth session not found or already used")
	}
	session := value.(oauthSession)
	if time.Now().After(session.ExpiresAt) {
		return Account{}, invalidArgument("session_id", "oauth session expired")
	}
	imported, err := subscription.CompleteAuthorization(ctx, s.tokens, session.Challenge, code, state, "", session.ProxyURL)
	if err != nil {
		return Account{}, err
	}
	results, err := subscription.ImportAccounts(ctx, importerQueries{s.queries}, session.ChannelID, []subscription.ImportAccount{imported})
	if err != nil {
		return Account{}, err
	}
	if len(results) != 1 || !results[0].Imported {
		reason := "import rejected"
		if len(results) == 1 && results[0].Reason != "" {
			reason = results[0].Reason
		}
		return Account{}, conflict(reason)
	}
	row, err := s.queries.AdminGetSubscriptionAccount(ctx, results[0].AccountID)
	if err != nil {
		return Account{}, storeFailed(err, "load imported account")
	}
	return accountFromRow(row), nil
}

// UpdateConfigInput 是调度参数编辑入参。
type UpdateConfigInput struct {
	AccountID        int64
	DisplayName      string
	ProxyURL         string
	ConcurrencyLimit *int64
	Priority         int32
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

	var updated sqlc.SubscriptionAccount
	err = s.publishAccountChange(ctx, account.ChannelID, func(ctx context.Context, qtx *sqlc.Queries) error {
		row, updateErr := qtx.AdminUpdateSubscriptionAccountConfig(ctx, sqlc.AdminUpdateSubscriptionAccountConfigParams{
			ID:               in.AccountID,
			DisplayName:      strings.TrimSpace(in.DisplayName),
			ProxyUrl:         optionalText(in.ProxyURL),
			ConcurrencyLimit: optionalInt4(in.ConcurrencyLimit),
			Priority:         in.Priority,
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

// SetStatusInput 是状态流转入参。
type SetStatusInput struct {
	AccountID int64
	// Action: enable / disable / archive / restore。
	Action string
	// DisabledReason 仅 disable 有意义（缺省 manual）。
	DisabledReason string
	// ConfirmSupplyImpact 确认「停用/归档最后一个可调度账号」的供给影响（边界 11）。
	ConfirmSupplyImpact bool
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

	// 供给联动（边界 11）：停用最后一个可调度账号会让整条池失去候选资格。
	// 渠道仍 enabled 时要求显式确认，防止「只是想下一个号」把模型供给静默打断。
	if account.Status == "enabled" && nextStatus != "enabled" {
		schedulable, countErr := s.queries.CountSchedulableAccountsByChannel(ctx, account.ChannelID)
		if countErr != nil {
			return Account{}, storeFailed(countErr, "count schedulable accounts")
		}
		if schedulable <= 1 && !in.ConfirmSupplyImpact {
			channelRow, chErr := s.queries.GetChannel(ctx, account.ChannelID)
			if chErr == nil && channelRow.Status == "enabled" {
				return Account{}, failure.New(
					failure.CodeAdminConflict,
					failure.WithMessage("这是该池最后一个可调度账号：停用后渠道将失去全部供给，绑定模型的请求会得到 503。确认请携带 confirm_supply_impact=true 重试"),
					failure.WithField("reason_code", "account_last_supply_confirmation_required"),
				)
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

// RefreshToken 手动触发一次令牌刷新（与后台保活同一条代码路径）。
func (s *Service) RefreshToken(ctx context.Context, accountID int64) (Account, error) {
	if accountID <= 0 {
		return Account{}, invalidArgument("id", "account id must be positive")
	}
	account, err := s.queries.AdminGetSubscriptionAccount(ctx, accountID)
	if err != nil {
		return Account{}, accountLoadError(err)
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
		return Account{}, err
	}
	row, err := s.queries.AdminGetSubscriptionAccount(ctx, accountID)
	if err != nil {
		return Account{}, accountLoadError(err)
	}
	return accountFromRow(row), nil
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
