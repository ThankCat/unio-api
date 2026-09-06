package quota

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"go.uber.org/zap"

	"github.com/ThankCat/unio-gateway/internal/core/adapter"
	"github.com/ThankCat/unio-gateway/internal/platform/failure"
	"github.com/ThankCat/unio-gateway/internal/platform/logging"
	"github.com/ThankCat/unio-gateway/internal/platform/store/sqlc"
	"github.com/ThankCat/unio-gateway/internal/service/subscription"
)

// Queries 是用量面落库所需的最小查询集。
type Queries interface {
	AdminGetSubscriptionAccount(ctx context.Context, id int64) (sqlc.SubscriptionAccount, error)
	UpdateAccountResetCreditsSnapshot(ctx context.Context, arg sqlc.UpdateAccountResetCreditsSnapshotParams) error
	UpdateAccountProfileSnapshot(ctx context.Context, arg sqlc.UpdateAccountProfileSnapshotParams) error
	UpdateAccountSubscriptionFacts(ctx context.Context, arg sqlc.UpdateAccountSubscriptionFactsParams) error
}

// IdentityResolver 解析账号的出站身份（生产实现 *subscription.ProbeIdentityResolver）。
type IdentityResolver interface {
	ResolveAccountIdentity(ctx context.Context, accountID int64) (subscription.ProbeIdentity, error)
}

// Upstream 是用量面 + 账号画像的上游能力（生产实现 *Client；单测用假实现）。
type Upstream interface {
	FetchUsage(ctx context.Context, identity Identity) (Usage, error)
	FetchResetCredits(ctx context.Context, identity Identity) (ResetCredits, error)
	ConsumeResetCredit(ctx context.Context, identity Identity, creditID, redeemRequestID string) (ConsumeResult, error)
	FetchAccountCheck(ctx context.Context, identity Identity) (AccountCheck, error)
	FetchMe(ctx context.Context, identity Identity) (Me, error)
}

// UsageObserver 消费主动查到的用量：落 usage_snapshot 并按生效阈值评估暂停/恢复
// （生产实现 subscription/health.Recorder，与请求路径的观测同一条链路）。
type UsageObserver interface {
	RecordAccountUsageObservation(ctx context.Context, accountID int64, usage *adapter.AccountUsageFacts)
}

// CreditSnapshot 是落库/展示用的一张卡（只留到期与发放时刻，不留卡 id）。
type CreditSnapshot struct {
	GrantedAt time.Time `json:"granted_at,omitzero"`
	ExpiresAt time.Time `json:"expires_at"`
	Title     string    `json:"title,omitempty"`
}

// CreditsSnapshot 是 subscription_accounts.reset_credits_snapshot 的持久化形态。
type CreditsSnapshot struct {
	// AvailableCount 是账号持有的可用卡数。
	AvailableCount int              `json:"available_count"`
	Credits        []CreditSnapshot `json:"credits"`
	FetchedAt      time.Time        `json:"fetched_at"`
}

// ParseCreditsSnapshot 解析快照列；空值或损坏返回 ok=false。
func ParseCreditsSnapshot(raw []byte) (CreditsSnapshot, bool) {
	if len(raw) == 0 {
		return CreditsSnapshot{}, false
	}
	var snapshot CreditsSnapshot
	if err := json.Unmarshal(raw, &snapshot); err != nil {
		return CreditsSnapshot{}, false
	}
	return snapshot, true
}

// Report 是一次主动查用量的结果。usable 只在服务内流转（含卡 id），不进 JSON。
type Report struct {
	Usage     Usage           `json:"usage"`
	Credits   CreditsSnapshot `json:"credits"`
	FetchedAt time.Time       `json:"fetched_at"`
	// CreditsError 非空表示卡明细端点失败：用量已刷新，卡数退回 /wham/usage 的计数，明细为空。
	CreditsError string `json:"credits_error,omitempty"`

	usable []ResetCredit
}

