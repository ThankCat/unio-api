package quota

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"
)

// 账号画像：ChatGPT 后端两个只读接口给出账号本身（而非用量）的状态。
//   - GET /backend-api/accounts/check/v4-2023-04-27：按 account_id 列出账号的套餐、订阅 entitlement、停用/欠费标记、
//     账号结构（personal / workspace）等；需要 Origin/Referer 头（ChatGPT 网页同源），Codex OAuth token 直接可用；
//   - GET /backend-api/me：用户画像（MFA、注册时间、国家/地区、组织与角色）。
//
// 形状以 2026-09-06 真实响应为准（dev 账号，脱敏样例见 sandbox/codex/wire/samples/upstream-accounts-check.json 与 upstream-me.json）。

const (
	accountsCheckURL = "https://chatgpt.com/backend-api/accounts/check/v4-2023-04-27"
	meURL            = "https://chatgpt.com/backend-api/me"
)

// AccountCheck 是 accounts/check 里与本账号（chatgpt-account-id）对应的那条记录的最小解码。
type AccountCheck struct {
	AccountID                     string
	PlanType                      string
	PlanDisplayName               string
	Structure                     string
	WorkspaceType                 string
	IsDeactivated                 bool
	EligibleForReactivation       bool
	HasPreviouslyPaidSubscription bool
	CreatedTime                   time.Time
	ComputeResidency              string
	Entitlement                   *Entitlement
	FeatureCount                  int
}

// Entitlement 是订阅授权：有效性、计划名、到期/续订/取消时刻、计费周期、欠费与宽限期、促销折扣。
type Entitlement struct {
	SubscriptionID        string
	HasActiveSubscription bool
	IsGratis              bool
	SubscriptionPlan      string
	ExpiresAt             time.Time
	RenewsAt              time.Time
	CancelsAt             time.Time
	BillingPeriod         string
	BillingCurrency       string
	IsDelinquent          bool
	GracePeriodEnd        time.Time
	PromoCampaignID       string
	DiscountPercent       float64
	DiscountExpiresAt     time.Time
}

// Me 是 /backend-api/me 的最小解码：不取手机号、头像等与运维无关的字段。
type Me struct {
	Email           string
	Name            string
	MFAEnabled      bool
	Created         time.Time
	Country         string
	Region          string
	EmailDomainType string
	Orgs            []MeOrg
}

// MeOrg 是用户所属组织（Codex 订阅账号通常只有一个 personal org）。
type MeOrg struct {
	Title     string
	Personal  bool
	IsDefault bool
	Role      string
	Banned    bool
}

type accountsCheckPayload struct {
	Accounts map[string]struct {
		Account struct {
			AccountID                     string  `json:"account_id"`
			PlanType                      string  `json:"plan_type"`
			PlanDisplayName               string  `json:"plan_display_name"`
			Structure                     string  `json:"structure"`
			WorkspaceType                 *string `json:"workspace_type"`
			IsDeactivated                 bool    `json:"is_deactivated"`
			EligibleForReactivation       bool    `json:"eligible_for_reactivation"`
			HasPreviouslyPaidSubscription bool    `json:"has_previously_paid_subscription"`
			CreatedTime                   string  `json:"created_time"`
			ComputeResidency              string  `json:"account_compute_residency"`
		} `json:"account"`
		Features    []string `json:"features"`
		Entitlement *struct {
			SubscriptionID             string  `json:"subscription_id"`
			HasActiveSubscription      bool    `json:"has_active_subscription"`
			IsActiveSubscriptionGratis bool    `json:"is_active_subscription_gratis"`
			SubscriptionPlan           string  `json:"subscription_plan"`
			ExpiresAt                  *string `json:"expires_at"`
			RenewsAt                   *string `json:"renews_at"`
			CancelsAt                  *string `json:"cancels_at"`
			BillingPeriod              string  `json:"billing_period"`
			BillingCurrency            string  `json:"billing_currency"`
			IsDelinquent               bool    `json:"is_delinquent"`
			GracePeriodEndTimestamp    *string `json:"grace_period_end_timestamp"`
			Discount                   *struct {
				DiscountType      string  `json:"discount_type"`
				Amount            float64 `json:"amount"`
				DiscountExpiresAt *string `json:"discount_expires_at"`
				PromoCampaignID   string  `json:"promo_campaign_id"`
			} `json:"discount"`
		} `json:"entitlement"`
	} `json:"accounts"`
}

