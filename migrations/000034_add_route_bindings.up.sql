-- 阶段 15：API Key 与项目绑定线路。
-- 线路解析优先级：api_keys.route_id ?? projects.default_route_id ?? 内置「经济」。

-- api_keys.route_id：该 Key 选定的线路，NULL 表示回落项目默认 / 内置经济。
ALTER TABLE api_keys ADD COLUMN route_id BIGINT REFERENCES routes (id);

-- projects.default_route_id：项目级默认线路，NULL 表示回落内置经济。
ALTER TABLE projects ADD COLUMN default_route_id BIGINT REFERENCES routes (id);

-- 认证时按 route_id 解析线路。
CREATE INDEX idx_api_keys_route_id ON api_keys (route_id) WHERE route_id IS NOT NULL;
