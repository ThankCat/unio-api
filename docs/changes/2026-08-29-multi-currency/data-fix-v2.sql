-- 数据修正 v2（2026-08-29，撤销 data-fix.sql 的充值倍率改动）。
--
-- 背景：v1 把 starapi 充值倍率改为 1/6.742068，将汇率静态folding进倍率——与 §12.C.2 决策
-- （成本按渠道货币记账 + 当日汇率动态折算）矛盾，汇率波动无法再反映到这些渠道的毛利。
-- D2 修订后倍率路径成本按 provider 结算币种（CNY）记账：基准价数值 × 倍率 = CNY 金额，
-- 充值倍率回归本义「1 单位上游名义额度花多少原币」= 1.0（1:1 CNY 充值），
-- 守卫/毛利/报表按当日汇率动态折算（迁移 000059 已使守卫 B/C 分支币种感知）。
-- 售价倍率的 v1 修正（÷ 当日汇率）仍然正确，保持不变。
--
-- 提交时新守卫复验（分支 B）：ratio × rate ≥ multiplier × factor
--   gpt 组：  0.0296644887 × 6.742068 ≈ 0.20 ≥ 0.10 × 1.0 ✓
--   claude 组：0.0370806109 × 6.742068 ≈ 0.25 ≥ 0.12 × 1.0 ✓

BEGIN;

\echo '===== 修正前对照 ====='
SELECT crf.id, c.name AS channel, crf.factor
FROM channel_recharge_factors crf
JOIN channels c ON c.id = crf.channel_id
JOIN providers p ON p.id = c.provider_id
WHERE p.slug = 'starapi' AND crf.status = 'enabled'
ORDER BY crf.id;

UPDATE channel_recharge_factors crf
SET factor = 1.0, updated_at = now()
FROM channels c
JOIN providers p ON p.id = c.provider_id
WHERE crf.channel_id = c.id AND p.slug = 'starapi' AND crf.status = 'enabled';

\echo '===== 修正后对照 ====='
SELECT crf.id, c.name AS channel, crf.factor
FROM channel_recharge_factors crf
JOIN channels c ON c.id = crf.channel_id
JOIN providers p ON p.id = c.provider_id
WHERE p.slug = 'starapi' AND crf.status = 'enabled'
ORDER BY crf.id;

COMMIT;