// UsableCredits 返回可用于消费的卡（含 id），按到期升序。
func (r Report) UsableCredits() []ResetCredit { return r.usable }

// ResetOutcome 是一次手动消费重置卡的结果。
type ResetOutcome struct {
	Result ConsumeResult `json:"result"`
	// Report 是消费后重新查到的用量与卡数（nil 表示回读失败，见 RefreshError）。
	Report       *Report `json:"report,omitempty"`
	RefreshError string  `json:"refresh_error,omitempty"`
}

// Service 编排主动查用量、手动消费重置卡，以及供自动用卡 worker 复用的定向消费。
type Service struct {
	queries  Queries
	identity IdentityResolver
	upstream Upstream
	observer UsageObserver
	logger   *zap.Logger
	now      func() time.Time
}

// NewService 创建用量面服务。observer 可为 nil（不回写用量观测，仅测试）。
func NewService(queries Queries, identity IdentityResolver, upstream Upstream, observer UsageObserver, logger *zap.Logger) *Service {
	if logger == nil {
		logger = zap.NewNop()
	}
	return &Service{queries: queries, identity: identity, upstream: upstream, observer: observer, logger: logger, now: time.Now}
}

// QueryUsage 主动查一次账号用量：窗口水位经 UsageObserver 落快照并评估暂停/恢复（与请求路径同一口径），
// 重置卡计数与到期明细写入 reset_credits_snapshot。卡明细端点失败不阻断用量刷新。
func (s *Service) QueryUsage(ctx context.Context, accountID int64) (Report, error) {
	identity, err := s.resolveIdentity(ctx, accountID)
	if err != nil {
		return Report{}, err
	}
	return s.queryUsage(ctx, accountID, identity)
}

// RefreshReport 是「刷新状态」的结果：用量 + 重置卡 + 账号画像。
type RefreshReport struct {
	Report
	Profile Profile `json:"profile"`
}

// Refresh 刷新整个账号的上游状态：用量与重置卡（同 QueryUsage），再拉 accounts/check 与 me 组成画像落库；
// entitlement 里的套餐与到期回写 plan_type / subscription_expires_at（上游权威值取代手工录入）。
// 画像两个端点任一失败不阻断：错误记进 Profile.Errors，能拿到的分项照常落库。
func (s *Service) Refresh(ctx context.Context, accountID int64) (RefreshReport, error) {
	identity, err := s.resolveIdentity(ctx, accountID)
	if err != nil {
		return RefreshReport{}, err
	}
	report, err := s.queryUsage(ctx, accountID, identity)
	if err != nil {
		return RefreshReport{}, err
	}
	return RefreshReport{Report: report, Profile: s.refreshProfile(ctx, accountID, identity, &report.Usage)}, nil
}

