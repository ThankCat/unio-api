// Package subscriptionaccount 提供订阅账号管理的 Admin API（第九节）。
//
// 路由挂在渠道之下（账号恰好归属一个渠道）与账号自身之上：
//
//	GET    /channels/{id}/accounts                 账号列表 + 聚合 + Redis 运行态
//	POST   /channels/{id}/accounts/import          批量文件导入（sub2api-data v1）
//	POST   /channels/{id}/accounts/oauth/start     OAuth 导入：生成授权链接
//	POST   /channels/{id}/accounts/oauth/complete  OAuth 导入：回填 code 完成落库
//	PATCH  /subscription-accounts/{id}             调度参数编辑（并发/优先级/代理/备注名/订阅到期）
//	PUT    /subscription-accounts/{id}/usage-pause-threshold 账号用量暂停阈值（null 继承渠道，1~100 覆写；改后重算运行态）
//	POST   /subscription-accounts/{id}/refresh       刷新状态（不发模型请求）：用量水位 + 重置卡 + 账号画像（套餐/订阅/上游状态/用户）
//	POST   /subscription-accounts/{id}/reset-credit  手动使用一张重置卡（同时重置 5h/7d），随后回读用量
//	PUT    /subscription-accounts/{id}/auto-reset-credit 自动用卡配置（开关 + 5h/7d 阈值）
//	DELETE /subscription-accounts/{id}             物理删除（仅归档账号；有请求历史则拒绝）
//	POST   /subscription-accounts/{id}/status      启停/归档/恢复（含供给联动确认门）
//	POST   /subscription-accounts/{id}/refresh-token 手动令牌刷新
//	GET    /subscription-accounts/{id}/ledger      订阅台账
//	POST   /subscription-accounts/{id}/ledger      录入一期订阅费用
package subscriptionaccount

import (
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/ThankCat/unio-gateway/internal/app/adminapi/adminhttp"
	"github.com/ThankCat/unio-gateway/internal/platform/failure"
	"github.com/ThankCat/unio-gateway/internal/service/admin/subscriptionaccount"
)

// maxImportBytes 限制导入文件体积（凭据文档单账号几 KB，1000 个号也远小于此）。
const maxImportBytes = 8 << 20

// Handler 是订阅账号模块的 HTTP 层。
type Handler struct {
	service *subscriptionaccount.Service
}

// Register 注册订阅账号路由。
func Register(r chi.Router, service *subscriptionaccount.Service) {
	if service == nil {
		return
	}
	h := &Handler{service: service}
	r.Get("/channels/{id}/accounts", h.list)
	r.Get("/monitoring/account-pools", h.poolsOverview)
	r.Post("/channels/{id}/accounts/import", h.importFile)
	r.Post("/channels/{id}/accounts/oauth/start", h.oauthStart)
	r.Post("/channels/{id}/accounts/oauth/complete", h.oauthComplete)
	r.Patch("/subscription-accounts/{id}", h.updateConfig)
	r.Put("/subscription-accounts/{id}/usage-pause-threshold", h.updateUsagePauseThreshold)
	r.Post("/subscription-accounts/{id}/refresh", h.refreshStatus)
	r.Post("/subscription-accounts/{id}/reset-credit", h.resetCredit)
	r.Put("/subscription-accounts/{id}/auto-reset-credit", h.updateAutoResetCredit)
	r.Delete("/subscription-accounts/{id}", h.deleteAccount)
	r.Post("/subscription-accounts/{id}/status", h.setStatus)
	r.Post("/subscription-accounts/{id}/refresh-token", h.refreshToken)
	r.Get("/subscription-accounts/{id}/ledger", h.listLedger)
	r.Post("/subscription-accounts/{id}/ledger", h.createLedger)
}

func pathID(r *http.Request, name string) (int64, error) {
	id, err := strconv.ParseInt(chi.URLParam(r, name), 10, 64)
	if err != nil || id <= 0 {
		return 0, failure.New(
			failure.CodeAdminInvalidArgument,
			failure.WithMessage("path id must be a positive integer"),
		)
	}
	return id, nil
}

func (h *Handler) list(w http.ResponseWriter, r *http.Request) {
	channelID, err := pathID(r, "id")
	if err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}
	result, err := h.service.List(r.Context(), channelID, r.URL.Query().Get("status"))
	if err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}
	adminhttp.WriteData(w, http.StatusOK, result)
}

