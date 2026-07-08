-- 回滚:删除毫秒格式的行,旧版二进制启动时由 SeedDefaults 以旧格式(duration 字符串)补默认值。
DELETE FROM app_settings
WHERE key IN (
    'gateway.stream_idle_timeout_ms',
    'gateway.default_channel_timeout_ms',
    'gateway.circuit_breaker',
    'gateway.channel_ratelimit_cooldown'
);
