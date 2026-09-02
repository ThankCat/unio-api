-- 路由 trace 的账号维度（docs/changes/2026-09-02-account-pool 批次四，边界 14）。
--
-- 只记「实际尝试过的账号」与「最终选中的账号」两个可筛选事实；池内逐号扫描的细节
-- （每次 acquire 试了哪些号、各自被拒的原因）在 trace_payload.acquire_results 里，
-- 不为它们开列——那是解释性的过程数据，不是筛选维度。

ALTER TABLE public.routing_decision_traces
    -- attempted_account_ids: 真实发起过上游调用的订阅账号（credential 型请求恒为空数组）。--
    ADD COLUMN attempted_account_ids bigint[] DEFAULT '{}'::bigint[] NOT NULL,
    -- selected_account_id: 最终成功传输所用的账号；非池型或未成功时为 NULL。--
    ADD COLUMN selected_account_id bigint;

-- 请求记录与 trace 支持按账号筛选（§9 经营视图）。
CREATE INDEX idx_routing_traces_selected_account
    ON public.routing_decision_traces (selected_account_id)
    WHERE selected_account_id IS NOT NULL;
