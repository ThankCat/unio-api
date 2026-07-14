-- app_settings 通用 key→JSONB 全局设置存储的读写查询。

-- name: GetAppSetting :one
-- GetAppSetting 按 key 读取设置内容;不存在时返回 pgx.ErrNoRows(由调用方回退默认)。
SELECT value
FROM app_settings
WHERE key = $1;

-- name: ListAppSettings :many
-- ListAppSettings 列出全部已持久化设置(供 admin 面板对照展示)。
SELECT key, value, description, updated_at
FROM app_settings
ORDER BY key;

-- name: UpsertAppSetting :exec
-- UpsertAppSetting 写入/覆盖设置内容与说明,并刷新 updated_at。
INSERT INTO app_settings (key, value, description, updated_at)
VALUES ($1, $2, $3, now())
ON CONFLICT (key) DO UPDATE
SET value = EXCLUDED.value,
    description = EXCLUDED.description,
    updated_at = now();

-- name: SeedAppSetting :exec
-- SeedAppSetting 仅在 key 缺行时写入注册表默认值(启动 seed 用)。
-- 与 UpsertAppSetting 的关键区别:DO NOTHING——绝不覆盖运维已改过的值;幂等且并发安全,
-- gateway/admin 启动都会调用。
INSERT INTO app_settings (key, value, description, updated_at)
VALUES ($1, $2, $3, now())
ON CONFLICT (key) DO NOTHING;