// FetchAccountCheck 读取 accounts/check 并挑出 identity.UpstreamAccountID 对应的账号；
// 没有精确匹配时回落到列表里唯一的一条（个人账号只有一条），仍找不到按上游错误处理。
func (c *Client) FetchAccountCheck(ctx context.Context, identity Identity) (AccountCheck, error) {
	var payload accountsCheckPayload
	extra := http.Header{}
	extra.Set("Origin", "https://chatgpt.com")
	extra.Set("Referer", "https://chatgpt.com/")
	if err := c.doWithHeaders(ctx, identity, http.MethodGet, c.checkURL, nil, "accounts check", &payload, extra); err != nil {
		return AccountCheck{}, err
	}
	key := strings.TrimSpace(identity.UpstreamAccountID)
	entry, ok := payload.Accounts[key]
	if !ok {
		if len(payload.Accounts) != 1 {
			return AccountCheck{}, &UpstreamError{Operation: "accounts check", StatusCode: http.StatusOK, Body: "account not present in accounts/check response"}
		}
		for k, v := range payload.Accounts {
			key, entry = k, v
		}
	}
	check := AccountCheck{
		AccountID:                     firstNonEmpty(entry.Account.AccountID, key),
		PlanType:                      entry.Account.PlanType,
		PlanDisplayName:               entry.Account.PlanDisplayName,
		Structure:                     entry.Account.Structure,
		IsDeactivated:                 entry.Account.IsDeactivated,
		EligibleForReactivation:       entry.Account.EligibleForReactivation,
		HasPreviouslyPaidSubscription: entry.Account.HasPreviouslyPaidSubscription,
		CreatedTime:                   parseUpstreamTime(entry.Account.CreatedTime),
		ComputeResidency:              entry.Account.ComputeResidency,
		FeatureCount:                  len(entry.Features),
	}
	if entry.Account.WorkspaceType != nil {
		check.WorkspaceType = *entry.Account.WorkspaceType
	}
	if entry.Entitlement != nil {
		e := entry.Entitlement
		entitlement := &Entitlement{
			SubscriptionID:        e.SubscriptionID,
			HasActiveSubscription: e.HasActiveSubscription,
			IsGratis:              e.IsActiveSubscriptionGratis,
			SubscriptionPlan:      e.SubscriptionPlan,
			ExpiresAt:             parseUpstreamTime(deref(e.ExpiresAt)),
			RenewsAt:              parseUpstreamTime(deref(e.RenewsAt)),
			CancelsAt:             parseUpstreamTime(deref(e.CancelsAt)),
			BillingPeriod:         e.BillingPeriod,
			BillingCurrency:       e.BillingCurrency,
			IsDelinquent:          e.IsDelinquent,
			GracePeriodEnd:        parseUpstreamTime(deref(e.GracePeriodEndTimestamp)),
		}
		if e.Discount != nil {
			entitlement.PromoCampaignID = e.Discount.PromoCampaignID
			if strings.EqualFold(e.Discount.DiscountType, "percentage") {
				entitlement.DiscountPercent = e.Discount.Amount
			}
			entitlement.DiscountExpiresAt = parseUpstreamTime(deref(e.Discount.DiscountExpiresAt))
		}
		check.Entitlement = entitlement
	}
	return check, nil
}

type mePayload struct {
	Email           string `json:"email"`
	Name            string `json:"name"`
	Created         int64  `json:"created"`
	MFAFlagEnabled  bool   `json:"mfa_flag_enabled"`
	EmailDomainType string `json:"email_domain_type"`
	Country         string `json:"country"`
	Region          string `json:"region"`
	Orgs            struct {
		Data []struct {
			Title     string `json:"title"`
			Personal  bool   `json:"personal"`
			IsDefault bool   `json:"is_default"`
			Role      string `json:"role"`
			Banned    *bool  `json:"banned"`
		} `json:"data"`
	} `json:"orgs"`
}

