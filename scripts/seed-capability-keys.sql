-- 能力 key 字典种子（DEC-024 / DESIGN-capability-manual-declaration §4）。
-- capability_keys 是合法能力 key 的唯一真源，取代代码内 keys.go 常量注册表。
--
-- 这份种子从建表迁移里剥离出来，独立维护：
--   * 新增能力 = 在末尾追加一行，再执行一遍本脚本即可（ON CONFLICT DO NOTHING 保证幂等）。
--   * 只会插入尚不存在的 key，绝不改动/覆盖库里已有的行（不影响之前的数据）。
--   * 新增的行只能追加在最后，不要插在中间，保持与历史执行一致。
--
-- 执行：psql "$DATABASE_URL" -f scripts/seed-capability-keys.sql
--
-- 列含义：
--   key            稳定能力标识，命名形如 <domain>.<feature>[.<sub>]，公开契约。
--   domain         分组（text/image/audio/file/tools/reasoning/response_format/cache/stream/server_state/responses 等），仅供 Admin 分组展示。
--   display_name   简短可读名（可中文）。
--   description    中文描述，写明能力含义与所属协议/厂商语境，供运维区分。
--   sort_order     Admin 展示排序（同 domain 内）。
--   protocol_scope 协议归属：shared=OpenAI+Anthropic 通用；openai=OpenAI Chat/Responses 专有；anthropic=Anthropic Messages 专有。
-- （deprecated 默认 FALSE；created_at/updated_at 默认 now()。）

INSERT INTO capability_keys (key, domain, display_name, description, sort_order, protocol_scope) VALUES
    ('text.input',                     'text',            '文本输入',        '接受文本输入（所有对话模型基线能力）。', 10, 'shared'),
    ('text.output',                    'text',            '文本输出',        '返回文本输出（所有对话模型基线能力）。', 20, 'shared'),
    ('image.input',                    'image',           '图片输入',        '接受图片作为输入（多模态视觉理解）。', 30, 'shared'),
    ('image.output',                   'image',           '图片输出',        '生成图片输出模态。', 40, 'shared'),
    ('audio.input',                    'audio',           '音频输入',        '接受音频作为输入。', 50, 'shared'),
    ('audio.output',                   'audio',           '音频输出',        '生成音频输出模态。', 60, 'shared'),
    ('file.input',                     'file',            '文件输入',        '接受文件/文档作为输入（如 PDF）。', 70, 'shared'),
    ('tools.function',                 'tools',           '函数工具',        '支持客户自定义 function calling 工具。', 80, 'shared'),
    ('tools.custom',                   'tools',           '自定义工具',      'OpenAI custom 工具类型（非标准 function）。', 90, 'openai'),
    ('tools.parallel',                 'tools',           '并行工具调用',    '单轮响应内并行调用多个工具。', 100, 'shared'),
    ('tools.choice_required',          'tools',           '强制工具调用',    '强制至少调用一个工具（OpenAI required / Anthropic any|tool）。', 110, 'shared'),
    ('tools.builtin.web_search',       'tools.builtin',   '内置联网搜索',    '服务端内置 web_search 工具（OpenAI Responses 等）；DeepSeek 无对应内置工具。', 120, 'openai'),
    ('tools.builtin.file_search',      'tools.builtin',   '内置文件检索',    '服务端内置 file_search 工具。', 130, 'openai'),
    ('tools.builtin.code_interpreter', 'tools.builtin',   '内置代码解释器',  '服务端内置 code_interpreter 工具。', 140, 'openai'),
    ('tools.builtin.computer_use',     'tools.builtin',   '内置电脑操作',    '服务端内置 computer_use 工具。', 150, 'openai'),
    ('tools.builtin.image_generation', 'tools.builtin',   '内置图片生成',    '服务端内置 image_generation 工具。', 160, 'openai'),
    ('tools.builtin.mcp',              'tools.builtin',   '内置 MCP 工具',   '服务端内置 MCP（Model Context Protocol）工具调用。', 170, 'openai'),
    ('reasoning.effort',               'reasoning',       '推理强度档位',    'OpenAI reasoning_effort / Responses reasoning.effort 档位（low/medium/high）。Anthropic 用 thinking budget 另计。', 180, 'openai'),
    ('reasoning.budget',               'reasoning',       '思考预算',        'Anthropic thinking budget（与 OpenAI effort 档位区分）。', 190, 'anthropic'),
    ('reasoning.summary',              'reasoning',       '推理摘要',        '返回推理过程摘要（Responses reasoning.summary）。', 200, 'openai'),
    ('response_format.json_object',    'response_format', 'JSON 对象输出',   '结构化输出为 JSON 对象（response_format json_object）。', 210, 'shared'),
    ('response_format.json_schema',    'response_format', 'JSON Schema 输出', '按 JSON Schema 约束结构化输出（response_format json_schema）。', 220, 'shared'),
    ('prompt_cache',                   'cache',           '提示缓存',        '支持 prompt 缓存命中（cache_read_input_tokens > 0）。', 230, 'shared'),
    ('logprobs',                       'output',          'Token 概率',      '返回 token logprobs。', 240, 'shared'),
    ('service_tier',                   'output',          '服务层级',        '请求指定 service tier（如 OpenAI flex/priority）。', 250, 'openai'),
    ('stream',                         'stream',          '流式响应',        '支持 SSE 流式响应（分发方式，非模型能力本身）。', 260, 'shared'),
    ('stream.tools',                   'stream',          '流式工具调用',    '流式响应中回传工具调用增量。', 270, 'shared'),
    ('stream.usage',                   'stream',          '流式用量回传',    '流式响应中回传 usage（如 OpenAI stream_options.include_usage）。', 280, 'shared'),
    ('server_state.store',             'server_state',    '服务端存储',      '服务端保存会话状态（Responses store=true）。', 290, 'openai'),
    ('server_state.background',        'server_state',    '后台执行',        '服务端后台异步执行（Responses background=true）。', 300, 'openai'),
    ('responses.encrypted_content',    'responses',       '推理项加密透传',  'Responses 推理项 encrypted_content 跨轮携带。', 310, 'openai'),
    ('responses.compact.native',       'responses',       '原生压缩',        '上游原生 /responses/compact 原文透传（OpenAI 加密 compaction）。', 320, 'openai'),
    ('responses.compact.synthetic',    'responses',       '降级压缩',        '网关以 chat 摘要降级实现 compact（无状态摘要，非加密等价）。', 330, 'openai')
ON CONFLICT (key) DO NOTHING;
