// Package ticket 编排 Admin 侧的工单运营：队列、详情、回复与状态流转。
//
// 单管理员模型：不记录具体坐席身份，author_type=admin 即客服发言。
// 正文与附件的校验规则与 Console 侧完全一致（core/ticket 白名单）。
package ticket

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	coreticket "github.com/ThankCat/unio-gateway/internal/core/ticket"
	"github.com/ThankCat/unio-gateway/internal/platform/failure"
	"github.com/ThankCat/unio-gateway/internal/platform/store/sqlc"
	"github.com/ThankCat/unio-gateway/internal/service/admin/opsutil"
)

// TxBeginner 提供事务能力（由 pgxpool 满足）：回复要求「消息落库 + 附件绑定 +
// 状态推进」同进同出。
type TxBeginner interface {
	Begin(ctx context.Context) (pgx.Tx, error)
}

// Service 编排 Admin 工单读写。
type Service struct {
	db      TxBeginner
	queries *sqlc.Queries
	signer  *coreticket.AttachmentSigner
	now     func() time.Time
}

// NewService 创建 Admin 工单服务。
func NewService(db TxBeginner, queries *sqlc.Queries, signer *coreticket.AttachmentSigner) *Service {
	if db == nil {
		panic("admin ticket: tx beginner is required")
	}
	if queries == nil {
		panic("admin ticket: queries is required")
	}
	if signer == nil {
		panic("admin ticket: attachment signer is required")
	}
	return &Service{db: db, queries: queries, signer: signer, now: time.Now}
}

// QueueItem 是队列页的工单行（带用户邮箱）。
type QueueItem struct {
	UID           uuid.UUID
	Subject       string
	Category      string
	Status        string
	AdminUnread   bool
	LastMessageAt time.Time
	CreatedAt     time.Time
	UserID        int64
	UserEmail     string
}

