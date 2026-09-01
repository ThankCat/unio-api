// Package emaillog 提供 Admin 客户中心「邮件」列表的只读查询（email_messages 发送记录）。
//
// 记录是发送事实日志：status=sent 只代表 SMTP 服务商接受提交，不代表进入收件箱。
package emaillog

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/ThankCat/unio-gateway/internal/platform/failure"
	"github.com/ThankCat/unio-gateway/internal/platform/store/sqlc"
)

// Store 定义查询所需的最小存储能力（由 *sqlc.Queries 实现）。
type Store interface {
	ListEmailMessages(ctx context.Context, arg sqlc.ListEmailMessagesParams) ([]sqlc.ListEmailMessagesRow, error)
	CountEmailMessages(ctx context.Context, arg sqlc.CountEmailMessagesParams) (int64, error)
	GetEmailMessage(ctx context.Context, id int64) (sqlc.EmailMessage, error)
}

// Service 是邮件发送记录查询服务。
type Service struct {
	store Store
}

// NewService 创建查询服务。
func NewService(store Store) *Service {
	if store == nil {
		panic("emaillog: store is required")
	}
	return &Service{store: store}
}

// ListParams 是列表查询参数；零值字段表示不过滤。
type ListParams struct {
	EmailType string
	Status    string
	Recipient string
	From      *time.Time
	To        *time.Time
	Limit     int32
	Offset    int32
}

// Item 是列表项（不含正文，正文较大只在详情返回）。
type Item struct {
	ID           int64
	EmailType    string
	Recipient    string
	Sender       string
	Subject      string
	Status       string
	ErrorSummary *string
	Locale       string
	DurationMs   *int32
	SentAt       *time.Time
	CreatedAt    time.Time
}

// Detail 是单条记录详情（含 HTML 正文，供 Admin 隔离预览）。
type Detail struct {
	Item
	BodyHTML string
}

// List 分页返回发送记录（新记录在前）与同口径总数。
func (s *Service) List(ctx context.Context, params ListParams) ([]Item, int64, error) {
	if params.Limit <= 0 {
		params.Limit = 20
	}
	listArgs := sqlc.ListEmailMessagesParams{
		EmailType:  textNarg(params.EmailType),
		Status:     textNarg(params.Status),
		Recipient:  textNarg(params.Recipient),
		FromTime:   timeNarg(params.From),
		ToTime:     timeNarg(params.To),
		PageLimit:  params.Limit,
		PageOffset: params.Offset,
	}
	rows, err := s.store.ListEmailMessages(ctx, listArgs)
	if err != nil {
		return nil, 0, storeFailed(err, "list email messages")
	}
	total, err := s.store.CountEmailMessages(ctx, sqlc.CountEmailMessagesParams{
		EmailType: listArgs.EmailType,
		Status:    listArgs.Status,
		Recipient: listArgs.Recipient,
		FromTime:  listArgs.FromTime,
		ToTime:    listArgs.ToTime,
	})
	if err != nil {
		return nil, 0, storeFailed(err, "count email messages")
	}
	items := make([]Item, 0, len(rows))
	for _, row := range rows {
		items = append(items, Item{
			ID:           row.ID,
			EmailType:    row.EmailType,
			Recipient:    row.Recipient,
			Sender:       row.Sender,
			Subject:      row.Subject,
			Status:       row.Status,
			ErrorSummary: textPtr(row.ErrorSummary),
			Locale:       row.Locale,
			DurationMs:   int4Ptr(row.DurationMs),
			SentAt:       timePtr(row.SentAt),
			CreatedAt:    row.CreatedAt.Time,
		})
	}
	return items, total, nil
}

// Get 返回单条记录详情；不存在时返回 admin_not_found。
func (s *Service) Get(ctx context.Context, id int64) (Detail, error) {
	row, err := s.store.GetEmailMessage(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return Detail{}, failure.New(failure.CodeAdminNotFound, failure.WithMessage("email message not found"))
	}
	if err != nil {
		return Detail{}, storeFailed(err, "get email message")
	}
	return Detail{
		Item: Item{
			ID:           row.ID,
			EmailType:    row.EmailType,
			Recipient:    row.Recipient,
			Sender:       row.Sender,
			Subject:      row.Subject,
			Status:       row.Status,
			ErrorSummary: textPtr(row.ErrorSummary),
			Locale:       row.Locale,
			DurationMs:   int4Ptr(row.DurationMs),
			SentAt:       timePtr(row.SentAt),
			CreatedAt:    row.CreatedAt.Time,
		},
		BodyHTML: row.BodyHtml,
	}, nil
}

func textNarg(value string) pgtype.Text {
	if value == "" {
		return pgtype.Text{}
	}
	return pgtype.Text{String: value, Valid: true}
}

func timeNarg(value *time.Time) pgtype.Timestamptz {
	if value == nil {
		return pgtype.Timestamptz{}
	}
	return pgtype.Timestamptz{Time: *value, Valid: true}
}

func textPtr(value pgtype.Text) *string {
	if !value.Valid {
		return nil
	}
	s := value.String
	return &s
}

func int4Ptr(value pgtype.Int4) *int32 {
	if !value.Valid {
		return nil
	}
	v := value.Int32
	return &v
}

func timePtr(value pgtype.Timestamptz) *time.Time {
	if !value.Valid {
		return nil
	}
	t := value.Time
	return &t
}

func storeFailed(err error, message string) error {
	return failure.Wrap(failure.CodeAdminStoreFailed, err, failure.WithMessage(message))
}
