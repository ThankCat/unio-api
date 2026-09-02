package subscription

import (
	"context"
	"errors"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/redis/go-redis/v9"
	"go.uber.org/zap"
	"golang.org/x/sync/singleflight"

	"github.com/ThankCat/unio-gateway/internal/platform/breakerstore"
	"github.com/ThankCat/unio-gateway/internal/platform/failure"
	"github.com/ThankCat/unio-gateway/internal/platform/logging"
	"github.com/ThankCat/unio-gateway/internal/platform/store/sqlc"
	"github.com/ThankCat/unio-gateway/internal/service/gateway/lifecycle"
)

// freshnessSkew 是请求时兜底刷新的提前量：过期前 5 分钟内视为不新鲜，带锁同步刷一次再出站。
// 长流请求凭据只在 transport 开始时取一次，流建立后不受影响（边界 13）。
const freshnessSkew = 5 * time.Minute

// refreshLockTTL 是跨实例刷新锁的存活时间：覆盖一次令牌端点往返的最坏情况。
const refreshLockTTL = 30 * time.Second

// OutboundQueries 是出站凭据解析所需的最小查询集。
type OutboundQueries interface {
	GetAccountOutboundCredential(ctx context.Context, id int64) (sqlc.GetAccountOutboundCredentialRow, error)
	UpdateAccountTokens(ctx context.Context, arg sqlc.UpdateAccountTokensParams) error
	MarkAccountDisabled(ctx context.Context, arg sqlc.MarkAccountDisabledParams) error
}

// AccountRuntimeControl 是刷新失败时的账号运行态处置能力。
type AccountRuntimeControl interface {
	MarkAccountUnschedulable(ctx context.Context, accountID, durationMs int64, reason breakerstore.AccountUnschedulableReason) (int64, error)
	ClearAccountUnschedulable(ctx context.Context, accountID int64) error
}

// Outbound 实现 lifecycle.AccountOutboundResolver：按 permit 固化的账号 ID 解析出站凭据，
// 不新鲜则带锁同步刷一次再出站（请求时兜底，第六节）。
type Outbound struct {
	queries OutboundQueries
	tokens  *TokenClient
	runtime AccountRuntimeControl
	redis   redis.Cmdable
	logger  *zap.Logger
	group   singleflight.Group
	now     func() time.Time
}

// NewOutbound 创建出站凭据解析器。redis 用于跨实例刷新锁（nil 时退化为进程内 singleflight）。
func NewOutbound(queries OutboundQueries, tokens *TokenClient, runtime AccountRuntimeControl, redisClient redis.Cmdable, logger *zap.Logger) *Outbound {
	if logger == nil {
		logger = zap.NewNop()
	}
	return &Outbound{
		queries: queries, tokens: tokens, runtime: runtime,
		redis: redisClient, logger: logger, now: time.Now,
	}
}

var _ lifecycle.AccountOutboundResolver = (*Outbound)(nil)

// ResolveAccountOutbound 取账号出站凭据；access token 不新鲜时同步刷新（singleflight +
// Redis 锁防多实例重复刷——refresh token 会轮换，并发刷会把对方的新令牌作废）。
func (o *Outbound) ResolveAccountOutbound(ctx context.Context, accountID int64) (lifecycle.AccountOutbound, error) {
	row, err := o.queries.GetAccountOutboundCredential(ctx, accountID)
	if err != nil {
		return lifecycle.AccountOutbound{}, failure.Wrap(
			failure.CodeDependencyPostgresUnavailable, err,
			failure.WithMessage("load subscription account outbound credential"),
		)
	}
	if row.Status != "enabled" {
		// permit 固化后账号被停用：按固化身份收口的是 permit 槽位，但新的出站绝不该带失效凭据。
		return lifecycle.AccountOutbound{}, failure.New(
			failure.CodeRoutingCredentialResolveFailed,
			failure.WithMessage("subscription account is no longer enabled"),
		)
	}
	creds, err := DecodeCredentials(row.Credentials)
	if err != nil {
		return lifecycle.AccountOutbound{}, err
	}
	proxyURL := textOrEmpty(row.ProxyUrl)

	if !creds.FreshFor(freshnessSkew, o.now()) {
		refreshed, refreshErr := o.refreshWithLock(ctx, accountID, proxyURL)
		if refreshErr != nil {
			return lifecycle.AccountOutbound{}, refreshErr
		}
		creds = refreshed
	}

	return lifecycle.AccountOutbound{
		AccessToken:       creds.AccessToken,
		UpstreamAccountID: row.UpstreamAccountID,
		ProxyURL:          proxyURL,
	}, nil
}

// refreshWithLock 同步刷新一个账号的令牌，进程内 singleflight 合并并发请求，
// 跨实例用 Redis SET NX 锁；拿不到锁时短暂等待后重读（对方实例大概率正在刷）。
func (o *Outbound) refreshWithLock(ctx context.Context, accountID int64, proxyURL string) (Credentials, error) {
	value, err, _ := o.group.Do(strconv.FormatInt(accountID, 10), func() (any, error) {
		return o.refreshLocked(ctx, accountID, proxyURL)
	})
	if err != nil {
		return Credentials{}, err
	}
	return value.(Credentials), nil
}

