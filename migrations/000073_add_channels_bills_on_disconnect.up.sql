-- upstream_bills_on_disconnect（DESIGN-bill-on-cancel 阶段一）：标记该渠道的上游在连接断开后
-- 仍会完成生成并计费（典型：sub2api 类订阅中转，断开不取消、drain 到底照扣）。
-- 打开后，gateway 在「请求已发出但本 attempt 不会产生真实结算成本」的失败/取消路径上，
-- 会向 channel_cost_exposures 记一条平台成本敞口（保守上界估算），供成本对账与渠道横向比较。
-- 不影响路由与客户计费，纯平台侧观测。
ALTER TABLE channels
    ADD COLUMN upstream_bills_on_disconnect BOOLEAN NOT NULL DEFAULT FALSE;
