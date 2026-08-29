-- 多货币存量数据修正（PLAN M6 + §12.C.1/§12.C.2，执行于 2026-08-29，开发库）。
--
-- ⚠️ 部分已撤销（D2 修订，见 data-fix-v2.sql）：下方第 2) 项「充值倍率折汇率」已被撤销，
-- 倍率回归 1.0——倍率路径成本按 provider 币种记账 + 当日汇率动态折算（迁移 000059）。
-- 第 1) 项「售价倍率 ÷ 当日汇率」仍然有效。
--
-- 口径（业务决策 §12.C.1）：全部按执行日当天汇率 1 USD = 6.742068 CNY
-- （exchange_rates 当日 er-api 实拉值，rate_date=2026-08-29）。
--
-- 数据盘点结论：开发库无 channel_prices 绝对成本行、无绝对售价行，成本全走倍率路径；
-- 「售价 1.25/6.25 错标」的实体形态是 sale_price_ratio（5/25 基准 × 0.25 = 1.25/6.25 被当 USD 卖）。
-- starapi 为「1:1 CNY 充值买名义 USD」供应商（§12.C.2），按 D1 边界属于倍率路径 + 充值倍率折汇率，
-- 因此修正两件事：
--   1) 售价倍率 ÷ 当日汇率：客户 USD 实付回到与原 CNY 数字等值（0.2→0.0296645…，0.25→0.0370806…）；
--   2) starapi 渠道充值倍率 1.0 → 1/6.742068 ≈ 0.1483222833：把「充 1 CNY 得 1 名义 USD」的
--      汇率折进 DEC-027 本义，真实成本不再被高估 ~6.74 倍。
-- chat-settlement-* 测试残留供应商/渠道（无模型绑定、无真实流量语义）不动。
-- 守卫为 deferred 约束，提交时按当日汇率整体复验（分支 B：ratio ≥ multiplier × factor，已手算通过）。
--
-- 注：channel_recharge_factors 常规变更走「新建行 + 关闭旧窗口」追加范式；本次是一次性
-- 数据修正（dev 库、修正脚本本身进版本库即审计记录），直接 UPDATE 现行行。

BEGIN;

\echo '===== 修正前对照 ====='
SELECT id, model_id, sale_price_ratio FROM model_prices WHERE status = 'enabled' AND sale_price_ratio IS NOT NULL ORDER BY id;
SELECT crf.id, c.name AS channel, crf.factor
FROM channel_recharge_factors crf
JOIN channels c ON c.id = crf.channel_id
JOIN providers p ON p.id = c.provider_id
WHERE p.slug = 'starapi' AND crf.status = 'enabled'
ORDER BY crf.id;

-- 1) 售价倍率 ÷ 当日汇率（仅启用行；停用行是历史，保留原值）。
UPDATE model_prices
SET sale_price_ratio = round(sale_price_ratio / 6.742068, 10), updated_at = now()
WHERE status = 'enabled' AND sale_price_ratio IS NOT NULL;

-- 2) starapi 充值倍率折入 1:1 CNY 充值的汇率。
UPDATE channel_recharge_factors crf
SET factor = round(1.0 / 6.742068, 10), updated_at = now()
FROM channels c
JOIN providers p ON p.id = c.provider_id
WHERE crf.channel_id = c.id AND p.slug = 'starapi' AND crf.status = 'enabled';

\echo '===== 修正后对照 ====='
SELECT id, model_id, sale_price_ratio FROM model_prices WHERE status = 'enabled' AND sale_price_ratio IS NOT NULL ORDER BY id;
SELECT crf.id, c.name AS channel, crf.factor
FROM channel_recharge_factors crf
JOIN channels c ON c.id = crf.channel_id
JOIN providers p ON p.id = c.provider_id
WHERE p.slug = 'starapi' AND crf.status = 'enabled'
ORDER BY crf.id;

COMMIT;
