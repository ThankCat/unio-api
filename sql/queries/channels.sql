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

-- name: ListChannels :many
-- ListChannels 列出全部 channel，按 priority、id 升序，供 admin 管理台展示。
SELECT id, provider_id, name, protocol, adapter_key, base_url, credential_encrypted, status, priority, timeout_ms, created_at, updated_at
FROM channels
ORDER BY priority, id;

-- name: ListChannelsByProvider :many
-- ListChannelsByProvider 列出指定 provider 下的 channel，按 priority、id 升序。
SELECT id, provider_id, name, protocol, adapter_key, base_url, credential_encrypted, status, priority, timeout_ms, created_at, updated_at
FROM channels
WHERE provider_id = $1
ORDER BY priority, id;

-- name: GetChannel :one
-- GetChannel 按 id 读取单个 channel。
SELECT id, provider_id, name, protocol, adapter_key, base_url, credential_encrypted, status, priority, timeout_ms, created_at, updated_at
FROM channels
WHERE id = $1
LIMIT 1;

-- name: CreateChannel :one
-- CreateChannel 创建 channel；credential_encrypted 为已加密的上游凭据，protocol+adapter_key 复合键须先在 adapter registry 校验存在。
INSERT INTO channels (provider_id, name, protocol, adapter_key, base_url, credential_encrypted, status, priority, timeout_ms)
VALUES (sqlc.arg(provider_id), sqlc.arg(name), sqlc.arg(protocol), sqlc.arg(adapter_key), sqlc.arg(base_url), sqlc.arg(credential_encrypted), sqlc.arg(status), sqlc.arg(priority), sqlc.arg(timeout_ms))
RETURNING id, provider_id, name, protocol, adapter_key, base_url, credential_encrypted, status, priority, timeout_ms, created_at, updated_at;

-- name: UpdateChannel :one
-- UpdateChannel 更新 channel 的展示名、上游地址、启停状态、优先级与超时；protocol、adapter_key 与凭据不在此更新。
UPDATE channels
SET name = sqlc.arg(name), base_url = sqlc.arg(base_url), status = sqlc.arg(status), priority = sqlc.arg(priority), timeout_ms = sqlc.arg(timeout_ms), updated_at = now()
WHERE id = sqlc.arg(id)
RETURNING id, provider_id, name, protocol, adapter_key, base_url, credential_encrypted, status, priority, timeout_ms, created_at, updated_at;

-- name: UpdateChannelCredential :execrows
-- UpdateChannelCredential 轮换 channel 的上游凭据；只写密文，不回读；返回受影响行数用于判定 channel 是否存在。
UPDATE channels
SET credential_encrypted = sqlc.arg(credential_encrypted), updated_at = now()
WHERE id = sqlc.arg(id);