// FetchMe 读取用户画像。
func (c *Client) FetchMe(ctx context.Context, identity Identity) (Me, error) {
	var payload mePayload
	if err := c.do(ctx, identity, http.MethodGet, c.meURL, nil, "me", &payload); err != nil {
		return Me{}, err
	}
	me := Me{
		Email: payload.Email, Name: payload.Name, MFAEnabled: payload.MFAFlagEnabled,
		Country: payload.Country, Region: payload.Region, EmailDomainType: payload.EmailDomainType,
	}
	if payload.Created > 0 {
		me.Created = time.Unix(payload.Created, 0).UTC()
	}
	for _, org := range payload.Orgs.Data {
		me.Orgs = append(me.Orgs, MeOrg{
			Title: org.Title, Personal: org.Personal, IsDefault: org.IsDefault, Role: org.Role,
			Banned: org.Banned != nil && *org.Banned,
		})
	}
	return me, nil
}

// Profile 是 subscription_accounts.account_profile 的持久化形态（脱敏：不存手机号、头像、上游用户 id）。
type Profile struct {
	FetchedAt time.Time `json:"fetched_at"`
	// Errors 记录分项失败（键：accounts_check / me / usage）；成功的分项照常填充，展示端按项降级。
	Errors map[string]string `json:"errors,omitempty"`

	Account      *ProfileAccount      `json:"account,omitempty"`
	Subscription *ProfileSubscription `json:"subscription,omitempty"`
	User         *ProfileUser         `json:"user,omitempty"`
	Credits      *ProfileCredits      `json:"credits,omitempty"`
}

// ProfileAccount 是账号本身的状态。
type ProfileAccount struct {
	PlanType                      string    `json:"plan_type,omitempty"`
	PlanDisplayName               string    `json:"plan_display_name,omitempty"`
	Structure                     string    `json:"structure,omitempty"`
	WorkspaceType                 string    `json:"workspace_type,omitempty"`
	IsDeactivated                 bool      `json:"is_deactivated"`
	EligibleForReactivation       bool      `json:"eligible_for_reactivation"`
	HasPreviouslyPaidSubscription bool      `json:"has_previously_paid_subscription"`
	CreatedTime                   time.Time `json:"created_time,omitzero"`
	ComputeResidency              string    `json:"compute_residency,omitempty"`
	FeatureCount                  int       `json:"feature_count"`
}

// ProfileSubscription 是订阅授权（entitlement）。
type ProfileSubscription struct {
	HasActiveSubscription bool      `json:"has_active_subscription"`
	IsGratis              bool      `json:"is_gratis"`
	Plan                  string    `json:"plan,omitempty"`
	ExpiresAt             time.Time `json:"expires_at,omitzero"`
	RenewsAt              time.Time `json:"renews_at,omitzero"`
	CancelsAt             time.Time `json:"cancels_at,omitzero"`
	BillingPeriod         string    `json:"billing_period,omitempty"`
	BillingCurrency       string    `json:"billing_currency,omitempty"`
	IsDelinquent          bool      `json:"is_delinquent"`
	GracePeriodEnd        time.Time `json:"grace_period_end,omitzero"`
	PromoCampaignID       string    `json:"promo_campaign_id,omitempty"`
	DiscountPercent       float64   `json:"discount_percent,omitempty"`
	DiscountExpiresAt     time.Time `json:"discount_expires_at,omitzero"`
}

// ProfileUser 是用户画像。
type ProfileUser struct {
	Email           string       `json:"email,omitempty"`
	Name            string       `json:"name,omitempty"`
	MFAEnabled      bool         `json:"mfa_enabled"`
	Created         time.Time    `json:"created,omitzero"`
	Country         string       `json:"country,omitempty"`
	Region          string       `json:"region,omitempty"`
	EmailDomainType string       `json:"email_domain_type,omitempty"`
	Orgs            []ProfileOrg `json:"orgs,omitempty"`
}

