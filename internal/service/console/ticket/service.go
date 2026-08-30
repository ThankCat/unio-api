// Package ticket 提供 Console 侧的工单自助服务。
//
// 两条贯穿全包的规则：
//
//  1. 归属。所有入参都带 UserID 并进入 SQL 条件；按 uid 定位工单时必须同时匹配 user_id。
//  2. 正文。写入前必须经 core/ticket.ParseBody 白名单校验并规范化；附件绑定集合以
//     正文里 attachment:{uid} 的引用为准，不接受额外的附件参数。
package ticket

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"go.uber.org/zap"

	coreticket "github.com/ThankCat/unio-gateway/internal/core/ticket"
	"github.com/ThankCat/unio-gateway/internal/platform/store/sqlc"
	"github.com/ThankCat/unio-gateway/internal/service/admin/adminmessage"
	"github.com/ThankCat/unio-gateway/internal/service/admin/opsutil"
	console "github.com/ThankCat/unio-gateway/internal/service/console"
)

// 工单是低频资源；列表分页上限与 Console 其他页面一致。
const (
	defaultPageSize = 20
	maxPageSize     = 100
)

// 稳定错误码（Console 公开契约）。
const (
	CodeTicketNotFound        = "ticket_not_found"
	CodeTicketClosed          = "ticket_closed"
	CodeAttachmentInvalid     = "attachment_invalid"
	CodeAttachmentQuota       = "attachment_quota_exceeded"
	CodeAttachmentLinkInvalid = "attachment_link_invalid"
)

// TxBeginner 提供事务能力（由 pgxpool 满足）：创建/回复要求「消息落库 + 附件绑定 +
// 工单状态推进」同进同出，否则会留下引用了未绑定附件的消息。
type TxBeginner interface {
	Begin(ctx context.Context) (pgx.Tx, error)
}

// AdminNotifier 把工单动态写入 Admin 站内消息中心（顶栏铃铛），由 adminmessage.Service 满足。
// admin_messages 的表注释即预留了「用户工单复用本表范式（扩 topic）」。
type AdminNotifier interface {
	Publish(ctx context.Context, params adminmessage.PublishParams) (bool, error)
}

// Service 编排 Console 工单读写。
type Service struct {
	db       TxBeginner
	queries  *sqlc.Queries
	signer   *coreticket.AttachmentSigner
	notifier AdminNotifier
	logger   *zap.Logger
	now      func() time.Time
}

// NewService 创建 Console 工单服务。
func NewService(db TxBeginner, queries *sqlc.Queries, signer *coreticket.AttachmentSigner) *Service {
	if db == nil {
		panic("console ticket: tx beginner is required")
	}
	if queries == nil {
		panic("console ticket: queries is required")
	}
	if signer == nil {
		panic("console ticket: attachment signer is required")
	}
	return &Service{db: db, queries: queries, signer: signer, logger: zap.NewNop(), now: time.Now}
}

// WithAdminNotifier 注入 Admin 站内消息发布能力：用户创建/回复工单时提醒运营。
// 通知是弱信号，发布失败只记日志，不影响工单操作本身。
func (s *Service) WithAdminNotifier(notifier AdminNotifier, logger *zap.Logger) *Service {
	if s != nil {
		s.notifier = notifier
		if logger != nil {
			s.logger = logger
		}
	}
	return s
}

// Item 是列表页的工单行。
type Item struct {
	UID           uuid.UUID
	Subject       string
	Category      string
	Status        string
	UserUnread    bool
	LastMessageAt time.Time
	CreatedAt     time.Time
}

// Ticket 是详情页的工单头。
type Ticket struct {
	Item
	ResolvedAt *time.Time
	ClosedAt   *time.Time
	UpdatedAt  time.Time
}

// Message 是对话流中的一条消息；Body 是规范化后的 Tiptap JSON。
type Message struct {
	ID         int64
	AuthorType string
	Body       json.RawMessage
	CreatedAt  time.Time
}

