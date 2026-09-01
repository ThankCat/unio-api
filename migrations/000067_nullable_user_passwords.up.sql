-- 账户可以仅通过邮箱验证码登录；密码在用户首次设置前允许为空。
ALTER TABLE public.users
    ALTER COLUMN password_hash DROP NOT NULL;
