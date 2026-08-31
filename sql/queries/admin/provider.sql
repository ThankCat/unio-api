-- name: ListProviders :many
-- ListProviders 列出全部 provider，按 id 升序，供 admin 管理台展示。
SELECT id, slug, name, origin, origin_revision, status, status_revision, created_at, updated_at, archived_at, currency
FROM providers
ORDER BY id;

-- name: ListProvidersPage :many
-- ListProvidersPage 按状态/关键字过滤后分页列出 provider；status、q 为 NULL 时不过滤。
SELECT id, slug, name, origin, origin_revision, status, status_revision, created_at, updated_at, archived_at, currency
FROM providers
WHERE (sqlc.narg('status')::text IS NULL OR status = sqlc.narg('status')::text)
  AND (
    sqlc.narg('q')::text IS NULL
    OR slug ILIKE '%' || sqlc.narg('q')::text || '%'
    OR name ILIKE '%' || sqlc.narg('q')::text || '%'
  )
ORDER BY id
LIMIT sqlc.arg('page_limit') OFFSET sqlc.arg('page_offset');

-- name: CountProviders :one
-- CountProviders 返回与 ListProvidersPage 相同过滤条件下的总条数。
SELECT COUNT(*) AS total
FROM providers
WHERE (sqlc.narg('status')::text IS NULL OR status = sqlc.narg('status')::text)
  AND (
    sqlc.narg('q')::text IS NULL
    OR slug ILIKE '%' || sqlc.narg('q')::text || '%'
    OR name ILIKE '%' || sqlc.narg('q')::text || '%'
  );

-- name: GetProvider :one
-- GetProvider 按 id 读取单个 provider。
SELECT id, slug, name, origin, origin_revision, status, status_revision, created_at, updated_at, archived_at, currency
FROM providers
WHERE id = $1
LIMIT 1;

-- name: CreateProvider :one
-- CreateProvider 创建 provider；slug 全局唯一由 DB 唯一约束保证。
-- currency 是该供应商的结算币种（D3），创建时必选；产生价格/账务引用后不可修改（应用层强制）。
INSERT INTO providers (slug, name, origin, status, currency)
VALUES (sqlc.arg(slug), sqlc.arg(name), sqlc.arg(origin), sqlc.arg(status), sqlc.arg(currency))
RETURNING id, slug, name, origin, origin_revision, status, status_revision, created_at, updated_at, archived_at, currency;

-- name: UpdateProvider :one
-- UpdateProvider 更新 provider 的展示名；slug、origin 与 status 使用各自专用入口。currency 不可改。
UPDATE providers
SET name = sqlc.arg(name), updated_at = now()
WHERE id = sqlc.arg(id)
RETURNING id, slug, name, origin, origin_revision, status, status_revision, created_at, updated_at, archived_at, currency;

-- name: CountProviderCurrencyRefs :one
-- CountProviderCurrencyRefs 统计 provider 币种语义的引用数（渠道价格 + 账本 + 余额行）。
-- 任一引用存在即禁止修改 currency（D3 不可变规则的应用层依据）。
SELECT
    (SELECT COUNT(*) FROM channel_prices cp JOIN channels c ON c.id = cp.channel_id WHERE c.provider_id = sqlc.arg(provider_id))
  + (SELECT COUNT(*) FROM provider_ledger_entries ple WHERE ple.provider_id = sqlc.arg(provider_id))
  + (SELECT COUNT(*) FROM provider_balances pb WHERE pb.provider_id = sqlc.arg(provider_id)) AS total;

-- name: DeleteProvider :execrows
-- DeleteProvider 物理删除 provider，用于清理录错且从未使用的脏数据。
-- 终态 provider_routing_operations 随 Provider 清理；非终态操作通过 RESTRICT 阻止删除。
-- 纯手工调额的账本（adjustment_credit/adjustment_debit）与余额缓存行属管理员自录的管理数据，
-- 无真实交易归因，随删除一并清理——测试服务商设过余额也能删；一旦存在任何交易性分录
--（usage_debit/probe_debit 等），账本保留原样，由 NO ACTION 外键拦下整个删除。
-- Provider 本身不做请求/账务级联：一旦名下仍有 channel，或 provider 被请求/账务历史
--（request_records/request_attempts/cost_snapshots/settlement_recovery_jobs
-- 等 NO ACTION 外键）引用，整条语句报 23503 全部回滚，上层降级为 conflict，提示先删渠道或改用停用。
-- 数据修改型 CTE 保证各段各执行一次、外键在语句末统一校验，故清子表 + 删主体在单语句内原子完成。
WITH deleted_terminal_ops AS (
    DELETE FROM provider_routing_operations
    WHERE provider_id = sqlc.arg(id)
      AND state IN ('committed', 'aborted')
), admin_only_ledger AS (
    SELECT NOT EXISTS (
        SELECT 1 FROM provider_ledger_entries e
        WHERE e.provider_id = sqlc.arg(id)
          AND e.entry_type NOT IN ('adjustment_credit', 'adjustment_debit')
    ) AS ok
), deleted_admin_ledger AS (
    DELETE FROM provider_ledger_entries
    WHERE provider_id = sqlc.arg(id)
      AND (SELECT ok FROM admin_only_ledger)
), deleted_balances AS (
    DELETE FROM provider_balances
    WHERE provider_id = sqlc.arg(id)
      AND (SELECT ok FROM admin_only_ledger)
)
DELETE FROM providers WHERE providers.id = sqlc.arg(id);

