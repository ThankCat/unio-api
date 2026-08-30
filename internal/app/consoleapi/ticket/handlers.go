package ticket

import (
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	consoleauth "github.com/ThankCat/unio-gateway/internal/app/consoleapi/auth"
	"github.com/ThankCat/unio-gateway/internal/app/consoleapi/transport"
	coreticket "github.com/ThankCat/unio-gateway/internal/core/ticket"
	"github.com/ThankCat/unio-gateway/internal/platform/httpx"
	consoleservice "github.com/ThankCat/unio-gateway/internal/service/console"
	serviceauth "github.com/ThankCat/unio-gateway/internal/service/console/auth"
	consoleticket "github.com/ThankCat/unio-gateway/internal/service/console/ticket"
)

const (
	defaultPageSize = 20
	maxPageSize     = 100
	// uploadBodyLimit 给 multipart 边界与表单字段留 512KB 余量。
	uploadBodyLimit = coreticket.MaxAttachmentBytes + 512*1024
)

func (h *handler) list(w http.ResponseWriter, r *http.Request) {
	principal, ok := requirePrincipal(w, h.errorWriter, r)
	if !ok {
		return
	}
	page, pageSize := parsePage(r)
	items, total, listErr := h.service.List(r.Context(), consoleticket.ListParams{
		UserID: principal.UserID,
		Status: r.URL.Query().Get("status"),
		Search: strings.TrimSpace(r.URL.Query().Get("q")),
		Limit:  int32(pageSize),
		Offset: int32((page - 1) * pageSize),
	})
	if listErr != nil {
		h.errorWriter.Write(w, r, listErr)
		return
	}
	out := make([]itemDTO, 0, len(items))
	for _, item := range items {
		out = append(out, toItemDTO(item))
	}
	_ = transport.WriteData(w, http.StatusOK, listData{
		Items:    out,
		Page:     page,
		PageSize: pageSize,
		Total:    total,
	})
}

func (h *handler) create(w http.ResponseWriter, r *http.Request) {
	principal, ok := requirePrincipal(w, h.errorWriter, r)
	if !ok {
		return
	}
	var req createRequest
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		h.errorWriter.Write(w, r, consoleservice.InvalidArgument("body", "The request body is not valid JSON."))
		return
	}
	detail, createErr := h.service.Create(r.Context(), consoleticket.CreateParams{
		UserID:   principal.UserID,
		Subject:  req.Subject,
		Category: req.Category,
		Body:     req.Body,
	})
	if createErr != nil {
		h.errorWriter.Write(w, r, createErr)
		return
	}
	_ = transport.WriteData(w, http.StatusCreated, toDetailData(detail))
}

func (h *handler) get(w http.ResponseWriter, r *http.Request) {
	principal, ok := requirePrincipal(w, h.errorWriter, r)
	if !ok {
		return
	}
	uid, err := parseUID(r)
	if err != nil {
		h.errorWriter.Write(w, r, err)
		return
	}
	detail, getErr := h.service.Get(r.Context(), principal.UserID, uid)
	if getErr != nil {
		h.errorWriter.Write(w, r, getErr)
		return
	}
	_ = transport.WriteData(w, http.StatusOK, toDetailData(detail))
}

func (h *handler) reply(w http.ResponseWriter, r *http.Request) {
	principal, ok := requirePrincipal(w, h.errorWriter, r)
	if !ok {
		return
	}
	uid, err := parseUID(r)
	if err != nil {
		h.errorWriter.Write(w, r, err)
		return
	}
	var req replyRequest
	if decodeErr := httpx.DecodeJSON(w, r, &req); decodeErr != nil {
		h.errorWriter.Write(w, r, consoleservice.InvalidArgument("body", "The request body is not valid JSON."))
		return
	}
	detail, replyErr := h.service.Reply(r.Context(), consoleticket.ReplyParams{
		UserID: principal.UserID,
		UID:    uid,
		Body:   req.Body,
	})
	if replyErr != nil {
		h.errorWriter.Write(w, r, replyErr)
		return
	}
	_ = transport.WriteData(w, http.StatusCreated, toDetailData(detail))
}

func (h *handler) closeTicket(w http.ResponseWriter, r *http.Request) {
	principal, ok := requirePrincipal(w, h.errorWriter, r)
	if !ok {
		return
	}
	uid, err := parseUID(r)
	if err != nil {
		h.errorWriter.Write(w, r, err)
		return
	}
	ticket, closeErr := h.service.Close(r.Context(), principal.UserID, uid)
	if closeErr != nil {
		h.errorWriter.Write(w, r, closeErr)
		return
	}
	_ = transport.WriteData(w, http.StatusOK, toTicketDTO(ticket))
}

