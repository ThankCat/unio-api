-- 回填历史 price_snapshots.price_ratio（以及配套 settlement_recovery_jobs.price_ratio）。
--
-- 背景：migration 000071 之前的历史结算没有记录「线路倍率」快照，导致请求详情/列表的「线路倍率」
--       与倒推出的「模型基准价」实时读当前 routes.price_ratio，会被后续改倍率污染（显示成当前值）。
--
-- 原理（精确还原，非臆造）：model_prices 是不可变的、带生效窗口的模型基准售价（改价 = 新增一行，
--       账务可复算）。结算时：售价快照 = 结算当时生效的模型基准价 × 线路倍率。因此可逐行反解：
--         线路倍率 = 售价快照.uncached_input_price ÷ 结算当时生效的基准价.uncached_input_price
--       按 request_records.started_at 命中当时生效的 model_prices 窗口，逐行还原——自动适配不同线路、
--       不同历史倍率，且与「当时真实扣费」口径一致（比一刀切填某个值更准确）。
--
-- 幂等：仅回填 price_ratio IS NULL 的行；可安全重复执行。base 单价为 0（免费模型）无法反解的行保持 NULL。

BEGIN;

-- 1) 客户售价快照：逐行反解结算当时的线路倍率。
UPDATE price_snapshots ps
SET price_ratio = round(ps.uncached_input_price / mp.uncached_input_price, 6)
FROM request_records rr
JOIN models m ON m.model_id = rr.requested_model_id
JOIN LATERAL (
    SELECT uncached_input_price
    FROM model_prices
    WHERE model_id = m.id
      AND status = 'enabled'
      AND effective_from <= rr.started_at
      AND (effective_to IS NULL OR effective_to > rr.started_at)
    ORDER BY effective_from DESC, id DESC
    LIMIT 1
) mp ON true
WHERE ps.price_ratio IS NULL
  AND rr.id = ps.request_record_id
  AND mp.uncached_input_price > 0;

-- 2) 结算补偿任务：与其请求的售价快照倍率保持一致（这些历史任务多为终态、不再重放，仅保持列一致）。
UPDATE settlement_recovery_jobs j
SET price_ratio = ps.price_ratio
FROM price_snapshots ps
WHERE ps.request_record_id = j.request_record_id
  AND j.price_ratio IS NULL
  AND ps.price_ratio IS NOT NULL;

COMMIT;
