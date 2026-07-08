# 透传链路审计 · Anthropic vs OpenAI(beta 头 / 未知字段 / 计费维度）

> **查阅日期**:2026-07-07
> **方法**:以**代码实际行为**为准绳,官方文档/SDK 枚举/定价页为对照,**不以本仓既有文档为事实来源**
>（既有矩阵与 README 已发现多处滞后,见 §5)。
> **触发**:用户实测发现——Claude 桌面端直连中转站(sub2api)能看到 `ephemeral_1h` 缓存标记,
> 经本网关转发则从未出现。根因定位为 `anthropic-beta` 白名单丢弃 `extended-cache-ttl-2025-04-11`。
> 本审计把该个案上升为「透传链路静默丢弃」这一类问题,across 两个 provider 做系统排查。

---

## 1. 结论速览

| # | 问题 | provider | 类别 | 严重度 | 状态 |
| --- | --- | --- | --- | --- | --- |
| F1 | `anthropic-beta` 白名单丢弃真实 beta → 建模能力静默降级 | Anthropic | 功能 | 🔴 高 | ✅ **已修**(P0-A 补 1h 缓存 + P0-B 改透传) |
| F2 | `code_execution` / `memory` 工具已建模,但其 beta 头被 F1 丢弃 → 工具静默失效 | Anthropic | 功能 | 🔴 高 | ✅ **已修**:P0-B 透传后 `memory`/`code_execution` 均放行 |
| F3 | `code_execution` 按 container-hour 计费,usage 解析无此维度 → 少计 | Anthropic | 计费 | 🟡 中 | ✅ **已决**:走 1 透传吸收(对齐 new-api/sub2api,免费额度兜底) |
| F4 | 白名单含 2 条不在官方 SDK 枚举的 token → 排查是名字错还是已 GA | Anthropic | 功能 | 🟡 中 | ✅ **已查证(P1-B)**:两条均**已 GA**,名字没错。透传下功能正常;见 §8 |
| F5 | `anthropic-version` 钉死 `2023-06-01`,覆盖客户端 | Anthropic | 兼容 | 🟢 低 | ✅ **已查证(P2)**:`2023-06-01` 是当前唯一稳定版,钉死正确;见 §9 |
| F6 | OpenAI 侧出站不透传任何客户端头(仅 Authorization/Content-Type/Accept) | OpenAI | 潜在 | 🟢 低 | 已确认(代码) |
| F7 | 透传型新计费维度字段(两侧)能过但可能漏计 | 两者 | 计费 | 🟡 中 | 结构性风险 |
| — | context-1m 分层计费缺口 | Anthropic | 计费 | ⬇️ **已基本过时** | 见 §4 |

**一句话**:真正的系统性问题只有一个——**Anthropic 的 `anthropic-beta` 采用「白名单严进,未登记静默丢且不报错」策略,且白名单滞后于官方**;它同时制造了功能缺陷(F1/F2)与潜在计费缺陷(F3)。OpenAI 侧是 body 驱动 + 全透传,**无同类功能缺陷**。

---

## 2. 两侧透传模型(代码事实）

### 2.1 Anthropic(`internal/core/adapter/anthropic/messages`)

出站请求体**不是 RawBody 原样透传**,而是从 typed struct `messagesRequest` 重新编码(`wire.go`),
再把 ingress 捕获的未知顶层字段 `Extensions` **merge 回请求体**(`buildMessagesRequestBody` → `mergeJSONObjects`)。

- **未知顶层 body 字段**:ingress `decode.go` 的 `UnmarshalJSON` 把不在 `knownMessageFields`(13 个)内的合法字段收进 `Extensions`,
  经 `message_dto_map.go` 透传给 adapter,再 merge 回上游 body。**→ body 侧未知字段不丢**(`service_tier`/`container`/`mcp_servers`/`output_config`/`inference_geo` 等都能到上游)。
- **`anthropic-beta` 头**:**透传 + 小黑名单,且策略运行时可配(管理端可编辑)**。
  `official_adapter.go: applyBetaPolicy` 每次读 `activeBetaPolicy`;策略经 `BetaPolicyProvider` 注入(gateway 侧由
  `appsettings.SettingsStore` 供给:PostgreSQL `app_settings` key=`anthropic.beta_policy` 为权威源,Redis 作实时缓存实现跨进程秒级生效,
  各进程再叠一层 ~3s 本地缓存去抖;admin 改后无需重启)。三种模式:`passthrough`(全透传)/ `filter`(黑名单)/ `whitelist`(白名单)。未配置时回退 `DefaultBetaPolicy`。
  **→ 未知/未来 beta 不再静默丢弃,与 OpenAI 侧对齐。** (历史:P0-B 前为白名单严进,曾漏 `extended-cache-ttl` 致 1h 缓存降级。)
