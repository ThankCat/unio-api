-- 能力自动校正单例分布式租约：多实例 worker 互斥执行，避免并发跑 rollup 重复计数（DESIGN 风险 A）。
-- 复用单行表 capability_calibration_state（id 恒为 1）；worker 抢到租约才执行，运行中续租、结束释放。
ALTER TABLE capability_calibration_state
    -- locked_by: 当前持有租约的 worker 实例标识（NULL 表示空闲）。--
    ADD COLUMN locked_by TEXT,
    -- locked_until: 租约到期时刻；过期即可被其他实例抢占（防 worker 崩溃后死锁）。--
    ADD COLUMN locked_until TIMESTAMPTZ;