func (h *Handler) poolsOverview(w http.ResponseWriter, r *http.Request) {
	pools, err := h.service.PoolsOverview(r.Context())
	if err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}
	adminhttp.WriteData(w, http.StatusOK, map[string]any{"pools": pools})
}

func (h *Handler) importFile(w http.ResponseWriter, r *http.Request) {
	channelID, err := pathID(r, "id")
	if err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}
	raw, err := io.ReadAll(io.LimitReader(r.Body, maxImportBytes+1))
	if err != nil || int64(len(raw)) > maxImportBytes {
		adminhttp.WriteServiceError(w, failure.New(
			failure.CodeAdminInvalidArgument,
			failure.WithMessage("import file is unreadable or exceeds size limit"),
		))
		return
	}
	results, err := h.service.ImportFile(r.Context(), channelID, raw)
	if err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}
	imported := 0
	for _, item := range results {
		if item.Imported {
			imported++
		}
	}
	adminhttp.WriteData(w, http.StatusOK, map[string]any{
		"imported": imported,
		"total":    len(results),
		"results":  results,
	})
}

type oauthStartRequest struct {
	ProxyURL string `json:"proxy_url"`
	// ProxyID 是出站代理实体引用（优先于裸 URL；两者都缺省 = 直连）。
	ProxyID int64 `json:"proxy_id"`
}

func (h *Handler) oauthStart(w http.ResponseWriter, r *http.Request) {
	channelID, err := pathID(r, "id")
	if err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}
	var req oauthStartRequest
	if r.ContentLength != 0 {
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			adminhttp.WriteServiceError(w, failure.New(
				failure.CodeAdminInvalidArgument, failure.WithMessage("invalid json body"),
			))
			return
		}
	}
	sessionID, authorizationURL, err := h.service.StartOAuth(r.Context(), channelID, req.ProxyURL, req.ProxyID)
	if err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}
	adminhttp.WriteData(w, http.StatusOK, map[string]string{
		"session_id":        sessionID,
		"authorization_url": authorizationURL,
	})
}

type oauthCompleteRequest struct {
	SessionID string `json:"session_id"`
	Code      string `json:"code"`
	State     string `json:"state"`
}

func (h *Handler) oauthComplete(w http.ResponseWriter, r *http.Request) {
	if _, err := pathID(r, "id"); err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}
	var req oauthCompleteRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		adminhttp.WriteServiceError(w, failure.New(
			failure.CodeAdminInvalidArgument, failure.WithMessage("invalid json body"),
		))
		return
	}
	result, err := h.service.CompleteOAuth(r.Context(), req.SessionID, req.Code, req.State)
	if err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}
	// 重新授权语义上是更新既有账号，回 200；新导入回 201。
	status := http.StatusCreated
	if result.Reauthorized {
		status = http.StatusOK
	}
	adminhttp.WriteData(w, status, result)
}

type updateConfigRequest struct {
	DisplayName string `json:"display_name"`
	ProxyURL    string `json:"proxy_url"`
	// ProxyID 出站代理实体引用（>0 生效并取代裸 URL；0=不用实体）。
	ProxyID          int64  `json:"proxy_id"`
	ConcurrencyLimit *int64 `json:"concurrency_limit"`
	Priority         int32  `json:"priority"`
	// SubscriptionExpiresAt 是订阅到期时间（RFC3339）；空串/缺省表示清除（未知）。
	SubscriptionExpiresAt string `json:"subscription_expires_at"`
	// FingerprintMode 是指纹收敛档位（off / device）；缺省/空串表示不改。种子由系统管理，不接受输入。
	FingerprintMode string `json:"fingerprint_mode"`
	// ResponseTimeoutMs / FirstTokenTimeoutMs 是账号级超时覆写：null/缺省=继承渠道，0=不限制，正数=覆写。
	// 表单整体提交：每次都带当前值。
	ResponseTimeoutMs   *int32 `json:"response_timeout_ms"`
	FirstTokenTimeoutMs *int32 `json:"first_token_timeout_ms"`
}