- **`anthropic-version`**:钉死常量 `2023-06-01`,覆盖客户端传入(F5)。

默认策略(`DefaultBetaPolicy`,DB 无记录时用):`filter` 模式 + 黑名单仅 `context-1m-2025-08-07`
(遗留模型 >200K 分层价、`channel_prices` 无分层列)。`code-execution-2025-05-22` 已按「走 1(透传吸收 container 成本)」
**默认放行**——container 成本靠 Anthropic 每月免费额度兜底,只对 token 正常计费。运维可在管理端「系统设置 → Provider 设置」随时调整。

### 2.2 OpenAI(`internal/core/adapter/openai/{chatcompletions,responses}`)

- **ChatCompletions**:typed struct 重编码 + `Extensions` merge 回 body(`request_wire.go: buildChatCompletionRequestBody`)。未知 body 字段不丢。
- **Responses**:直传路径以客户端**原始请求体 `RawBody()` 零损耗重放**,仅改写 `model`/`stream`(`direct_response.go: encodeUpstreamResponsesBody`);无 raw body 时回退 typed + merge Extensions。
- **出站头**:仅 `Content-Type`/`Authorization`/`Accept`(`chat.go`/`adapter.go`/`compact.go`),**不透传任何客户端头**(F6)。
- OpenAI 的能力开关是 **body 参数驱动**(`reasoning_effort`/`verbosity`/`service_tier`/`prompt_cache_*` 等),Chat/Responses **不依赖 beta 头**,body 又全透传 → **无 F1/F2 同类问题**。

---

## 3. F1/F2:beta 白名单缺口(核心）

