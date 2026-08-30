-- Admin 侧工单查询：运营队列、详情与状态流转。单管理员，无坐席归属概念。

-- name: ListAdminTickets :many
-- ListAdminTickets 分页列出工单队列（最近更新在前），可按状态、分类过滤，
-- 按主题或用户邮箱模糊搜索。
SELECT t.id, t.uid, t.subject, t.category, t.status, t.admin_unread,
    t.last_message_at, t.created_at,
    u.id AS user_id, u.email AS user_email
FROM tickets t
JOIN users u ON u.id = t.user_id
WHERE (sqlc.narg(status)::text IS NULL OR t.status = sqlc.narg(status)::text)
  AND (sqlc.narg(category)::text IS NULL OR t.category = sqlc.narg(category)::text)
  AND (
    sqlc.narg(search)::text IS NULL
    OR t.subject ILIKE '%' || sqlc.narg(search)::text || '%'
    OR u.email ILIKE '%' || sqlc.narg(search)::text || '%'
  )
ORDER BY t.last_message_at DESC, t.id DESC
LIMIT sqlc.arg(page_limit) OFFSET sqlc.arg(page_offset);

-- name: CountAdminTickets :one
-- CountAdminTickets 统计与 ListAdminTickets 同口径的总数（分页 meta 用）。
SELECT COUNT(*) AS total
FROM tickets t
JOIN users u ON u.id = t.user_id
WHERE (sqlc.narg(status)::text IS NULL OR t.status = sqlc.narg(status)::text)
  AND (sqlc.narg(category)::text IS NULL OR t.category = sqlc.narg(category)::text)
  AND (
    sqlc.narg(search)::text IS NULL
    OR t.subject ILIKE '%' || sqlc.narg(search)::text || '%'
    OR u.email ILIKE '%' || sqlc.narg(search)::text || '%'
  );

-- name: GetAdminTicket :one
-- GetAdminTicket 按 uid 取工单详情，连带用户侧栏信息。
SELECT t.id, t.uid, t.subject, t.category, t.status, t.user_unread, t.admin_unread,
    t.last_message_at, t.resolved_at, t.closed_at, t.created_at, t.updated_at,
    u.id AS user_id, u.email AS user_email, u.created_at AS user_created_at
FROM tickets t
JOIN users u ON u.id = t.user_id
WHERE t.uid = sqlc.arg(uid);

-- name: ClearTicketAdminUnread :execrows
-- ClearTicketAdminUnread 运营打开详情时清除队列红点（幂等）。
UPDATE tickets
SET admin_unread = false, updated_at = now()
WHERE id = sqlc.arg(id) AND admin_unread;

-- name: TouchTicketOnAdminMessage :one
-- TouchTicketOnAdminMessage 客服回复后的状态推进：转入 pending 并提醒用户；
-- closed 不匹配任何行，由调用方判定并拒绝。
UPDATE tickets
SET status = 'pending', user_unread = true, last_message_at = now(), updated_at = now()
WHERE id = sqlc.arg(id) AND status <> 'closed'
RETURNING id, uid, user_id, subject, category, status, user_unread, admin_unread,
    last_message_at, resolved_at, closed_at, created_at, updated_at;

-- name: ResolveAdminTicket :one
-- ResolveAdminTicket 标记已解决；仅 open/pending 可解决，resolved 超期由 worker 关闭。
UPDATE tickets
SET status = 'resolved', resolved_at = now(), user_unread = true, updated_at = now()
WHERE uid = sqlc.arg(uid) AND status = ANY(ARRAY['open'::text, 'pending'::text])
RETURNING id, uid, user_id, subject, category, status, user_unread, admin_unread,
    last_message_at, resolved_at, closed_at, created_at, updated_at;

-- name: CloseAdminTicket :one
-- CloseAdminTicket 运营直接关闭工单（终态）。
UPDATE tickets
SET status = 'closed', closed_at = now(), updated_at = now()
WHERE uid = sqlc.arg(uid) AND status <> 'closed'
RETURNING id, uid, user_id, subject, category, status, user_unread, admin_unread,
    last_message_at, resolved_at, closed_at, created_at, updated_at;

-- name: ReopenAdminTicket :one
-- ReopenAdminTicket 运营把 resolved/closed 工单拉回 open（误操作恢复）。
UPDATE tickets
SET status = 'open', resolved_at = NULL, closed_at = NULL, updated_at = now()
WHERE uid = sqlc.arg(uid) AND status = ANY(ARRAY['resolved'::text, 'closed'::text])
RETURNING id, uid, user_id, subject, category, status, user_unread, admin_unread,
    last_message_at, resolved_at, closed_at, created_at, updated_at;
