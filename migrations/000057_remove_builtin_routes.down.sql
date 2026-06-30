ALTER TABLE routes ADD COLUMN is_builtin BOOLEAN NOT NULL DEFAULT false;

ALTER TABLE routes ADD CONSTRAINT ck_routes_builtin_pool
    CHECK (NOT is_builtin OR pool_kind = 'all');

INSERT INTO routes (name, mode, pool_kind, is_builtin, status, description)
VALUES
    ('经济', 'cheapest', 'all', true, 'enabled', '系统自动在该模型所有可用渠道中选择售价最低的一条，成本优先。'),
    ('稳定', 'stable', 'all', true, 'enabled', '系统自动在该模型所有可用渠道中优先选择健康、低延迟的一条，稳定优先。')
ON CONFLICT (name) DO NOTHING;
