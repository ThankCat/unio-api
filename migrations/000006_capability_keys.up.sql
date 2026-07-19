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

-- capability_keys 是模型能力外键的唯一真源，空库迁移必须同时具备基线字典，不能依赖手工脚本。
INSERT INTO public.capability_keys (key, domain, display_name, description, sort_order, protocol_scope) VALUES
    ('text.input',                     'text',            '文本输入',         '接受文本输入（所有对话模型基线能力）。', 10, 'shared'),
    ('text.output',                    'text',            '文本输出',         '返回文本输出（所有对话模型基线能力）。', 20, 'shared'),
    ('image.input',                    'image',           '图片输入',         '接受图片作为输入（多模态视觉理解）。', 30, 'shared'),
    ('image.output',                   'image',           '图片输出',         '生成图片输出模态。', 40, 'shared'),
    ('audio.input',                    'audio',           '音频输入',         '接受音频作为输入。', 50, 'shared'),
    ('audio.output',                   'audio',           '音频输出',         '生成音频输出模态。', 60, 'shared'),
    ('file.input',                     'file',            '文件输入',         '接受文件/文档作为输入（如 PDF）。', 70, 'shared'),
    ('tools.function',                 'tools',           '函数工具',         '支持客户自定义 function calling 工具。', 80, 'shared'),
    ('tools.custom',                   'tools',           '自定义工具',       'OpenAI custom 工具类型（非标准 function）。', 90, 'openai'),
    ('tools.parallel',                 'tools',           '并行工具调用',     '单轮响应内并行调用多个工具。', 100, 'shared'),
    ('tools.choice_required',          'tools',           '强制工具调用',     '强制至少调用一个工具（OpenAI required / Anthropic any|tool）。', 110, 'shared'),
    ('tools.builtin.web_search',       'tools.builtin',   '内置联网搜索',     '服务端内置 web_search 工具。', 120, 'openai'),
    ('tools.builtin.file_search',      'tools.builtin',   '内置文件检索',     '服务端内置 file_search 工具。', 130, 'openai'),
    ('tools.builtin.code_interpreter', 'tools.builtin',   '内置代码解释器',   '服务端内置 code_interpreter 工具。', 140, 'openai'),
    ('tools.builtin.computer_use',     'tools.builtin',   '内置电脑操作',     '服务端内置 computer_use 工具。', 150, 'openai'),
    ('tools.builtin.image_generation', 'tools.builtin',   '内置图片生成',     '服务端内置 image_generation 工具。', 160, 'openai'),
    ('tools.builtin.mcp',              'tools.builtin',   '内置 MCP 工具',    '服务端内置 MCP 工具调用。', 170, 'openai'),
    ('reasoning.effort',               'reasoning',       '推理强度档位',     'OpenAI reasoning effort 档位。', 180, 'openai'),
    ('reasoning.budget',               'reasoning',       '思考预算',         'Anthropic thinking budget。', 190, 'anthropic'),
    ('reasoning.summary',              'reasoning',       '推理摘要',         '返回推理过程摘要。', 200, 'openai'),
    ('response_format.json_object',    'response_format', 'JSON 对象输出',    '结构化输出为 JSON 对象。', 210, 'shared'),
    ('response_format.json_schema',    'response_format', 'JSON Schema 输出', '按 JSON Schema 约束结构化输出。', 220, 'shared'),
    ('prompt_cache',                   'cache',           '提示缓存',         '支持 prompt 缓存命中。', 230, 'shared'),
    ('logprobs',                       'output',          'Token 概率',       '返回 token logprobs。', 240, 'shared'),
    ('service_tier',                   'output',          '服务层级',         '请求指定 service tier。', 250, 'openai'),
    ('stream',                         'stream',          '流式响应',         '支持 SSE 流式响应。', 260, 'shared'),
    ('stream.tools',                   'stream',          '流式工具调用',     '流式响应中回传工具调用增量。', 270, 'shared'),
    ('stream.usage',                   'stream',          '流式用量回传',     '流式响应中回传 usage。', 280, 'shared'),
    ('server_state.store',             'server_state',    '服务端存储',       '服务端保存会话状态。', 290, 'openai'),
    ('server_state.background',        'server_state',    '后台执行',         '服务端后台异步执行。', 300, 'openai'),
    ('responses.encrypted_content',    'responses',       '推理项加密透传',   'Responses encrypted_content 跨轮携带。', 310, 'openai'),
    ('responses.compact.native',       'responses',       '原生压缩',         '上游原生 responses compact。', 320, 'openai'),
    ('responses.compact.synthetic',    'responses',       '降级压缩',         '网关以 chat 摘要降级实现 compact。', 330, 'openai')
ON CONFLICT (key) DO NOTHING;

-- ---------------------------------------------------------------------------
-- 后续迁移补充的设计说明（列/约束演进，原 ALTER 迁移的中文注释归档）：
-- ---------------------------------------------------------------------------
-- [000048_add_capability_keys_protocol_scope]
-- 能力 key 字典增加协议归属（DEC-024 运维区分 OpenAI / Anthropic / 通用）。
-- [000049_rename_protocol_scope_both_to_shared]
-- protocol_scope：both → shared（语义「双协议通用」，Admin 展示为「通用」）。
