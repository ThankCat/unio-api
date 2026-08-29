// Package adminmessage 编排 Admin 站内消息中心（告警通道 MVP）。
//
// 写入方是 worker / 服务端（Publish，不经 HTTP），消费方是管理台（列表、未读数、标记已读）。
// 消息只追加 + 标记已读，不提供删除；dedupe_key 让周期性告警在上一条未读被处理前不重复轰炸。
package adminmessage

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/ThankCat/unio-gateway/internal/platform/failure"
	"github.com/ThankCat/unio-gateway/internal/platform/store/sqlc"
	"github.com/ThankCat/unio-gateway/internal/service/admin/opsutil"
)

// 告警级别：info=一般通知 / warning=需关注 / critical=需立即处理（与表 CHECK 约束一致）。
const (
	SeverityInfo     = "info"
	SeverityWarning  = "warning"
	SeverityCritical = "critical"
)

// Store 定义消息中心所需的存储能力，由 *sqlc.Queries 满足。
type Store interface {
	CreateAdminMessage(ctx context.Context, arg sqlc.CreateAdminMessageParams) (int64, error)
	ListAdminMessagesPage(ctx context.Context, arg sqlc.ListAdminMessagesPageParams) ([]sqlc.AdminMessage, error)
	CountAdminMessages(ctx context.Context, arg sqlc.CountAdminMessagesParams) (int64, error)
	CountUnreadAdminMessages(ctx context.Context) (int64, error)
	MarkAdminMessageRead(ctx context.Context, id int64) (sqlc.AdminMessage, error)
	MarkAllAdminMessagesRead(ctx context.Context) (int64, error)
}

type Service struct {
	store Store
}

func NewService(store Store) *Service {
	if store == nil {
		panic("adminmessage: store is required")
	}
	return &Service{store: store}
}

// Message 是消息中心对外的领域 DTO。
type Message struct {
	ID        int64
	Severity  string
	Topic     string
	Title     string
	Body      string
	Source    string
	CreatedAt time.Time
	ReadAt    *time.Time
}

// PublishParams 是 worker / 服务端写入消息的参数。
type PublishParams struct {
	Severity string
	Topic    string
	Title    string
	Body     string
	Source   string
	// DedupeKey 非空时启用未读去重：同 key 存在未读消息则本次写入被跳过（Publish 返回 false）。
	DedupeKey string
}

// Publish 写入一条站内消息；返回是否真正落库（false = 命中未读去重被跳过）。
func (s *Service) Publish(ctx context.Context, params PublishParams) (bool, error) {
	severity := strings.TrimSpace(params.Severity)
	switch severity {
	case SeverityInfo, SeverityWarning, SeverityCritical:
	default:
		return false, invalidArgument("severity", "severity must be one of info/warning/critical")
	}
	topic := strings.TrimSpace(params.Topic)
	if topic == "" {
		return false, invalidArgument("topic", "topic must not be empty")
	}
	title := strings.TrimSpace(params.Title)
	if title == "" {
		return false, invalidArgument("title", "title must not be empty")
	}
	body := strings.TrimSpace(params.Body)
	if body == "" {
		return false, invalidArgument("body", "body must not be empty")
	}
	source := strings.TrimSpace(params.Source)
	if source == "" {
		return false, invalidArgument("source", "source must not be empty")
	}

	rows, err := s.store.CreateAdminMessage(ctx, sqlc.CreateAdminMessageParams{
		Severity:  severity,
		Topic:     topic,
		Title:     title,
		Body:      body,
		Source:    source,
		DedupeKey: opsutil.TextNarg(strings.TrimSpace(params.DedupeKey)),
	})
	if err != nil {
		return false, storeFailed(err, "create admin message")
	}
	return rows > 0, nil
}

// ListParams 是消息列表查询参数。
type ListParams struct {
	UnreadOnly bool
	Topic      string
	Limit      int32
	Offset     int32
}

// List 分页列出消息（新消息在前），返回列表与同口径总数。
func (s *Service) List(ctx context.Context, params ListParams) ([]Message, int64, error) {
	if params.Limit <= 0 {
		params.Limit = 20
	}
	topic := opsutil.TextNarg(strings.TrimSpace(params.Topic))
	rows, err := s.store.ListAdminMessagesPage(ctx, sqlc.ListAdminMessagesPageParams{
		UnreadOnly: params.UnreadOnly,
		Topic:      topic,
		PageLimit:  params.Limit,
		PageOffset: params.Offset,
	})
	if err != nil {
		return nil, 0, storeFailed(err, "list admin messages")
	}
	total, err := s.store.CountAdminMessages(ctx, sqlc.CountAdminMessagesParams{
		UnreadOnly: params.UnreadOnly,
		Topic:      topic,
	})
	if err != nil {
		return nil, 0, storeFailed(err, "count admin messages")
	}
	out := make([]Message, 0, len(rows))
	for _, row := range rows {
		out = append(out, messageFrom(row))
	}
	return out, total, nil
}

// UnreadCount 返回未读总数（顶栏铃铛轮询）。
func (s *Service) UnreadCount(ctx context.Context) (int64, error) {
	total, err := s.store.CountUnreadAdminMessages(ctx)
	if err != nil {
		return 0, storeFailed(err, "count unread admin messages")
	}
	return total, nil
}

// MarkRead 幂等标记单条消息已读；消息不存在时返回 admin_not_found。
func (s *Service) MarkRead(ctx context.Context, id int64) (Message, error) {
	if id <= 0 {
		return Message{}, invalidArgument("id", "id must be greater than zero")
	}
	row, err := s.store.MarkAdminMessageRead(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return Message{}, failure.New(failure.CodeAdminNotFound, failure.WithMessage("message not found"))
	}
	if err != nil {
		return Message{}, storeFailed(err, "mark admin message read")
	}
	return messageFrom(row), nil
}

// MarkAllRead 把全部未读标记为已读，返回标记条数。
func (s *Service) MarkAllRead(ctx context.Context) (int64, error) {
	updated, err := s.store.MarkAllAdminMessagesRead(ctx)
	if err != nil {
		return 0, storeFailed(err, "mark all admin messages read")
	}
	return updated, nil
}

func messageFrom(row sqlc.AdminMessage) Message {
	msg := Message{
		ID:        row.ID,
		Severity:  row.Severity,
		Topic:     row.Topic,
		Title:     row.Title,
		Body:      row.Body,
		Source:    row.Source,
		CreatedAt: row.CreatedAt.Time,
	}
	if row.ReadAt.Valid {
		readAt := row.ReadAt.Time
		msg.ReadAt = &readAt
	}
	return msg
}

func invalidArgument(field, message string) error {
	return failure.New(failure.CodeAdminInvalidArgument, failure.WithMessage(message), failure.WithField("field", field))
}

func storeFailed(err error, message string) error {
	return failure.Wrap(failure.CodeAdminStoreFailed, err, failure.WithMessage(message))
}
