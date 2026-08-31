-- name: FindActiveProviderRechargeRate :one
-- FindActiveProviderRechargeRate 查找指定 provider 在指定时间生效的充值汇率（服务商级，其下所有渠道共享）。
SELECT *
FROM provider_recharge_rates
WHERE provider_id = sqlc.arg(provider_id)
    AND status = 'enabled'
    AND effective_from <= sqlc.arg(at_time)
    AND (
        effective_to IS NULL
        OR effective_to > sqlc.arg(at_time)
    )
ORDER BY effective_from DESC, id DESC
LIMIT 1;

-- name: GetChannelProviderBilling :one
-- GetChannelProviderBilling 取渠道所属 provider 的 id 与结算币种：
-- 倍率路径成本按 provider 币种记账，充值汇率按 provider 解析（settlement / probe 共用）。
SELECT p.id AS provider_id, p.currency
FROM providers p
JOIN channels c ON c.provider_id = p.id
WHERE c.id = sqlc.arg(channel_id);