func (h *handler) summary(w http.ResponseWriter, r *http.Request) {
	principal, ok := requirePrincipal(w, h.errorWriter, r)
	if !ok {
		return
	}
	summary, summaryErr := h.service.TicketSummary(r.Context(), principal.UserID)
	if summaryErr != nil {
		h.errorWriter.Write(w, r, summaryErr)
		return
	}
	_ = transport.WriteData(w, http.StatusOK, summaryData{
		ActiveTotal: summary.ActiveTotal,
		UnreadTotal: summary.UnreadTotal,
	})
}

func (h *handler) uploadAttachment(w http.ResponseWriter, r *http.Request) {
	principal, ok := requirePrincipal(w, h.errorWriter, r)
	if !ok {
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, uploadBodyLimit)
	if err := r.ParseMultipartForm(2 << 20); err != nil {
		h.errorWriter.Write(w, r, consoleservice.InvalidArgument(
			"file", "The upload must be a multipart form within the 5MB size limit.",
		))
		return
	}
	file, header, err := r.FormFile("file")
	if err != nil {
		h.errorWriter.Write(w, r, consoleservice.InvalidArgument("file", "The file field is required."))
		return
	}
	defer func() { _ = file.Close() }()
	data, err := io.ReadAll(file)
	if err != nil {
		h.errorWriter.Write(w, r, consoleservice.InvalidArgument("file", "The uploaded file could not be read."))
		return
	}
	attachment, uploadErr := h.service.CreateAttachment(r.Context(), consoleticket.UploadParams{
		UserID:   principal.UserID,
		FileName: header.Filename,
		Data:     data,
	})
	if uploadErr != nil {
		h.errorWriter.Write(w, r, uploadErr)
		return
	}
	_ = transport.WriteData(w, http.StatusCreated, toAttachmentDTO(attachment))
}

func (h *handler) downloadAttachment(w http.ResponseWriter, r *http.Request) {
	uid, err := parseUID(r)
	if err != nil {
		h.errorWriter.Write(w, r, err)
		return
	}
	expiresAt, parseErr := strconv.ParseInt(r.URL.Query().Get("exp"), 10, 64)
	if parseErr != nil {
		h.errorWriter.Write(w, r, consoleservice.InvalidArgument("exp", "The exp query parameter is required."))
		return
	}
	content, loadErr := h.service.LoadAttachment(r.Context(), uid, expiresAt, r.URL.Query().Get("sig"))
	if loadErr != nil {
		h.errorWriter.Write(w, r, loadErr)
		return
	}
	w.Header().Set("Content-Type", content.MimeType)
	w.Header().Set("Content-Length", strconv.Itoa(len(content.Data)))
	w.Header().Set("Content-Disposition", fmt.Sprintf("inline; filename=%q", content.FileName))
	w.Header().Set("X-Content-Type-Options", "nosniff")
	// 签名 URL 本身带有效期；允许浏览器短暂缓存避免对话流反复拉图。
	w.Header().Set("Cache-Control", "private, max-age=300")
	_, _ = w.Write(content.Data)
}

func parseUID(r *http.Request) (uuid.UUID, *consoleservice.Error) {
	uid, err := uuid.Parse(chi.URLParam(r, "uid"))
	if err != nil {
		return uuid.UUID{}, consoleservice.InvalidArgument("uid", "The ticket identifier is not valid.")
	}
	return uid, nil
}

func parsePage(r *http.Request) (page, pageSize int) {
	page = 1
	pageSize = defaultPageSize
	if raw := r.URL.Query().Get("page"); raw != "" {
		if parsed, err := strconv.Atoi(raw); err == nil && parsed > 0 {
			page = parsed
		}
	}
	if raw := r.URL.Query().Get("page_size"); raw != "" {
		if parsed, err := strconv.Atoi(raw); err == nil && parsed > 0 {
			pageSize = min(parsed, maxPageSize)
		}
	}
	return page, pageSize
}

func requirePrincipal(
	w http.ResponseWriter,
	errorWriter transport.ErrorWriter,
	r *http.Request,
) (serviceauth.Principal, bool) {
	principal, ok := consoleauth.PrincipalFromContext(r.Context())
	if ok {
		return principal, true
	}
	errorWriter.Write(w, r, &consoleservice.Error{
		Code:    serviceauth.CodeSessionInvalid,
		Message: "The current session is invalid.",
		Status:  http.StatusUnauthorized,
	})
	return serviceauth.Principal{}, false
}