-- name: ArchiveProvider :execrows
-- ArchiveProvider 只在无未归档 Channel、无非终态围栏操作时归档，并释放 origin 唯一槽位。
UPDATE providers
SET status = 'archived', status_revision = status_revision + 1,
    origin = origin || '__archived_' || id::text,
    archived_at = now(), updated_at = now()
WHERE providers.id = sqlc.arg(id)
  AND providers.status <> 'archived'
  AND NOT EXISTS (SELECT 1 FROM channels c WHERE c.provider_id = providers.id AND c.status <> 'archived')
  AND NOT EXISTS (
      SELECT 1 FROM provider_routing_operations operation
      WHERE operation.provider_id = providers.id AND operation.state NOT IN ('committed', 'aborted')
  );

-- name: RestoreProvider :execrows
-- RestoreProvider 取消归档 provider：archived → disabled（archived_at 清空）。不向下级联恢复渠道。
UPDATE providers
SET status = 'disabled', status_revision = status_revision + 1,
    origin = regexp_replace(origin, '__archived_' || id::text || '$', ''),
    archived_at = NULL, updated_at = now()
WHERE id = sqlc.arg(id)
  AND status = 'archived'
  AND origin LIKE '%__archived_' || id::text;

-- name: CountNonArchivedChannelsByProvider :one
SELECT COUNT(*) FROM channels WHERE provider_id = sqlc.arg(provider_id) AND status <> 'archived';

-- name: CreateProviderRechargeRate :one
-- CreateProviderRechargeRate 创建一条服务商充值汇率版本。provider_currency 由服务端从 providers.currency
-- 快照写入，不受客户端控制；启用窗口重叠由 ex_provider_recharge_rates_enabled_window 保证，违反报 23P01。
INSERT INTO provider_recharge_rates (
    provider_id,
    provider_currency,
    rate,
    status,
    source,
    reason,
    created_by,
    effective_from,
    effective_to
)
VALUES (
    sqlc.arg(provider_id),
    sqlc.arg(provider_currency),
    sqlc.arg(rate),
    sqlc.arg(status),
    sqlc.arg(source),
    sqlc.narg(reason),
    sqlc.narg(created_by),
    sqlc.arg(effective_from),
    sqlc.arg(effective_to)
)
RETURNING *;

-- name: ListProviderRechargeRatesByProvider :many
-- ListProviderRechargeRatesByProvider 列出某 provider 的全部充值汇率版本（含历史与停用），供服务商详情展示。
SELECT *
FROM provider_recharge_rates
WHERE provider_id = sqlc.arg(provider_id)
ORDER BY effective_from DESC, id DESC;

-- name: ListEnabledProviderRechargeRateWindows :many
-- ListEnabledProviderRechargeRateWindows 取某 provider 全部启用中的充值汇率生效窗口，供「窗口不重叠」校验；
-- exclude_id 用于更新时排除自身（创建时传 0）。
SELECT id, effective_from, effective_to
FROM provider_recharge_rates
WHERE provider_id = sqlc.arg(provider_id)
    AND status = 'enabled'
    AND id <> sqlc.arg(exclude_id);

-- name: UpdateProviderRechargeRateWindow :one
-- UpdateProviderRechargeRateWindow 调整生效结束时间与启停状态；汇率数值不可改（改汇率请新建一条），账务可复算。
UPDATE provider_recharge_rates
SET effective_to = sqlc.arg(effective_to),
    status = sqlc.arg(status),
    updated_at = now()
WHERE id = sqlc.arg(id)
RETURNING *;

-- name: CountEnabledChannelsByProvider :one
SELECT COUNT(*) FROM channels WHERE provider_id = sqlc.arg(provider_id) AND status = 'enabled';

-- §3.2 服务商聚合视图只读运维聚合。轻聚合：无 12 卡，表 + 4 Tab 抽屉。
-- provider 维度天然由 request_attempts.provider_id 归因（每次尝试记录 provider）。
-- 区间 [from,to) 半开；attempt 粒度性能/成功率；延迟由 completed_at-started_at 推导（毫秒）。

-- name: ProvidersOpsTable :many
-- ProvidersOpsTable 服务商运维主表（分页）：静态元数据 + 渠道/模型/线路数；指标在详情页聚合。
SELECT
    p.id,
    p.slug,
    p.name,
    p.status,
    p.origin,
    p.origin_revision,
    p.status_revision,
    p.currency,
    p.created_at,
    -- 余额按 provider 原始币种记账（D2）；balance_usd 是按最新汇率折算的展示口径（缺汇率时 NULL）。
    pb.balance AS balance,
    (CASE WHEN p.currency = 'USD' THEN pb.balance ELSE pb.balance / fx.rate END)::numeric AS balance_usd,
    fx.rate AS fx_rate,
    fx.rate_date AS fx_rate_date,
    -- 服务商当前生效充值汇率（服务商级，其下所有渠道共享；未配置时 rate 为 NULL、id 为 0，渠道不可启用/不进路由）。
    COALESCE(prr.id, 0)::bigint AS current_recharge_rate_id,
    prr.rate AS current_recharge_rate,
    prr.effective_from AS current_recharge_effective_from,
    -- 低余额判定按 USD 等值口径（§12.C.5）；缺汇率时比较为 NULL，落到 normal 不误报。
    CASE
        WHEN pb.balance IS NULL THEN 'unconfigured'
        WHEN pb.balance < 0 THEN 'negative'
        WHEN (CASE WHEN p.currency = 'USD' THEN pb.balance ELSE pb.balance / fx.rate END) < 10 THEN 'low'
        ELSE 'normal'
    END AS balance_status,
    (SELECT COUNT(*) FROM channels c WHERE c.provider_id = p.id) AS channel_total,
    (
        SELECT COUNT(DISTINCT cm.model_id)
        FROM channel_models cm
        JOIN channels c ON c.id = cm.channel_id
        WHERE c.provider_id = p.id AND cm.status = 'enabled'
    ) AS models_count
