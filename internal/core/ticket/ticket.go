// Package ticket 是用户反馈工单的核心领域：状态机常量、消息正文（Tiptap JSON）的
// 白名单校验与规范化、附件下载 URL 的 HMAC 签名。
//
// Console 与 Admin 两侧的 service 共用本包；归属与权限校验不在这里，由各 surface 完成。
package ticket

// 工单状态机（与 tickets.status CHECK 一致）：
// open（待客服）→ pending（待用户）→ resolved（已解决，可回复重开）→ closed（终态）。
const (
	StatusOpen     = "open"
	StatusPending  = "pending"
	StatusResolved = "resolved"
	StatusClosed   = "closed"
)

// 问题分类（与 tickets.category CHECK 一致）。
const (
	CategoryBilling = "billing"
	CategoryAPI     = "api"
	CategoryModel   = "model"
	CategoryAccount = "account"
	CategoryOther   = "other"
)

// 发言方（与 ticket_messages.author_type CHECK 一致）。
const (
	AuthorUser  = "user"
	AuthorAdmin = "admin"
)

// 主题与附件的服务层约束；attachment 表的 CHECK 是双保险。
const (
	MaxSubjectChars = 200
	// MaxAttachmentBytes 单张图片体积上限（5MB）。
	MaxAttachmentBytes = 5 * 1024 * 1024
	// MaxOrphanAttachmentsPerUser 用户同时存在的未绑定附件上限，防滥用。
	MaxOrphanAttachmentsPerUser = 30
)

// AllowedImageMIMEs 附件 MIME 白名单（与表 CHECK 一致）；服务层还会做内容嗅探二次确认。
var AllowedImageMIMEs = map[string]bool{
	"image/png":  true,
	"image/jpeg": true,
	"image/webp": true,
	"image/gif":  true,
}

// ValidStatus 判断状态筛选值是否合法。
func ValidStatus(s string) bool {
	switch s {
	case StatusOpen, StatusPending, StatusResolved, StatusClosed:
		return true
	}
	return false
}

// ValidCategory 判断分类是否合法。
func ValidCategory(s string) bool {
	switch s {
	case CategoryBilling, CategoryAPI, CategoryModel, CategoryAccount, CategoryOther:
		return true
	}
	return false
}
