-- 工单跨 surface 共用查询：消息与附件的读写由 Console 与 Admin 两侧共同使用，
-- 归属与状态校验在各自 service 层完成后才会调到这里。

-- name: CreateTicketMessage :one
-- CreateTicketMessage 追加一条工单消息（对话流按 id 升序即时间序）。
INSERT INTO ticket_messages (ticket_id, author_type, body, body_text)
VALUES (sqlc.arg(ticket_id), sqlc.arg(author_type), sqlc.arg(body), sqlc.arg(body_text))
RETURNING id, ticket_id, author_type, body, body_text, created_at;

-- name: ListTicketMessages :many
-- ListTicketMessages 取某工单全部消息（升序），详情页对话流用。
SELECT id, ticket_id, author_type, body, body_text, created_at
FROM ticket_messages
WHERE ticket_id = sqlc.arg(ticket_id)
ORDER BY id;

-- name: CreateTicketAttachment :one
-- CreateTicketAttachment 写入孤儿附件（ticket_id / message_id 为 NULL），提交消息时再绑定。
INSERT INTO ticket_attachments (uid, user_id, uploader_type, file_name, mime_type, size_bytes, data)
VALUES (
    sqlc.arg(uid), sqlc.narg(user_id), sqlc.arg(uploader_type),
    sqlc.arg(file_name), sqlc.arg(mime_type), sqlc.arg(size_bytes), sqlc.arg(data)
)
RETURNING id, uid, ticket_id, message_id, uploader_type, file_name, mime_type, size_bytes, created_at;

-- name: GetTicketAttachmentData :one
-- GetTicketAttachmentData 按 uid 取附件内容（签名下载端点用；签名即鉴权，不查归属）。
SELECT uid, file_name, mime_type, size_bytes, data
FROM ticket_attachments
WHERE uid = sqlc.arg(uid);

-- name: ListTicketAttachmentsMeta :many
-- ListTicketAttachmentsMeta 取某工单全部已绑定附件的元数据（不含二进制），详情页拼签名 URL 用。
SELECT id, uid, ticket_id, message_id, uploader_type, file_name, mime_type, size_bytes, created_at
FROM ticket_attachments
WHERE ticket_id = sqlc.arg(ticket_id) AND message_id IS NOT NULL
ORDER BY id;

-- name: BindUserTicketAttachments :execrows
-- BindUserTicketAttachments 把用户本人的孤儿附件绑定到消息；返回实际绑定条数，
-- 少于入参个数说明存在无效引用（不存在 / 已绑定 / 不属于该用户），由调用方在事务里回滚。
UPDATE ticket_attachments
SET ticket_id = sqlc.arg(ticket_id), message_id = sqlc.arg(message_id)
WHERE uid = ANY(sqlc.arg(uids)::uuid[])
  AND message_id IS NULL
  AND uploader_type = 'user'
  AND user_id = sqlc.arg(user_id);

-- name: BindAdminTicketAttachments :execrows
-- BindAdminTicketAttachments 把运营上传的孤儿附件绑定到消息，语义同上。
UPDATE ticket_attachments
SET ticket_id = sqlc.arg(ticket_id), message_id = sqlc.arg(message_id)
WHERE uid = ANY(sqlc.arg(uids)::uuid[])
  AND message_id IS NULL
  AND uploader_type = 'admin';

-- name: CountUserOrphanTicketAttachments :one
-- CountUserOrphanTicketAttachments 统计用户未绑定附件数（上传配额检查，命中孤儿部分索引）。
SELECT COUNT(*) AS total
FROM ticket_attachments
WHERE user_id = sqlc.arg(user_id) AND message_id IS NULL;
