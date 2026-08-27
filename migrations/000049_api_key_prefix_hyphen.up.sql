-- 命名空间分隔符由下划线改为连字符：新签发的 key 形如 sk-unio-<48 位 base36>。
--
-- 只改注释，不动数据。存量行的 key_prefix 仍是 sk_unio_ 开头，它们至今有效——
-- 认证只比对 key_hash，从不解析前缀（migration 000046 换过一次前缀时已经验证过这一点）。
-- 展示层同时认这两种命名空间，掩码不会把老 key 的前缀截断。
COMMENT ON COLUMN public.api_keys.key_prefix IS 'API Key 明文前缀（命名空间 + 前 8 位随机）。当前命名空间为 sk-unio-，2026-08-27 之前签发的为 sk_unio_，两者都有效。明文不落库，识别与搜索都依赖这一列。';
