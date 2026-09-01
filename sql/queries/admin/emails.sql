-- name: ListEmailMessages :many
-- ListEmailMessages 分页列出邮件发送记录（新记录在前）。列表不返回 body_html（正文较大，详情单独取）。
-- 筛选：email_type / status 精确匹配；recipient 子串匹配（ILIKE）；from/to 为 created_at 半开区间。
SELECT id, email_type, recipient, sender, subject, status, error_summary, locale, duration_ms, sent_at, created_at
FROM email_messages
WHERE (sqlc.narg(email_type)::text IS NULL OR email_type = sqlc.narg(email_type)::text)
  AND (sqlc.narg(status)::text IS NULL OR status = sqlc.narg(status)::text)
  AND (sqlc.narg(recipient)::text IS NULL OR recipient ILIKE '%' || sqlc.narg(recipient)::text || '%')
  AND (sqlc.narg(from_time)::timestamptz IS NULL OR created_at >= sqlc.narg(from_time)::timestamptz)
  AND (sqlc.narg(to_time)::timestamptz IS NULL OR created_at < sqlc.narg(to_time)::timestamptz)
ORDER BY id DESC
LIMIT sqlc.arg(page_limit) OFFSET sqlc.arg(page_offset);

-- name: CountEmailMessages :one
-- CountEmailMessages 统计与 ListEmailMessages 同口径的总数（分页 meta 用）。
SELECT COUNT(*) AS total
FROM email_messages
WHERE (sqlc.narg(email_type)::text IS NULL OR email_type = sqlc.narg(email_type)::text)
  AND (sqlc.narg(status)::text IS NULL OR status = sqlc.narg(status)::text)
  AND (sqlc.narg(recipient)::text IS NULL OR recipient ILIKE '%' || sqlc.narg(recipient)::text || '%')
  AND (sqlc.narg(from_time)::timestamptz IS NULL OR created_at >= sqlc.narg(from_time)::timestamptz)
  AND (sqlc.narg(to_time)::timestamptz IS NULL OR created_at < sqlc.narg(to_time)::timestamptz);

-- name: GetEmailMessage :one
-- GetEmailMessage 读取单条发送记录详情（含 body_html，供 Admin 详情页隔离预览）。
SELECT id, email_type, recipient, sender, subject, body_html, status, error_summary, locale, duration_ms, sent_at, created_at
FROM email_messages
WHERE id = sqlc.arg(id);
