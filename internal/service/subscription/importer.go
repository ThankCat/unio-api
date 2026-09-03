package subscription

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/ThankCat/unio-gateway/internal/platform/failure"
	"github.com/ThankCat/unio-gateway/internal/platform/store/sqlc"
)

// 批量文件导入（第六节）：格式解析器可插拔，本期唯一支持 Sub2API `sub2api-data` v1。
// 导入落库 disabled；带 refresh token 的账号自然进入保活扫描；按 (platform, upstream_account_id)
// 去重，重复导入拒绝并提示已存在于哪个池（边界 21，续命走「重新授权」显式操作）。

// sub2apiFile 是 sub2api-data v1 的文件形态（对照上传样例逐字段核实）。
type sub2apiFile struct {
	Type     string         `json:"type"`
	Version  int            `json:"version"`
	Proxies  []sub2apiProxy `json:"proxies"`
	Accounts []sub2apiEntry `json:"accounts"`
}

type sub2apiProxy struct {
	Key string `json:"key"`
	URL string `json:"url"`
}

type sub2apiEntry struct {
	Name        string          `json:"name"`
	Type        string          `json:"type"`
	Platform    string          `json:"platform"`
	Priority    int32           `json:"priority"`
	Concurrency int32           `json:"concurrency"`
	ProxyKey    string          `json:"proxy_key"`
	Credentials json.RawMessage `json:"credentials"`
}

// sub2apiCredentials 只解出规范化所需字段；其余（live_identity 等诊断信息）丢弃。
type sub2apiCredentials struct {
	Email                 string `json:"email"`
	Name                  string `json:"name"`
	IDToken               string `json:"id_token"`
	AccessToken           string `json:"access_token"`
	RefreshToken          string `json:"refresh_token"`
	ClientID              string `json:"client_id"`
	PlanType              string `json:"plan_type"`
	AccountID             string `json:"account_id"`
	ChatGPTAccountID      string `json:"chatgpt_account_id"`
	ExpiresAt             string `json:"expires_at"`
	SubscriptionExpiresAt string `json:"subscription_expires_at"`
}

// ImportAccount 是一条解析归一后的待导入账号。
type ImportAccount struct {
	Platform          string
	UpstreamAccountID string
	DisplayName       string
	PlanType          string
	Credentials       Credentials
	ProxyURL          string
	// ProxyID 是出站代理实体引用（OAuth 向导选择的代理）；nil 时回退 ProxyURL 裸串。
	ProxyID           *int64
	Priority          int32
	Concurrency       *int32
	SubscriptionUntil time.Time
}

// ParseSub2APIData 解析 sub2api-data v1 文件为归一化账号列表。
func ParseSub2APIData(raw []byte) ([]ImportAccount, error) {
	var file sub2apiFile
	if err := json.Unmarshal(raw, &file); err != nil {
		return nil, failure.Wrap(failure.CodeConfigInvalid, err, failure.WithMessage("parse sub2api-data file"))
	}
	if file.Type != "sub2api-data" || file.Version != 1 {
		return nil, failure.New(
			failure.CodeConfigInvalid,
			failure.WithMessage(fmt.Sprintf("unsupported import format: type=%q version=%d (only sub2api-data v1)", file.Type, file.Version)),
		)
	}
	proxies := make(map[string]string, len(file.Proxies))
	for _, proxy := range file.Proxies {
		if proxy.Key != "" && proxy.URL != "" {
			proxies[proxy.Key] = proxy.URL
		}
	}

	accounts := make([]ImportAccount, 0, len(file.Accounts))
	for index, entry := range file.Accounts {
		if entry.Platform != "openai" {
			return nil, importEntryError(index, "platform %q is not supported (openai only)", entry.Platform)
		}
		if entry.Type != "oauth" {
			return nil, importEntryError(index, "credential type %q is not supported (oauth only)", entry.Type)
		}
		var creds sub2apiCredentials
		if err := json.Unmarshal(entry.Credentials, &creds); err != nil {
			return nil, importEntryError(index, "credentials are not decodable: %v", err)
		}
		if strings.TrimSpace(creds.AccessToken) == "" {
			return nil, importEntryError(index, "credentials missing access_token")
		}
		upstreamID := firstNonEmpty(creds.ChatGPTAccountID, creds.AccountID)
		normalized := Credentials{
			AccessToken:  creds.AccessToken,
			RefreshToken: creds.RefreshToken,
			IDToken:      creds.IDToken,
			ClientID:     creds.ClientID,
			Email:        creds.Email,
		}
		if t, err := time.Parse(time.RFC3339, creds.ExpiresAt); err == nil {
			normalized.ExpiresAt = t
		} else if exp, ok := jwtExpiry(creds.AccessToken); ok {
			normalized.ExpiresAt = exp
		}
		// 文件字段缺失时从令牌声明兜底（上游账号 ID 是全局唯一键，绝不能缺）。
		identity := ParseIdentity(normalized)
		if upstreamID == "" {
			upstreamID = identity.ChatGPTAccountID
		}
		if upstreamID == "" {
			return nil, importEntryError(index, "cannot determine upstream account id (chatgpt_account_id)")
		}
		planType := firstNonEmpty(creds.PlanType, identity.PlanType)
		displayName := firstNonEmpty(entry.Name, creds.Email, creds.Name, upstreamID)

		account := ImportAccount{
			Platform:          "openai",
			UpstreamAccountID: upstreamID,
			DisplayName:       displayName,
			PlanType:          planType,
			Credentials:       normalized,
			ProxyURL:          proxies[entry.ProxyKey],
			Priority:          entry.Priority,
		}
		if account.Priority <= 0 {
			account.Priority = 50
		}
		if entry.Concurrency > 0 {
			concurrency := entry.Concurrency
			account.Concurrency = &concurrency
		}
		if t, err := time.Parse(time.RFC3339, creds.SubscriptionExpiresAt); err == nil {
			account.SubscriptionUntil = t
		} else if !identity.SubscriptionUntil.IsZero() {
			account.SubscriptionUntil = identity.SubscriptionUntil
		}
		accounts = append(accounts, account)
	}
	if len(accounts) == 0 {
		return nil, failure.New(failure.CodeConfigInvalid, failure.WithMessage("import file contains no accounts"))
	}
	return accounts, nil
}

