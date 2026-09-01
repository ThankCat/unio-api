-- name: InsertEmailMessage :one
-- InsertEmailMessage 写入一条邮件发送记录（同步发送路径在 SMTP 提交完成后调用，成功失败都记录）。
INSERT INTO email_messages (
    email_type, recipient, sender, subject, body_html,
    status, error_summary, locale, duration_ms, sent_at
)
VALUES (
    sqlc.arg(email_type), sqlc.arg(recipient), sqlc.arg(sender), sqlc.arg(subject), sqlc.arg(body_html),
    sqlc.arg(status), sqlc.narg(error_summary), sqlc.arg(locale), sqlc.narg(duration_ms), sqlc.narg(sent_at)
)
RETURNING id;
