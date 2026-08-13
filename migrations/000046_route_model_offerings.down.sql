-- 回滚只移除 Offering 关系表。
-- up 中「disabled Model 下 enabled Binding 置 disabled」是单向不变量修复，不在回滚中恢复。
DROP TABLE IF EXISTS public.route_model_offerings;
