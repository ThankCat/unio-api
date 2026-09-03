package subscription

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"

	"github.com/ThankCat/unio-gateway/internal/platform/failure"
	"github.com/ThankCat/unio-gateway/internal/platform/store/sqlc"
)

// ProbeIdentity 是管理面主动出站（渠道检测 / 模型发现 / 模型验证）使用的账号身份。
// 池型渠道不持渠道级凭据，这些操作必须像真实请求一样以某个账号的身份出站。
type ProbeIdentity struct {
	AccountID         int64
	DisplayName       string
	AccessToken       string
	UpstreamAccountID string
	ProxyURL          string
}

// ProbeIdentityQueries 是解析所需的最小存储能力（sqlc.Queries 的子集）。
type ProbeIdentityQueries interface {
	AdminPickProbeAccount(ctx context.Context, channelID int64) (int64, error)
	AdminGetSubscriptionAccount(ctx context.Context, id int64) (sqlc.SubscriptionAccount, error)
}

// ProbeIdentityResolver 为池型渠道的管理面出站解析账号身份。
//
// 选号规则与真实调度同向：未指定账号时取 enabled 中 priority 最小者（同档按 ID 稳定），
// 使「检测通过」尽可能预言「真实请求也会通过」。指定账号时允许 disabled——
// 「先测后启用」是导入账号后的标准动作，不能强迫管理员先启用坏号才能测它。
type ProbeIdentityResolver struct {
	queries  ProbeIdentityQueries
	outbound *Outbound
}

// NewProbeIdentityResolver 创建解析器。outbound 可为 nil（测试）；
// 非 nil 时 enabled 账号经它解析（不新鲜自动刷新，与真实出站同一条链路）。
func NewProbeIdentityResolver(queries ProbeIdentityQueries, outbound *Outbound) *ProbeIdentityResolver {
	return &ProbeIdentityResolver{queries: queries, outbound: outbound}
}

// ResolveProbeIdentity 解析检测身份。accountID=0 表示自动选号。
func (r *ProbeIdentityResolver) ResolveProbeIdentity(ctx context.Context, channelID, accountID int64) (ProbeIdentity, error) {
	if accountID == 0 {
		picked, err := r.queries.AdminPickProbeAccount(ctx, channelID)
		if errors.Is(err, pgx.ErrNoRows) {
			return ProbeIdentity{}, failure.New(
				failure.CodeAdminConflict,
				failure.WithMessage("该池当前没有可调度（enabled）账号，无法以账号身份出站；先启用一个账号，或指定 account_id 检测停用中的账号"),
			)
		}
		if err != nil {
			return ProbeIdentity{}, failure.Wrap(
				failure.CodeAdminStoreFailed, err,
				failure.WithMessage("pick probe account"),
			)
		}
		accountID = picked
	}

	account, err := r.queries.AdminGetSubscriptionAccount(ctx, accountID)
	if errors.Is(err, pgx.ErrNoRows) {
		return ProbeIdentity{}, failure.New(
			failure.CodeAdminNotFound,
			failure.WithMessage("account not found"),
		)
	}
	if err != nil {
		return ProbeIdentity{}, failure.Wrap(
			failure.CodeAdminStoreFailed, err,
			failure.WithMessage("load probe account"),
		)
	}
	if account.ChannelID != channelID {
		return ProbeIdentity{}, failure.New(
			failure.CodeAdminInvalidArgument,
			failure.WithMessage("账号不属于该渠道"),
		)
	}
	if account.Status == "archived" {
		return ProbeIdentity{}, failure.New(
			failure.CodeAdminConflict,
			failure.WithMessage("归档账号不可用于检测；先恢复为停用"),
		)
	}

	identity := ProbeIdentity{
		AccountID:         account.ID,
		DisplayName:       account.DisplayName,
		UpstreamAccountID: account.UpstreamAccountID,
	}
	if account.ProxyUrl.Valid {
		identity.ProxyURL = account.ProxyUrl.String
	}

	// enabled 账号走真实出站解析（不新鲜带锁刷新），检测结果 = 真实调用行为；
	// disabled 账号用存量令牌直发——诊断口径，就是要把「令牌已坏」这样的症状暴露给检测结果。
	if account.Status == "enabled" && r.outbound != nil {
		out, resolveErr := r.outbound.ResolveAccountOutbound(ctx, account.ID)
		if resolveErr == nil {
			identity.AccessToken = out.AccessToken
			if out.UpstreamAccountID != "" {
				identity.UpstreamAccountID = out.UpstreamAccountID
			}
			identity.ProxyURL = out.ProxyURL
			return identity, nil
		}
		// 解析失败（如刷新被拒）不终止检测：退回存量令牌直发，让探测把真实症状带回来。
	}

	creds, err := DecodeCredentials(account.Credentials)
	if err != nil {
		return ProbeIdentity{}, err
	}
	identity.AccessToken = creds.AccessToken
	return identity, nil
}