FROM providers p
LEFT JOIN provider_balances pb ON pb.provider_id = p.id AND pb.currency = p.currency
LEFT JOIN LATERAL (
    SELECT er.rate, er.rate_date
    FROM exchange_rates er
    WHERE er.base_currency = 'USD' AND er.quote_currency = p.currency
    ORDER BY er.rate_date DESC, er.fetched_at DESC
    LIMIT 1
) fx ON p.currency <> 'USD'
LEFT JOIN LATERAL (
    SELECT r.id, r.rate, r.effective_from
    FROM provider_recharge_rates r
    WHERE r.provider_id = p.id
      AND r.status = 'enabled'
      AND r.effective_from <= now()
      AND (r.effective_to IS NULL OR r.effective_to > now())
    ORDER BY r.effective_from DESC, r.id DESC
    LIMIT 1
) prr ON TRUE
WHERE (sqlc.narg('status')::text IS NULL OR p.status = sqlc.narg('status')::text)
  AND (sqlc.narg('search')::text IS NULL OR p.name ILIKE '%' || sqlc.narg('search')::text || '%' OR p.slug ILIKE '%' || sqlc.narg('search')::text || '%')
  AND (
      sqlc.narg('low_balance')::bool IS NULL
      OR NOT sqlc.narg('low_balance')::bool
      OR (pb.balance IS NOT NULL AND (
          pb.balance < 0
          OR (CASE WHEN p.currency = 'USD' THEN pb.balance ELSE pb.balance / fx.rate END) < 10
      ))
  )
ORDER BY
  CASE WHEN COALESCE(sqlc.narg('sort_field')::text, 'name') IN ('', 'name') AND COALESCE(sqlc.narg('sort_desc')::bool, false) THEN p.name END DESC NULLS LAST,
  CASE WHEN COALESCE(sqlc.narg('sort_field')::text, 'name') IN ('', 'name') AND NOT COALESCE(sqlc.narg('sort_desc')::bool, false) THEN p.name END ASC NULLS LAST,
  CASE WHEN sqlc.narg('sort_field')::text = 'created_at' AND COALESCE(sqlc.narg('sort_desc')::bool, false) THEN p.created_at END DESC NULLS LAST,
  CASE WHEN sqlc.narg('sort_field')::text = 'created_at' AND NOT COALESCE(sqlc.narg('sort_desc')::bool, false) THEN p.created_at END ASC NULLS LAST,
  CASE WHEN sqlc.narg('sort_field')::text = 'channels' AND COALESCE(sqlc.narg('sort_desc')::bool, false) THEN (
        SELECT COUNT(*) FROM channels c WHERE c.provider_id = p.id
    ) END DESC NULLS LAST,
  CASE WHEN sqlc.narg('sort_field')::text = 'channels' AND NOT COALESCE(sqlc.narg('sort_desc')::bool, false) THEN (
        SELECT COUNT(*) FROM channels c WHERE c.provider_id = p.id
    ) END ASC NULLS LAST,
  CASE WHEN sqlc.narg('sort_field')::text = 'models' AND COALESCE(sqlc.narg('sort_desc')::bool, false) THEN (
        SELECT COUNT(DISTINCT cm.model_id)
        FROM channel_models cm
        JOIN channels c ON c.id = cm.channel_id
        WHERE c.provider_id = p.id AND cm.status = 'enabled'
    ) END DESC NULLS LAST,
  CASE WHEN sqlc.narg('sort_field')::text = 'models' AND NOT COALESCE(sqlc.narg('sort_desc')::bool, false) THEN (
        SELECT COUNT(DISTINCT cm.model_id)
        FROM channel_models cm
        JOIN channels c ON c.id = cm.channel_id
        WHERE c.provider_id = p.id AND cm.status = 'enabled'
    ) END ASC NULLS LAST,
  CASE WHEN sqlc.narg('sort_field')::text = 'status' AND COALESCE(sqlc.narg('sort_desc')::bool, false) THEN p.status END DESC NULLS LAST,
  CASE WHEN sqlc.narg('sort_field')::text = 'status' AND NOT COALESCE(sqlc.narg('sort_desc')::bool, false) THEN p.status END ASC NULLS LAST,
  p.name
LIMIT sqlc.arg('page_limit') OFFSET sqlc.arg('page_offset');

