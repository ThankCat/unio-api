-- app_settings 通用 key→JSONB 全局设置存储的读写查询。

-- name: GetAppSetting :one
-- GetAppSetting 按 key 读取设置内容;不存在时返回 pgx.ErrNoRows(由调用方回退默认)。
SELECT value
FROM app_settings
WHERE key = $1;

-- name: UpsertAppSetting :exec
-- UpsertAppSetting 写入/覆盖设置内容,并刷新 updated_at。
INSERT INTO app_settings (key, value, updated_at)
VALUES ($1, $2, now())
ON CONFLICT (key) DO UPDATE
SET value = EXCLUDED.value,
    updated_at = now();