// refreshProfile 拉取并落库账号画像，同时回写订阅事实。
func (s *Service) refreshProfile(ctx context.Context, accountID int64, identity Identity, usage *Usage) Profile {
	now := s.now()
	errs := map[string]string{}
	var check *AccountCheck
	if fetched, err := s.upstream.FetchAccountCheck(ctx, identity); err != nil {
		errs["accounts_check"] = describeUpstreamError(err)
		logging.Warn(s.logger, "runtime", "account", "codex accounts check failed",
			zap.Int64("account_id", accountID), zap.String("error_message", err.Error()))
	} else {
		check = &fetched
	}
	var me *Me
	if fetched, err := s.upstream.FetchMe(ctx, identity); err != nil {
		errs["me"] = describeUpstreamError(err)
		logging.Warn(s.logger, "runtime", "account", "codex me failed",
			zap.Int64("account_id", accountID), zap.String("error_message", err.Error()))
	} else {
		me = &fetched
	}
	profile := profileFromUpstream(check, me, usage, now, errs)
	if raw, marshalErr := json.Marshal(profile); marshalErr == nil {
		if err := s.queries.UpdateAccountProfileSnapshot(ctx, sqlc.UpdateAccountProfileSnapshotParams{
			ID: accountID, AccountProfile: raw,
		}); err != nil {
			logging.Warn(s.logger, "runtime", "account", "persist account profile failed",
				zap.Int64("account_id", accountID), zap.String("error_message", err.Error()))
		}
	}
	if check != nil {
		params := sqlc.UpdateAccountSubscriptionFactsParams{ID: accountID}
		if plan := strings.TrimSpace(check.PlanType); plan != "" {
			params.PlanType = pgtype.Text{String: plan, Valid: true}
		}
		if check.Entitlement != nil && !check.Entitlement.ExpiresAt.IsZero() {
			params.SubscriptionExpiresAt = pgtype.Timestamptz{Time: check.Entitlement.ExpiresAt, Valid: true}
		}
		if params.PlanType.Valid || params.SubscriptionExpiresAt.Valid {
			if err := s.queries.UpdateAccountSubscriptionFacts(ctx, params); err != nil {
				logging.Warn(s.logger, "runtime", "account", "persist subscription facts failed",
					zap.Int64("account_id", accountID), zap.String("error_message", err.Error()))
			}
		}
	}
	return profile
}

// RefreshMany 在后台按有限并发刷新一批账号（导入后首次拉取）：每个账号独立超时，失败只记日志。
// 调用方传入的 ctx 只用于取消整批；上游调用用不受请求生命周期影响的派生 ctx。
func (s *Service) RefreshMany(ctx context.Context, accountIDs []int64, concurrency int) {
	if len(accountIDs) == 0 {
		return
	}
	if concurrency <= 0 {
		concurrency = 2
	}
	base := context.WithoutCancel(ctx)
	sem := make(chan struct{}, concurrency)
	var wg sync.WaitGroup
	for _, accountID := range accountIDs {
		wg.Add(1)
		sem <- struct{}{}
		go func(id int64) {
			defer wg.Done()
			defer func() { <-sem }()
			callCtx, cancel := context.WithTimeout(base, refreshManyPerAccountTimeout)
			defer cancel()
			if _, err := s.Refresh(callCtx, id); err != nil {
				logging.Warn(s.logger, "runtime", "account", "initial account refresh failed",
					zap.Int64("account_id", id), zap.String("error_message", err.Error()))
			}
		}(accountID)
	}
	wg.Wait()
}

// refreshManyPerAccountTimeout 是批量刷新时单账号（三次上游调用）的上限。
const refreshManyPerAccountTimeout = 60 * time.Second

// describeUpstreamError 把上游错误压成画像里可展示的短句。
func describeUpstreamError(err error) string {
	var upstream *UpstreamError
	if errors.As(err, &upstream) {
		return fmt.Sprintf("upstream %d: %s", upstream.StatusCode, truncate(upstream.Body, 120))
	}
	return truncate(err.Error(), 200)
}

// queryUsage 是 QueryUsage 去掉身份解析后的主体，供 Refresh 复用同一身份。
func (s *Service) queryUsage(ctx context.Context, accountID int64, identity Identity) (Report, error) {
	usage, err := s.upstream.FetchUsage(ctx, identity)
	if err != nil {
		return Report{}, s.upstreamFailure(accountID, "query usage", err)
	}
	now := s.now()
	if s.observer != nil {
		if facts := usageFacts(usage, now); facts != nil {
			s.observer.RecordAccountUsageObservation(ctx, accountID, facts)
		}
	}

	report := Report{Usage: usage, FetchedAt: now}
	credits, creditsErr := s.upstream.FetchResetCredits(ctx, identity)
	if creditsErr != nil {
		report.CreditsError = creditsErr.Error()
		logging.Warn(s.logger, "runtime", "account", "codex reset credits fetch failed",
			zap.Int64("account_id", accountID), zap.String("error_message", creditsErr.Error()))
	} else {
		report.usable = credits.UsableCredits()
	}
	report.Credits = buildCreditsSnapshot(usage, credits, creditsErr == nil, now)
	if raw, marshalErr := json.Marshal(report.Credits); marshalErr == nil {
		if err := s.queries.UpdateAccountResetCreditsSnapshot(ctx, sqlc.UpdateAccountResetCreditsSnapshotParams{
			ID: accountID, ResetCreditsSnapshot: raw,
		}); err != nil {
			logging.Warn(s.logger, "runtime", "account", "persist reset credits snapshot failed",
				zap.Int64("account_id", accountID), zap.String("error_message", err.Error()))
		}
	}
	return report, nil
}

