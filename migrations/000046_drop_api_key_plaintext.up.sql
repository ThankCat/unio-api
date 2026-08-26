-- API Key 明文改为「只在创建响应里出现一次」，不再落库。
--
-- 原设计把完整明文存进 key_plaintext，好处是控制台和 Admin 能随时复制，
-- 代价是一份可直接调用的凭证以明文躺在业务库里：库被拖走、备份泄露、
-- 或任何一个回显它的接口鉴权写错，都等于把用户的调用权限直接送出去。
-- 现在改成创建时返回一次、之后永不可回显——用户丢了就重建一把，
-- 这点麻烦换掉的是一整类不可挽回的泄露面。
--
-- 明文丢弃后，key_prefix 成为识别「这是哪一把 key」的唯一依据，因此保留并继续索引。
-- 认证只比对 key_hash，不读明文，所以本迁移不影响任何存量 key 的可用性。

ALTER TABLE public.api_keys
    DROP COLUMN key_plaintext;

COMMENT ON COLUMN public.api_keys.key_prefix IS 'API Key 明文前缀（sk_unio_ + 前 8 位随机）。明文不落库，识别与搜索都依赖这一列。';

COMMENT ON COLUMN public.api_keys.key_hash IS 'API Key 明文的 SHA-256，认证唯一依据。明文只在创建响应里返回一次，此后无法从任何接口取回。';
