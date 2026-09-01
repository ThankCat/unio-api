-- 回滚邮件发送记录表（docs/changes/2026-09-01-email-verification-code）。
DROP TABLE IF EXISTS public.email_messages;
