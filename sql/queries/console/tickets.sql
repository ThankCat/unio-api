-- Console 侧工单查询：所有读写都强制携带 user_id 归属条件，防止越权。

-- name: CreateTicket :one
-- CreateTicket 创建工单（初始态 open，admin_unread 默认 true 提示运营有新工单）。
INSERT INTO tickets (uid, user_id, subject, category)
VALUES (sqlc.arg(uid), sqlc.arg(user_id), sqlc.arg(subject), sqlc.arg(category))
RETURNING id, uid, user_id, subject, category, status, user_unread, admin_unread,
    last_message_at, resolved_at, closed_at, created_at, updated_at;

-- name: ListConsoleTickets :many
-- ListConsoleTickets 分页列出本人工单（最近更新在前），可按主题和状态过滤。
SELECT id, uid, subject, category, status, user_unread, last_message_at, created_at
FROM tickets
WHERE user_id = sqlc.arg(user_id)
  AND (sqlc.narg(status)::text IS NULL OR status = sqlc.narg(status)::text)
  AND (
    sqlc.narg(search)::text IS NULL
    OR subject ILIKE '%' || sqlc.narg(search)::text || '%'
  )
ORDER BY last_message_at DESC, id DESC
LIMIT sqlc.arg(page_limit) OFFSET sqlc.arg(page_offset);

-- name: CountConsoleTickets :one
-- CountConsoleTickets 统计与 ListConsoleTickets 同口径的总数（分页 meta 用）。
SELECT COUNT(*) AS total
FROM tickets
WHERE user_id = sqlc.arg(user_id)
  AND (sqlc.narg(status)::text IS NULL OR status = sqlc.narg(status)::text)
  AND (
    sqlc.narg(search)::text IS NULL
    OR subject ILIKE '%' || sqlc.narg(search)::text || '%'
  );

-- name: GetConsoleTicket :one
-- GetConsoleTicket 按 uid 取本人工单；uid 与 user_id 必须同时匹配。
SELECT id, uid, user_id, subject, category, status, user_unread, admin_unread,
    last_message_at, resolved_at, closed_at, created_at, updated_at
FROM tickets
WHERE uid = sqlc.arg(uid) AND user_id = sqlc.arg(user_id);

-- name: SummarizeConsoleTickets :one
-- SummarizeConsoleTickets 返回未关闭工单数与未读数（侧栏角标轮询）。
SELECT
    COUNT(*) FILTER (WHERE status <> 'closed') AS active_total,
    COUNT(*) FILTER (WHERE user_unread) AS unread_total
FROM tickets
WHERE user_id = sqlc.arg(user_id);

-- name: ClearTicketUserUnread :execrows
-- ClearTicketUserUnread 用户打开详情时清除未读红点（幂等）。
UPDATE tickets
SET user_unread = false, updated_at = now()
WHERE id = sqlc.arg(id) AND user_unread;

-- name: TouchTicketOnUserMessage :one
-- TouchTicketOnUserMessage 用户回复后的状态推进：pending/resolved 回到 open（重开），
-- 提醒运营有新消息；closed 不匹配任何行，由调用方判定并拒绝。
UPDATE tickets
SET status = 'open', admin_unread = true, resolved_at = NULL,
    last_message_at = now(), updated_at = now()
WHERE id = sqlc.arg(id) AND status <> 'closed'
RETURNING id, uid, user_id, subject, category, status, user_unread, admin_unread,
    last_message_at, resolved_at, closed_at, created_at, updated_at;

-- name: CloseConsoleTicket :one
-- CloseConsoleTicket 用户主动关闭本人工单；已关闭时不匹配任何行（幂等由调用方兜底）。
UPDATE tickets
SET status = 'closed', closed_at = now(), user_unread = false, updated_at = now()
WHERE uid = sqlc.arg(uid) AND user_id = sqlc.arg(user_id) AND status <> 'closed'
RETURNING id, uid, user_id, subject, category, status, user_unread, admin_unread,
    last_message_at, resolved_at, closed_at, created_at, updated_at;