// ResetCredit 手动消费一张重置卡（由上游挑卡），随后重新查用量：新水位经观测链路落库并解除用量暂停。
// 消费成功而回读失败时不报错：卡已经用掉了，只在 RefreshError 里说明「列表可能还没更新」。
func (s *Service) ResetCredit(ctx context.Context, accountID int64) (ResetOutcome, error) {
	identity, err := s.resolveIdentity(ctx, accountID)
	if err != nil {
		return ResetOutcome{}, err
	}
	result, err := s.upstream.ConsumeResetCredit(ctx, identity, "", uuid.NewString())
	if err != nil {
		return ResetOutcome{}, s.upstreamFailure(accountID, "consume reset credit", err)
	}
	logging.Info(s.logger, "runtime", "account", "codex reset credit consumed",
		zap.Int64("account_id", accountID),
		zap.String("code", result.Code),
		zap.Int("windows_reset", result.WindowsReset),
		zap.String("trigger", "manual"),
	)
	return s.afterConsume(ctx, accountID, result), nil
}

// ConsumeTargeted 定向消费指定卡（自动用卡用）：redeemRequestID 由调用方派生并在重试时复用，
// 保证上游幂等；不做回读，调用方按自己的状态机决定后续。
func (s *Service) ConsumeTargeted(ctx context.Context, accountID int64, creditID, redeemRequestID string) (ConsumeResult, error) {
	identity, err := s.resolveIdentity(ctx, accountID)
	if err != nil {
		return ConsumeResult{}, err
	}
	result, err := s.upstream.ConsumeResetCredit(ctx, identity, creditID, redeemRequestID)
	if err != nil {
		return ConsumeResult{}, s.upstreamFailure(accountID, "consume reset credit", err)
	}
	logging.Info(s.logger, "runtime", "account", "codex reset credit consumed",
		zap.Int64("account_id", accountID),
		zap.String("code", result.Code),
		zap.Int("windows_reset", result.WindowsReset),
		zap.String("trigger", "auto"),
	)
	return result, nil
}

// afterConsume 消费后回读：刷新用量（观测链路会按新水位解除暂停）与卡数。
func (s *Service) afterConsume(ctx context.Context, accountID int64, result ConsumeResult) ResetOutcome {
	outcome := ResetOutcome{Result: result}
	report, err := s.QueryUsage(ctx, accountID)
	if err != nil {
		outcome.RefreshError = err.Error()
		return outcome
	}
	outcome.Report = &report
	return outcome
}

func (s *Service) resolveIdentity(ctx context.Context, accountID int64) (Identity, error) {
	if accountID <= 0 {
		return Identity{}, failure.New(failure.CodeAdminInvalidArgument, failure.WithMessage("account id must be positive"))
	}
	account, err := s.queries.AdminGetSubscriptionAccount(ctx, accountID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Identity{}, failure.New(failure.CodeAdminNotFound, failure.WithMessage("subscription account not found"))
		}
		return Identity{}, failure.Wrap(failure.CodeAdminStoreFailed, err, failure.WithMessage("load subscription account"))
	}
	if account.Platform != "openai" {
		return Identity{}, failure.New(failure.CodeAdminInvalidArgument, failure.WithMessage("只有 OpenAI/Codex 订阅账号支持主动查用量与重置卡"))
	}
	probe, err := s.identity.ResolveAccountIdentity(ctx, accountID)
	if err != nil {
		return Identity{}, err
	}
	return Identity{AccessToken: probe.AccessToken, UpstreamAccountID: probe.UpstreamAccountID, ProxyURL: probe.ProxyURL}, nil
}