func importEntryError(index int, format string, args ...any) error {
	return failure.New(
		failure.CodeConfigInvalid,
		failure.WithMessage(fmt.Sprintf("accounts[%d]: %s", index, fmt.Sprintf(format, args...))),
	)
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

// ImporterQueries 是导入落库所需的最小查询集。
type ImporterQueries interface {
	AdminCreateSubscriptionAccount(ctx context.Context, arg sqlc.AdminCreateSubscriptionAccountParams) (sqlc.SubscriptionAccount, error)
	GetAccountByPlatformUpstreamID(ctx context.Context, arg sqlc.GetAccountByPlatformUpstreamIDParams) (sqlc.GetAccountByPlatformUpstreamIDRow, error)
}

// ImportResultItem 是单条导入的结果（成功或带原因的拒绝）。
type ImportResultItem struct {
	DisplayName       string `json:"display_name"`
	UpstreamAccountID string `json:"upstream_account_id"`
	AccountID         int64  `json:"account_id,omitempty"`
	Imported          bool   `json:"imported"`
	// Reason 是拒绝原因；重复导入时提示已存在于哪个池（边界 21）。
	Reason string `json:"reason,omitempty"`
}

// ImportAccounts 逐条落库（disabled）。重复导入拒绝该条并继续其余条目——
// 批量文件里混一个已存在的号不应让整批失败。
func ImportAccounts(ctx context.Context, queries ImporterQueries, channelID int64, accounts []ImportAccount) ([]ImportResultItem, error) {
	results := make([]ImportResultItem, 0, len(accounts))
	for _, account := range accounts {
		item := ImportResultItem{
			DisplayName:       account.DisplayName,
			UpstreamAccountID: account.UpstreamAccountID,
		}
		raw, err := account.Credentials.Encode()
		if err != nil {
			item.Reason = "credentials not encodable"
			results = append(results, item)
			continue
		}
		params := sqlc.AdminCreateSubscriptionAccountParams{
			ChannelID:         channelID,
			Platform:          account.Platform,
			CredentialType:    "oauth",
			UpstreamAccountID: account.UpstreamAccountID,
			DisplayName:       account.DisplayName,
			Credentials:       raw,
			Priority:          account.Priority,
		}
		if account.PlanType != "" {
			params.PlanType = pgtype.Text{String: account.PlanType, Valid: true}
		}
		if account.ProxyURL != "" {
			params.ProxyUrl = pgtype.Text{String: account.ProxyURL, Valid: true}
		}
		if account.ProxyID != nil && *account.ProxyID > 0 {
			params.ProxyID = pgtype.Int8{Int64: *account.ProxyID, Valid: true}
		}
		if account.Concurrency != nil {
			params.ConcurrencyLimit = pgtype.Int4{Int32: *account.Concurrency, Valid: true}
		}
		if !account.SubscriptionUntil.IsZero() {
			params.SubscriptionExpiresAt = pgtype.Timestamptz{Time: account.SubscriptionUntil, Valid: true}
		}
		created, err := queries.AdminCreateSubscriptionAccount(ctx, params)
		if err != nil {
			var pgErr *pgconn.PgError
			if errors.As(err, &pgErr) && pgErr.Code == "23505" {
				item.Reason = duplicateReason(ctx, queries, account)
				results = append(results, item)
				continue
			}
			return results, failure.Wrap(
				failure.CodeDependencyPostgresUnavailable, err,
				failure.WithMessage("create subscription account"),
			)
		}
		item.Imported = true
		item.AccountID = created.ID
		results = append(results, item)
	}
	return results, nil
}

func duplicateReason(ctx context.Context, queries ImporterQueries, account ImportAccount) string {
	existing, err := queries.GetAccountByPlatformUpstreamID(ctx, sqlc.GetAccountByPlatformUpstreamIDParams{
		Platform:          account.Platform,
		UpstreamAccountID: account.UpstreamAccountID,
	})
	if err != nil {
		return "该上游账号已存在"
	}
	// 这段文案直接透出到管理端 toast：说人话，并指出两条出路（同渠道重授权 / 异渠道先归档）。
	return fmt.Sprintf(
		"该上游账号已在渠道「%s」（channel_id=%d, account_id=%d）：同渠道请用「重新授权」更新凭据；要换池须先归档原账号",
		existing.ChannelName, existing.ChannelID, existing.ID,
	)
}
