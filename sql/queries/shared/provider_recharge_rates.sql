-- name: GetProviderRechargeRate :one
-- GetProviderRechargeRate 按主键读取单条服务商充值汇率。
SELECT * FROM provider_recharge_rates WHERE id = sqlc.arg(id) LIMIT 1;
