-- 运行时配置时长格式改版:duration 字符串("30s"/"10m0s")→ int 毫秒(字段/key 带 _ms 后缀,
-- 对齐 channels.timeout_ms 惯例)。涉及 4 个 key:
--   gateway.stream_idle_timeout      → 改名 gateway.stream_idle_timeout_ms(值改 int 毫秒)
--   gateway.default_channel_timeout  → 改名 gateway.default_channel_timeout_ms(值改 int 毫秒)
--   gateway.circuit_breaker          → 字段 window/open_duration 改 window_ms/open_duration_ms
--   gateway.channel_ratelimit_cooldown → 字段 cooldown/cap 改 cooldown_ms/cap_ms
--
-- 处理方式:直接删除旧行,服务下次启动由 SeedDefaults 以新格式补默认值。
-- 依据:该 4 个 key 于同日上线,唯一部署(dev)中均为默认值,无运维自定义可丢失。
DELETE FROM app_settings
WHERE key IN (
    'gateway.stream_idle_timeout',
    'gateway.default_channel_timeout',
    'gateway.circuit_breaker',
    'gateway.channel_ratelimit_cooldown'
);
