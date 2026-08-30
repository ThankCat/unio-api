-- 工单维护 worker 查询：超期自动关闭与孤儿附件清理。

-- name: AutoCloseResolvedTickets :execrows
-- AutoCloseResolvedTickets 把 resolved 且超期无新消息的工单批量关闭，返回关闭条数。
UPDATE tickets
SET status = 'closed', closed_at = now(), updated_at = now()
WHERE status = 'resolved' AND last_message_at < sqlc.arg(cutoff)
;

-- name: DeleteOrphanTicketAttachments :execrows
-- DeleteOrphanTicketAttachments 删除超期未绑定消息的孤儿附件（编辑中放弃的上传），返回删除条数。
DELETE FROM ticket_attachments
WHERE message_id IS NULL AND created_at < sqlc.arg(cutoff);
