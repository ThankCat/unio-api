-- 折叠 user → project → api_key 三级为 user → api_key 两级，彻底移除 projects 概念。
-- API Key、模型策略与请求归属全部直接挂在用户上。
-- 同时把线路改为 API Key 必填：彻底移除「用户/项目默认线路」回落，线路只认 api_keys.route_id。
-- 数据无需保留，但仍写正确回填，保证存量库平滑迁移。

-- 1. api_keys.project_id → api_keys.user_id（API Key 直接归属用户）。
ALTER TABLE api_keys ADD COLUMN user_id BIGINT;

-- 回填：经由旧 projects 把 API Key 归属解析到用户。
UPDATE api_keys k
SET user_id = p.user_id
FROM projects p
WHERE p.id = k.project_id;

ALTER TABLE api_keys ALTER COLUMN user_id SET NOT NULL;
ALTER TABLE api_keys
    ADD CONSTRAINT api_keys_user_id_fkey
        FOREIGN KEY (user_id) REFERENCES users (id) ON DELETE CASCADE;

-- 认证和管理接口常按 user_id 查询 API Key。
CREATE INDEX idx_api_keys_user_id ON api_keys (user_id);

-- DROP COLUMN 自动连带删除 project_id 的外键与 idx_api_keys_project_id 索引。
ALTER TABLE api_keys DROP COLUMN project_id;

-- 2. api_keys.route_id 改为必填：线路必须显式绑定到 Key（移除默认线路回落）。
-- 兜底清理历史无线路的 Key（重置流程下无此类行）；随后置 NOT NULL，DB 层兜底「线路必填」。
DELETE FROM api_keys WHERE route_id IS NULL;
ALTER TABLE api_keys ALTER COLUMN route_id SET NOT NULL;

-- 3. project_model_policies → user_model_policies（模型可见性策略改挂用户）。
CREATE TABLE user_model_policies (
    -- user_id: 策略所属用户 ID。--
    user_id BIGINT NOT NULL REFERENCES users (id) ON DELETE CASCADE,

    -- model_id: 策略作用的模型 ID。--
    model_id BIGINT NOT NULL REFERENCES models (id) ON DELETE CASCADE,

    -- visibility: 模型对该用户的可见性。--
    visibility TEXT NOT NULL CHECK (visibility IN ('allowed', 'denied')),

    -- created_at: 记录创建时间。--
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),

    -- updated_at: 记录更新时间。--
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),

    -- 同一用户对同一模型只能有一条可见性策略。--
    PRIMARY KEY (user_id, model_id)
);

-- 查询用户可见模型时会按 user 和 visibility 过滤。
CREATE INDEX idx_user_model_policies_user_visibility
    ON user_model_policies (user_id, visibility);

-- 模型下线或排查策略时需要按 model_id 反查引用。
CREATE INDEX idx_user_model_policies_model_id
    ON user_model_policies (model_id);

-- 回填：把旧 project 策略经由 projects 折叠到用户维度；同一用户同一模型多条时取最早一条。
INSERT INTO user_model_policies (user_id, model_id, visibility, created_at, updated_at)
SELECT DISTINCT ON (p.user_id, pmp.model_id)
    p.user_id, pmp.model_id, pmp.visibility, pmp.created_at, pmp.updated_at
FROM project_model_policies pmp
JOIN projects p ON p.id = pmp.project_id
ORDER BY p.user_id, pmp.model_id, pmp.created_at
ON CONFLICT (user_id, model_id) DO NOTHING;

-- 4. 移除 request_records.project_id（请求只按 user/api_key 归属）。
-- 账本组合外键用 (request_record_id, user_id)，不涉及 project_id，删列安全。
-- DROP COLUMN 自动连带删除 project_id 的外键与 idx_request_records_project_created_at 索引。
ALTER TABLE request_records DROP COLUMN project_id;

-- 5. 删除旧表（此时已无外键引用 projects）。
DROP TABLE project_model_policies;
DROP TABLE projects;
