# OpenAI Chat Completions · 请求/响应参数说明

依据同目录 [official.md](official.md) 整理(来源:OpenAI 官方,查阅 2026-06-07)。
本文件做忠实整理与通俗说明,**不写 Unio 的适配决定**(那是 [docs/providers/](../../../providers/README.md) 的事)。
完整、最新字段以 `official.md` 为准。

术语:
- **token**:模型处理文本的最小计量单位(约 1 个英文词或 1 个汉字)。
- **SSE**(Server-Sent Events):服务器持续推送数据的流式响应格式。
- **tool / function call**:工具调用,模型请求调用方执行某个函数并回传结果。

## 端点全集与 Unio 立场

OpenAI 的 Chat Completions 不止「创建」一个操作。其余操作依赖 **stored completions**
(`store=true` 时把对话存到 OpenAI 服务端,事后可按 id 拉取/管理)。Unio 是无状态的,不做服务端存储,故只实现创建。

| 操作 | 方法/路径 | 官方用途 | Unio 立场 |
| --- | --- | --- | --- |
| Create(创建) | `POST /v1/chat/completions` | 发起一次对话补全 | ✅ 已实现 |
| Get(获取) | `GET /v1/chat/completions/{id}` | 取回已存储的补全 | 不支持(无状态) |
| Get messages | `GET /v1/chat/completions/{id}/messages` | 取回已存储补全的消息 | 不支持(无状态) |
| List(列出) | `GET /v1/chat/completions` | 列出已存储补全 | 不支持(无状态) |
| Update(更新) | `POST /v1/chat/completions/{id}` | 改已存储补全的 metadata | 不支持(无状态) |
| Delete(删除) | `DELETE /v1/chat/completions/{id}` | 删除已存储补全 | 不支持(无状态) |

下面只整理 **Create** 的参数。

## 请求参数(Create)

必填:

| 参数 | 类型 | 含义 | 备注 |
| --- | --- | --- | --- |
| `model` | string | 使用的模型 ID | |
| `messages` | array | 对话消息列表 | 每条含 `role` 与 `content`;见下「消息结构」 |

常用可选:

| 参数 | 类型 | 含义 | 备注 |
| --- | --- | --- | --- |
| `max_completion_tokens` | integer | 本次最多生成的 token 数(含 reasoning) | 取代旧的 `max_tokens` |
| `max_tokens` | integer | 旧版最大生成 token 数 | 已废弃,建议用 `max_completion_tokens` |
| `temperature` | number | 采样温度,越高越发散 | 一般 0~2 |
| `top_p` | number | 核采样(nucleus sampling)概率阈值 | 与 temperature 二选一调 |
| `n` | integer | 生成几条候选回复 | 默认 1 |
| `stream` | boolean | 是否流式返回(SSE) | |
| `stream_options` | object | 流式选项,如 `include_usage` 在流尾返回 usage | 仅 `stream=true` 时有效 |
| `stop` | string/array | 命中即停止生成的字符串 | 最多 4 条 |
| `presence_penalty` | number | 重复主题惩罚 | -2~2 |
| `frequency_penalty` | number | 重复词惩罚 | -2~2 |
| `logit_bias` | map | 调整指定 token 出现概率 | |
| `logprobs` | boolean | 是否返回每个 token 的对数概率 | |
| `top_logprobs` | integer | 每个位置返回前 N 个候选概率 | 需 `logprobs=true` |
| `tools` | array | 可调用的工具(function)定义 | |
| `tool_choice` | string/object | 工具选择策略 | `none`/`auto`/`required`/指定函数 |
| `parallel_tool_calls` | boolean | 是否允许一次返回多个工具调用 | |
| `response_format` | object | 输出格式 | `text` / `json_object` / `json_schema` |
| `reasoning_effort` | string | reasoning 模型的思考强度 | 仅 reasoning 模型;如 `minimal`/`low`/`medium`/`high` |
| `seed` | integer | 采样随机种子,尽量复现 | 尽力而非保证 |
| `service_tier` | string | 服务层级 | 如 `auto`/`default` |
| `store` | boolean | 是否在 OpenAI 服务端存储本次对话 | 启用 stored completions |
| `metadata` | map | 附加键值,随存储一起保留 | |
| `user` / `safety_identifier` | string | 终端用户标识,用于风控 | |
| `modalities` | array | 输出模态,如 `["text"]`、`["text","audio"]` | |
| `audio` | object | 音频输出配置 | 配合 `modalities` |
| `prediction` | object | 预测输出(predicted outputs),加速已知内容 | |
| `web_search_options` | object | 内置 web 搜索配置 | |
| `prompt_cache_key` / `prompt_cache_retention` | string | prompt 缓存控制 | |

### 消息结构(`messages[]`)

| 字段 | 说明 |
| --- | --- |
| `role` | `system` / `developer` / `user` / `assistant` / `tool` |
| `content` | 文本字符串,或多模态 part 数组(text / image_url / input_audio / file) |
| `name` | 可选,消息作者名 |
| `tool_calls` | assistant 发起的工具调用列表 |
| `tool_call_id` | `tool` 角色回传时对应的工具调用 id |
| `reasoning_content` | reasoning 模型的思考过程(部分厂商扩展) |

## 响应结构(非流式)

| 字段 | 类型 | 含义 |
| --- | --- | --- |
| `id` | string | 补全 id |
| `object` | string | 固定 `chat.completion` |
| `created` | integer | 创建时间戳(秒) |
| `model` | string | 实际使用的模型 |
| `choices` | array | 候选回复列表 |
| `choices[].index` | integer | 序号 |
| `choices[].message` | object | 回复消息(`role`/`content`/`tool_calls`/`reasoning_content`) |
| `choices[].finish_reason` | string | 结束原因:`stop`/`length`/`tool_calls`/`content_filter` |
| `choices[].logprobs` | object | token 概率(若请求) |
| `usage` | object | token 用量(见下) |
| `system_fingerprint` | string | 后端配置指纹 |
| `service_tier` | string | 实际服务层级 |

### usage 用量

| 字段 | 含义 |
| --- | --- |
| `prompt_tokens` | 输入 token 数 |
| `completion_tokens` | 输出 token 数(含 reasoning) |
| `total_tokens` | 总和 |
| `prompt_tokens_details.cached_tokens` | 命中缓存的输入 token 数 |
| `completion_tokens_details.reasoning_tokens` | 输出中 reasoning 的 token 数 |

## 流式响应(`stream=true`)

- 以 SSE 逐块推送 `chat.completion.chunk` 对象,`choices[].delta` 承载增量内容。
- 结束以 `data: [DONE]` 收尾。
- 若 `stream_options.include_usage=true`,在最后一个含 usage 的块返回总用量。
- 完整事件/字段以 [official.md](official.md) 为准。