-- name: ProvidersOpsTableCount :one
SELECT COUNT(*) AS total
FROM providers p
LEFT JOIN provider_balances pb ON pb.provider_id = p.id AND pb.currency = p.currency
LEFT JOIN LATERAL (
    SELECT er.rate
    FROM exchange_rates er
    WHERE er.base_currency = 'USD' AND er.quote_currency = p.currency
    ORDER BY er.rate_date DESC, er.fetched_at DESC
    LIMIT 1
) fx ON p.currency <> 'USD'
WHERE (sqlc.narg('status')::text IS NULL OR p.status = sqlc.narg('status')::text)
  AND (sqlc.narg('search')::text IS NULL OR p.name ILIKE '%' || sqlc.narg('search')::text || '%' OR p.slug ILIKE '%' || sqlc.narg('search')::text || '%')
  AND (
      sqlc.narg('low_balance')::bool IS NULL
      OR NOT sqlc.narg('low_balance')::bool
      OR (pb.balance IS NOT NULL AND (
          pb.balance < 0
          OR (CASE WHEN p.currency = 'USD' THEN pb.balance ELSE pb.balance / fx.rate END) < 10
      ))
  );

-- name: ProviderOpsDetail :one
-- ProviderOpsDetail 单服务商详情概览：渠道数 + attempt 聚合 + Token/利润/TPS。
-- 全部用标量子查询，避免 CROSS JOIN + COUNT 混用导致 GROUP BY 错误，且区间内无 attempt 时仍返回一行。
WITH money AS (
    SELECT
        COALESCE(SUM(
            u.uncached_input_tokens + u.cache_read_input_tokens
            + u.cache_creation_5m_input_tokens + u.cache_creation_1h_input_tokens + u.cache_creation_30m_input_tokens
            + u.output_tokens_total
        ), 0)::bigint AS tokens_total,
        COALESCE(SUM(le.amount) FILTER (WHERE le.entry_type = 'debit' AND le.currency = 'USD'), 0)::numeric AS revenue_usd,
        -- 成本统一读 USD 归一列（D8）：每笔按结算时钉住的汇率折算，跨币种可直接求和。
        COALESCE(SUM(cs.total_cost_amount_usd), 0)::numeric AS cost_usd
    FROM request_records r
    LEFT JOIN usage_records u ON u.request_record_id = r.id
    LEFT JOIN cost_snapshots cs ON cs.request_record_id = r.id
    LEFT JOIN ledger_entries le ON le.request_record_id = r.id
    WHERE r.final_provider_id = sqlc.arg('provider_id')
      AND (sqlc.narg('from_time')::timestamptz IS NULL OR r.created_at >= sqlc.narg('from_time')::timestamptz)
      AND (sqlc.narg('to_time')::timestamptz IS NULL OR r.created_at < sqlc.narg('to_time')::timestamptz)
),
balance AS (
    -- 余额按 provider 原始币种行读取（D2），折算列供展示；同时带出估值汇率与当前充值汇率。
    SELECT pb.balance, p.currency,
        (CASE WHEN p.currency = 'USD' THEN pb.balance ELSE pb.balance / fx.rate END)::numeric AS balance_usd,
        fx.rate AS fx_rate,
        fx.rate_date AS fx_rate_date,
        COALESCE(prr.id, 0)::bigint AS current_recharge_rate_id,
        prr.rate AS current_recharge_rate,
        prr.effective_from AS current_recharge_effective_from
    FROM providers p
    LEFT JOIN provider_balances pb ON pb.provider_id = p.id AND pb.currency = p.currency
    LEFT JOIN LATERAL (
        SELECT er.rate, er.rate_date
        FROM exchange_rates er
        WHERE er.base_currency = 'USD' AND er.quote_currency = p.currency
        ORDER BY er.rate_date DESC, er.fetched_at DESC
        LIMIT 1
    ) fx ON p.currency <> 'USD'
    LEFT JOIN LATERAL (
        SELECT r.id, r.rate, r.effective_from
        FROM provider_recharge_rates r
        WHERE r.provider_id = p.id
          AND r.status = 'enabled'
          AND r.effective_from <= now()
          AND (r.effective_to IS NULL OR r.effective_to > now())
        ORDER BY r.effective_from DESC, r.id DESC
        LIMIT 1
    ) prr ON TRUE
    WHERE p.id = sqlc.arg('provider_id')
),
tps AS (
    SELECT COALESCE(
        SUM(u.output_tokens_total)::float8 / NULLIF(SUM(
            CASE
                WHEN a.completed_at IS NOT NULL
                THEN EXTRACT(EPOCH FROM (a.completed_at - COALESCE(a.gateway_first_token_at, a.started_at)))
            END
        ), 0),
        0
    )::float8 AS avg_tps
    FROM request_attempts a
    JOIN usage_records u ON u.request_record_id = a.request_record_id
    WHERE a.provider_id = sqlc.arg('provider_id')
      AND a.status = 'succeeded'
      AND (sqlc.narg('from_time')::timestamptz IS NULL OR a.created_at >= sqlc.narg('from_time')::timestamptz)
      AND (sqlc.narg('to_time')::timestamptz IS NULL OR a.created_at < sqlc.narg('to_time')::timestamptz)
),
attempts AS (
    SELECT *
    FROM request_attempts a
    WHERE a.provider_id = sqlc.arg('provider_id')
      AND (sqlc.narg('from_time')::timestamptz IS NULL OR a.created_at >= sqlc.narg('from_time')::timestamptz)
      AND (sqlc.narg('to_time')::timestamptz IS NULL OR a.created_at < sqlc.narg('to_time')::timestamptz)
),
cache_usage AS (
    SELECT u.*
    FROM request_records r
    JOIN usage_records u ON u.request_record_id = r.id
    WHERE r.final_provider_id = sqlc.arg('provider_id')
      AND u.usage_source IN ('upstream_response', 'upstream_stream')
      AND (sqlc.narg('from_time')::timestamptz IS NULL OR r.created_at >= sqlc.narg('from_time')::timestamptz)
      AND (sqlc.narg('to_time')::timestamptz IS NULL OR r.created_at < sqlc.narg('to_time')::timestamptz)
),
cache AS (
    SELECT
        COALESCE(SUM(uncached_input_tokens) FILTER (WHERE
            uncached_input_tokens_state = 'known'
            AND cache_read_input_tokens_state = 'known'
            AND cache_creation_5m_input_tokens_state <> 'unknown'
            AND cache_creation_1h_input_tokens_state <> 'unknown'
            AND cache_creation_30m_input_tokens_state <> 'unknown'
            AND uncached_input_tokens + cache_read_input_tokens
                + cache_creation_5m_input_tokens + cache_creation_1h_input_tokens + cache_creation_30m_input_tokens > 0
        ), 0)::bigint AS cache_uncached_input,
        COALESCE(SUM(cache_read_input_tokens) FILTER (WHERE
            uncached_input_tokens_state = 'known'
            AND cache_read_input_tokens_state = 'known'
            AND cache_creation_5m_input_tokens_state <> 'unknown'
            AND cache_creation_1h_input_tokens_state <> 'unknown'
            AND cache_creation_30m_input_tokens_state <> 'unknown'
            AND uncached_input_tokens + cache_read_input_tokens
                + cache_creation_5m_input_tokens + cache_creation_1h_input_tokens + cache_creation_30m_input_tokens > 0
        ), 0)::bigint AS cache_read_input,
        COALESCE(SUM(cache_creation_5m_input_tokens) FILTER (WHERE
            uncached_input_tokens_state = 'known'
            AND cache_read_input_tokens_state = 'known'
            AND cache_creation_5m_input_tokens_state <> 'unknown'
            AND cache_creation_1h_input_tokens_state <> 'unknown'
            AND cache_creation_30m_input_tokens_state <> 'unknown'
            AND uncached_input_tokens + cache_read_input_tokens
                + cache_creation_5m_input_tokens + cache_creation_1h_input_tokens + cache_creation_30m_input_tokens > 0
        ), 0)::bigint AS cache_creation_5m_input,
        COALESCE(SUM(cache_creation_1h_input_tokens) FILTER (WHERE
            uncached_input_tokens_state = 'known'
            AND cache_read_input_tokens_state = 'known'
            AND cache_creation_5m_input_tokens_state <> 'unknown'
            AND cache_creation_1h_input_tokens_state <> 'unknown'
            AND cache_creation_30m_input_tokens_state <> 'unknown'
            AND uncached_input_tokens + cache_read_input_tokens
                + cache_creation_5m_input_tokens + cache_creation_1h_input_tokens + cache_creation_30m_input_tokens > 0
        ), 0)::bigint AS cache_creation_1h_input,
        COALESCE(SUM(cache_creation_30m_input_tokens) FILTER (WHERE
            uncached_input_tokens_state = 'known'
            AND cache_read_input_tokens_state = 'known'
            AND cache_creation_5m_input_tokens_state <> 'unknown'
            AND cache_creation_1h_input_tokens_state <> 'unknown'
            AND cache_creation_30m_input_tokens_state <> 'unknown'
            AND uncached_input_tokens + cache_read_input_tokens
                + cache_creation_5m_input_tokens + cache_creation_1h_input_tokens + cache_creation_30m_input_tokens > 0
        ), 0)::bigint AS cache_creation_30m_input,
        COUNT(*) AS cache_usage_records,
        COUNT(*) FILTER (WHERE
            uncached_input_tokens_state = 'known'
            AND cache_read_input_tokens_state = 'known'
            AND cache_creation_5m_input_tokens_state <> 'unknown'
            AND cache_creation_1h_input_tokens_state <> 'unknown'
            AND cache_creation_30m_input_tokens_state <> 'unknown'
            AND uncached_input_tokens + cache_read_input_tokens
                + cache_creation_5m_input_tokens + cache_creation_1h_input_tokens + cache_creation_30m_input_tokens > 0
        ) AS cache_evaluable_records,
        COUNT(*) FILTER (WHERE cache_read_input_tokens_state = 'not_applicable') AS cache_read_not_applicable_records
    FROM cache_usage
)
SELECT
    (SELECT balance FROM balance) AS balance,
    (SELECT currency FROM balance) AS balance_currency,
    (SELECT balance_usd FROM balance) AS balance_usd,
    (SELECT fx_rate FROM balance) AS balance_fx_rate,
    (SELECT fx_rate_date FROM balance) AS balance_fx_rate_date,
    (SELECT current_recharge_rate_id FROM balance) AS current_recharge_rate_id,
    (SELECT current_recharge_rate FROM balance) AS current_recharge_rate,
    (SELECT current_recharge_effective_from FROM balance) AS current_recharge_effective_from,
    CASE
        WHEN (SELECT balance FROM balance) IS NULL THEN 'unconfigured'
        WHEN (SELECT balance FROM balance) < 0 THEN 'negative'
        WHEN (SELECT balance_usd FROM balance) < 10 THEN 'low'
        ELSE 'normal'
    END AS balance_status,
    (SELECT COUNT(*) FROM channels c WHERE c.provider_id = sqlc.arg('provider_id')) AS channel_total,
    (SELECT COUNT(*) FROM channels c WHERE c.provider_id = sqlc.arg('provider_id') AND c.status = 'enabled') AS channel_enabled,
    (SELECT COUNT(*) FROM attempts WHERE status = 'succeeded' OR fault_party = 'upstream') AS attempt_total,
    (SELECT COUNT(*) FROM attempts WHERE status = 'succeeded') AS attempt_succeeded,
    (SELECT COUNT(*) FROM attempts WHERE status = 'failed' AND (error_code ILIKE '%timeout%' OR error_code = 'context_deadline_exceeded')) AS timeout_total,
    (SELECT COUNT(*) FROM attempts WHERE status = 'succeeded' AND completed_at IS NOT NULL) AS latency_sample,
    (SELECT COALESCE(AVG(CASE WHEN status = 'succeeded' AND completed_at IS NOT NULL
        THEN (EXTRACT(EPOCH FROM (completed_at - started_at)) * 1000)::float8 END), 0)::float8 FROM attempts) AS latency_avg,
    (SELECT COALESCE(percentile_cont(0.5) WITHIN GROUP (ORDER BY
        CASE WHEN status = 'succeeded' AND completed_at IS NOT NULL
             THEN (EXTRACT(EPOCH FROM (completed_at - started_at)) * 1000)::float8 END), 0)::float8 FROM attempts) AS latency_p50,
    (SELECT COALESCE(percentile_cont(0.9) WITHIN GROUP (ORDER BY
        CASE WHEN status = 'succeeded' AND completed_at IS NOT NULL
             THEN (EXTRACT(EPOCH FROM (completed_at - started_at)) * 1000)::float8 END), 0)::float8 FROM attempts) AS latency_p90,
    (SELECT COALESCE(percentile_cont(0.95) WITHIN GROUP (ORDER BY
        CASE WHEN status = 'succeeded' AND completed_at IS NOT NULL
             THEN (EXTRACT(EPOCH FROM (completed_at - started_at)) * 1000)::float8 END), 0)::float8 FROM attempts) AS latency_p95,
    (SELECT COALESCE(percentile_cont(0.99) WITHIN GROUP (ORDER BY
        CASE WHEN status = 'succeeded' AND completed_at IS NOT NULL
             THEN (EXTRACT(EPOCH FROM (completed_at - started_at)) * 1000)::float8 END), 0)::float8 FROM attempts) AS latency_p99,
    (SELECT tokens_total FROM money) AS tokens_total,
    (SELECT revenue_usd FROM money) AS revenue_usd,
    (SELECT cost_usd FROM money) AS cost_usd,
    (SELECT avg_tps FROM tps) AS avg_tps,
    (SELECT cache_uncached_input FROM cache) AS cache_uncached_input,
    (SELECT cache_read_input FROM cache) AS cache_read_input,
    (SELECT cache_creation_5m_input FROM cache) AS cache_creation_5m_input,
    (SELECT cache_creation_1h_input FROM cache) AS cache_creation_1h_input,
    (SELECT cache_creation_30m_input FROM cache) AS cache_creation_30m_input,
    (SELECT cache_usage_records FROM cache) AS cache_usage_records,
    (SELECT cache_evaluable_records FROM cache) AS cache_evaluable_records,
    (SELECT cache_read_not_applicable_records FROM cache) AS cache_read_not_applicable_records;

