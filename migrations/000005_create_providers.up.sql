-- Provider 是业务服务商，例如 OpenAI、Anthropic，不等于 Go adapter 接口。
CREATE TABLE providers (
    -- id: 主键。--
    id BIGSERIAL PRIMARY KEY,

    -- slug: provider 稳定业务标识。--
    slug TEXT NOT NULL UNIQUE,

    -- name: provider 展示名称。--
    name TEXT NOT NULL,

    -- status: provider 启停状态。--
    status TEXT NOT NULL CHECK (status IN ('enabled', 'disabled')),

    -- created_at: 记录创建时间。--
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),

    -- updated_at: 记录更新时间。--
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
