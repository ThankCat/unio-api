-- name: UpsertExchangeRate :one
-- UpsertExchangeRate 写入一条汇率；同 (币种对, 汇率日, 来源) 已存在时刷新 rate 与 fetched_at
-- （worker 同日重复拉取 / 手工同日修正都走此路径），行只追加或刷新、从不删除。
INSERT INTO exchange_rates (base_currency, quote_currency, rate, rate_date, source)
VALUES (
    sqlc.arg(base_currency), sqlc.arg(quote_currency), sqlc.arg(rate),
    sqlc.arg(rate_date), sqlc.arg(source)
)
ON CONFLICT (base_currency, quote_currency, rate_date, source)
DO UPDATE SET rate = EXCLUDED.rate, fetched_at = now()
RETURNING id, base_currency, quote_currency, rate, rate_date, source, fetched_at;

-- name: LatestExchangeRate :one
-- LatestExchangeRate 取某币种对当前生效汇率：rate_date 最新优先，同日 fetched_at 最新优先
-- （手工行只要日期/时间更新即覆盖外部源）。守卫 / 结算 / 展示的唯一消费口径。
SELECT id, base_currency, quote_currency, rate, rate_date, source, fetched_at
FROM exchange_rates
WHERE base_currency = sqlc.arg(base_currency)
  AND quote_currency = sqlc.arg(quote_currency)
ORDER BY rate_date DESC, fetched_at DESC
LIMIT 1;

-- name: ListExchangeRatesPage :many
-- ListExchangeRatesPage 分页列出汇率历史（admin 汇率页），可按目标币种过滤。
SELECT id, base_currency, quote_currency, rate, rate_date, source, fetched_at
FROM exchange_rates
WHERE (sqlc.narg(quote_currency)::text IS NULL OR quote_currency = sqlc.narg(quote_currency)::text)
ORDER BY rate_date DESC, fetched_at DESC, id DESC
LIMIT sqlc.arg(page_limit) OFFSET sqlc.arg(page_offset);

-- name: CountExchangeRates :one
-- CountExchangeRates 统计与 ListExchangeRatesPage 同口径的总数。
SELECT COUNT(*) AS total
FROM exchange_rates
WHERE (sqlc.narg(quote_currency)::text IS NULL OR quote_currency = sqlc.narg(quote_currency)::text);
