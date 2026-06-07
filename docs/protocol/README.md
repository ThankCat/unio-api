# 协议端点文档规范(docs/protocol)

本目录记录 **Unio 对外暴露的公开协议**的端点规范,只包含两个协议族：
OpenAI 与 Anthropic。它描述「协议本身长什么样」,与具体上游(DeepSeek 等)无关。

## 1. 定位与边界

- 本目录只放**公开协议端点**的规范:OpenAI 协议族、Anthropic 协议族。
- **第三方上游**自身的协议或兼容性文档(如某上游的 OpenAI / Anthropic 兼容说明)
  **不放这里**,归 [docs/providers/<provider>/](../providers/README.md) 维护。
- 引用关系:`docs/providers/` 在描述某上游差异时**引用**本目录的标准协议,不重抄。
- 一句话:协议本身归 `docs/protocol/`,某个上游怎么接归 `docs/providers/`。

## 2. 目录结构

按协议族分目录,每个端点(Endpoint,这里指 Unio 对外暴露的一组 API 操作,
如 chat-completions / responses / messages)一个子目录,子目录内固定两份文档：

```text
docs/protocol/
  README.md
  openai/
    chat-completions/
      official.md      # 官方文档(快照 / 摘要)
      params.md        # 依据官方整理的请求/响应参数说明
    responses/
      official.md
      params.md
  anthropic/
    messages/
      official.md
      params.md
```

## 3. 每个端点两份文档的职责

### official.md(官方文档)
- 来自官方的端点规范快照或忠实摘要,**尽量贴近官方原文,不掺入我方结论或适配决定**。
- 顶部必须标注:`来源:[官方·页面名](URL)(查阅 YYYY-MM-DD)`。
- 官方有多页(如主文档 + 流式事件 + 其它端点)时,可合并进 `official.md` 分节,
  也可拆成 `official-<子主题>.md` 多份(如 `official-streaming-events.md`);均分别标注来源链接,
  并在主 `official.md` 里指向这些配套子文件。

### params.md(参数整理与说明)
- 依据 `official.md`,把该端点的**请求参数**与**响应参数**整理成表,便于查阅。
- 每个参数至少给出:参数名、类型、是否必填、含义、取值范围/枚举、默认值、备注。
- 请求与响应分两节;流式响应(如有)单列事件序列与各事件字段。
- 这里只做「忠实整理 + 通俗说明」,**不写 Unio 的适配/转换决定**(那是 providers 的事)。
- params.md **只覆盖 Unio 已实现的操作**(见下「已实现 vs 未实现的操作」)。

### 已实现 vs 未实现的操作

一个协议端点(如 chat-completions)在官方可能含多个操作(create / get / list / update / delete 等),
但 Unio 不一定全部实现。处理原则:

- **子目录以 Unio 暴露的操作为主体**;完整的 `official.md` + `params.md` 深度文档**只给已实现的操作**。
- 协议中存在、但 Unio **不实现**的操作(典型如依赖服务端存储 `store=true` 的「stored completions」
  管理端点,而 Unio 无状态),在该端点 `official.md` 末尾以一节「端点全集与 Unio 立场」**轻量列出**:
  逐个标注操作名、官方用途一句话、Unio 立场(实现 / 不支持-无状态 / 501 等)。
- 未实现的操作**不单独建子目录、不写 `params.md`**,避免给永不实现的接口背维护负担。
- 示例:`openai/chat-completions/` 深写 `POST /v1/chat/completions`(create);get/list/update/delete
  依赖 stored completions,Unio 无状态不支持,只在 `official.md` 末尾列全集 + 立场。

## 4. 事实可追溯(不可捏造)

- 每条事实必须能追溯到官方来源:`来源:[官方·页面名](URL)(查阅 YYYY-MM-DD)`。
- 官方未明确、拿不准的点,标 `⚠️ 待查证` 并写明要确认的页面,**不得**写成结论。
- 价格、模型、限制这类**有时效**的信息,更新后刷新查阅日期。

## 5. 文档风格

- 与 [docs/providers/README.md](../providers/README.md) 第 5 节一致:面向懂技术但不熟该协议的读者,
  不堆术语也不写大白话;简写与专业名词首次出现按 `术语(全称 / 一句话解释)` 标注。

## 6. 当前在册端点

| 协议族 | 端点 | 目录 | official | params |
| --- | --- | --- | --- | --- |
| OpenAI | Chat Completions(对话补全) | `openai/chat-completions/` | ✅ 已就位 | ✅ |
| OpenAI | Responses(Codex 兼容) | `openai/responses/` | ✅ 已就位(主 + 2 配套) | ✅ |
| Anthropic | Messages(消息) | `anthropic/messages/` | ✅ 已就位 | ✅ |

## 7. 迁移记录(2026-06-07 完成)

本目录已从「平铺单文件」迁移为「每端点子目录 + official/params」。原文件归位如下(用 `git mv` 保真迁移):

| 原文件 | 去向 |
| --- | --- |
| `openai_chat_completion.md` | `openai/chat-completions/official.md` |
| `openai_responses.md` | `openai/responses/official.md` |
| `openai_responses_streaming_events.md` | `openai/responses/official-streaming-events.md` |
| `openai_responses_other_endpoints.md` | `openai/responses/official-other-endpoints.md` |
| `anthropic_message.md` | `anthropic/messages/official.md` |
| `deepseek_anthropic_api.md` | **移出本目录** → `docs/providers/deepseek/anthropic-api-reference.md`(第三方上游) |

引用旧路径的其它文档(chapters 等)已同步更新链接。