// Attachment 是附件元数据；URL 是短时效签名下载路径（相对路径，前端拼 API base）。
type Attachment struct {
	UID       uuid.UUID
	FileName  string
	MimeType  string
	SizeBytes int32
	URL       string
}

// Detail 是详情页数据：工单头 + 对话流 + 全部已绑定附件。
type Detail struct {
	Ticket
	Messages    []Message
	Attachments []Attachment
}

// Summary 是侧栏角标数据。
type Summary struct {
	ActiveTotal int64
	UnreadTotal int64
}

// AttachmentContent 是签名下载端点的响应内容。
type AttachmentContent struct {
	FileName  string
	MimeType  string
	SizeBytes int32
	Data      []byte
}

// ListParams 是列表查询参数。
type ListParams struct {
	UserID int64
	Status string
	Search string
	Limit  int32
	Offset int32
}

// List 分页列出本人工单（最近更新在前）。
func (s *Service) List(ctx context.Context, params ListParams) ([]Item, int64, *console.Error) {
	if params.Status != "" && !coreticket.ValidStatus(params.Status) {
		return nil, 0, console.InvalidArgument("status", "The status filter is not valid.")
	}
	if params.Limit <= 0 {
		params.Limit = defaultPageSize
	}
	if params.Limit > maxPageSize {
		params.Limit = maxPageSize
	}
	status := opsutil.TextNarg(params.Status)
	search := opsutil.TextNarg(strings.TrimSpace(params.Search))
	rows, err := s.queries.ListConsoleTickets(ctx, sqlc.ListConsoleTicketsParams{
		UserID:     params.UserID,
		Status:     status,
		Search:     search,
		PageLimit:  params.Limit,
		PageOffset: params.Offset,
	})
	if err != nil {
		return nil, 0, console.RequestUnavailable("list console tickets", err)
	}
	total, err := s.queries.CountConsoleTickets(ctx, sqlc.CountConsoleTicketsParams{
		UserID: params.UserID,
		Status: status,
		Search: search,
	})
	if err != nil {
		return nil, 0, console.RequestUnavailable("count console tickets", err)
	}
	items := make([]Item, 0, len(rows))
	for _, row := range rows {
		items = append(items, Item{
			UID:           uuid.UUID(row.Uid.Bytes),
			Subject:       row.Subject,
			Category:      row.Category,
			Status:        row.Status,
			UserUnread:    row.UserUnread,
			LastMessageAt: row.LastMessageAt.Time,
			CreatedAt:     row.CreatedAt.Time,
		})
	}
	return items, total, nil
}

