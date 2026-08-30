// Package ticket 提供 Admin 工单运营的 HTTP 端点（队列 / 详情 / 回复 / 状态流转 / 附件）。
//
// 附件下载是本模块唯一的公开路由：<img> 无法携带 Bearer，访问控制由短时效 HMAC 签名承载。
// 因此模块整体挂载在 AdminAuth 分组之外，由 Deps.Auth 在内部为其余路由套认证中间件。
package ticket

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/ThankCat/unio-gateway/internal/app/adminapi/adminhttp"
	coreticket "github.com/ThankCat/unio-gateway/internal/core/ticket"
	"github.com/ThankCat/unio-gateway/internal/platform/failure"
	adminticket "github.com/ThankCat/unio-gateway/internal/service/admin/ticket"
)

// uploadBodyLimit 给 multipart 边界与表单字段留 512KB 余量。
const uploadBodyLimit = coreticket.MaxAttachmentBytes + 512*1024

// TicketService 定义工单运营所需能力。
type TicketService interface {
	List(context.Context, adminticket.ListParams) ([]adminticket.QueueItem, int64, error)
	Get(context.Context, uuid.UUID) (adminticket.Detail, error)
	Reply(context.Context, adminticket.ReplyParams) (adminticket.Detail, error)
	SetStatus(ctx context.Context, uid uuid.UUID, target string) (adminticket.Ticket, error)
	CreateAttachment(context.Context, adminticket.UploadParams) (adminticket.Attachment, error)
	LoadAttachment(ctx context.Context, uid uuid.UUID, expiresAt int64, signature string) (adminticket.AttachmentContent, error)
}

var _ TicketService = (*adminticket.Service)(nil)

// Deps 是工单模块的路由依赖；Auth 是 AdminAuth 中间件（下载路由之外的全部路由生效）。
type Deps struct {
	Service TicketService
	Auth    func(http.Handler) http.Handler
}

// Register 注册工单路由。
func Register(r chi.Router, d Deps) {
	if d.Service == nil {
		return
	}
	h := &ticketsHandler{service: d.Service}
	r.Route("/tickets", func(r chi.Router) {
		// 公开：签名即鉴权（详情响应里的 URL 自带 exp/sig）。
		r.Get("/attachments/{uid}", h.downloadAttachment)

		r.Group(func(r chi.Router) {
			if d.Auth != nil {
				r.Use(d.Auth)
			}
			r.Get("/", h.list)
			r.Post("/attachments", h.uploadAttachment)
			r.Get("/{uid}", h.get)
			r.Post("/{uid}/messages", h.reply)
			r.Post("/{uid}/status", h.setStatus)
		})
	})
}

type ticketsHandler struct {
	service TicketService
}

type queueItemDTO struct {
	UID           string `json:"uid"`
	Subject       string `json:"subject"`
	Category      string `json:"category"`
	Status        string `json:"status"`
	AdminUnread   bool   `json:"admin_unread"`
	LastMessageAt string `json:"last_message_at"`
	CreatedAt     string `json:"created_at"`
	UserID        int64  `json:"user_id"`
	UserEmail     string `json:"user_email"`
}

type ticketDTO struct {
	UID           string  `json:"uid"`
	Subject       string  `json:"subject"`
	Category      string  `json:"category"`
	Status        string  `json:"status"`
	AdminUnread   bool    `json:"admin_unread"`
	LastMessageAt string  `json:"last_message_at"`
	ResolvedAt    *string `json:"resolved_at"`
	ClosedAt      *string `json:"closed_at"`
	CreatedAt     string  `json:"created_at"`
	UpdatedAt     string  `json:"updated_at"`
}

type userDTO struct {
	ID        int64  `json:"id"`
	Email     string `json:"email"`
	CreatedAt string `json:"created_at"`
}

type messageDTO struct {
	ID         int64           `json:"id"`
	AuthorType string          `json:"author_type"`
	Body       json.RawMessage `json:"body"`
	CreatedAt  string          `json:"created_at"`
}