// upstreamFailure 把上游失败翻成 admin 可读错误：非 2xx 带状态码与截断正文（502），网络错误同样归 502；
// 其它（配置/编码）原样返回。
func (s *Service) upstreamFailure(accountID int64, operation string, err error) error {
	var upstream *UpstreamError
	if errors.As(err, &upstream) {
		logging.Warn(s.logger, "runtime", "account", "codex "+operation+" rejected",
			zap.Int64("account_id", accountID),
			zap.Int("status", upstream.StatusCode),
			zap.String("body", upstream.Body),
		)
		message := fmt.Sprintf("上游返回 %d", upstream.StatusCode)
		if body := strings.TrimSpace(upstream.Body); body != "" {
			message += ": " + body
		}
		return failure.Wrap(
			failure.CodeAdminUpstreamUnavailable, err,
			failure.WithMessage(message),
			failure.WithField("upstream_status", upstream.StatusCode),
		)
	}
	if failure.CodeOf(err) == failure.CodeAdapterSendRequestFailed {
		return failure.Wrap(failure.CodeAdminUpstreamUnavailable, err, failure.WithMessage("上游不可达或响应异常，可稍后重试"))
	}
	return err
}

// usageFacts 把 /wham/usage 的窗口转成观测事实（与响应头采集同一结构）；无窗口返回 nil。
func usageFacts(usage Usage, now time.Time) *adapter.AccountUsageFacts {
	if usage.RateLimit == nil {
		return nil
	}
	facts := &adapter.AccountUsageFacts{PlanType: usage.PlanType}
	facts.Primary = windowFacts(usage.RateLimit.PrimaryWindow, now)
	facts.Secondary = windowFacts(usage.RateLimit.SecondaryWindow, now)
	if !facts.Primary.Present && !facts.Secondary.Present {
		return nil
	}
	return facts
}

func windowFacts(window *Window, now time.Time) adapter.AccountUsageWindowFacts {
	if window == nil {
		return adapter.AccountUsageWindowFacts{}
	}
	facts := adapter.AccountUsageWindowFacts{
		Present:           true,
		UsedPercent:       window.UsedPercent,
		WindowMinutes:     window.LimitWindowSeconds / 60,
		ResetAtUnix:       window.ResetAt,
		ResetAfterSeconds: window.ResetAfterSeconds,
	}
	if facts.ResetAtUnix <= 0 && facts.ResetAfterSeconds > 0 {
		facts.ResetAtUnix = now.Unix() + facts.ResetAfterSeconds
	}
	return facts
}

// buildCreditsSnapshot 合并两个端点的卡信息：计数优先取 /wham/usage，明细取卡端点。
func buildCreditsSnapshot(usage Usage, credits ResetCredits, creditsOK bool, now time.Time) CreditsSnapshot {
	snapshot := CreditsSnapshot{FetchedAt: now.UTC(), Credits: []CreditSnapshot{}}
	if usage.ResetCreditCounts != nil {
		snapshot.AvailableCount = usage.ResetCreditCounts.AvailableCount
	} else if creditsOK {
		snapshot.AvailableCount = credits.AvailableCount
	}
	if creditsOK {
		for _, credit := range credits.UsableCredits() {
			snapshot.Credits = append(snapshot.Credits, CreditSnapshot{
				GrantedAt: credit.GrantedAt, ExpiresAt: credit.ExpiresAt, Title: credit.Title,
			})
		}
		if usage.ResetCreditCounts == nil {
			snapshot.AvailableCount = len(snapshot.Credits)
		}
	}
	return snapshot
}
