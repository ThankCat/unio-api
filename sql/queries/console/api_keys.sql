-- Console API 密钥管理。
--
-- 归属是这一组查询的安全底线：每条语句都带 user_id 条件，包括按 id 定位的读和写。
-- Admin 侧可以只按 id 定位，Console 不行——少一个 user_id 就等于让用户操作别人的密钥。
--
-- 消耗与请求数一律沿用「账本 USD 净扣费 > 0」口径，与 console/requests.sql、
-- console/usage.sql 完全一致。三个页面必须能互相对上，否则用户会认为哪边算错了。
-- 代价是这里的「请求数」不含未计费的失败请求，所以密钥页不展示成功率。
--
-- 账本一律走 JOIN LATERAL + idx_ledger_entries_request_record_id 索引点查，
-- 理由同 console/usage.sql 顶部注释：CTE 再 JOIN 会被优化器估错行数退化成嵌套循环。

-- name: ListConsoleAPIKeys :many
-- 当前用户的密钥目录，附带时间窗内的计费请求数与消耗。
-- 状态由时间戳派生，并支持按派生状态过滤——DB 里没有 status 列。
SELECT
    k.id,
    k.name,
    k.key_prefix,
    k.key_suffix,
    k.spend_limit,
    k.spent_total,
    k.last_used_at,
    k.expires_at,
    k.disabled_at,
    k.revoked_at,
    k.created_at,
    k.updated_at,
    COALESCE(agg.request_count, 0)::bigint AS request_count,
    COALESCE(agg.charge_usd, 0)::numeric AS period_charge_usd
FROM api_keys k
LEFT JOIN LATERAL (
    SELECT
        COUNT(*) AS request_count,
        SUM(ch.charge_usd) AS charge_usd
    FROM request_records r
    JOIN LATERAL (
        SELECT SUM(
            CASE
                WHEN le.entry_type IN ('debit', 'adjustment_debit') THEN le.amount
                WHEN le.entry_type IN ('credit', 'refund', 'adjustment_credit') THEN -le.amount
                ELSE 0
            END
        ) AS charge_usd
        FROM ledger_entries le
        WHERE le.request_record_id = r.id AND le.currency = 'USD'
    ) ch ON ch.charge_usd > 0
    WHERE r.api_key_id = k.id
      AND r.created_at >= sqlc.arg(from_time)::timestamptz
      AND r.created_at < sqlc.arg(to_time)::timestamptz
) agg ON true
WHERE k.user_id = sqlc.arg(user_id)
  AND (
      sqlc.narg(search)::text IS NULL
      OR k.name ILIKE '%' || sqlc.narg(search)::text || '%'
      OR k.key_prefix ILIKE '%' || sqlc.narg(search)::text || '%'
  )
  AND (
      sqlc.narg(status)::text IS NULL
      OR CASE
             WHEN k.revoked_at IS NOT NULL THEN 'revoked'
             WHEN k.disabled_at IS NOT NULL THEN 'disabled'
             WHEN k.expires_at IS NOT NULL AND k.expires_at <= now() THEN 'expired'
             ELSE 'active'
         END = sqlc.narg(status)::text
  )
ORDER BY k.created_at DESC, k.id DESC
LIMIT sqlc.arg(page_limit) OFFSET sqlc.arg(page_offset);

-- name: CountConsoleAPIKeys :one
-- 与 ListConsoleAPIKeys 同过滤条件下的总数。
SELECT COUNT(*) AS total
FROM api_keys k
WHERE k.user_id = sqlc.arg(user_id)
  AND (
      sqlc.narg(search)::text IS NULL
      OR k.name ILIKE '%' || sqlc.narg(search)::text || '%'
      OR k.key_prefix ILIKE '%' || sqlc.narg(search)::text || '%'
  )
  AND (
      sqlc.narg(status)::text IS NULL
      OR CASE
             WHEN k.revoked_at IS NOT NULL THEN 'revoked'
             WHEN k.disabled_at IS NOT NULL THEN 'disabled'
             WHEN k.expires_at IS NOT NULL AND k.expires_at <= now() THEN 'expired'
             ELSE 'active'
         END = sqlc.narg(status)::text
  );

