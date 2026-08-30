package ticket

import (
	"encoding/json"
	"time"

	consoleticket "github.com/ThankCat/unio-gateway/internal/service/console/ticket"
)

type itemDTO struct {
	UID           string `json:"uid"`
	Subject       string `json:"subject"`
	Category      string `json:"category"`
	Status        string `json:"status"`
	UserUnread    bool   `json:"user_unread"`
	LastMessageAt string `json:"last_message_at"`
	CreatedAt     string `json:"created_at"`
}

type ticketDTO struct {
	itemDTO
	ResolvedAt *string `json:"resolved_at"`
	ClosedAt   *string `json:"closed_at"`
	UpdatedAt  string  `json:"updated_at"`
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
	// URL 是相对下载路径（含 exp/sig），前端拼各自的 API base。
	URL string `json:"url"`
}

type detailData struct {
	Ticket      ticketDTO       `json:"ticket"`
	Messages    []messageDTO    `json:"messages"`
	Attachments []attachmentDTO `json:"attachments"`
}

type listData struct {
	Items    []itemDTO `json:"items"`
	Page     int       `json:"page"`
	PageSize int       `json:"page_size"`
	Total    int64     `json:"total"`
}

type summaryData struct {
	ActiveTotal int64 `json:"active_total"`
	UnreadTotal int64 `json:"unread_total"`
}

type createRequest struct {
	Subject  string          `json:"subject"`
	Category string          `json:"category"`
	Body     json.RawMessage `json:"body"`
}

type replyRequest struct {
	Body json.RawMessage `json:"body"`
}

func toItemDTO(item consoleticket.Item) itemDTO {
	return itemDTO{
		UID:           item.UID.String(),
		Subject:       item.Subject,
		Category:      item.Category,
		Status:        item.Status,
		UserUnread:    item.UserUnread,
		LastMessageAt: rfc3339(item.LastMessageAt),
		CreatedAt:     rfc3339(item.CreatedAt),
	}
}

func toTicketDTO(ticket consoleticket.Ticket) ticketDTO {
	return ticketDTO{
		itemDTO:    toItemDTO(ticket.Item),
		ResolvedAt: rfc3339Ptr(ticket.ResolvedAt),
		ClosedAt:   rfc3339Ptr(ticket.ClosedAt),
		UpdatedAt:  rfc3339(ticket.UpdatedAt),
	}
}

func toDetailData(detail consoleticket.Detail) detailData {
	messages := make([]messageDTO, 0, len(detail.Messages))
	for _, message := range detail.Messages {
		messages = append(messages, messageDTO{
			ID:         message.ID,
			AuthorType: message.AuthorType,
			Body:       message.Body,
			CreatedAt:  rfc3339(message.CreatedAt),
		})
	}
	attachments := make([]attachmentDTO, 0, len(detail.Attachments))
	for _, attachment := range detail.Attachments {
		attachments = append(attachments, toAttachmentDTO(attachment))
	}
	return detailData{Ticket: toTicketDTO(detail.Ticket), Messages: messages, Attachments: attachments}
}

func toAttachmentDTO(attachment consoleticket.Attachment) attachmentDTO {
	return attachmentDTO{
		UID:       attachment.UID.String(),
		FileName:  attachment.FileName,
		MimeType:  attachment.MimeType,
		SizeBytes: attachment.SizeBytes,
		URL:       attachment.URL,
	}
}

func rfc3339(t time.Time) string {
	return t.UTC().Format(time.RFC3339)
}

func rfc3339Ptr(t *time.Time) *string {
	if t == nil {
		return nil
	}
	formatted := rfc3339(*t)
	return &formatted
}