-- name: ProviderOpsChannelCatalog :many
-- ProviderOpsChannelCatalog 服务商渠道清单（列表 Tip，无指标）。
SELECT c.id, c.name, c.status
FROM channels c
WHERE c.provider_id = sqlc.arg('provider_id')
ORDER BY c.name, c.id;

-- name: ProviderOpsModelCatalog :many
-- ProviderOpsModelCatalog 服务商绑定模型清单（列表 Tip）。
SELECT DISTINCT m.model_id, m.display_name
FROM models m
JOIN channel_models cm ON cm.model_id = m.id AND cm.status = 'enabled'
JOIN channels c ON c.id = cm.channel_id
WHERE c.provider_id = sqlc.arg('provider_id')
ORDER BY m.model_id
LIMIT 500;

-- name: ProviderOpsChannels :many
-- ProviderOpsChannels 单服务商下渠道精简子列表 + attempt 指标（抽屉渠道 Tab）。
-- 逐 Channel 缓存画像排除 Sticky 跨渠道切换 usage；ProviderOpsDetail 汇总仍保留真实消耗。
WITH cache AS (
    SELECT
        r.final_channel_id AS channel_id,
        COALESCE(SUM(u.uncached_input_tokens) FILTER (WHERE
            u.uncached_input_tokens_state = 'known'
            AND u.cache_read_input_tokens_state = 'known'
            AND u.cache_creation_5m_input_tokens_state <> 'unknown'
            AND u.cache_creation_1h_input_tokens_state <> 'unknown'
            AND u.cache_creation_30m_input_tokens_state <> 'unknown'
            AND u.uncached_input_tokens + u.cache_read_input_tokens
                + u.cache_creation_5m_input_tokens + u.cache_creation_1h_input_tokens + u.cache_creation_30m_input_tokens > 0
        ), 0)::bigint AS cache_uncached_input,
        COALESCE(SUM(u.cache_read_input_tokens) FILTER (WHERE
            u.uncached_input_tokens_state = 'known'
            AND u.cache_read_input_tokens_state = 'known'
            AND u.cache_creation_5m_input_tokens_state <> 'unknown'
            AND u.cache_creation_1h_input_tokens_state <> 'unknown'
            AND u.cache_creation_30m_input_tokens_state <> 'unknown'
            AND u.uncached_input_tokens + u.cache_read_input_tokens
                + u.cache_creation_5m_input_tokens + u.cache_creation_1h_input_tokens + u.cache_creation_30m_input_tokens > 0
        ), 0)::bigint AS cache_read_input,
        COALESCE(SUM(u.cache_creation_5m_input_tokens) FILTER (WHERE
            u.uncached_input_tokens_state = 'known'
            AND u.cache_read_input_tokens_state = 'known'
            AND u.cache_creation_5m_input_tokens_state <> 'unknown'
            AND u.cache_creation_1h_input_tokens_state <> 'unknown'
            AND u.cache_creation_30m_input_tokens_state <> 'unknown'
            AND u.uncached_input_tokens + u.cache_read_input_tokens
                + u.cache_creation_5m_input_tokens + u.cache_creation_1h_input_tokens + u.cache_creation_30m_input_tokens > 0
        ), 0)::bigint AS cache_creation_5m_input,
        COALESCE(SUM(u.cache_creation_1h_input_tokens) FILTER (WHERE
            u.uncached_input_tokens_state = 'known'
            AND u.cache_read_input_tokens_state = 'known'
            AND u.cache_creation_5m_input_tokens_state <> 'unknown'
            AND u.cache_creation_1h_input_tokens_state <> 'unknown'
            AND u.cache_creation_30m_input_tokens_state <> 'unknown'
            AND u.uncached_input_tokens + u.cache_read_input_tokens
                + u.cache_creation_5m_input_tokens + u.cache_creation_1h_input_tokens + u.cache_creation_30m_input_tokens > 0
        ), 0)::bigint AS cache_creation_1h_input,
        COALESCE(SUM(u.cache_creation_30m_input_tokens) FILTER (WHERE
            u.uncached_input_tokens_state = 'known'
            AND u.cache_read_input_tokens_state = 'known'
            AND u.cache_creation_5m_input_tokens_state <> 'unknown'
            AND u.cache_creation_1h_input_tokens_state <> 'unknown'
            AND u.cache_creation_30m_input_tokens_state <> 'unknown'
            AND u.uncached_input_tokens + u.cache_read_input_tokens
                + u.cache_creation_5m_input_tokens + u.cache_creation_1h_input_tokens + u.cache_creation_30m_input_tokens > 0
        ), 0)::bigint AS cache_creation_30m_input,
        COUNT(*) AS cache_usage_records,
        COUNT(*) FILTER (WHERE
            u.uncached_input_tokens_state = 'known'
            AND u.cache_read_input_tokens_state = 'known'
            AND u.cache_creation_5m_input_tokens_state <> 'unknown'
            AND u.cache_creation_1h_input_tokens_state <> 'unknown'
            AND u.cache_creation_30m_input_tokens_state <> 'unknown'
            AND u.uncached_input_tokens + u.cache_read_input_tokens
                + u.cache_creation_5m_input_tokens + u.cache_creation_1h_input_tokens + u.cache_creation_30m_input_tokens > 0
        ) AS cache_evaluable_records,
        COUNT(*) FILTER (WHERE u.cache_read_input_tokens_state = 'not_applicable') AS cache_read_not_applicable_records
    FROM request_records r
    JOIN usage_records u ON u.request_record_id = r.id
    WHERE r.final_provider_id = sqlc.arg('provider_id')
      AND r.final_channel_id IS NOT NULL
      AND u.usage_source IN ('upstream_response', 'upstream_stream')
      AND (sqlc.narg('from_time')::timestamptz IS NULL OR r.created_at >= sqlc.narg('from_time')::timestamptz)
      AND (sqlc.narg('to_time')::timestamptz IS NULL OR r.created_at < sqlc.narg('to_time')::timestamptz)
      AND NOT EXISTS (
          SELECT 1
          FROM routing_decision_traces rdt
          WHERE rdt.request_record_id = r.id
            AND rdt.sticky_before_channel_id IS NOT NULL
            AND rdt.sticky_before_channel_id <> r.final_channel_id
      )
    GROUP BY r.final_channel_id
)
SELECT
    c.id,
    c.name,
    p.origin,
    c.status,
    COUNT(a.id) FILTER (WHERE a.status = 'succeeded' OR a.fault_party = 'upstream') AS attempt_total,
    COUNT(a.id) FILTER (WHERE a.status = 'succeeded') AS attempt_succeeded,
    COUNT(a.id) FILTER (WHERE a.status = 'succeeded' AND a.completed_at IS NOT NULL) AS latency_sample,
    COALESCE(AVG(CASE WHEN a.status = 'succeeded' AND a.completed_at IS NOT NULL
        THEN (EXTRACT(EPOCH FROM (a.completed_at - a.started_at)) * 1000)::float8 END), 0)::float8 AS latency_avg,
    COALESCE(percentile_cont(0.5) WITHIN GROUP (ORDER BY
        CASE WHEN a.status = 'succeeded' AND a.completed_at IS NOT NULL
             THEN (EXTRACT(EPOCH FROM (a.completed_at - a.started_at)) * 1000)::float8 END), 0)::float8 AS latency_p50,
    COALESCE(percentile_cont(0.9) WITHIN GROUP (ORDER BY
        CASE WHEN a.status = 'succeeded' AND a.completed_at IS NOT NULL
             THEN (EXTRACT(EPOCH FROM (a.completed_at - a.started_at)) * 1000)::float8 END), 0)::float8 AS latency_p90,
    COALESCE(percentile_cont(0.95) WITHIN GROUP (ORDER BY
        CASE WHEN a.status = 'succeeded' AND a.completed_at IS NOT NULL
             THEN (EXTRACT(EPOCH FROM (a.completed_at - a.started_at)) * 1000)::float8 END), 0)::float8 AS latency_p95,
    COALESCE(percentile_cont(0.99) WITHIN GROUP (ORDER BY
        CASE WHEN a.status = 'succeeded' AND a.completed_at IS NOT NULL
             THEN (EXTRACT(EPOCH FROM (a.completed_at - a.started_at)) * 1000)::float8 END), 0)::float8 AS latency_p99,
    COALESCE(cache.cache_uncached_input, 0)::bigint AS cache_uncached_input,
    COALESCE(cache.cache_read_input, 0)::bigint AS cache_read_input,
    COALESCE(cache.cache_creation_5m_input, 0)::bigint AS cache_creation_5m_input,
    COALESCE(cache.cache_creation_1h_input, 0)::bigint AS cache_creation_1h_input,
    COALESCE(cache.cache_creation_30m_input, 0)::bigint AS cache_creation_30m_input,
    COALESCE(cache.cache_usage_records, 0)::bigint AS cache_usage_records,
    COALESCE(cache.cache_evaluable_records, 0)::bigint AS cache_evaluable_records,
    COALESCE(cache.cache_read_not_applicable_records, 0)::bigint AS cache_read_not_applicable_records
