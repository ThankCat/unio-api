-- 能力 key 字典表（DEC-024 / DESIGN-capability-manual-declaration §4）：合法能力 key 的唯一真源，
-- 取代代码内 keys.go 常量注册表。新增能力 = 往本表插一行（带中文描述供运维区分），无需改代码。
CREATE TABLE public.capability_keys (
    -- key: 稳定能力标识，命名形如 <domain>.<feature>[.<sub>]，公开契约。
    key text NOT NULL,
    -- domain: 分组（text/image/audio/file/tools/reasoning/response_format/cache/stream/server_state/responses 等），仅供 Admin 分组展示。
    domain text DEFAULT ''::text NOT NULL,
    -- display_name: 简短可读名（可中文）。
    display_name text DEFAULT ''::text NOT NULL,
    -- description: 中文描述，写明能力含义与所属协议/厂商语境，供运维区分。
    description text DEFAULT ''::text NOT NULL,
    -- sort_order: Admin 展示排序（同 domain 内）。
    sort_order integer DEFAULT 0 NOT NULL,
    -- deprecated: 软退役标记；退役 key 仍保留以兼容历史声明，但默认不再供新建选择。
    deprecated boolean DEFAULT false NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    protocol_scope text DEFAULT 'shared'::text NOT NULL,
    CONSTRAINT capability_keys_protocol_scope_check CHECK ((protocol_scope = ANY (ARRAY['shared'::text, 'openai'::text, 'anthropic'::text])))
);

ALTER TABLE ONLY public.capability_keys
    ADD CONSTRAINT capability_keys_pkey PRIMARY KEY (key);

COMMENT ON COLUMN public.capability_keys.protocol_scope IS '协议归属：shared=OpenAI+Anthropic 通用；openai=OpenAI Chat/Responses 专有；anthropic=Anthropic Messages 专有。';

-- ---------------------------------------------------------------------------
-- 后续迁移补充的设计说明（列/约束演进，原 ALTER 迁移的中文注释归档）：
-- ---------------------------------------------------------------------------
-- [000048_add_capability_keys_protocol_scope]
-- 能力 key 字典增加协议归属（DEC-024 运维区分 OpenAI / Anthropic / 通用）。
-- [000049_rename_protocol_scope_both_to_shared]
-- protocol_scope：both → shared（语义「双协议通用」，Admin 展示为「通用」）。
