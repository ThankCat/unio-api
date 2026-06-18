# 能力 Key 注册表(Capability Keys v1)

本文件是 Unio 能力架构([DEC-015](../production/DECISIONS.md))的**公开稳定契约**:
列出全部合法的 `capability_key`。它描述「一个模型/渠道可以声明哪些能力」,与具体协议端点
解耦,被三层能力数据(模型层 `model_capabilities`、渠道层 `channel_capability_overrides`)
共同引用。

代码侧权威实现:[internal/core/capability/keys.go](../../internal/core/capability/keys.go)。
两者必须保持一致(有一致性测试守护)。

## 1. 契约规则

- **只增不删**:已发布的 key 永不改名、永不删除;新增能力只能追加新 key。
- **语义化版本**:本表为 `v1`。破坏性调整(改名/删除/改语义)需升 `v2` 并保留 `v1` 兼容期。
- key 命名形如 `<domain>.<feature>[.<sub>]`,全小写、`.` 分层、`_` 连接复合词。
- 合法性边界在应用层 `capability.IsRegisteredKey` 校验;**数据库不做枚举约束**(以支持只增不删)。
- 未列入本表的 key 不被接受;写入会被拒绝(`capability_invalid_key`)。

## 2. 支持级别(support_level)

| 取值 | 含义 | 模型层 | 渠道收紧层 |
| --- | --- | --- | --- |
| `full` | 完整支持 | ✅ | ❌(渠道只能做减法) |
| `limited` | 部分支持,受 `limits` 进一步约束(如仅允许某些 effort 值) | ✅ | ✅ |
| `unsupported` | 不支持 | ✅ | ✅ |

- **模型层**(`model_capabilities`)声明模型本身的能力,三种级别都可用。
- **渠道层**(`channel_capability_overrides`)只能在模型基线上**收紧**,只允许 `limited` / `unsupported`,
  不允许 `full`(不能反向放开模型未声明的能力)。

## 3. 能力声明来源(source)

| 取值 | 含义 | 适用 |
| --- | --- | --- |
| `models_dev` | 来自 models.dev 同步 | model_capabilities / sync_jobs |
| `manual` | 运营手工维护(同步永不覆盖) | model_capabilities / sync_jobs |
| `adapter_seed` | 由 adapter 能力种子回填 | model_capabilities |

> 同步任务(`model_capability_sync_jobs.source`)仅允许 `models_dev` / `manual`。

## 4. v1 Key 列表

### 4.1 文本 / 多模态 I/O

| key | 含义 |
| --- | --- |
| `text.input` | 接受文本输入 |
| `text.output` | 产出文本输出 |
| `image.input` | 接受图像输入(多模态理解) |
| `image.output` | 产出图像输出 |
| `audio.input` | 接受音频输入 |
| `audio.output` | 产出音频输出 |
| `file.input` | 接受文件输入 |

### 4.2 工具调用(Function / Tool Calling)

| key | 含义 |
| --- | --- |
| `tools.function` | 支持函数工具(function tools) |
| `tools.custom` | 支持自定义工具(custom tools) |
| `tools.parallel` | 支持单轮并行多工具调用 |
| `tools.choice_required` | 支持 `tool_choice=required`(强制至少调用一个工具) |

### 4.3 内置工具(Server-side Built-in Tools)

| key | 含义 |
| --- | --- |
| `tools.builtin.web_search` | 内置联网搜索工具 |
| `tools.builtin.file_search` | 内置文件检索工具 |
| `tools.builtin.code_interpreter` | 内置代码执行工具 |
| `tools.builtin.computer_use` | 内置 computer-use 工具 |
| `tools.builtin.image_generation` | 内置图像生成工具 |
| `tools.builtin.mcp` | 内置 MCP(Model Context Protocol)工具接入 |

### 4.4 推理(Reasoning)

| key | 含义 |
| --- | --- |
| `reasoning.effort` | 支持 `reasoning_effort` / effort 档位 |
| `reasoning.budget` | 支持思考预算 token(thinking budget) |
| `reasoning.summary` | 支持返回推理摘要(reasoning summary) |