// Get 返回本人工单详情，并顺带清除用户侧未读红点。
func (s *Service) Get(ctx context.Context, userID int64, uid uuid.UUID) (Detail, *console.Error) {
	row, err := s.queries.GetConsoleTicket(ctx, sqlc.GetConsoleTicketParams{
		Uid:    pgUUID(uid),
		UserID: userID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return Detail{}, ticketNotFound()
	}
	if err != nil {
		return Detail{}, console.RequestUnavailable("get console ticket", err)
	}
	if _, err := s.queries.ClearTicketUserUnread(ctx, row.ID); err != nil {
		return Detail{}, console.RequestUnavailable("clear ticket user unread", err)
	}
	ticket := ticketFrom(row)
	ticket.UserUnread = false
	return s.loadDetail(ctx, s.queries, ticket, row.ID)
}

// CreateParams 是创建工单参数；Body 是编辑器提交的 Tiptap JSON。
type CreateParams struct {
	UserID   int64
	Subject  string
	Category string
	Body     []byte
}

// Create 创建工单：工单头、首条消息与附件绑定在一个事务里完成。
func (s *Service) Create(ctx context.Context, params CreateParams) (Detail, *console.Error) {
	subject := strings.TrimSpace(params.Subject)
	if subject == "" {
		return Detail{}, console.InvalidArgument("subject", "The subject is required.")
	}
	if utf8.RuneCountInString(subject) > coreticket.MaxSubjectChars {
		return Detail{}, console.InvalidArgument("subject", "The subject is too long.")
	}
	if !coreticket.ValidCategory(params.Category) {
		return Detail{}, console.InvalidArgument("category", "The category is not valid.")
	}
	body, bodyErr := parseBody(params.Body)
	if bodyErr != nil {
		return Detail{}, bodyErr
	}

	tx, err := s.db.Begin(ctx)
	if err != nil {
		return Detail{}, console.RequestUnavailable("begin create ticket", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	qtx := s.queries.WithTx(tx)

	row, err := qtx.CreateTicket(ctx, sqlc.CreateTicketParams{
		Uid:      pgUUID(uuid.New()),
		UserID:   params.UserID,
		Subject:  subject,
		Category: params.Category,
	})
	if err != nil {
		return Detail{}, console.RequestUnavailable("create ticket", err)
	}
	if _, svcErr := s.appendMessage(ctx, qtx, row.ID, params.UserID, body); svcErr != nil {
		return Detail{}, svcErr
	}
	detail, svcErr := s.loadDetail(ctx, qtx, ticketFrom(row), row.ID)
	if svcErr != nil {
		return Detail{}, svcErr
	}
	if err := tx.Commit(ctx); err != nil {
		return Detail{}, console.RequestUnavailable("commit create ticket", err)
	}
	s.notifyAdmin(ctx, detail.Ticket, params.UserID, true)
	return detail, nil
}

// ReplyParams 是回复参数。
type ReplyParams struct {
	UserID int64
	UID    uuid.UUID
	Body   []byte
}

// Reply 追加用户回复：closed 拒绝；pending/resolved 自动回到 open（重开）。
func (s *Service) Reply(ctx context.Context, params ReplyParams) (Detail, *console.Error) {
	body, bodyErr := parseBody(params.Body)
	if bodyErr != nil {
		return Detail{}, bodyErr
	}

	tx, err := s.db.Begin(ctx)
	if err != nil {
		return Detail{}, console.RequestUnavailable("begin ticket reply", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	qtx := s.queries.WithTx(tx)

	current, err := qtx.GetConsoleTicket(ctx, sqlc.GetConsoleTicketParams{
		Uid:    pgUUID(params.UID),
		UserID: params.UserID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return Detail{}, ticketNotFound()
	}
	if err != nil {
		return Detail{}, console.RequestUnavailable("get console ticket", err)
	}
	if current.Status == coreticket.StatusClosed {
		return Detail{}, ticketClosed()
	}
	updated, err := qtx.TouchTicketOnUserMessage(ctx, current.ID)
	if errors.Is(err, pgx.ErrNoRows) {
		return Detail{}, ticketClosed()
	}
	if err != nil {
		return Detail{}, console.RequestUnavailable("advance ticket on user message", err)
	}
	if _, svcErr := s.appendMessage(ctx, qtx, updated.ID, params.UserID, body); svcErr != nil {
		return Detail{}, svcErr
	}
	detail, svcErr := s.loadDetail(ctx, qtx, ticketFrom(updated), updated.ID)
	if svcErr != nil {
		return Detail{}, svcErr
	}
	if err := tx.Commit(ctx); err != nil {
		return Detail{}, console.RequestUnavailable("commit ticket reply", err)
	}
	s.notifyAdmin(ctx, detail.Ticket, params.UserID, false)
	return detail, nil
}

// Close 关闭本人工单；重复关闭幂等返回当前状态。
func (s *Service) Close(ctx context.Context, userID int64, uid uuid.UUID) (Ticket, *console.Error) {
	row, err := s.queries.CloseConsoleTicket(ctx, sqlc.CloseConsoleTicketParams{
		Uid:    pgUUID(uid),
		UserID: userID,
	})
	if err == nil {
		return ticketFrom(row), nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return Ticket{}, console.RequestUnavailable("close console ticket", err)
	}
	// 没有命中更新：要么工单不存在，要么已经是 closed（幂等返回现状）。
	current, getErr := s.queries.GetConsoleTicket(ctx, sqlc.GetConsoleTicketParams{
		Uid:    pgUUID(uid),
		UserID: userID,
	})
	if errors.Is(getErr, pgx.ErrNoRows) {
		return Ticket{}, ticketNotFound()
	}
	if getErr != nil {
		return Ticket{}, console.RequestUnavailable("get console ticket", getErr)
	}
	return ticketFrom(current), nil
}

// TicketSummary 返回侧栏角标数据（未关闭工单数与未读数）。
func (s *Service) TicketSummary(ctx context.Context, userID int64) (Summary, *console.Error) {
	row, err := s.queries.SummarizeConsoleTickets(ctx, userID)
	if err != nil {
		return Summary{}, console.RequestUnavailable("summarize console tickets", err)
	}
	return Summary{ActiveTotal: row.ActiveTotal, UnreadTotal: row.UnreadTotal}, nil
}

// UploadParams 是附件上传参数；MIME 以服务端嗅探结果为准，不信任客户端声明。
type UploadParams struct {
	UserID   int64
	FileName string
	Data     []byte
}

// CreateAttachment 写入孤儿附件（提交消息时按正文引用绑定）。
func (s *Service) CreateAttachment(ctx context.Context, params UploadParams) (Attachment, *console.Error) {
	if len(params.Data) == 0 {
		return Attachment{}, &console.Error{
			Code: CodeAttachmentInvalid, Message: "The uploaded file is empty.", Status: http.StatusBadRequest,
		}
	}
	if len(params.Data) > coreticket.MaxAttachmentBytes {
		return Attachment{}, &console.Error{
			Code: CodeAttachmentInvalid, Message: "The image exceeds the 5MB size limit.", Status: http.StatusBadRequest,
		}
	}
	mimeType := http.DetectContentType(params.Data)
	if !coreticket.AllowedImageMIMEs[mimeType] {
		return Attachment{}, &console.Error{
			Code: CodeAttachmentInvalid, Message: "Only PNG, JPEG, WebP and GIF images are allowed.", Status: http.StatusBadRequest,
		}
	}
	orphans, err := s.queries.CountUserOrphanTicketAttachments(ctx, pgtype.Int8{Int64: params.UserID, Valid: true})
	if err != nil {
		return Attachment{}, console.RequestUnavailable("count orphan ticket attachments", err)
	}
	if orphans >= coreticket.MaxOrphanAttachmentsPerUser {
		return Attachment{}, &console.Error{
			Code:    CodeAttachmentQuota,
			Message: "Too many pending uploads. Please submit or discard your draft first.",
			Status:  http.StatusTooManyRequests,
		}
	}
	row, err := s.queries.CreateTicketAttachment(ctx, sqlc.CreateTicketAttachmentParams{
		Uid:          pgUUID(uuid.New()),
		UserID:       pgtype.Int8{Int64: params.UserID, Valid: true},
		UploaderType: coreticket.AuthorUser,
		FileName:     cleanFileName(params.FileName),
		MimeType:     mimeType,
		SizeBytes:    int32(len(params.Data)),
		Data:         params.Data,
	})
	if err != nil {
		return Attachment{}, console.RequestUnavailable("create ticket attachment", err)
	}
	return s.attachmentFromMeta(row.Uid, row.FileName, row.MimeType, row.SizeBytes), nil
}

// LoadAttachment 校验签名后返回附件内容（公开下载端点，签名即鉴权）。
func (s *Service) LoadAttachment(ctx context.Context, uid uuid.UUID, expiresAt int64, signature string) (AttachmentContent, *console.Error) {
	if !s.signer.Verify(uid, expiresAt, signature, s.now()) {
		return AttachmentContent{}, &console.Error{
			Code:    CodeAttachmentLinkInvalid,
			Message: "The attachment link is invalid or has expired.",
			Status:  http.StatusForbidden,
		}
	}
	row, err := s.queries.GetTicketAttachmentData(ctx, pgUUID(uid))
	if errors.Is(err, pgx.ErrNoRows) {
		return AttachmentContent{}, &console.Error{
			Code: CodeTicketNotFound, Message: "The attachment was not found.", Status: http.StatusNotFound,
		}
	}
	if err != nil {
		return AttachmentContent{}, console.RequestUnavailable("load ticket attachment", err)
	}
	return AttachmentContent{
		FileName:  row.FileName,
		MimeType:  row.MimeType,
		SizeBytes: row.SizeBytes,
		Data:      row.Data,
	}, nil
}

// appendMessage 在事务内追加消息并按正文引用绑定本人附件。
func (s *Service) appendMessage(ctx context.Context, qtx *sqlc.Queries, ticketID, userID int64, body coreticket.Body) (sqlc.TicketMessage, *console.Error) {
	message, err := qtx.CreateTicketMessage(ctx, sqlc.CreateTicketMessageParams{
		TicketID:   ticketID,
		AuthorType: coreticket.AuthorUser,
		Body:       body.JSON,
		BodyText:   body.Text,
	})
	if err != nil {
		return sqlc.TicketMessage{}, console.RequestUnavailable("create ticket message", err)
	}
	if len(body.AttachmentUIDs) == 0 {
		return message, nil
	}
	uids := make([]pgtype.UUID, 0, len(body.AttachmentUIDs))
	for _, uid := range body.AttachmentUIDs {
		uids = append(uids, pgUUID(uid))
	}
	bound, err := qtx.BindUserTicketAttachments(ctx, sqlc.BindUserTicketAttachmentsParams{
		TicketID:  pgtype.Int8{Int64: ticketID, Valid: true},
		MessageID: pgtype.Int8{Int64: message.ID, Valid: true},
		Uids:      uids,
		UserID:    pgtype.Int8{Int64: userID, Valid: true},
	})
	if err != nil {
		return sqlc.TicketMessage{}, console.RequestUnavailable("bind ticket attachments", err)
	}
	if bound != int64(len(body.AttachmentUIDs)) {
		// 引用了不存在 / 已被使用 / 不属于本人的附件：整个事务回滚。
		return sqlc.TicketMessage{}, &console.Error{
			Code:    CodeAttachmentInvalid,
			Message: "The message references attachments that are not available.",
			Status:  http.StatusBadRequest,
		}
	}
	return message, nil
}

// loadDetail 在给定 store（可以是事务句柄）上装配详情。
func (s *Service) loadDetail(ctx context.Context, q *sqlc.Queries, ticket Ticket, ticketID int64) (Detail, *console.Error) {
	messageRows, err := q.ListTicketMessages(ctx, ticketID)
	if err != nil {
		return Detail{}, console.RequestUnavailable("list ticket messages", err)
	}
	attachmentRows, err := q.ListTicketAttachmentsMeta(ctx, pgtype.Int8{Int64: ticketID, Valid: true})
	if err != nil {
		return Detail{}, console.RequestUnavailable("list ticket attachments", err)
	}
	messages := make([]Message, 0, len(messageRows))
	for _, row := range messageRows {
		messages = append(messages, Message{
			ID:         row.ID,
			AuthorType: row.AuthorType,
			Body:       json.RawMessage(row.Body),
			CreatedAt:  row.CreatedAt.Time,
		})
	}
	attachments := make([]Attachment, 0, len(attachmentRows))
	for _, row := range attachmentRows {
		attachments = append(attachments, s.attachmentFromMeta(row.Uid, row.FileName, row.MimeType, row.SizeBytes))
	}
	return Detail{Ticket: ticket, Messages: messages, Attachments: attachments}, nil
}

func (s *Service) attachmentFromMeta(uid pgtype.UUID, fileName, mimeType string, sizeBytes int32) Attachment {
	id := uuid.UUID(uid.Bytes)
	return Attachment{
		UID:       id,
		FileName:  fileName,
		MimeType:  mimeType,
		SizeBytes: sizeBytes,
		URL:       s.signer.SignedPath(id, s.now(), coreticket.DefaultAttachmentURLTTL),
	}
}

func parseBody(raw []byte) (coreticket.Body, *console.Error) {
	body, err := coreticket.ParseBody(raw)
	if err == nil {
		return body, nil
	}
	if errors.Is(err, coreticket.ErrBodyInvalid) {
		return coreticket.Body{}, console.InvalidArgument("body", err.Error())
	}
	return coreticket.Body{}, console.RequestUnavailable("parse ticket body", err)
}

func ticketFrom(row sqlc.Ticket) Ticket {
	t := Ticket{
		Item: Item{
			UID:           uuid.UUID(row.Uid.Bytes),
			Subject:       row.Subject,
			Category:      row.Category,
			Status:        row.Status,
			UserUnread:    row.UserUnread,
			LastMessageAt: row.LastMessageAt.Time,
			CreatedAt:     row.CreatedAt.Time,
		},
		UpdatedAt: row.UpdatedAt.Time,
	}
	if row.ResolvedAt.Valid {
		resolvedAt := row.ResolvedAt.Time
		t.ResolvedAt = &resolvedAt
	}
	if row.ClosedAt.Valid {
		closedAt := row.ClosedAt.Time
		t.ClosedAt = &closedAt
	}
	return t
}

// 通知正文里的分类中文名（Admin 消息中心是中文界面）。
var categoryLabelsZH = map[string]string{
	coreticket.CategoryBilling: "账务",
	coreticket.CategoryAPI:     "API 使用",
	coreticket.CategoryModel:   "模型",
	coreticket.CategoryAccount: "账号",
	coreticket.CategoryOther:   "其他",
}

// notifyAdmin 在提交成功后把工单动态写入 Admin 消息中心（弱信号，失败只记日志）。
// 回复用未读去重（同一工单在上一条提醒被读掉前不重复轰炸）；新工单每张都提醒。
func (s *Service) notifyAdmin(ctx context.Context, ticket Ticket, userID int64, isNew bool) {
	if s.notifier == nil {
		return
	}
	userLabel := fmt.Sprintf("用户 #%d", userID)
	if row, err := s.queries.GetUserByID(ctx, userID); err == nil {
		userLabel = row.Email
	}
	title := "工单有新回复：" + ticket.Subject
	dedupeKey := "ticket-reply:" + ticket.UID.String()
	if isNew {
		title = "新工单：" + ticket.Subject
		dedupeKey = ""
	}
	category := categoryLabelsZH[ticket.Category]
	if category == "" {
		category = ticket.Category
	}
	_, err := s.notifier.Publish(ctx, adminmessage.PublishParams{
		Severity:  adminmessage.SeverityInfo,
		Topic:     "ticket",
		Title:     title,
		Body:      fmt.Sprintf("用户：%s\n分类：%s\n工单编号：%s", userLabel, category, ticket.UID),
		Source:    "console-ticket",
		DedupeKey: dedupeKey,
	})
	if err != nil {
		s.logger.Warn("publish ticket admin message failed",
			zap.String("ticket_uid", ticket.UID.String()), zap.Error(err))
	}
}

func ticketNotFound() *console.Error {
	return &console.Error{Code: CodeTicketNotFound, Message: "The ticket was not found.", Status: http.StatusNotFound}
}

func ticketClosed() *console.Error {
	return &console.Error{
		Code:    CodeTicketClosed,
		Message: "The ticket is closed and no longer accepts replies.",
		Status:  http.StatusConflict,
	}
}

func cleanFileName(name string) string {
	name = strings.TrimSpace(filepath.Base(name))
	if name == "" || name == "." || name == string(filepath.Separator) {
		return "image"
	}
	if utf8.RuneCountInString(name) > 200 {
		runes := []rune(name)
		name = string(runes[len(runes)-200:])
	}
	return name
}

func pgUUID(uid uuid.UUID) pgtype.UUID {
	return pgtype.UUID{Bytes: uid, Valid: true}
}