-- name: SummarizeConsoleAPIKeys :one
-- 页面顶栏：密钥总数 / 启用中 / 接近额度。
-- near_limit 取已用 ≥ 80%：这是页面上唯一需要用户当场处理的信号，阈值定在 SQL 里
-- 而不是前端，保证列表高亮与顶栏计数用同一个标准。
SELECT
    COUNT(*)::bigint AS key_total,
    COUNT(*) FILTER (
        WHERE disabled_at IS NULL
          AND revoked_at IS NULL
          AND (expires_at IS NULL OR expires_at > now())
    )::bigint AS key_active,
    COUNT(*) FILTER (
        WHERE spend_limit IS NOT NULL
          AND spend_limit > 0
          AND spent_total >= spend_limit * 0.8
    )::bigint AS near_limit
FROM api_keys
WHERE user_id = sqlc.arg(user_id);

-- name: SummarizeConsoleAPIKeyWindow :one
-- 页面顶栏的时间窗合计：全部密钥在窗口内的计费请求数与消耗。
SELECT
    COUNT(*)::bigint AS request_count,
    COALESCE(SUM(ch.charge_usd), 0)::numeric AS charge_usd
FROM request_records r
JOIN LATERAL (
    SELECT SUM(
        CASE
            WHEN le.entry_type IN ('debit', 'adjustment_debit') THEN le.amount
            WHEN le.entry_type IN ('credit', 'refund', 'adjustment_credit') THEN -le.amount
            ELSE 0
        END
    ) AS charge_usd
    FROM ledger_entries le
    WHERE le.request_record_id = r.id AND le.currency = 'USD'
) ch ON ch.charge_usd > 0
WHERE r.user_id = sqlc.arg(user_id)
  AND r.created_at >= sqlc.arg(from_time)::timestamptz
  AND r.created_at < sqlc.arg(to_time)::timestamptz;

-- name: GetConsoleAPIKey :one
-- 单把密钥。user_id 必须参与定位，否则会读到别人的密钥。
SELECT
    k.id,
    k.name,
    k.key_prefix,
    k.key_suffix,
    k.spend_limit,
    k.spent_total,
    k.last_used_at,
    k.expires_at,
    k.disabled_at,
    k.revoked_at,
    k.created_at,
    k.updated_at,
    COALESCE(agg.request_count, 0)::bigint AS request_count,
    COALESCE(agg.charge_usd, 0)::numeric AS period_charge_usd
FROM api_keys k
LEFT JOIN LATERAL (
    SELECT
        COUNT(*) AS request_count,
        SUM(ch.charge_usd) AS charge_usd
    FROM request_records r
    JOIN LATERAL (
        SELECT SUM(
            CASE
                WHEN le.entry_type IN ('debit', 'adjustment_debit') THEN le.amount
                WHEN le.entry_type IN ('credit', 'refund', 'adjustment_credit') THEN -le.amount
                ELSE 0
            END
        ) AS charge_usd
        FROM ledger_entries le
        WHERE le.request_record_id = r.id AND le.currency = 'USD'
    ) ch ON ch.charge_usd > 0
    WHERE r.api_key_id = k.id
      AND r.created_at >= sqlc.arg(from_time)::timestamptz
      AND r.created_at < sqlc.arg(to_time)::timestamptz
) agg ON true
WHERE k.id = sqlc.arg(id) AND k.user_id = sqlc.arg(user_id)
LIMIT 1;

-- name: ListConsoleAPIKeyDailyCharge :many
-- 按天分桶的消耗，同时供列表迷你走势和详情趋势图使用。
-- 不传 api_key_id 时一次返回该用户全部密钥的分桶，避免列表逐把查询造成 N+1。
-- 分桶按调用方时区切天，与用量统计页的 tz 处理保持一致。
SELECT
    r.api_key_id,
    -- 显式转 timestamptz：不写这一层 sqlc 会把 AT TIME ZONE 的结果推成 interface{}。
    (
        date_trunc('day', r.created_at AT TIME ZONE sqlc.arg(tz)::text)
            AT TIME ZONE sqlc.arg(tz)::text
    )::timestamptz AS bucket_start,
    COUNT(*)::bigint AS request_count,
    SUM(ch.charge_usd)::numeric AS charge_usd
FROM request_records r
JOIN LATERAL (
    SELECT SUM(
        CASE
            WHEN le.entry_type IN ('debit', 'adjustment_debit') THEN le.amount
            WHEN le.entry_type IN ('credit', 'refund', 'adjustment_credit') THEN -le.amount
            ELSE 0
        END
    ) AS charge_usd
    FROM ledger_entries le
    WHERE le.request_record_id = r.id AND le.currency = 'USD'
) ch ON ch.charge_usd > 0
WHERE r.user_id = sqlc.arg(user_id)
  AND r.created_at >= sqlc.arg(from_time)::timestamptz
  AND r.created_at < sqlc.arg(to_time)::timestamptz
  AND (sqlc.narg(api_key_id)::bigint IS NULL OR r.api_key_id = sqlc.narg(api_key_id)::bigint)
