-- 为 channel 增加渠道级限流上限（P2-8）：RPM 每分钟请求数、TPM 每分钟 token 数、RPD 每日请求数。
-- 三列均可空：NULL 表示「继承全局默认」，0 表示「显式不限」，>0 表示具体上限。
-- 渠道级限流在每次调用上游前生效，命中即跳过该候选 fallback 到下一渠道，不直接整盘失败。
ALTER TABLE channels
    ADD COLUMN rpm_limit INTEGER CHECK (rpm_limit IS NULL OR rpm_limit >= 0),
    ADD COLUMN tpm_limit INTEGER CHECK (tpm_limit IS NULL OR tpm_limit >= 0),
    ADD COLUMN rpd_limit INTEGER CHECK (rpd_limit IS NULL OR rpd_limit >= 0);