// ProfileOrg 是所属组织。
type ProfileOrg struct {
	Title     string `json:"title,omitempty"`
	Personal  bool   `json:"personal"`
	IsDefault bool   `json:"is_default"`
	Role      string `json:"role,omitempty"`
	Banned    bool   `json:"banned"`
}

// ProfileCredits 是 /wham/usage 里的按量付费 credits 余额（有 credits 时某些模型即便窗口打满也能用）。
type ProfileCredits struct {
	HasCredits          bool   `json:"has_credits"`
	Unlimited           bool   `json:"unlimited"`
	OverageLimitReached bool   `json:"overage_limit_reached"`
	Balance             string `json:"balance,omitempty"`
}

// ParseProfile 解析画像列；空值或损坏返回 ok=false。
func ParseProfile(raw []byte) (Profile, bool) {
	if len(raw) == 0 {
		return Profile{}, false
	}
	var profile Profile
	if err := json.Unmarshal(raw, &profile); err != nil || profile.FetchedAt.IsZero() {
		return Profile{}, false
	}
	return profile, true
}

// Abnormal 报告画像里是否有值得在列表上标红的上游状态：已停用、欠费、无有效订阅、组织被封。
// 返回可读原因，正常时为空串。
func (p Profile) Abnormal() string {
	if p.Account != nil && p.Account.IsDeactivated {
		return "上游已停用账号"
	}
	if p.User != nil {
		for _, org := range p.User.Orgs {
			if org.Banned {
				return "所属组织已被封禁"
			}
		}
	}
	if p.Subscription != nil {
		if p.Subscription.IsDelinquent {
			return "订阅欠费"
		}
		if !p.Subscription.HasActiveSubscription {
			return "无有效订阅"
		}
	}
	return ""
}

func profileFromUpstream(check *AccountCheck, me *Me, usage *Usage, now time.Time, errs map[string]string) Profile {
	profile := Profile{FetchedAt: now.UTC()}
	if len(errs) > 0 {
		profile.Errors = errs
	}
	if check != nil {
		profile.Account = &ProfileAccount{
			PlanType: check.PlanType, PlanDisplayName: check.PlanDisplayName, Structure: check.Structure,
			WorkspaceType: check.WorkspaceType, IsDeactivated: check.IsDeactivated,
			EligibleForReactivation: check.EligibleForReactivation, HasPreviouslyPaidSubscription: check.HasPreviouslyPaidSubscription,
			CreatedTime: check.CreatedTime, ComputeResidency: check.ComputeResidency, FeatureCount: check.FeatureCount,
		}
		if e := check.Entitlement; e != nil {
			profile.Subscription = &ProfileSubscription{
				HasActiveSubscription: e.HasActiveSubscription, IsGratis: e.IsGratis, Plan: e.SubscriptionPlan,
				ExpiresAt: e.ExpiresAt, RenewsAt: e.RenewsAt, CancelsAt: e.CancelsAt,
				BillingPeriod: e.BillingPeriod, BillingCurrency: e.BillingCurrency,
				IsDelinquent: e.IsDelinquent, GracePeriodEnd: e.GracePeriodEnd,
				PromoCampaignID: e.PromoCampaignID, DiscountPercent: e.DiscountPercent, DiscountExpiresAt: e.DiscountExpiresAt,
			}
		}
	}
	if me != nil {
		user := &ProfileUser{
			Email: me.Email, Name: me.Name, MFAEnabled: me.MFAEnabled, Created: me.Created,
			Country: me.Country, Region: me.Region, EmailDomainType: me.EmailDomainType,
		}
		for _, org := range me.Orgs {
			user.Orgs = append(user.Orgs, ProfileOrg(org))
		}
		profile.User = user
	}
	if usage != nil && usage.Credits != nil {
		profile.Credits = &ProfileCredits{
			HasCredits: usage.Credits.HasCredits, Unlimited: usage.Credits.Unlimited,
			OverageLimitReached: usage.Credits.OverageLimitReached, Balance: usage.Credits.Balance,
		}
	}
	return profile
}

func deref(v *string) string {
	if v == nil {
		return ""
	}
	return *v
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}
