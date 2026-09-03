-- 缓存画像的账号口径（docs/changes/2026-09-02-account-pool 边界 10）。
--
-- 缓存画像把「破坏了粘性的请求」排除出渠道缓存统计，现有依据只有渠道维度
-- （sticky_before_channel_id <> final_channel_id）。池型渠道的「池内换号」同样会丢上游缓存，
-- 但渠道没变——需要请求开始时 sticky 绑定的账号这条事实才能识别。
-- 与 sticky_before_channel_id 同口径：请求规划开始时读到的绑定快照，credential 型恒 NULL/0。

ALTER TABLE public.routing_decision_traces
    -- sticky_before_account_id: 规划开始时 sticky 绑定的账号（v2 绑定值的账号分量）；无绑定或
    -- credential 型渠道为 NULL。--
    ADD COLUMN sticky_before_account_id bigint;
