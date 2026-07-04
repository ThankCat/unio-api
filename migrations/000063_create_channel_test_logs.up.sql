-- 渠道检测日志：记录渠道凭据有效性相关的「事件历史」。
-- channels 表只保留当前布尔 credential_valid；本表存 when/why 与每次检测明细，供详情页回放。
-- 写入口径（R1(b)，节流防刷屏）：
--   - worker 巡检：仅在「检测失败」或「credential_valid 发生跳变」时写；健康且状态未变的成功探测不写。
--   - 手动检测：管理员显式操作，总写一条（留痕）。
--   - 运行时连续 401 翻失效：写一条 source='runtime_401'。
-- 保留：按渠道保留最近 N 条（CHANNEL_TEST_LOG_RETENTION_PER_CHANNEL，默认 200），worker 每轮清理。
CREATE TABLE channel_test_logs (
    id                     BIGSERIAL PRIMARY KEY,
    channel_id             BIGINT NOT NULL REFERENCES channels (id) ON DELETE CASCADE,
    created_at             TIMESTAMPTZ NOT NULL DEFAULT now(),
    -- 事件来源：worker（自动巡检）/ manual（管理员检测）/ runtime_401（网关连续 401 翻失效）。
    source                 TEXT NOT NULL CHECK (source IN ('worker', 'manual', 'runtime_401')),
    success                BOOLEAN NOT NULL,
    -- 失败稳定错误码（credential_invalid / model_unavailable / timeout / unreachable / rate_limited / ...）；成功为 NULL。
    error_code             TEXT,
    http_status            INTEGER,
    latency_ms             INTEGER CHECK (latency_ms IS NULL OR latency_ms >= 0),
    tested_model           TEXT,
    -- 本次事件后的 credential_valid 状态，便于回放跳变。
    credential_valid_after BOOLEAN NOT NULL,
    message                TEXT
);

-- 详情页按渠道倒序分页 + worker 保留清理都走此索引。
CREATE INDEX idx_channel_test_logs_channel_created
    ON channel_test_logs (channel_id, created_at DESC, id DESC);
