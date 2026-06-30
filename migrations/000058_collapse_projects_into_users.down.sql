-- 反向迁移：重建 projects / project_model_policies 与相关列。
-- best-effort：数据无需精确还原，每个用户重建一个名为 'default' 的项目承接归属。

-- 1. 重建 projects 表（含 default_route_id 列）。
CREATE TABLE projects (
    id BIGSERIAL PRIMARY KEY,
    user_id BIGINT NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    name TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    default_route_id BIGINT REFERENCES routes (id),
    UNIQUE (user_id, name)
);
CREATE INDEX idx_projects_user_id ON projects (user_id);

-- 每个用户重建一个默认项目（default_route_id 留空：up 已彻底移除用户默认线路）。
INSERT INTO projects (user_id, name)
SELECT id, 'default'
FROM users;

-- 2. 重建 project_model_policies 表。
CREATE TABLE project_model_policies (
    project_id BIGINT NOT NULL REFERENCES projects (id) ON DELETE CASCADE,
    model_id BIGINT NOT NULL REFERENCES models (id) ON DELETE CASCADE,
    visibility TEXT NOT NULL CHECK (visibility IN ('allowed', 'denied')),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (project_id, model_id)
);
CREATE INDEX idx_project_model_policies_project_visibility
    ON project_model_policies (project_id, visibility);
CREATE INDEX idx_project_model_policies_model_id
    ON project_model_policies (model_id);

-- 把用户策略回迁到其默认项目。
INSERT INTO project_model_policies (project_id, model_id, visibility, created_at, updated_at)
SELECT p.id, ump.model_id, ump.visibility, ump.created_at, ump.updated_at
FROM user_model_policies ump
JOIN projects p ON p.user_id = ump.user_id;

-- 3. api_keys.user_id → api_keys.project_id（并解除 route_id 必填）。
-- up 把 route_id 置为 NOT NULL，这里先解除，回到「可空＝回落默认线路」的旧语义。
ALTER TABLE api_keys ALTER COLUMN route_id DROP NOT NULL;
ALTER TABLE api_keys ADD COLUMN project_id BIGINT;

UPDATE api_keys k
SET project_id = p.id
FROM projects p
WHERE p.user_id = k.user_id;

ALTER TABLE api_keys ALTER COLUMN project_id SET NOT NULL;
ALTER TABLE api_keys
    ADD CONSTRAINT api_keys_project_id_fkey
        FOREIGN KEY (project_id) REFERENCES projects (id) ON DELETE CASCADE;
CREATE INDEX idx_api_keys_project_id ON api_keys (project_id);

-- DROP COLUMN 自动连带删除 user_id 的外键与 idx_api_keys_user_id 索引。
ALTER TABLE api_keys DROP COLUMN user_id;

-- 4. 重建 request_records.project_id。
ALTER TABLE request_records ADD COLUMN project_id BIGINT;

UPDATE request_records r
SET project_id = p.id
FROM projects p
WHERE p.user_id = r.user_id;

ALTER TABLE request_records ALTER COLUMN project_id SET NOT NULL;
ALTER TABLE request_records
    ADD CONSTRAINT request_records_project_id_fkey
        FOREIGN KEY (project_id) REFERENCES projects (id);
CREATE INDEX idx_request_records_project_created_at
    ON request_records (project_id, created_at DESC);

-- 5. 删除 user_model_policies（up 未新增 users.default_route_id，故无需回滚该列）。
DROP TABLE user_model_policies;