### 4.5 响应格式(Structured Output)

| key | 含义 |
| --- | --- |
| `response_format.json_object` | 支持 `response_format=json_object` |
| `response_format.json_schema` | 支持 `response_format=json_schema`(结构化输出) |

### 4.6 其它请求能力

| key | 含义 |
| --- | --- |
| `prompt_cache` | 支持 prompt 缓存(命中计费区分) |
| `logprobs` | 支持返回 token logprobs |
| `service_tier` | 支持 `service_tier`(服务等级,如 flex/priority) |

### 4.7 流式(Streaming)

| key | 含义 |
| --- | --- |
| `stream` | 支持 SSE 流式响应 |
| `stream.tools` | 流式下支持工具调用增量 |
| `stream.usage` | 流式下支持回传 usage(如 `stream_options.include_usage`) |

### 4.8 服务端状态(Server State)

| key | 含义 |
| --- | --- |
| `server_state.store` | 支持服务端存储会话(`store=true`) |
| `server_state.background` | 支持后台(background)异步执行 |

### 4.9 Responses 专有

| key | 含义 |
| --- | --- |
| `responses.encrypted_content` | 支持 Responses API 推理项的 `encrypted_content` 跨轮携带 |
| `responses.compact.native` | 上游原生支持 `POST /v1/responses/compact`（原文透传压缩，OpenAI 加密 compaction 语义，GAP-11-014） |
| `responses.compact.synthetic` | 网关以 chat 摘要降级实现 compact（无状态摘要，非加密等价，GAP-11-007 / GAP-11-014） |

> compact 双路径的**运行期分流以 adapter 代码能力为准**（`HasResponsesCompact`，与 DEC-018 直传分流一致）：
> 上述两个 key 用于能力契约/矩阵声明与可观测，不作为强制路由前置条件。

## 5. 已知上游标注(参考,非种子数据)

实际能力种子数据归阶段 12 后续任务(同步/回填),此处仅记录已坐实的结论:

- **DeepSeek**:
  - `tools.builtin.web_search` = `unsupported`(DeepSeek 无可声明的内置搜索工具,需客户用 function calling 自接外部搜索)。
  - `reasoning.effort` = `limited`,`limits` 仅 `high` / `max`(其余档位归一到 `high`)。
  - 详见 [docs/providers/deepseek/](../providers/deepseek/README.md)。

## 5.1 能力自动校正的证据来源（DEC-020 / GAP-12-013）

「能力自动校正」从真实成功流量被动学习模型实际能力并补齐 `model_capabilities`（不新增 key）。
**强证据 = 响应真用到了该能力**，才允许自动补（`auto` 档）；否则只产生建议待人工采纳：

| 能力 key | 强证据来源 |
| --- | --- |
| `tools.function` / `tools.custom` / `tools.parallel` / `tools.choice_required` | `request_attempts.finish_class = tool_use` |
| `prompt_cache` | `usage_records.cache_read_input_tokens > 0` |
| `reasoning.effort` / `reasoning.budget` | `usage_records.reasoning_output_tokens > 0`（有 limits 维度，恒只建议不自动） |
| 其余（builtin 工具 / `responses.encrypted_content` / 模态 / 结构化输出 等） | 当前审计无落点 → 弱证据，只建议 |

> 注：`finish_class=tool_use` 仅证明「某工具被调」，无法区分 function/custom；且 Responses 直传上游的
> `finish_class` 恒为 `stop`（不出 `tool_use`），故该类上游的 `tools.*` 恒弱证据。要让 `tools.*` 也能强证据
> 自动补，需给 adapter 加「按 key 命中埋点」（DESIGN-capability-autocalibration TASK-H）。

## 6. 版本记录

| 版本 | 日期 | 变更 |
| --- | --- | --- |
| v1 | 2026-06-07 | 首次发布,冻结上表 31 个 key。 |
| v1.1 | 2026-06-18 | 追加 `responses.compact.native` / `responses.compact.synthetic`（GAP-11-014 compact 双路径），共 33 个 key。 |
