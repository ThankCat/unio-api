-- 移除内置线路（经济/稳定）及 is_builtin 标识；线路须显式绑定到 API Key 或项目默认。
UPDATE projects
SET default_route_id = NULL
WHERE default_route_id IN (SELECT id FROM routes WHERE is_builtin);

UPDATE api_keys
SET route_id = NULL
WHERE route_id IN (SELECT id FROM routes WHERE is_builtin);

DELETE FROM routes WHERE is_builtin;

ALTER TABLE routes DROP CONSTRAINT ck_routes_builtin_pool;
ALTER TABLE routes DROP COLUMN is_builtin;
