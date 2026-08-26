-- 选路观测按「模型 + 时间窗」聚合，现有索引撑不住这条查询路径。
--
-- 表上原本只有 created_at 单列索引（000033）。按模型统计落点分布与排除原因时，
-- 规划器只能用时间范围扫出整个窗口的全部模型再逐行 filter；模型数一多，
-- 每个模型的详情页都要扫一遍全量窗口。
--
-- requested_model_id 在本表是冗余列（写入时从 request 复制），因此聚合不需要
-- JOIN request_records —— 那张表反而没有 (requested_model_id, created_at) 索引。
CREATE INDEX IF NOT EXISTS idx_routing_decision_traces_model_created
    ON public.routing_decision_traces (requested_model_id, created_at DESC);

COMMENT ON INDEX public.idx_routing_decision_traces_model_created IS
    '按模型 + 时间窗聚合选路结果与候选排除原因（Admin 选路观测）。';
