-- API Key 软删除：deleted_at 非空即视为已删除。
-- 软删除仅对已吊销（revoked_at 非空）的密钥开放：吊销负责让密钥失效，
-- 删除只负责把它从 Console 列表与详情中移走；request_records 及账单等
-- 历史展示按 api_key_id 关联，不受删除影响。
ALTER TABLE public.api_keys
    ADD COLUMN deleted_at timestamp with time zone;

COMMENT ON COLUMN public.api_keys.deleted_at IS '用户软删除时间；非空即从 Console 列表与详情隐藏，历史请求展示不受影响。';
