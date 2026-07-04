-- 线路选路策略新增 random：每次请求对候选顺序随机洗牌，仍保留完整 fallback。
ALTER TABLE routes DROP CONSTRAINT IF EXISTS routes_mode_check;
ALTER TABLE routes ADD CONSTRAINT routes_mode_check
    CHECK (mode IN ('cheapest', 'stable', 'fixed', 'random'));