对照 [anthropic-sdk-python `AnthropicBetaParam` 权威枚举](https://raw.githubusercontent.com/anthropics/anthropic-sdk-python/main/src/anthropic/types/anthropic_beta_param.py)(2026-07-07,最新含 `agent-memory-2026-07-22`):

| SDK 枚举 beta | 对应已建模能力(矩阵) | 是否需头(2026-07) | 白名单 | 影响 |
| --- | --- | --- | --- | --- |
| `extended-cache-ttl-2025-04-11` | `cache_control.ttl=1h` | **是** | ❌ 缺 | 🔴 1h 缓存降级为 5m(实测证实) |
| `code-execution-2025-05-22` | `code_execution_*` 工具 | **是** | ❌ 缺 | 🔴 工具静默失效 + 计费缺口(F3) |
| `context-management-2025-06-27` | `memory_20250818` 工具 | **是** | ❌ 缺 | 🔴 工具静默失效 |
| `interleaved-thinking-2025-05-14` | thinking + 多轮 tool call 交错 | 是 | ❌ 缺 | 🟡 需验响应解析能吃住多个 thinking block |
| `context-1m-2025-08-07` | 1M 上下文 | **仅遗留模型** | ❌ 缺 | ⬇️ 见 §4(当前模型已 GA) |
| `token-efficient-tools-2025-02-19` | tool 调用省 token | 是 | ❌ 缺 | 🟢 低 |
| `output-128k-2025-02-19` / `output-300k-2026-03-24` | 大输出上限 | 是 | ❌ 缺 | 🟢 低 |

**要点**:`web_search_*` / `web_fetch_*` 已 **GA**(不在 SDK beta 枚举里),不需头,当前工作正常;
真正被白名单卡住的建模工具是 **`code_execution` 与 `memory`**。

### F4:白名单里 2 条无法用 SDK 枚举确认
`fine-grained-tool-streaming-2025-05-14` 与 `structured-outputs-2025-11-13` **不在** SDK 枚举中。可能:
1. 已 GA 从 beta 毕业(fine-grained streaming 很可能);
2. 官方 token 名有细微差异。
矩阵把 `output_config.format.json_schema` 标为 `Typed`——**若 `structured-outputs` 这个名字写错,结构化输出会静默失效**。
→ **上线前用真 key 各发一个请求实测**(白名单里名字写错 = 没登记)。

---

## 4. context-1m 计费缺口:已基本过时(纠正上一轮判断）

早前判断为 🔴「透传 context-1m 但 `channel_prices` 无分层定价 → 大请求倒赔」。**2026-03-13 官方已取消 200K 以上长上下文溢价**
([官方 Pricing · Long context](https://platform.claude.com/docs/en/about-claude/pricing)):

- **当前模型**(Opus 4.6/4.7/4.8、Sonnet 4.6/5、Fable 5、Mythos 5 等):1M 窗口**全程平价、不需 beta 头**(带了静默忽略)。**无计费缺口。**
- **仅遗留模型**(Sonnet 4 / 4.5):仍需 `context-1m-2025-08-07` 头,且 >200K 进 $6/$22.5 分层。**只有在售卖这些遗留模型且允许 >200K 时**,才存在「上游按分层扣、本侧按单档收」的倒赔风险。

`channel_prices` 现状:`pricing_unit` CHECK 仅 `per_1m_tokens`、`uncached_input_price` 单列,**无按输入规模分层的列**——这个结构限制属实,但**只对遗留模型是问题**。结论:降级为「仅遗留模型需关注」,不阻塞当前模型透传。

---

## 5. 文档漂移纠正(既有知识库与代码不符）

以代码为准,以下既有文档表述**已失真**,需修正:

1. `chapters/phase-10-.../ANTHROPIC_MESSAGES_MATRIX.md §3`:把顶层 `service_tier` / `container` / `inference_geo` / `output_config` / `cache_control` 标为 `Typed`。
   **实际**:`decode.go: knownMessageFields` 只建模 13 个字段,以上均**走 `Extensions` Passthrough**,非 Typed。功能等价(都透传到上游),但状态标注错误。
2. `chapters/phase-10-.../ANTHROPIC_MESSAGES_MATRIX.md §2` 与 `providers/anthropic/README.md` L42:称 `anthropic-beta` 「当前未透传 / provider Drop」。
   **实际**:官方 adapter 已有白名单(`beta.go`,5 条)**部分透传**,并非全 Drop。
3. `providers/anthropic/upgrade-plan.md N1`:⚠️「首批要支持的 beta 集合待查证」。
   **本审计已给出权威集合**(见 §3),该待查证项应关闭并替换为「白名单缺口 + 架构选型」。

---

## 6. 改造计划

### P0-A · 补 `extended-cache-ttl-2025-04-11`(最小、安全）
- 动作:`beta.go` 白名单加 `"extended-cache-ttl-2025-04-11": {}`;`beta_test.go` 补「该 beta 透传到上游」用例。
- **前端显示**:链路已完整,**无需前端改动**。`wire.go` 解析 `ephemeral_1h_input_tokens` → `usage.go: ToUsageFacts` 拆成 `CacheWrite1hInputTokens` → `billing/service.go` 独立计价 → `request-cells.tsx` / `TokenTip.tsx` 已渲染「缓存写入·1h」并对 0 值 `.filter(>0)` 隐藏(故 `ttl:1h` 请求只显示 1h、不显示 5m,与 sub2api 一致)。当前只显示 5m 纯粹是因为 beta 被丢、上游降级返回 5m。
- ⚠️ **计费配置依赖(重要)**:`price.go` 中 `cacheWrite1hInputRate` 在 1h 价格列未配置时**静默回退到未缓存基础价(1x)**;而 Anthropic 的 1h 写入是 **2x**。因此 P0-A 让 1h token「不遗漏、会计费」,但要按正确 2x 计价,必须配置(DEC-026 双侧):
  - **成本**:`channel_prices.cache_write_1h_input_cost` —— 渠道页 → 渠道行「⋯」→「价格」→「1 小时缓存写入」;
  - **售价**:`model_prices.cache_write_1h_input_price` —— 模型页 → 模型行「⋯」→「基准价」→「1 小时缓存写入」(客户最终价 = 基准价 × 线路倍率)。
  否则按 1x 少收、吃毛利。5m 档同理(未配回退基础价,漏 0.25x 溢价),建议一并补齐。
- 风险:代码侧无;运营需补 1h 价格列。**这是修复用户实测缺陷的直接项。**

### P0-B · 决策 beta 策略(根因）✅ 已定并落地:**透传 + 小黑名单,且配置化**
白名单每次官方出新 beta 都要改代码且易漏(本次已漏)。已选定**方向 2(透传 + 小黑名单)并叠加方向 3(配置化)**。
- **已实现**(2026-07-07):
  - `beta.go`:引入 `BetaMode`(passthrough/filter/whitelist)+ `BetaPolicy` + `BetaPolicyProvider` + `DefaultBetaPolicy`;
    `forwardableBetas`/`blockedBetas` 改为策略驱动;`SetBetaPolicyProvider` 包级注入(对齐 `tokenest.Configure` 先例)。
  - `app_settings`(migration 000068,通用 key→JSONB)存储;`appsettings` 包提供 TTL 缓存 provider + admin 读写服务。
  - admin API:`GET/PUT /admin/v1/provider-settings/anthropic/beta-policy`;admin UI:系统设置 →「Provider 设置」卡片(模式下拉 + 清单 + 说明)。
  - gateway bootstrap 注入 provider(TTL≈30s),admin 改后约 30s 内热更新、无需重启。
- **P1-A 决策**:code_execution **走 1(透传吸收)**,已从默认黑名单移除(对齐 new-api/sub2api 的 token 级计费实践;container 成本非单请求可计量)。
- 扩展性:将来 OpenAI/Gemini 配置直接新增 `app_settings` key + 各自卡片,无需改表。

### P1-A · code_execution 计费 ✅ 已决:走 1(透传吸收)
- **决策**(2026-07-07):对齐 new-api / sub2api 的实践——两者均为 **token 级计费,不单独计量 container-hour**
  (根因:单次响应 usage 不含 container 运行时长,网关侧无权威数据)。故 `code-execution-2025-05-22` **默认放行透传**,
  container 成本靠 Anthropic 每月免费额度兜底,只对 token 正常计费。重度 standalone code execution 若超免费额度会吃点成本,属可接受的已知取舍。
- 若将来要精算 container 成本,需另找数据源(响应头 / 账单 API),再决定是否补 `channel_prices` 维度;当前不做。
- `interleaved-thinking` / `memory`:随透传放行;thinking token 已计入 `output_tokens_details`,响应 content block 原样透传不丢。

### P1-B · F4 查证闭环 ✅ 已完成(文档查证,见 §8)
- 结论:`structured-outputs-2025-11-13`(GA 2026-02-04)、`fine-grained-tool-streaming-2025-05-14`(GA 2026-02-05)**均已 GA**,名字没写错。
- 透传架构下功能正常,无需真 key 实测即可结案。**新增风险**:`fine-grained-tool-streaming` 旧头在 **Bedrock + Opus 4.7 会被拒(400)**——透传模式的固有代价,必要时用黑名单挡(见 §8)。

### P2 · F5 `anthropic-version` 策略 ✅ 已完成(结论:不改,见 §9)
- 结论:`2023-06-01` 是当前**唯一稳定版**(`2023-01-01` 已废弃),官方 SDK 也硬编码此值。钉死**正确**,现在不需要改。
- 将来官方若出新版本头,用 `app_settings` 机制加可配置项即可(近零成本);当前做配置化属过度设计。

### 结构性 · F7 计费护栏(两侧)
- 透传模式对**新计费维度字段**天然有漏计风险。建议:上游 usage 出现未知计费维度时告警,而非静默按已知维度结算。

---

## 7. 需真 key 验证的清单

- [ ] 加 `extended-cache-ttl` 后,`ttl:1h` 请求上游返回 `ephemeral_1h_input_tokens` 且计费正确(闭合用户实测缺陷)。
- [x] `structured-outputs-2025-11-13` / `fine-grained-tool-streaming-2025-05-14` 名字与 GA 状态 → **文档查证已闭合(§8)**。
- [ ] `code-execution` 响应 content block 与 container 计费维度形状。
- [ ] `interleaved-thinking` 多 thinking block 的响应解析。
- [ ] 是否售卖遗留 Sonnet 4/4.5;若是,>200K 分层计费(§4)。

---

## 8. P1-B 查证:两个 beta 均已 GA(名字没错）

对照官方文档(2026-07-07 查):

| beta | 现状 | 迁移方式 | 依据 |
| --- | --- | --- | --- |
| `structured-outputs-2025-11-13` | **GA(2026-02-04)** | 头不再需要;`output_format` → `output_config.format`(旧头/旧参数过渡期仍可用) | [官方博客](https://claude.com/blog/structured-outputs-on-the-claude-developer-platform) / [官方文档](https://platform.claude.com/docs/en/build-with-claude/structured-outputs) |
| `fine-grained-tool-streaming-2025-05-14` | **GA(2026-02-05,全模型全平台)** | 改用 tool 上的 `eager_input_streaming: true`;旧头过渡期仍可用 | [官方文档](https://platform.claude.com/docs/en/agents-and-tools/tool-use/fine-grained-tool-streaming) |

结论:F4 是「已 GA 从 beta 毕业」,不是名字写错。不在最新 SDK 枚举里正是因为毕业了。透传(filter)模式下客户端发来照样转发,**功能不受影响,无需真 key 实测**。

⚠️ **透传模式的固有代价(新发现,记录备查)**:`fine-grained-tool-streaming-2025-05-14` 旧头在 **AWS Bedrock + Opus 4.7 会被 passthrough 校验拒收(400)**([vercel/ai#14542](https://github.com/vercel/ai/pull/14542) 实测)。即:若将来接 Bedrock 渠道、客户端又用旧 SDK 发此头,透传会导致整请求 400。这正是保留黑名单机制的价值——真遇到时,运维在「Provider 设置」把该头加进黑名单即可挡掉。同理适用于任何"上游已不认的旧头"。

## 9. P2 查证:`anthropic-version` 钉死 `2023-06-01` 正确

官方 [Versions 文档](https://platform.claude.com/docs/en/api/versioning):`anthropic-version` 当前**唯一稳定值就是 `2023-06-01`**(`2023-01-01` 已废弃,官方 SDK 也硬编码 `2023-06-01`)。因此 `adapter.go: anthropicVersionHeaderValue = "2023-06-01"` **正确,现在不需要改,也不需要透传客户端的版本头**(客户端乱传反而可能触发上游 400)。

- 现在做配置化 = 过度设计(没有第二个有效值可选)。
- **将来动作**:官方若发布新版本头且需支持其新特性,用已建好的 `app_settings` 机制加一个 `anthropic.api_version` 配置项即可(近零成本)。对齐 upgrade-plan N4,该待办据此**降级为「观察项」**。

---

## 10. 缓存 TTL 默认值变更(2026-03-06)与计费无关性

**背景(2026-07-08 查证)**:5m/1h 两档缓存**依然存在,官方未取消**(5m 写入=1.25x、1h 写入=2x、读取=0.1x)。
唯一变化是:**2026-03-06 起,`cache_control:{"type":"ephemeral"}` 不带 `ttl` 时的默认 TTL 从 1h 悄悄降为 5m**
(官方 2026-04-23 postmortem 确认)。要 1h 必须**显式** `ttl:"1h"`。

**对本网关计费的影响:无。** 计费是**响应驱动**,不解释请求里的 `ttl`:

- `wire.go` 解析上游 usage 的 `cache_creation.ephemeral_5m_input_tokens` / `ephemeral_1h_input_tokens`;
- `usage.go: ToUsageFacts` 按上游返回的拆分生成事实;`billing/service.go` 5m/1h 分别计价。

因此无论客户端显式发 `ttl:"1h"`(上游→`ephemeral_1h`→按 2x)还是用新默认 5m(上游→`ephemeral_5m`→按 1.25x),
本网关都**如实按上游实际档位计费**,零影响。2026-07-08 的 10 条 Kiro 请求已端到端对账验证(5m/1h/read/uncached/output 逐 token 一致)。

**唯一理论前提(非本变更引入)**:计费正确性依赖**上游在 usage 里返回 5m/1h 拆分**。若某上游对 1h 写入只回 flat 总量、不给拆分,
`ToUsageFacts` 兜底会全算 5m(1.25x)→ 少收(见 `wire.go: mergeUsageWire` 针对 sub2api 的注释)。当前上游正确返回拆分,不受影响。

**给运维的提示**:若客户反馈「1h 缓存命中率暴跌/成本上升」,大概率是其**客户端未显式带 `ttl:"1h"`**、吃了新默认值,
不是网关 bug——网关只是如实反映上游实际做的档位。

---

## 附:权威来源
- [anthropic-sdk-python `AnthropicBetaParam` 枚举](https://raw.githubusercontent.com/anthropics/anthropic-sdk-python/main/src/anthropic/types/anthropic_beta_param.py)(beta 权威清单)
- [Anthropic Beta headers](https://docs.claude.com/en/api/beta-headers)(无效头上游返 400)
- [Anthropic Prompt caching](https://docs.claude.com/en/docs/build-with-claude/prompt-caching)(1h TTL=2x;基础 caching 已 GA 不需前缀;5m/1h 两档仍在,2026-03-06 起不带 ttl 默认降为 5m)
- [Anthropic Pricing · Long context](https://platform.claude.com/docs/en/about-claude/pricing)(2026-03-13 取消 >200K 溢价)
- [Anthropic Structured outputs](https://platform.claude.com/docs/en/build-with-claude/structured-outputs)(structured-outputs GA)
- [Anthropic Fine-grained tool streaming](https://platform.claude.com/docs/en/agents-and-tools/tool-use/fine-grained-tool-streaming)(fine-grained streaming GA)
- [Anthropic API Versions](https://platform.claude.com/docs/en/api/versioning)(2023-06-01 为当前唯一稳定版)
- 代码:`beta.go` / `official_adapter.go` / `adapter.go` / `wire.go` / `decode.go` / `message_dto_map.go`(anthropic);`request_wire.go` / `direct_response.go` / `chat.go` / `responses/adapter.go`(openai)