func (h *Handler) updateConfig(w http.ResponseWriter, r *http.Request) {
	accountID, err := pathID(r, "id")
	if err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}
	var req updateConfigRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		adminhttp.WriteServiceError(w, failure.New(
			failure.CodeAdminInvalidArgument, failure.WithMessage("invalid json body"),
		))
		return
	}
	var expiresAt *time.Time
	if req.SubscriptionExpiresAt != "" {
		parsed, parseErr := time.Parse(time.RFC3339, req.SubscriptionExpiresAt)
		if parseErr != nil {
			adminhttp.WriteServiceError(w, failure.New(
				failure.CodeAdminInvalidArgument, failure.WithMessage("subscription_expires_at must be RFC3339"),
			))
			return
		}
		expiresAt = &parsed
	}
	account, err := h.service.UpdateConfig(r.Context(), subscriptionaccount.UpdateConfigInput{
		AccountID:             accountID,
		DisplayName:           req.DisplayName,
		ProxyURL:              req.ProxyURL,
		ProxyID:               req.ProxyID,
		ConcurrencyLimit:      req.ConcurrencyLimit,
		Priority:              req.Priority,
		SubscriptionExpiresAt: expiresAt,
		FingerprintMode:       req.FingerprintMode,
		ResponseTimeoutMs:     req.ResponseTimeoutMs,
		FirstTokenTimeoutMs:   req.FirstTokenTimeoutMs,
	})
	if err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}
	adminhttp.WriteData(w, http.StatusOK, account)
}

// usagePauseThresholdRequest 是账号用量暂停阈值的独立编辑体：null 继承渠道，1~100 覆写（不接受 0）。
type usagePauseThresholdRequest struct {
	UsagePauseThresholdPercent *int32 `json:"usage_pause_threshold_percent"`
}

// updateUsagePauseThreshold 单独修改账号阈值，并按该账号最近快照重算 Redis 暂停标记。
// 响应带 account（含生效阈值与来源）与 runtime_refresh 统计；重算失败以 runtime_refresh_error 报出，阈值已保存。
func (h *Handler) updateUsagePauseThreshold(w http.ResponseWriter, r *http.Request) {
	accountID, err := pathID(r, "id")
	if err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}
	var req usagePauseThresholdRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		adminhttp.WriteServiceError(w, failure.New(
			failure.CodeAdminInvalidArgument, failure.WithMessage("invalid json body"),
		))
		return
	}
	result, err := h.service.UpdateUsagePauseThreshold(r.Context(), accountID, req.UsagePauseThresholdPercent)
	if err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}
	adminhttp.WriteData(w, http.StatusOK, result)
}

// refreshStatus 向上游拉取该账号的全部状态（不发模型请求），返回上游报告与刷新后的账号视图。
func (h *Handler) refreshStatus(w http.ResponseWriter, r *http.Request) {
	accountID, err := pathID(r, "id")
	if err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}
	result, err := h.service.RefreshStatus(r.Context(), accountID)
	if err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}
	adminhttp.WriteData(w, http.StatusOK, result)
}

// resetCredit 手动消费一张重置卡；上游拒绝（无可用卡 / 窗口未打满）以 502 带上游正文返回。
func (h *Handler) resetCredit(w http.ResponseWriter, r *http.Request) {
	accountID, err := pathID(r, "id")
	if err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}
	result, err := h.service.ResetCredit(r.Context(), accountID)
	if err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}
	adminhttp.WriteData(w, http.StatusOK, result)
}

// autoResetCreditRequest 是自动用卡配置体：enabled 必填；mode 为 any（任一达到）/ all（同时达到），缺省 any；
// 阈值 null = 该窗口不参与触发，1~100 参与。开启时至少一个窗口参与。
type autoResetCreditRequest struct {
	Enabled            bool   `json:"enabled"`
	Mode               string `json:"mode"`
	Threshold5hPercent *int32 `json:"threshold_5h_percent"`
	Threshold7dPercent *int32 `json:"threshold_7d_percent"`
}

