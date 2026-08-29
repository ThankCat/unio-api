// Package message 提供 Admin 站内消息中心的 HTTP 端点（列表 / 未读数 / 标记已读）。
//
// 消息的写入方是 worker / 服务端（adminmessage.Service.Publish），不暴露 HTTP 写入端点。
package message

import (
	"context"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/ThankCat/unio-gateway/internal/app/adminapi/adminhttp"
	"github.com/ThankCat/unio-gateway/internal/service/admin/adminmessage"
)

// MessageService 定义消息中心管理台所需能力。
type MessageService interface {
	List(context.Context, adminmessage.ListParams) ([]adminmessage.Message, int64, error)
	UnreadCount(context.Context) (int64, error)
	MarkRead(context.Context, int64) (adminmessage.Message, error)
	MarkAllRead(context.Context) (int64, error)
}

// Deps 是消息模块的路由依赖。
type Deps struct {
	Service MessageService
}

// Register 注册消息中心路由。静态 /messages/unread-count、/messages/read-all
// 在 /messages/{id}/read 之前注册（chi 静态优先于通配，顺序仅为可读性）。
func Register(r chi.Router, d Deps) {
	if d.Service == nil {
		return
	}
	h := &messagesHandler{service: d.Service}
	r.Get("/messages", h.list)
	r.Get("/messages/unread-count", h.unreadCount)
	r.Post("/messages/read-all", h.markAllRead)
	r.Post("/messages/{id}/read", h.markRead)
}

type messageDTO struct {
	ID        int64   `json:"id"`
	Severity  string  `json:"severity"`
	Topic     string  `json:"topic"`
	Title     string  `json:"title"`
	Body      string  `json:"body"`
	Source    string  `json:"source"`
	CreatedAt string  `json:"created_at"`
	ReadAt    *string `json:"read_at"`
}

type messagesHandler struct {
	service MessageService
}

func (h *messagesHandler) list(w http.ResponseWriter, r *http.Request) {
	page := adminhttp.ParsePage(r)
	items, total, err := h.service.List(r.Context(), adminmessage.ListParams{
		UnreadOnly: adminhttp.BoolQuery(r, "unread_only"),
		Topic:      adminhttp.QueryString(r, "topic"),
		Limit:      page.Limit(),
		Offset:     page.Offset(),
	})
	if err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}
	out := make([]messageDTO, 0, len(items))
	for _, item := range items {
		out = append(out, messageDTOFrom(item))
	}
	adminhttp.WriteList(w, http.StatusOK, out, page, total)
}

func (h *messagesHandler) unreadCount(w http.ResponseWriter, r *http.Request) {
	count, err := h.service.UnreadCount(r.Context())
	if err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}
	adminhttp.WriteData(w, http.StatusOK, map[string]int64{"count": count})
}

func (h *messagesHandler) markRead(w http.ResponseWriter, r *http.Request) {
	id, err := adminhttp.PathID(r)
	if err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}
	msg, err := h.service.MarkRead(r.Context(), id)
	if err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}
	adminhttp.WriteData(w, http.StatusOK, messageDTOFrom(msg))
}

func (h *messagesHandler) markAllRead(w http.ResponseWriter, r *http.Request) {
	updated, err := h.service.MarkAllRead(r.Context())
	if err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}
	adminhttp.WriteData(w, http.StatusOK, map[string]int64{"updated": updated})
}

func messageDTOFrom(msg adminmessage.Message) messageDTO {
	return messageDTO{
		ID:        msg.ID,
		Severity:  msg.Severity,
		Topic:     msg.Topic,
		Title:     msg.Title,
		Body:      msg.Body,
		Source:    msg.Source,
		CreatedAt: adminhttp.RFC3339(msg.CreatedAt),
		ReadAt:    adminhttp.RFC3339Ptr(msg.ReadAt),
	}
}
