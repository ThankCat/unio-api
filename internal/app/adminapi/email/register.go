// Package email 提供 Admin 客户中心「邮件」列表的 HTTP 端点（发送记录列表 / 详情）。
//
// 记录的写入方是同步发送路径（internal/service/email.Mailer），不暴露 HTTP 写入端点。
package email

import (
	"context"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/ThankCat/unio-gateway/internal/app/adminapi/adminhttp"
	"github.com/ThankCat/unio-gateway/internal/service/admin/emaillog"
)

// EmailLogService 定义邮件记录管理台所需能力。
type EmailLogService interface {
	List(ctx context.Context, params emaillog.ListParams) ([]emaillog.Item, int64, error)
	Get(ctx context.Context, id int64) (emaillog.Detail, error)
}

// Deps 是邮件模块的路由依赖。
type Deps struct {
	Service EmailLogService
}

// Register 注册邮件记录路由。
func Register(r chi.Router, d Deps) {
	if d.Service == nil {
		return
	}
	h := &emailsHandler{service: d.Service}
	r.Get("/emails", h.list)
	r.Get("/emails/{id}", h.get)
}

type emailListItemDTO struct {
	ID           int64   `json:"id"`
	EmailType    string  `json:"email_type"`
	Recipient    string  `json:"recipient"`
	Sender       string  `json:"sender"`
	Subject      string  `json:"subject"`
	Status       string  `json:"status"`
	ErrorSummary *string `json:"error_summary"`
	Locale       string  `json:"locale"`
	DurationMs   *int32  `json:"duration_ms"`
	SentAt       *string `json:"sent_at"`
	CreatedAt    string  `json:"created_at"`
}

type emailDetailDTO struct {
	emailListItemDTO
	BodyHTML string `json:"body_html"`
}

type emailsHandler struct {
	service EmailLogService
}

func (h *emailsHandler) list(w http.ResponseWriter, r *http.Request) {
	page := adminhttp.ParsePage(r)
	from, err := adminhttp.OptionalTimeQuery(r, "from")
	if err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}
	to, err := adminhttp.OptionalTimeQuery(r, "to")
	if err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}
	items, total, err := h.service.List(r.Context(), emaillog.ListParams{
		EmailType: adminhttp.QueryString(r, "email_type"),
		Status:    listStatus(r),
		Recipient: adminhttp.QueryString(r, "recipient"),
		From:      from,
		To:        to,
		Limit:     page.Limit(),
		Offset:    page.Offset(),
	})
	if err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}
	out := make([]emailListItemDTO, 0, len(items))
	for _, item := range items {
		out = append(out, listItemDTOFrom(item))
	}
	adminhttp.WriteList(w, http.StatusOK, out, page, total)
}

func (h *emailsHandler) get(w http.ResponseWriter, r *http.Request) {
	id, err := adminhttp.PathID(r)
	if err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}
	detail, err := h.service.Get(r.Context(), id)
	if err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}
	adminhttp.WriteData(w, http.StatusOK, emailDetailDTO{
		emailListItemDTO: listItemDTOFrom(detail.Item),
		BodyHTML:         detail.BodyHTML,
	})
}

// listStatus 只接受 sent/failed 作为发送结果过滤值，其它一律视为不过滤。
func listStatus(r *http.Request) string {
	switch r.URL.Query().Get("status") {
	case "sent":
		return "sent"
	case "failed":
		return "failed"
	default:
		return ""
	}
}

func listItemDTOFrom(item emaillog.Item) emailListItemDTO {
	return emailListItemDTO{
		ID:           item.ID,
		EmailType:    item.EmailType,
		Recipient:    item.Recipient,
		Sender:       item.Sender,
		Subject:      item.Subject,
		Status:       item.Status,
		ErrorSummary: item.ErrorSummary,
		Locale:       item.Locale,
		DurationMs:   item.DurationMs,
		SentAt:       adminhttp.RFC3339Ptr(item.SentAt),
		CreatedAt:    adminhttp.RFC3339(item.CreatedAt),
	}
}
