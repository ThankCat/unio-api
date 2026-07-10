-- 渠道在途并发上限（DEC-029）：同一渠道「同时进行中」的上游调用数上限（in-flight，含整段流式传输）。
-- 与 RPM（每分钟请求数）正交：并发上限专门防「慢上游 + 客户端重试风暴」把长耗时请求堆在同一渠道上，
-- 每个在途请求都可能被上游计费（如 sub2api 断开仍扣费），RPM 无法限制这种堆积。
-- NULL 表示「继承全局默认」（gateway.concurrency_defaults.channel），0 表示「显式不限」，>0 表示具体上限。
-- 命中上限时该候选被跳过（fallback 到下一渠道），不产生上游调用，也不写 attempt 记录。
ALTER TABLE channels
    ADD COLUMN concurrency_limit INTEGER CHECK (concurrency_limit IS NULL OR concurrency_limit >= 0);
