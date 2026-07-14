-- 000037: 成本基数改用模型基准价（DEC-031，退役 model_reference_costs）。
--
-- 背景：DEC-027 用独立表 model_reference_costs 作「上游参考成本」基数，但其列与 model_prices
-- （DEC-026 模型基准价）几乎一一对应（*_cost ↔ *_price），运维需同一模型录两遍，且「只配基准价 +
-- 渠道倍率」会被判「未定价」（Enabled·不可售根因之一）。DEC-031 令 model_prices 成为售价与成本的
-- 唯一基数：真实成本 = model_prices 向量 × 价格倍率 × 充值倍率（或 channel_prices 绝对覆盖）。
--
-- 本迁移做三件事：
--   1) 把 cost_snapshots / settlement_recovery_jobs 的成本来源 pin 列 model_reference_cost_id
--      重命名为 cost_base_model_price_id（语义：结算所用成本基数的 model_prices.id）。
--   2) 历史行的旧值是 model_reference_costs.id，作 model_prices.id 无意义 → 一律置 NULL。
--      账务权威是快照冻结的绝对金额列（uncached_input_cost_amount 等），来源 id 仅供审计辅助，
--      置 NULL 不影响历史账单。
--   3) DROP model_reference_costs 表。
--
-- 重要（DEC-031 rev.1）：cost_base_model_price_id 保持无 FK。cost_snapshots/settlement_recovery_jobs
-- 是 append-only 审计表，权威是冻结金额列；无 FK 让 model_prices 行可自由停用/删除而不破坏历史，
-- 与旧 model_reference_cost_id 同为无 FK 的设计一致。故此处仅 rename 列，不新增/改动任何外键。
--
-- 前置门禁（见 DESIGN-cost-base-from-model-price.md §7 阶段 0/1）：
--   - 无 pending 的 settlement_recovery_jobs 仍引用 model_reference_costs（replay 会失败）。
--   - model_reference_costs 与 model_prices 金额口径已对齐（或库空/参考成本可弃）。

BEGIN;

-- 1) cost_snapshots：rename 成本来源 pin 列，历史旧值置 NULL（旧值是 refcost id，非 model_price id）。
ALTER TABLE public.cost_snapshots
    RENAME COLUMN model_reference_cost_id TO cost_base_model_price_id;
UPDATE public.cost_snapshots SET cost_base_model_price_id = NULL;

-- 2) settlement_recovery_jobs：同样 rename + 置 NULL。
ALTER TABLE public.settlement_recovery_jobs
    RENAME COLUMN model_reference_cost_id TO cost_base_model_price_id;
UPDATE public.settlement_recovery_jobs SET cost_base_model_price_id = NULL;

-- 3) 退役 model_reference_costs（表 + sequence + 索引 + exclusion 约束随 CASCADE 一并删除）。
DROP TABLE IF EXISTS public.model_reference_costs CASCADE;

COMMIT;
