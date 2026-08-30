// Package ticket 暴露 Console 侧的工单自助接口。
//
// 归属由 handler 强制：UserID 一律取自会话 principal，从不接受请求体或查询参数里的
// user_id。附件下载是本模块唯一的公开路由——<img> 带不上 Cookie，访问控制由短时效
// HMAC 签名承载（见 core/ticket.AttachmentSigner），因此模块整体挂载在认证分组之外，
// 并在内部为其余路由自行套 RequireAuth。
package ticket

import (
	"context"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	consoleauth "github.com/ThankCat/unio-gateway/internal/app/consoleapi/auth"
	"github.com/ThankCat/unio-gateway/internal/app/consoleapi/transport"
	consoleservice "github.com/ThankCat/unio-gateway/internal/service/console"
	consoleticket "github.com/ThankCat/unio-gateway/internal/service/console/ticket"
)

// Service 定义 HTTP 适配层依赖的工单能力。
type Service interface {
	List(context.Context, consoleticket.ListParams) ([]consoleticket.Item, int64, *consoleservice.Error)
	Get(ctx context.Context, userID int64, uid uuid.UUID) (consoleticket.Detail, *consoleservice.Error)
	Create(context.Context, consoleticket.CreateParams) (consoleticket.Detail, *consoleservice.Error)
	Reply(context.Context, consoleticket.ReplyParams) (consoleticket.Detail, *consoleservice.Error)
	Close(ctx context.Context, userID int64, uid uuid.UUID) (consoleticket.Ticket, *consoleservice.Error)
	TicketSummary(ctx context.Context, userID int64) (consoleticket.Summary, *consoleservice.Error)
	CreateAttachment(context.Context, consoleticket.UploadParams) (consoleticket.Attachment, *consoleservice.Error)
	LoadAttachment(ctx context.Context, uid uuid.UUID, expiresAt int64, signature string) (consoleticket.AttachmentContent, *consoleservice.Error)
}

var _ Service = (*consoleticket.Service)(nil)

// Deps 包含工单 HTTP 适配层的依赖。AuthService 用于模块内部挂 RequireAuth。
type Deps struct {
	Service     Service
	AuthService consoleauth.Service
	ErrorWriter transport.ErrorWriter
}

// Register 将工单路由挂载到 /tickets（本模块自行管理认证分组）。
func Register(r chi.Router, deps Deps) {
	h := &handler{
		service:     deps.Service,
		errorWriter: deps.ErrorWriter,
	}
	r.Route("/tickets", func(r chi.Router) {
		// 公开：签名即鉴权（详情响应里的 URL 自带 exp/sig）。
		r.Get("/attachments/{uid}", h.downloadAttachment)

		r.Group(func(r chi.Router) {
			r.Use(consoleauth.RequireAuth(deps.AuthService, deps.ErrorWriter))
			r.Get("/", h.list)
			r.Post("/", h.create)
			r.Get("/summary", h.summary)
			r.Post("/attachments", h.uploadAttachment)
			r.Get("/{uid}", h.get)
			r.Post("/{uid}/messages", h.reply)
			r.Post("/{uid}/close", h.closeTicket)
		})
	})
}

type handler struct {
	service     Service
	errorWriter transport.ErrorWriter
}
