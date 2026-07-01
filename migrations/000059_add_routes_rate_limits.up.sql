-- 为线路增加线路级限流上限（DEC-027）：RPM 每分钟请求 / TPM 每分钟 token / RPD 每日请求。
-- 三列均可空：NULL 表示「继承全局默认」，0 表示「显式不限」，>0 表示具体上限。
-- 计数在 Redis 滑动窗口按 (线路, 用户) 复合主体执行（同一用户在该线路下的所有 Key 共享一个桶，
-- 多建 Key 无法放大配额）；本列只持久化线路的「上限模板」。
ALTER TABLE routes
    ADD COLUMN rpm_limit INTEGER CHECK (rpm_limit IS NULL OR rpm_limit >= 0),
    ADD COLUMN tpm_limit INTEGER CHECK (tpm_limit IS NULL OR tpm_limit >= 0),
    ADD COLUMN rpd_limit INTEGER CHECK (rpd_limit IS NULL OR rpd_limit >= 0);