func (h *Handler) updateAutoResetCredit(w http.ResponseWriter, r *http.Request) {
	accountID, err := pathID(r, "id")
	if err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}
	var req autoResetCreditRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		adminhttp.WriteServiceError(w, failure.New(
			failure.CodeAdminInvalidArgument, failure.WithMessage("invalid json body"),
		))
		return
	}
	account, err := h.service.UpdateAutoResetCredit(r.Context(), accountID, subscriptionaccount.AutoResetCreditInput{
		Enabled:            req.Enabled,
		Mode:               req.Mode,
		Threshold5hPercent: req.Threshold5hPercent,
		Threshold7dPercent: req.Threshold7dPercent,
	})
	if err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}
	adminhttp.WriteData(w, http.StatusOK, account)
}

type setStatusRequest struct {
	Action              string `json:"action"`
	DisabledReason      string `json:"disabled_reason"`
	ConfirmSupplyImpact bool   `json:"confirm_supply_impact"`
	// ExpectedImpactFingerprint 与渠道停用确认同构：预览返回的指纹原样带回。
	ExpectedImpactFingerprint string `json:"expected_impact_fingerprint"`
}

func (h *Handler) setStatus(w http.ResponseWriter, r *http.Request) {
	accountID, err := pathID(r, "id")
	if err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}
	var req setStatusRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		adminhttp.WriteServiceError(w, failure.New(
			failure.CodeAdminInvalidArgument, failure.WithMessage("invalid json body"),
		))
		return
	}
	account, err := h.service.SetStatus(r.Context(), subscriptionaccount.SetStatusInput{
		AccountID:                 accountID,
		Action:                    req.Action,
		DisabledReason:            req.DisabledReason,
		ConfirmSupplyImpact:       req.ConfirmSupplyImpact,
		ExpectedImpactFingerprint: req.ExpectedImpactFingerprint,
	})
	if err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}
	adminhttp.WriteData(w, http.StatusOK, account)
}

func (h *Handler) deleteAccount(w http.ResponseWriter, r *http.Request) {
	accountID, err := pathID(r, "id")
	if err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}
	if err := h.service.Delete(r.Context(), accountID); err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) refreshToken(w http.ResponseWriter, r *http.Request) {
	accountID, err := pathID(r, "id")
	if err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}
	account, err := h.service.RefreshToken(r.Context(), accountID)
	if err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}
	adminhttp.WriteData(w, http.StatusOK, account)
}

func (h *Handler) listLedger(w http.ResponseWriter, r *http.Request) {
	accountID, err := pathID(r, "id")
	if err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}
	entries, err := h.service.ListLedger(r.Context(), accountID)
	if err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}
	adminhttp.WriteData(w, http.StatusOK, map[string]any{"entries": entries})
}

type createLedgerRequest struct {
	Amount      string `json:"amount"`
	Currency    string `json:"currency"`
	PeriodStart string `json:"period_start"`
	PeriodEnd   string `json:"period_end"`
	Note        string `json:"note"`
}

func (h *Handler) createLedger(w http.ResponseWriter, r *http.Request) {
	accountID, err := pathID(r, "id")
	if err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}
	var req createLedgerRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		adminhttp.WriteServiceError(w, failure.New(
			failure.CodeAdminInvalidArgument, failure.WithMessage("invalid json body"),
		))
		return
	}
	var amount pgtype.Numeric
	if err := amount.Scan(req.Amount); err != nil || !amount.Valid {
		adminhttp.WriteServiceError(w, failure.New(
			failure.CodeAdminInvalidArgument, failure.WithMessage("amount must be a decimal string"),
		))
		return
	}
	periodStart, err := time.Parse(time.RFC3339, req.PeriodStart)
	if err != nil {
		adminhttp.WriteServiceError(w, failure.New(
			failure.CodeAdminInvalidArgument, failure.WithMessage("period_start must be RFC3339"),
		))
		return
	}
	periodEnd, err := time.Parse(time.RFC3339, req.PeriodEnd)
	if err != nil {
		adminhttp.WriteServiceError(w, failure.New(
			failure.CodeAdminInvalidArgument, failure.WithMessage("period_end must be RFC3339"),
		))
		return
	}
	entry, err := h.service.CreateLedger(r.Context(), subscriptionaccount.CreateLedgerInput{
		AccountID:   accountID,
		Amount:      amount,
		Currency:    req.Currency,
		PeriodStart: periodStart,
		PeriodEnd:   periodEnd,
		Note:        req.Note,
		CreatedBy:   "admin",
	})
	if err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}
	adminhttp.WriteData(w, http.StatusCreated, entry)
}