FROM channels c
JOIN providers p ON p.id = c.provider_id
LEFT JOIN request_attempts a
    ON a.channel_id = c.id
    AND (sqlc.narg('from_time')::timestamptz IS NULL OR a.created_at >= sqlc.narg('from_time')::timestamptz)
    AND (sqlc.narg('to_time')::timestamptz IS NULL OR a.created_at < sqlc.narg('to_time')::timestamptz)
LEFT JOIN cache ON cache.channel_id = c.id
WHERE c.provider_id = sqlc.arg('provider_id')
GROUP BY c.id, c.name, p.origin, c.status,
    cache.cache_uncached_input, cache.cache_read_input, cache.cache_creation_5m_input,
    cache.cache_creation_1h_input, cache.cache_creation_30m_input, cache.cache_usage_records,
    cache.cache_evaluable_records, cache.cache_read_not_applicable_records
ORDER BY attempt_total DESC, c.id;

-- name: ProviderOpsPerformanceTimeseries :many
-- ProviderOpsPerformanceTimeseries 单服务商 attempt 趋势（抽屉性能 Tab）。
SELECT
    date_trunc(sqlc.arg('unit')::text, created_at)::timestamptz AS bucket,
    COUNT(*) FILTER (WHERE status = 'succeeded' OR fault_party = 'upstream') AS attempt_total,
    COUNT(*) FILTER (WHERE status = 'succeeded') AS attempt_succeeded,
    COALESCE(AVG(CASE WHEN status = 'succeeded' AND completed_at IS NOT NULL
        THEN (EXTRACT(EPOCH FROM (completed_at - started_at)) * 1000)::float8 END), 0)::float8 AS latency_avg
