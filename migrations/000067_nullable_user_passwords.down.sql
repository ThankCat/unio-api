-- 回滚到旧 Schema 时，以无效哈希占位，确保无密码账户仍无法通过密码登录。
UPDATE public.users
SET password_hash = ''
WHERE password_hash IS NULL;

ALTER TABLE public.users
    ALTER COLUMN password_hash SET NOT NULL;
