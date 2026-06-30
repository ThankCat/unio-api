-- API Key 是客户调用 /v1/* 的 opaque 凭证。
CREATE TABLE api_keys (
    -- id: 主键。--
    id BIGSERIAL PRIMARY KEY,

    -- project_id: API Key 所属项目 ID。--
    project_id BIGINT NOT NULL REFERENCES projects (id) ON DELETE CASCADE,

    -- name: 用户侧 API Key 名称。--
    name TEXT NOT NULL,

    -- key_prefix: API Key 明文前缀，用于定位和展示。--
    key_prefix TEXT NOT NULL,

    -- key_hash: API Key 哈希值，认证按它定位（不参与明文展示）。--
    key_hash TEXT NOT NULL UNIQUE,

    -- key_plaintext: 完整明文 key，供用户在控制台多次复制查看（产品决策：用户 key 明文留存）。NULL=历史/不可回显。--
    key_plaintext TEXT,

    -- last_used_at: 最近一次成功认证时间。--
    last_used_at TIMESTAMPTZ,

    -- expires_at: API Key 过期时间。--
    expires_at TIMESTAMPTZ,

    -- disabled_at: API Key 被禁用时间。--
    disabled_at TIMESTAMPTZ,

    -- revoked_at: API Key 被吊销时间。--
    revoked_at TIMESTAMPTZ,

    -- created_at: 记录创建时间。--
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),

    -- updated_at: 记录更新时间。--
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- 认证和管理接口常按 project_id 查询 API Key。
CREATE INDEX idx_api_keys_project_id ON api_keys (project_id);

-- 认证时先用 key_prefix 缩小候选 key 范围。
CREATE INDEX idx_api_keys_key_prefix ON api_keys (key_prefix);
