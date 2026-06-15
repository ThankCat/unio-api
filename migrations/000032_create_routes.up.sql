-- Route 是面向客户的「线路 / 渠道商品」（阶段 15）。
-- 线路只决定「候选池 + 排序策略」，叠加在既有能力闸门 / 熔断 / 协议过滤之上，不改变它们。
CREATE TABLE routes (
    -- id: 主键。--
    id BIGSERIAL PRIMARY KEY,

    -- name: 对外商品名（经济 / 稳定 / C-专线 ...），全局唯一。--
    name TEXT NOT NULL UNIQUE CHECK (name <> ''),

    -- mode: 选路策略。cheapest=按售价升序；stable=按渠道健康；fixed=锁定单条渠道。--
    mode TEXT NOT NULL CHECK (mode IN ('cheapest', 'stable', 'fixed')),

    -- pool_kind: 候选池类型。all=该模型全量可路由渠道（动态）；explicit=运营手挑渠道。--
    pool_kind TEXT NOT NULL CHECK (pool_kind IN ('all', 'explicit')),

    -- is_builtin: 是否内置线路（经济 / 稳定），内置线路不可删、池固定为 all。--
    is_builtin BOOLEAN NOT NULL DEFAULT false,

    -- status: 线路启停状态。--
    status TEXT NOT NULL CHECK (status IN ('enabled', 'disabled')),

    -- description: 线路简介（展示给客户的商品说明）。--
    description TEXT,

    -- created_at: 记录创建时间。--
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),

    -- updated_at: 记录更新时间。--
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),

    -- 内置线路池固定为 all（动态全量），不允许 explicit/fixed。--
    CONSTRAINT ck_routes_builtin_pool
        CHECK (NOT is_builtin OR pool_kind = 'all'),

    -- fixed 模式必须是 explicit 池（恰好锁定一条渠道，由 service 层强校验数量）。--
    CONSTRAINT ck_routes_fixed_pool
        CHECK (mode <> 'fixed' OR pool_kind = 'explicit')
);

-- 种子：内置「经济」(cheapest, all) 与「稳定」(stable, all)，系统自动判定、零配置。
INSERT INTO routes (name, mode, pool_kind, is_builtin, status, description)
VALUES
    ('经济', 'cheapest', 'all', true, 'enabled', '系统自动在该模型所有可用渠道中选择售价最低的一条，成本优先。'),
    ('稳定', 'stable', 'all', true, 'enabled', '系统自动在该模型所有可用渠道中优先选择健康、低延迟的一条，稳定优先。');
