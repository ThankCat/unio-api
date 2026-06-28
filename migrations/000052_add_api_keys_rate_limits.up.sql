-- 为 API Key 增加令牌级限流上限（P2-8）：RPM 每分钟请求数、TPM 每分钟 token 数、RPD 每日请求数。
-- 三列均可空：NULL 表示「继承全局默认」，0 表示「显式不限」，>0 表示具体上限。
-- 限流计数在 Redis 滑动窗口完成，这里只持久化每把 Key 的策略上限。
ALTER TABLE api_keys
    ADD COLUMN rpm_limit INTEGER CHECK (rpm_limit IS NULL OR rpm_limit >= 0),
    ADD COLUMN tpm_limit INTEGER CHECK (tpm_limit IS NULL OR tpm_limit >= 0),
    ADD COLUMN rpd_limit INTEGER CHECK (rpd_limit IS NULL OR rpd_limit >= 0);