// Ticket 是详情页的工单头。
type Ticket struct {
	UID           uuid.UUID
	Subject       string
	Category      string
	Status        string
	AdminUnread   bool
	LastMessageAt time.Time
	ResolvedAt    *time.Time
	ClosedAt      *time.Time
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

// UserInfo 是详情页侧栏的用户信息。
type UserInfo struct {
	ID        int64
	Email     string
	CreatedAt time.Time
}

// Message 是对话流中的一条消息。
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

// Detail 是详情页数据。
type Detail struct {
	Ticket
	User        UserInfo
	Messages    []Message
	Attachments []Attachment
}

// AttachmentContent 是签名下载端点的响应内容。
type AttachmentContent struct {
	FileName  string
	MimeType  string
	SizeBytes int32
	Data      []byte
}

// ListParams 是队列查询参数。
type ListParams struct {
	Status   string
	Category string
	Search   string
	Limit    int32
	Offset   int32
}

// List 分页列出工单队列（最近更新在前）。
func (s *Service) List(ctx context.Context, params ListParams) ([]QueueItem, int64, error) {
	if params.Status != "" && !coreticket.ValidStatus(params.Status) {
		return nil, 0, invalidArgument("status", "status must be one of open/pending/resolved/closed")
	}
	if params.Category != "" && !coreticket.ValidCategory(params.Category) {
		return nil, 0, invalidArgument("category", "category is not valid")
	}
	if params.Limit <= 0 {
		params.Limit = 20
	}
	args := sqlc.ListAdminTicketsParams{
		Status:     opsutil.TextNarg(params.Status),
		Category:   opsutil.TextNarg(params.Category),
		Search:     opsutil.TextNarg(strings.TrimSpace(params.Search)),
		PageLimit:  params.Limit,
		PageOffset: params.Offset,
	}
	rows, err := s.queries.ListAdminTickets(ctx, args)
	if err != nil {
		return nil, 0, storeFailed(err, "list admin tickets")
	}
	total, err := s.queries.CountAdminTickets(ctx, sqlc.CountAdminTicketsParams{
		Status:   args.Status,
		Category: args.Category,
		Search:   args.Search,
	})
	if err != nil {
		return nil, 0, storeFailed(err, "count admin tickets")
	}
	items := make([]QueueItem, 0, len(rows))
	for _, row := range rows {
		items = append(items, QueueItem{
			UID:           uuid.UUID(row.Uid.Bytes),
			Subject:       row.Subject,
			Category:      row.Category,
			Status:        row.Status,
			AdminUnread:   row.AdminUnread,
			LastMessageAt: row.LastMessageAt.Time,
			CreatedAt:     row.CreatedAt.Time,
			UserID:        row.UserID,
			UserEmail:     row.UserEmail,
		})
	}
	return items, total, nil
}

// Get 返回工单详情，并顺带清除运营侧未读红点。
func (s *Service) Get(ctx context.Context, uid uuid.UUID) (Detail, error) {
	row, err := s.queries.GetAdminTicket(ctx, pgUUID(uid))
	if errors.Is(err, pgx.ErrNoRows) {
		return Detail{}, notFound()
	}
	if err != nil {
		return Detail{}, storeFailed(err, "get admin ticket")
	}
	if _, err := s.queries.ClearTicketAdminUnread(ctx, row.ID); err != nil {
		return Detail{}, storeFailed(err, "clear ticket admin unread")
	}
	ticket := ticketFromAdminRow(row)
	ticket.AdminUnread = false
	return s.loadDetail(ctx, s.queries, ticket, userInfoFrom(row), row.ID)
}

// ReplyParams 是客服回复参数。
type ReplyParams struct {
	UID  uuid.UUID
	Body []byte
}

// Reply 追加客服回复：closed 拒绝；工单转入 pending 并提醒用户。
func (s *Service) Reply(ctx context.Context, params ReplyParams) (Detail, error) {
	body, err := coreticket.ParseBody(params.Body)
	if err != nil {
		if errors.Is(err, coreticket.ErrBodyInvalid) {
			return Detail{}, invalidArgument("body", err.Error())
		}
		return Detail{}, storeFailed(err, "parse ticket body")
	}

	tx, err := s.db.Begin(ctx)
	if err != nil {
		return Detail{}, storeFailed(err, "begin admin ticket reply")
	}
	defer func() { _ = tx.Rollback(ctx) }()
	qtx := s.queries.WithTx(tx)

	current, err := qtx.GetAdminTicket(ctx, pgUUID(params.UID))
	if errors.Is(err, pgx.ErrNoRows) {
		return Detail{}, notFound()
	}
	if err != nil {
		return Detail{}, storeFailed(err, "get admin ticket")
	}
	if current.Status == coreticket.StatusClosed {
		return Detail{}, closedConflict()
	}
	updated, err := qtx.TouchTicketOnAdminMessage(ctx, current.ID)
	if errors.Is(err, pgx.ErrNoRows) {
		return Detail{}, closedConflict()
	}
	if err != nil {
		return Detail{}, storeFailed(err, "advance ticket on admin message")
	}
	message, err := qtx.CreateTicketMessage(ctx, sqlc.CreateTicketMessageParams{
		TicketID:   updated.ID,
		AuthorType: coreticket.AuthorAdmin,
		Body:       body.JSON,
		BodyText:   body.Text,
	})
	if err != nil {
		return Detail{}, storeFailed(err, "create ticket message")
	}
	if len(body.AttachmentUIDs) > 0 {
		uids := make([]pgtype.UUID, 0, len(body.AttachmentUIDs))
		for _, attachmentUID := range body.AttachmentUIDs {
			uids = append(uids, pgUUID(attachmentUID))
		}
		bound, err := qtx.BindAdminTicketAttachments(ctx, sqlc.BindAdminTicketAttachmentsParams{
			TicketID:  pgtype.Int8{Int64: updated.ID, Valid: true},
			MessageID: pgtype.Int8{Int64: message.ID, Valid: true},
			Uids:      uids,
		})
		if err != nil {
			return Detail{}, storeFailed(err, "bind ticket attachments")
		}
		if bound != int64(len(body.AttachmentUIDs)) {
			return Detail{}, invalidArgument("body", "message references attachments that are not available")
		}
	}
	ticket := ticketFrom(updated)
	detail, detailErr := s.loadDetail(ctx, qtx, ticket, userInfoFrom(current), updated.ID)
	if detailErr != nil {
		return Detail{}, detailErr
	}
	if err := tx.Commit(ctx); err != nil {
		return Detail{}, storeFailed(err, "commit admin ticket reply")
	}
	return detail, nil
}

// SetStatus 执行状态流转：resolved（标记解决）/ closed（关闭）/ open（重开）。
func (s *Service) SetStatus(ctx context.Context, uid uuid.UUID, target string) (Ticket, error) {
	var (
		row sqlc.Ticket
		err error
	)
	switch target {
	case coreticket.StatusResolved:
		row, err = s.queries.ResolveAdminTicket(ctx, pgUUID(uid))
	case coreticket.StatusClosed:
		row, err = s.queries.CloseAdminTicket(ctx, pgUUID(uid))
	case coreticket.StatusOpen:
		row, err = s.queries.ReopenAdminTicket(ctx, pgUUID(uid))
	default:
		return Ticket{}, invalidArgument("status", "status must be one of resolved/closed/open")
	}
	if err == nil {
		return ticketFrom(row), nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return Ticket{}, storeFailed(err, "set admin ticket status")
	}
	// 未命中更新：目标不存在，或当前状态不允许该流转。
	if _, getErr := s.queries.GetAdminTicket(ctx, pgUUID(uid)); errors.Is(getErr, pgx.ErrNoRows) {
		return Ticket{}, notFound()
	} else if getErr != nil {
		return Ticket{}, storeFailed(getErr, "get admin ticket")
	}
	return Ticket{}, failure.New(
		failure.CodeAdminConflict,
		failure.WithMessage("ticket status does not allow this transition"),
	)
}

// UploadParams 是客服附件上传参数。
type UploadParams struct {
	FileName string
	Data     []byte
}

// CreateAttachment 写入客服上传的孤儿附件。
func (s *Service) CreateAttachment(ctx context.Context, params UploadParams) (Attachment, error) {
	if len(params.Data) == 0 {
		return Attachment{}, invalidArgument("file", "uploaded file is empty")
	}
	if len(params.Data) > coreticket.MaxAttachmentBytes {
		return Attachment{}, invalidArgument("file", "image exceeds the 5MB size limit")
	}
	mimeType := http.DetectContentType(params.Data)
	if !coreticket.AllowedImageMIMEs[mimeType] {
		return Attachment{}, invalidArgument("file", "only PNG, JPEG, WebP and GIF images are allowed")
	}
	row, err := s.queries.CreateTicketAttachment(ctx, sqlc.CreateTicketAttachmentParams{
		Uid:          pgUUID(uuid.New()),
		UserID:       pgtype.Int8{},
		UploaderType: coreticket.AuthorAdmin,
		FileName:     cleanFileName(params.FileName),
		MimeType:     mimeType,
		SizeBytes:    int32(len(params.Data)),
		Data:         params.Data,
	})
	if err != nil {
		return Attachment{}, storeFailed(err, "create ticket attachment")
	}
	return s.attachmentFromMeta(row.Uid, row.FileName, row.MimeType, row.SizeBytes), nil
}

// LoadAttachment 校验签名后返回附件内容（公开下载端点，签名即鉴权）。
func (s *Service) LoadAttachment(ctx context.Context, uid uuid.UUID, expiresAt int64, signature string) (AttachmentContent, error) {
	if !s.signer.Verify(uid, expiresAt, signature, s.now()) {
		return AttachmentContent{}, failure.New(
			failure.CodeAdminAuthInvalidToken,
			failure.WithMessage("attachment link is invalid or has expired"),
		)
	}
	row, err := s.queries.GetTicketAttachmentData(ctx, pgUUID(uid))
	if errors.Is(err, pgx.ErrNoRows) {
		return AttachmentContent{}, notFound()
	}
	if err != nil {
		return AttachmentContent{}, storeFailed(err, "load ticket attachment")
	}
	return AttachmentContent{
		FileName:  row.FileName,
		MimeType:  row.MimeType,
		SizeBytes: row.SizeBytes,
		Data:      row.Data,
	}, nil
}

func (s *Service) loadDetail(ctx context.Context, q *sqlc.Queries, ticket Ticket, user UserInfo, ticketID int64) (Detail, error) {
	messageRows, err := q.ListTicketMessages(ctx, ticketID)
	if err != nil {
		return Detail{}, storeFailed(err, "list ticket messages")
	}
	attachmentRows, err := q.ListTicketAttachmentsMeta(ctx, pgtype.Int8{Int64: ticketID, Valid: true})
	if err != nil {
		return Detail{}, storeFailed(err, "list ticket attachments")
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
	return Detail{Ticket: ticket, User: user, Messages: messages, Attachments: attachments}, nil
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

func ticketFrom(row sqlc.Ticket) Ticket {
	t := Ticket{
		UID:           uuid.UUID(row.Uid.Bytes),
		Subject:       row.Subject,
		Category:      row.Category,
		Status:        row.Status,
		AdminUnread:   row.AdminUnread,
		LastMessageAt: row.LastMessageAt.Time,
		CreatedAt:     row.CreatedAt.Time,
		UpdatedAt:     row.UpdatedAt.Time,
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

func ticketFromAdminRow(row sqlc.GetAdminTicketRow) Ticket {
	t := Ticket{
		UID:           uuid.UUID(row.Uid.Bytes),
		Subject:       row.Subject,
		Category:      row.Category,
		Status:        row.Status,
		AdminUnread:   row.AdminUnread,
		LastMessageAt: row.LastMessageAt.Time,
		CreatedAt:     row.CreatedAt.Time,
		UpdatedAt:     row.UpdatedAt.Time,
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

func userInfoFrom(row sqlc.GetAdminTicketRow) UserInfo {
	return UserInfo{ID: row.UserID, Email: row.UserEmail, CreatedAt: row.UserCreatedAt.Time}
}

func notFound() error {
	return failure.New(failure.CodeAdminNotFound, failure.WithMessage("ticket not found"))
}

func closedConflict() error {
	return failure.New(failure.CodeAdminConflict, failure.WithMessage("ticket is closed and no longer accepts replies"))
}

func invalidArgument(field, message string) error {
	return failure.New(failure.CodeAdminInvalidArgument, failure.WithMessage(message), failure.WithField("field", field))
}

func storeFailed(err error, message string) error {
	return failure.Wrap(failure.CodeAdminStoreFailed, err, failure.WithMessage(message))
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
