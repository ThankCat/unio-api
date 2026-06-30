-- Channel 是某个 provider 下的一条具体上游渠道。
CREATE TABLE channels (
    -- id: 主键。--
    id BIGSERIAL PRIMARY KEY,

    -- provider_id: channel 所属 provider ID。--
    provider_id BIGINT NOT NULL REFERENCES providers (id),

    -- name: provider 内 channel 名称。--
    name TEXT NOT NULL,

    -- protocol: channel 对外协议族，决定 ingress 路由与 adapter 协议族；routing 只命中同协议 channel。--
    protocol TEXT NOT NULL CHECK (protocol IN ('openai', 'anthropic')),

    -- adapter_key: channel 运行时绑定的 adapter 注册键，routing 据此解析具体 adapter，不再从 provider 派生。--
    adapter_key TEXT NOT NULL,

    -- base_url: 上游 API 基础地址。--
    base_url TEXT NOT NULL,

    -- credential: 上游 API key，明文存储，便于管理端查看/复制/编辑（产品决策：渠道凭据不加密）。--
    credential TEXT NOT NULL CHECK (credential <> ''),

    -- status: channel 启停状态。--
    status TEXT NOT NULL CHECK (status IN ('enabled', 'disabled')),

    -- priority: routing 选择 channel 时的优先级，数值越小越靠前。--
    priority INTEGER NOT NULL CHECK (priority >= 0),

    -- timeout_ms: 该 channel 的上游请求超时时间，空值表示使用默认值。--
    timeout_ms INTEGER CHECK (timeout_ms IS NULL OR timeout_ms > 0),

    -- created_at: 记录创建时间。--
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),

    -- updated_at: 记录更新时间。--
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),

    -- channel ID 与 provider 组合需要支持下游账务事实复合引用。--
    CONSTRAINT uq_channels_id_provider
        UNIQUE (id, provider_id),

    -- 同一 provider 下 channel 名称不能重复。--
    UNIQUE (provider_id, name)
);

-- routing 会按 provider 查找可用 channel。
CREATE INDEX idx_channels_provider_id ON channels (provider_id);

-- routing 会按优先级稳定选择 channel。
CREATE INDEX idx_channels_priority ON channels (priority, id);

-- routing 会按 ingress protocol 过滤同协议 channel。
CREATE INDEX idx_channels_protocol ON channels (protocol);
