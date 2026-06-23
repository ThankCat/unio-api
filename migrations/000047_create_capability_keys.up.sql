-- 能力 key 字典表（DEC-024 / DESIGN-capability-manual-declaration §4）：合法能力 key 的唯一真源，
-- 取代代码内 keys.go 常量注册表。新增能力 = 往本表插一行（带中文描述供运维区分），无需改代码。
CREATE TABLE capability_keys (
    -- key: 稳定能力标识，命名形如 <domain>.<feature>[.<sub>]，公开契约。
    key TEXT PRIMARY KEY,

    -- domain: 分组（text/image/audio/file/tools/reasoning/response_format/cache/stream/server_state/responses 等），仅供 Admin 分组展示。
    domain TEXT NOT NULL DEFAULT '',

    -- display_name: 简短可读名（可中文）。
    display_name TEXT NOT NULL DEFAULT '',

    -- description: 中文描述，写明能力含义与所属协议/厂商语境，供运维区分。
    description TEXT NOT NULL DEFAULT '',

    -- sort_order: Admin 展示排序（同 domain 内）。
    sort_order INTEGER NOT NULL DEFAULT 0,

    -- deprecated: 软退役标记；退役 key 仍保留以兼容历史声明，但默认不再供新建选择。
    deprecated BOOLEAN NOT NULL DEFAULT FALSE,

    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- seed：冻结 v1 的 33 个 key（与 docs/protocol/CAPABILITY_KEYS.md 对齐）+ 中文描述。
INSERT INTO capability_keys (key, domain, display_name, description, sort_order) VALUES
    ('text.input',                     'text',          '文本输入',        '接受文本输入（所有对话模型基线能力）。', 10),
    ('text.output',                    'text',          '文本输出',        '返回文本输出（所有对话模型基线能力）。', 20),
    ('image.input',                    'image',         '图片输入',        '接受图片作为输入（多模态视觉理解）。', 30),
    ('image.output',                   'image',         '图片输出',        '生成图片输出模态。', 40),
    ('audio.input',                    'audio',         '音频输入',        '接受音频作为输入。', 50),
    ('audio.output',                   'audio',         '音频输出',        '生成音频输出模态。', 60),
    ('file.input',                     'file',          '文件输入',        '接受文件/文档作为输入（如 PDF）。', 70),
    ('tools.function',                 'tools',         '函数工具',        '支持客户自定义 function calling 工具。', 80),
    ('tools.custom',                   'tools',         '自定义工具',      'OpenAI custom 工具类型（非标准 function）。', 90),
    ('tools.parallel',                 'tools',         '并行工具调用',    '单轮响应内并行调用多个工具。', 100),
    ('tools.choice_required',          'tools',         '强制工具调用',    '强制至少调用一个工具（OpenAI required / Anthropic any|tool）。', 110),
    ('tools.builtin.web_search',       'tools.builtin', '内置联网搜索',    '服务端内置 web_search 工具（OpenAI Responses 等）；DeepSeek 无对应内置工具。', 120),
    ('tools.builtin.file_search',      'tools.builtin', '内置文件检索',    '服务端内置 file_search 工具。', 130),
    ('tools.builtin.code_interpreter', 'tools.builtin', '内置代码解释器',  '服务端内置 code_interpreter 工具。', 140),
    ('tools.builtin.computer_use',     'tools.builtin', '内置电脑操作',    '服务端内置 computer_use 工具。', 150),
    ('tools.builtin.image_generation', 'tools.builtin', '内置图片生成',    '服务端内置 image_generation 工具。', 160),
    ('tools.builtin.mcp',              'tools.builtin', '内置 MCP 工具',   '服务端内置 MCP（Model Context Protocol）工具调用。', 170),
    ('reasoning.effort',               'reasoning',     '推理强度档位',    'OpenAI reasoning_effort / Responses reasoning.effort 档位（low/medium/high）。Anthropic 用 thinking budget 另计。', 180),
    ('reasoning.budget',               'reasoning',     '思考预算',        'Anthropic thinking budget（与 OpenAI effort 档位区分）。', 190),
    ('reasoning.summary',              'reasoning',     '推理摘要',        '返回推理过程摘要（Responses reasoning.summary）。', 200),
    ('response_format.json_object',    'response_format', 'JSON 对象输出',  '结构化输出为 JSON 对象（response_format json_object）。', 210),
    ('response_format.json_schema',    'response_format', 'JSON Schema 输出', '按 JSON Schema 约束结构化输出（response_format json_schema）。', 220),
    ('prompt_cache',                   'cache',         '提示缓存',        '支持 prompt 缓存命中（cache_read_input_tokens > 0）。', 230),
    ('logprobs',                       'output',        'Token 概率',      '返回 token logprobs。', 240),
    ('service_tier',                   'output',        '服务层级',        '请求指定 service tier（如 OpenAI flex/priority）。', 250),
    ('stream',                         'stream',        '流式响应',        '支持 SSE 流式响应（分发方式，非模型能力本身）。', 260),
    ('stream.tools',                   'stream',        '流式工具调用',    '流式响应中回传工具调用增量。', 270),
    ('stream.usage',                   'stream',        '流式用量回传',    '流式响应中回传 usage（如 OpenAI stream_options.include_usage）。', 280),
    ('server_state.store',             'server_state',  '服务端存储',      '服务端保存会话状态（Responses store=true）。', 290),
    ('server_state.background',        'server_state',  '后台执行',        '服务端后台异步执行（Responses background=true）。', 300),
    ('responses.encrypted_content',    'responses',     '推理项加密透传',  'Responses 推理项 encrypted_content 跨轮携带。', 310),
    ('responses.compact.native',       'responses',     '原生压缩',        '上游原生 /responses/compact 原文透传（OpenAI 加密 compaction）。', 320),
    ('responses.compact.synthetic',    'responses',     '降级压缩',        '网关以 chat 摘要降级实现 compact（无状态摘要，非加密等价）。', 330);

-- model_capabilities.capability_key 外键约束到字典：杜绝声明字典外的 key；
-- ON DELETE RESTRICT 保证字典 key 仍被模型引用时不能删除（软退役用 deprecated）。
ALTER TABLE model_capabilities
    ADD CONSTRAINT model_capabilities_capability_key_fkey
        FOREIGN KEY (capability_key) REFERENCES capability_keys (key)
            ON UPDATE CASCADE ON DELETE RESTRICT;
