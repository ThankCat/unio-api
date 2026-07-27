-- name: ListEnabledChannelAdapters :many
-- ListEnabledChannelAdapters 列出启用 provider 下启用 channel 的协议与 adapter 注册键，供启动期 preflight 校验 channel 运行时绑定是否被当前进程支持。
SELECT
    c.id AS channel_id,
    c.protocol,
    c.adapter_key,
    p.slug AS provider_slug
FROM channels c
JOIN providers p ON p.id = c.provider_id
WHERE c.status = 'enabled'
  AND p.status = 'enabled'
ORDER BY c.id;
