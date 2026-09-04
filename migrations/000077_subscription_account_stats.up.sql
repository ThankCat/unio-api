-- 订阅账号生命周期累计（账号列表「用量」列）。
--
-- 24H 聚合是有界扫描（final_account_id + created_at 复合索引下最近一天）；生命周期累计若做
-- live 全表聚合，会随账号寿命线性变慢（request_records 无留存清理、只增不减）。因此改为在
-- 结算路径按首次结算增量累加到本表，列表 O(1) 读取。
--
-- 口径与账号 24H 聚合一致：只累计已归属账号（request_records.final_account_id 非空）的
-- 结算终态（成功 / 已结算失败 / 已结算取消）；纯失败与纯取消不写账号归属，也不进本表。
-- 幂等保障来自结算的 running 状态闸门：重放在增量语句执行前已短路（与 AddAPIKeySpentTotal 同源）。

CREATE TABLE public.subscription_account_stats (
    -- account_id: 订阅账号；账号物理删除时累计级联删除（删除本身已被请求历史外键闸门保护）。--
    account_id bigint PRIMARY KEY
        REFERENCES public.subscription_accounts (id) ON DELETE CASCADE,
    -- lifetime_requests: 已归属账号的终态请求总数（成功 + 已结算失败/取消）。--
    lifetime_requests bigint NOT NULL DEFAULT 0,
    -- lifetime_input_tokens: 累计输入 token（uncached + cache read + 三档 cache write，与账单口径一致）。--
    lifetime_input_tokens bigint NOT NULL DEFAULT 0,
    -- lifetime_output_tokens: 累计输出 token（含 reasoning 的 authoritative 总量）。--
    lifetime_output_tokens bigint NOT NULL DEFAULT 0,
    -- lifetime_sale_amount: 累计售卖额（客户实扣净额；平台当前单币种 USD，金额不经 float）。--
    lifetime_sale_amount numeric(20, 8) NOT NULL DEFAULT 0,
    updated_at timestamptz NOT NULL DEFAULT now()
);

-- 存量回填：从请求历史一次性聚合（仅迁移期执行一次，运行时绝不做此全表聚合）。
-- final_account_id 只由结算终态路径写入，凡非空即结算终态，与增量口径完全一致。
WITH usage_agg AS (
    SELECT
        r.final_account_id AS account_id,
        count(*)::bigint AS reqs,
        COALESCE(sum(
            ur.uncached_input_tokens + ur.cache_read_input_tokens
            + ur.cache_creation_5m_input_tokens + ur.cache_creation_1h_input_tokens
            + ur.cache_creation_30m_input_tokens
        ), 0)::bigint AS input_tokens,
        COALESCE(sum(ur.output_tokens_total), 0)::bigint AS output_tokens
    FROM request_records r
    LEFT JOIN usage_records ur ON ur.request_record_id = r.id
    WHERE r.final_account_id IS NOT NULL
    GROUP BY r.final_account_id
), sale_agg AS (
    SELECT
        r.final_account_id AS account_id,
        COALESCE(sum(CASE
            WHEN le.entry_type IN ('debit', 'adjustment_debit') THEN le.amount
            WHEN le.entry_type IN ('refund', 'adjustment_credit') THEN -le.amount
            ELSE 0
        END), 0) AS sale_amount
    FROM ledger_entries le
    JOIN request_records r ON r.id = le.request_record_id
    WHERE r.final_account_id IS NOT NULL
    GROUP BY r.final_account_id
)
INSERT INTO public.subscription_account_stats (
    account_id, lifetime_requests, lifetime_input_tokens, lifetime_output_tokens, lifetime_sale_amount, updated_at
)
SELECT u.account_id, u.reqs, u.input_tokens, u.output_tokens, COALESCE(s.sale_amount, 0), now()
FROM usage_agg u
LEFT JOIN sale_agg s ON s.account_id = u.account_id;