func (o *Outbound) refreshLocked(ctx context.Context, accountID int64, proxyURL string) (Credentials, error) {
	lockKey := "unio:account-refresh:" + strconv.FormatInt(accountID, 10)
	if o.redis != nil {
		acquired, err := o.redis.SetNX(ctx, lockKey, "1", refreshLockTTL).Result()
		if err == nil && !acquired {
			// 别的实例正在刷：等它写回后重读。等待上限取锁 TTL 的一小段。
			deadline := o.now().Add(5 * time.Second)
			for o.now().Before(deadline) {
				time.Sleep(300 * time.Millisecond)
				row, readErr := o.queries.GetAccountOutboundCredential(ctx, accountID)
				if readErr != nil {
					continue
				}
				creds, decodeErr := DecodeCredentials(row.Credentials)
				if decodeErr == nil && creds.FreshFor(freshnessSkew, o.now()) {
					return creds, nil
				}
			}
			return Credentials{}, failure.New(
				failure.CodeRoutingCredentialResolveFailed,
				failure.WithMessage("subscription account token refresh is held by another instance"),
			)
		}
		if err == nil {
			defer o.redis.Del(context.WithoutCancel(ctx), lockKey)
		}
	}

	// 拿到锁后重读：并发方可能已经刷完写回。
	row, err := o.queries.GetAccountOutboundCredential(ctx, accountID)
	if err != nil {
		return Credentials{}, failure.Wrap(
			failure.CodeDependencyPostgresUnavailable, err,
			failure.WithMessage("reload subscription account credential"),
		)
	}
	creds, err := DecodeCredentials(row.Credentials)
	if err != nil {
		return Credentials{}, err
	}
	if creds.FreshFor(freshnessSkew, o.now()) {
		return creds, nil
	}

	return o.RefreshAccount(ctx, accountID, creds, proxyURL)
}

// RefreshAccount 执行一次真实刷新并写回（请求时兜底与后台保活共用）：
//   - 成功：合并令牌（新 refresh token 非空才覆盖）、写回、清除刷新隔离；
//   - 明确拒绝（RefreshRejectedError）：确认吊销，置 disabled(token_revoked)；
//   - 网络失败：临时不可调度给下一轮重试留窗口（不禁用）。
func (o *Outbound) RefreshAccount(ctx context.Context, accountID int64, creds Credentials, proxyURL string) (Credentials, error) {
	result, err := o.tokens.Refresh(ctx, creds, proxyURL)
	if err != nil {
		var rejected *RefreshRejectedError
		if errors.As(err, &rejected) {
			if o.queries != nil {
				if markErr := o.queries.MarkAccountDisabled(ctx, sqlc.MarkAccountDisabledParams{
					ID: accountID, DisabledReason: pgtype.Text{String: "token_revoked", Valid: true},
				}); markErr != nil {
					o.warn(accountID, "mark account disabled failed", markErr)
				}
			}
			logging.Warn(o.logger, "subscription", "refresh", "refresh token confirmed revoked; account disabled",
				zap.Int64("account_id", accountID), zap.Int("status", rejected.StatusCode))
			return Credentials{}, failure.Wrap(
				failure.CodeRoutingCredentialResolveFailed, err,
				failure.WithMessage("subscription account refresh token revoked"),
			)
		}
		if o.runtime != nil {
			if _, markErr := o.runtime.MarkAccountUnschedulable(
				ctx, accountID, (10 * time.Minute).Milliseconds(),
				breakerstore.AccountUnschedulableTokenRefresh,
			); markErr != nil {
				o.warn(accountID, "mark account unschedulable failed", markErr)
			}
		}
		return Credentials{}, failure.Wrap(
			failure.CodeRoutingCredentialResolveFailed, err,
			failure.WithMessage("subscription account token refresh failed"),
		)
	}

	merged := creds.MergeRefreshed(result, o.now())
	raw, err := merged.Encode()
	if err != nil {
		return Credentials{}, err
	}
	if err := o.queries.UpdateAccountTokens(ctx, sqlc.UpdateAccountTokensParams{
		ID: accountID, Credentials: raw,
	}); err != nil {
		return Credentials{}, failure.Wrap(
			failure.CodeDependencyPostgresUnavailable, err,
			failure.WithMessage("persist refreshed subscription account tokens"),
		)
	}
	if o.runtime != nil {
		if clearErr := o.runtime.ClearAccountUnschedulable(ctx, accountID); clearErr != nil {
			o.warn(accountID, "clear account unschedulable failed", clearErr)
		}
	}
	logging.Info(o.logger, "subscription", "refresh", "subscription account token refreshed",
		zap.Int64("account_id", accountID),
		zap.Time("expires_at", merged.ExpiresAt),
	)
	return merged, nil
}

func (o *Outbound) warn(accountID int64, message string, err error) {
	logging.Warn(o.logger, "subscription", "refresh", message,
		zap.Int64("account_id", accountID),
		zap.String("error_message", err.Error()),
	)
}

func textOrEmpty(v pgtype.Text) string {
	if !v.Valid {
		return ""
	}
	return v.String
}