FROM request_attempts
WHERE provider_id = sqlc.arg('provider_id')
  AND (sqlc.narg('from_time')::timestamptz IS NULL OR created_at >= sqlc.narg('from_time')::timestamptz)
  AND (sqlc.narg('to_time')::timestamptz IS NULL OR created_at < sqlc.narg('to_time')::timestamptz)
GROUP BY bucket
ORDER BY bucket;

-- name: ProviderOpsErrors :many
-- ProviderOpsErrors 单服务商错误明细（抽屉错误 Tab，分页）。
SELECT
    a.created_at,
    c.name AS channel_name,
    a.upstream_model,
    a.error_code,
    a.upstream_status_code,
    r.request_id
FROM request_attempts a
JOIN request_records r ON r.id = a.request_record_id
JOIN channels c ON c.id = a.channel_id
WHERE a.provider_id = sqlc.arg('provider_id')
  AND a.status = 'failed'
  AND (sqlc.narg('from_time')::timestamptz IS NULL OR a.created_at >= sqlc.narg('from_time')::timestamptz)
  AND (sqlc.narg('to_time')::timestamptz IS NULL OR a.created_at < sqlc.narg('to_time')::timestamptz)
ORDER BY a.created_at DESC
LIMIT sqlc.arg('page_limit') OFFSET sqlc.arg('page_offset');

-- name: ProviderOpsErrorsCount :one
SELECT COUNT(*) AS total
FROM request_attempts a
WHERE a.provider_id = sqlc.arg('provider_id')
  AND a.status = 'failed'
  AND (sqlc.narg('from_time')::timestamptz IS NULL OR a.created_at >= sqlc.narg('from_time')::timestamptz)
  AND (sqlc.narg('to_time')::timestamptz IS NULL OR a.created_at < sqlc.narg('to_time')::timestamptz);
