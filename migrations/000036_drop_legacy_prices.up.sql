-- 阶段 15：退役模型级 prices 与渠道级 channel_cost_prices，计费一律走 channel_prices。
-- 此前 000035 已把所有引用这两张表的外键改挂到 channel_prices，此处可安全删除。
DROP TABLE IF EXISTS channel_cost_prices;
DROP TABLE IF EXISTS prices;
