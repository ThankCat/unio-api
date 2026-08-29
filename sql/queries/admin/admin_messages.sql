-- name: CreateAdminMessage :execrows
-- CreateAdminMessage 写入一条站内消息。dedupe_key 非空且已存在同 key 未读消息时静默跳过
-- （命中部分唯一索引 uq_admin_messages_unread_dedupe，DO NOTHING），返回受影响行数 0；
-- 用于周期性 worker 告警防重复轰炸。
INSERT INTO admin_messages (severity, topic, title, body, source, dedupe_key)
VALUES (
    sqlc.arg(severity), sqlc.arg(topic), sqlc.arg(title),
    sqlc.arg(body), sqlc.arg(source), sqlc.narg(dedupe_key)
)
ON CONFLICT (dedupe_key) WHERE read_at IS NULL DO NOTHING;

-- name: ListAdminMessagesPage :many
-- ListAdminMessagesPage 分页列出站内消息（新消息在前），可选只看未读、按 topic 过滤。
SELECT id, severity, topic, title, body, source, dedupe_key, created_at, read_at
FROM admin_messages
WHERE (NOT sqlc.arg(unread_only)::boolean OR read_at IS NULL)
  AND (sqlc.narg(topic)::text IS NULL OR topic = sqlc.narg(topic)::text)
ORDER BY id DESC
LIMIT sqlc.arg(page_limit) OFFSET sqlc.arg(page_offset);

-- name: CountAdminMessages :one
-- CountAdminMessages 统计与 ListAdminMessagesPage 同口径的总数（分页 meta 用）。
SELECT COUNT(*) AS total
FROM admin_messages
WHERE (NOT sqlc.arg(unread_only)::boolean OR read_at IS NULL)
  AND (sqlc.narg(topic)::text IS NULL OR topic = sqlc.narg(topic)::text);

-- name: CountUnreadAdminMessages :one
-- CountUnreadAdminMessages 未读总数（admin 顶栏铃铛轮询，命中未读部分索引）。
SELECT COUNT(*) AS total
FROM admin_messages
WHERE read_at IS NULL;

-- name: MarkAdminMessageRead :one
-- MarkAdminMessageRead 幂等标记单条消息已读（重复标记保留首次 read_at），返回更新后的行；id 不存在时无行。
UPDATE admin_messages
SET read_at = COALESCE(read_at, now())
WHERE id = sqlc.arg(id)
RETURNING id, severity, topic, title, body, source, dedupe_key, created_at, read_at;

-- name: MarkAllAdminMessagesRead :execrows
-- MarkAllAdminMessagesRead 把全部未读消息标记为已读，返回标记条数。
UPDATE admin_messages
SET read_at = now()
WHERE read_at IS NULL;