type attachmentDTO struct {
	UID       string `json:"uid"`
	FileName  string `json:"file_name"`
	MimeType  string `json:"mime_type"`
	SizeBytes int32  `json:"size_bytes"`
	// URL 是相对下载路径（含 exp/sig），前端拼 Admin API base。
	URL string `json:"url"`
}

type detailDTO struct {
	Ticket      ticketDTO       `json:"ticket"`
	User        userDTO         `json:"user"`
	Messages    []messageDTO    `json:"messages"`
	Attachments []attachmentDTO `json:"attachments"`
}

type statusRequest struct {
	Status string `json:"status"`
}

func (h *ticketsHandler) list(w http.ResponseWriter, r *http.Request) {
	page := adminhttp.ParsePage(r)
	items, total, err := h.service.List(r.Context(), adminticket.ListParams{
		Status:   adminhttp.QueryString(r, "status"),
		Category: adminhttp.QueryString(r, "category"),
		Search:   adminhttp.QueryString(r, "search"),
		Limit:    page.Limit(),
		Offset:   page.Offset(),
	})
	if err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}
	out := make([]queueItemDTO, 0, len(items))
	for _, item := range items {
		out = append(out, queueItemDTO{
			UID:           item.UID.String(),
			Subject:       item.Subject,
			Category:      item.Category,
			Status:        item.Status,
			AdminUnread:   item.AdminUnread,
			LastMessageAt: adminhttp.RFC3339(item.LastMessageAt),
			CreatedAt:     adminhttp.RFC3339(item.CreatedAt),
			UserID:        item.UserID,
			UserEmail:     item.UserEmail,
		})
	}
	adminhttp.WriteList(w, http.StatusOK, out, page, total)
}

func (h *ticketsHandler) get(w http.ResponseWriter, r *http.Request) {
	uid, err := pathUID(r)
	if err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}
	detail, getErr := h.service.Get(r.Context(), uid)
	if getErr != nil {
		adminhttp.WriteServiceError(w, getErr)
		return
	}
	adminhttp.WriteData(w, http.StatusOK, toDetailDTO(detail))
}

func (h *ticketsHandler) reply(w http.ResponseWriter, r *http.Request) {
	uid, err := pathUID(r)
	if err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}
	var req struct {
		Body json.RawMessage `json:"body"`
	}
	if decodeErr := json.NewDecoder(r.Body).Decode(&req); decodeErr != nil {
		adminhttp.WriteServiceError(w, failure.New(
			failure.CodeAdminInvalidArgument, failure.WithMessage("request body is not valid JSON"),
		))
		return
	}
	detail, replyErr := h.service.Reply(r.Context(), adminticket.ReplyParams{UID: uid, Body: req.Body})
	if replyErr != nil {
		adminhttp.WriteServiceError(w, replyErr)
		return
	}
	adminhttp.WriteData(w, http.StatusCreated, toDetailDTO(detail))
}

func (h *ticketsHandler) setStatus(w http.ResponseWriter, r *http.Request) {
	uid, err := pathUID(r)
	if err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}
	var req statusRequest
	if decodeErr := json.NewDecoder(r.Body).Decode(&req); decodeErr != nil {
		adminhttp.WriteServiceError(w, failure.New(
			failure.CodeAdminInvalidArgument, failure.WithMessage("request body is not valid JSON"),
		))
		return
	}
	ticket, statusErr := h.service.SetStatus(r.Context(), uid, req.Status)
	if statusErr != nil {
		adminhttp.WriteServiceError(w, statusErr)
		return
	}
	adminhttp.WriteData(w, http.StatusOK, toTicketDTO(ticket))
}

