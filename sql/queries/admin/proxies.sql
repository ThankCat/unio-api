-- 出站代理实体管理（网关中心 · 代理）。
-- 引用计数随行返回：删除护栏（被引用 → 409）与「停用后引用方回退直连」的提示都靠它。

-- name: AdminListProxies :many
SELECT p.*,
       (SELECT count(*) FROM channels c WHERE c.proxy_id = p.id AND c.status <> 'archived')::bigint AS channel_refs,
       (SELECT count(*) FROM subscription_accounts a WHERE a.proxy_id = p.id AND a.status <> 'archived')::bigint AS account_refs
FROM proxies p
ORDER BY p.status, p.name;

-- name: AdminGetProxy :one
SELECT * FROM proxies WHERE id = $1;

-- name: AdminCreateProxy :one
INSERT INTO proxies (name, protocol, host, port, username, password, url, status, note)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
RETURNING *;

-- name: AdminUpdateProxy :one
UPDATE proxies
SET name = $2,
    protocol = $3,
    host = $4,
    port = $5,
    username = $6,
    password = $7,
    url = $8,
    note = $9,
    updated_at = now()
WHERE id = $1
RETURNING *;

-- name: AdminSetProxyStatus :one
UPDATE proxies
SET status = $2,
    updated_at = now()
WHERE id = $1
RETURNING *;

-- name: AdminDeleteProxy :execrows
-- 物理删除；被 channels/subscription_accounts 引用时 FK RESTRICT 报 23503，上层降级 409。
DELETE FROM proxies WHERE id = $1;

-- name: AdminCountProxyReferences :one
SELECT
    (SELECT count(*) FROM channels c WHERE c.proxy_id = sqlc.arg(id)::bigint AND c.status <> 'archived')::bigint AS channel_refs,
    (SELECT count(*) FROM subscription_accounts a WHERE a.proxy_id = sqlc.arg(id)::bigint AND a.status <> 'archived')::bigint AS account_refs;