GROUP BY r.api_key_id, bucket_start
ORDER BY r.api_key_id, bucket_start;

-- name: ListConsoleAPIKeyTopModels :many
-- 详情页的主用模型排行，按消耗降序。
SELECT
    r.requested_model_id AS model_id,
    COALESCE(NULLIF(m.display_name, ''), r.requested_model_id) AS display_name,
    COUNT(*)::bigint AS request_count,
    SUM(ch.charge_usd)::numeric AS charge_usd
FROM request_records r
LEFT JOIN models m ON m.model_id = r.requested_model_id
JOIN LATERAL (
    SELECT SUM(
        CASE
            WHEN le.entry_type IN ('debit', 'adjustment_debit') THEN le.amount
            WHEN le.entry_type IN ('credit', 'refund', 'adjustment_credit') THEN -le.amount
            ELSE 0
        END
    ) AS charge_usd
    FROM ledger_entries le
    WHERE le.request_record_id = r.id AND le.currency = 'USD'
) ch ON ch.charge_usd > 0
WHERE r.user_id = sqlc.arg(user_id)
  AND r.api_key_id = sqlc.arg(api_key_id)
  AND r.created_at >= sqlc.arg(from_time)::timestamptz
  AND r.created_at < sqlc.arg(to_time)::timestamptz
GROUP BY r.requested_model_id, m.display_name
ORDER BY charge_usd DESC, request_count DESC, model_id
LIMIT sqlc.arg(row_limit);

-- name: CreateConsoleAPIKey :one
-- 用户自助创建密钥。只存 prefix 与 hash；明文由调用方放进创建响应，之后无处可取。
INSERT INTO api_keys (user_id, name, key_prefix, key_suffix, key_hash, expires_at, spend_limit)
VALUES (
    sqlc.arg(user_id),
    sqlc.arg(name),
    sqlc.arg(key_prefix),
    sqlc.arg(key_suffix),
    sqlc.arg(key_hash),
    sqlc.narg(expires_at),
    sqlc.narg(spend_limit)
)
RETURNING
    id, name, key_prefix, key_suffix, spend_limit, spent_total,
    last_used_at, expires_at, disabled_at, revoked_at, created_at, updated_at;

-- name: UpdateConsoleAPIKey :one
-- 一次更新名称 / 额度 / 有效期 / 启停：*_provided 决定该字段是否参与本次修改，
-- 对应的 narg 为 NULL 表示清空（不限额 / 永不过期 / 启用）。
-- 已吊销不可逆，因此 WHERE 带 revoked_at IS NULL；配合 user_id 一起定位。
UPDATE api_keys
SET
    name = CASE WHEN sqlc.arg(name_provided)::bool THEN sqlc.arg(name)::text ELSE name END,
    spend_limit = CASE WHEN sqlc.arg(spend_limit_provided)::bool THEN sqlc.narg(spend_limit) ELSE spend_limit END,
    expires_at = CASE WHEN sqlc.arg(expires_provided)::bool THEN sqlc.narg(expires_at) ELSE expires_at END,
    disabled_at = CASE WHEN sqlc.arg(disabled_provided)::bool THEN sqlc.narg(disabled_at) ELSE disabled_at END,
    updated_at = now()
WHERE id = sqlc.arg(id)
  AND user_id = sqlc.arg(user_id)
  AND revoked_at IS NULL
RETURNING
    id, name, key_prefix, key_suffix, spend_limit, spent_total,
    last_used_at, expires_at, disabled_at, revoked_at, created_at, updated_at;

-- name: RevokeConsoleAPIKey :one
-- 永久吊销（不可逆）。已吊销时返回零行，上层映射为 not_found。
UPDATE api_keys
SET revoked_at = now(), updated_at = now()
WHERE id = sqlc.arg(id)
  AND user_id = sqlc.arg(user_id)
  AND revoked_at IS NULL
RETURNING
    id, name, key_prefix, key_suffix, spend_limit, spent_total,
    last_used_at, expires_at, disabled_at, revoked_at, created_at, updated_at;

-- name: DeleteConsoleAPIKey :execrows
-- 物理删除，只用于清理误建且没有调用历史的密钥。
-- 有调用历史时 request_records 的外键会拒绝（23503），上层降级为 conflict 并提示改用吊销。
DELETE FROM api_keys
WHERE id = sqlc.arg(id) AND user_id = sqlc.arg(user_id);