func (h *ticketsHandler) uploadAttachment(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, uploadBodyLimit)
	if err := r.ParseMultipartForm(2 << 20); err != nil {
		adminhttp.WriteServiceError(w, failure.New(
			failure.CodeAdminInvalidArgument,
			failure.WithMessage("upload must be a multipart form within the 5MB size limit"),
		))
		return
	}
	file, header, err := r.FormFile("file")
	if err != nil {
		adminhttp.WriteServiceError(w, failure.New(
			failure.CodeAdminInvalidArgument, failure.WithMessage("file field is required"),
		))
		return
	}
	defer func() { _ = file.Close() }()
	data, err := io.ReadAll(file)
	if err != nil {
		adminhttp.WriteServiceError(w, failure.New(
			failure.CodeAdminInvalidArgument, failure.WithMessage("uploaded file could not be read"),
		))
		return
	}
	attachment, uploadErr := h.service.CreateAttachment(r.Context(), adminticket.UploadParams{
		FileName: header.Filename,
		Data:     data,
	})
	if uploadErr != nil {
		adminhttp.WriteServiceError(w, uploadErr)
		return
	}
	adminhttp.WriteData(w, http.StatusCreated, toAttachmentDTO(attachment))
}

func (h *ticketsHandler) downloadAttachment(w http.ResponseWriter, r *http.Request) {
	uid, err := pathUID(r)
	if err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}
	expiresAt, parseErr := strconv.ParseInt(r.URL.Query().Get("exp"), 10, 64)
	if parseErr != nil {
		adminhttp.WriteServiceError(w, failure.New(
			failure.CodeAdminInvalidArgument, failure.WithMessage("exp query parameter is required"),
		))
		return
	}
	content, loadErr := h.service.LoadAttachment(r.Context(), uid, expiresAt, r.URL.Query().Get("sig"))
	if loadErr != nil {
		adminhttp.WriteServiceError(w, loadErr)
		return
	}
	w.Header().Set("Content-Type", content.MimeType)
	w.Header().Set("Content-Length", strconv.Itoa(len(content.Data)))
	w.Header().Set("Content-Disposition", fmt.Sprintf("inline; filename=%q", content.FileName))
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "private, max-age=300")
	_, _ = w.Write(content.Data)
}

func toDetailDTO(detail adminticket.Detail) detailDTO {
	messages := make([]messageDTO, 0, len(detail.Messages))
	for _, message := range detail.Messages {
		messages = append(messages, messageDTO{
			ID:         message.ID,
			AuthorType: message.AuthorType,
			Body:       message.Body,
			CreatedAt:  adminhttp.RFC3339(message.CreatedAt),
		})
	}
	attachments := make([]attachmentDTO, 0, len(detail.Attachments))
	for _, attachment := range detail.Attachments {
		attachments = append(attachments, toAttachmentDTO(attachment))
	}
	return detailDTO{
		Ticket: toTicketDTO(detail.Ticket),
		User: userDTO{
			ID:        detail.User.ID,
			Email:     detail.User.Email,
			CreatedAt: adminhttp.RFC3339(detail.User.CreatedAt),
		},
		Messages:    messages,
		Attachments: attachments,
	}
}

func toTicketDTO(ticket adminticket.Ticket) ticketDTO {
	return ticketDTO{
		UID:           ticket.UID.String(),
		Subject:       ticket.Subject,
		Category:      ticket.Category,
		Status:        ticket.Status,
		AdminUnread:   ticket.AdminUnread,
		LastMessageAt: adminhttp.RFC3339(ticket.LastMessageAt),
		ResolvedAt:    adminhttp.RFC3339Ptr(ticket.ResolvedAt),
		ClosedAt:      adminhttp.RFC3339Ptr(ticket.ClosedAt),
		CreatedAt:     adminhttp.RFC3339(ticket.CreatedAt),
		UpdatedAt:     adminhttp.RFC3339(ticket.UpdatedAt),
	}
}

func toAttachmentDTO(attachment adminticket.Attachment) attachmentDTO {
	return attachmentDTO{
		UID:       attachment.UID.String(),
		FileName:  attachment.FileName,
		MimeType:  attachment.MimeType,
		SizeBytes: attachment.SizeBytes,
		URL:       attachment.URL,
	}
}

func pathUID(r *http.Request) (uuid.UUID, error) {
	uid, err := uuid.Parse(chi.URLParam(r, "uid"))
	if err != nil {
		return uuid.UUID{}, failure.New(
			failure.CodeAdminInvalidArgument, failure.WithMessage("uid must be a valid uuid"),
		)
	}
	return uid, nil
}
