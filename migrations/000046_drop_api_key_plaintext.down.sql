-- 回滚只能恢复列结构，无法恢复数据：明文一旦不落库就是单向的，
-- 存量 key 的 key_plaintext 会全部是 NULL。
-- 依赖明文回显的老代码回滚后会把所有 key 都当成「不可回显」，
-- 这是预期行为，不是数据丢失事故。

ALTER TABLE public.api_keys
    ADD COLUMN key_plaintext text;

COMMENT ON COLUMN public.api_keys.key_plaintext IS '完整明文 key，供用户在控制台多次复制查看（产品决策：用户 key 明文留存）。NULL=历史/不可回显。';

COMMENT ON COLUMN public.api_keys.key_prefix IS 'API Key 明文前缀，用于定位和展示。';

COMMENT ON COLUMN public.api_keys.key_hash IS 'API Key 哈希值，认证按它定位（不参与明文展示）。';
