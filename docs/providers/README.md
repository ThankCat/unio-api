# Providers 上游适配文档规范

本目录记录每个上游 provider(上游服务商,如 DeepSeek、OpenAI、Anthropic)的接入事实： 协议参数、适配与转换逻辑、计费口径、废弃与转换参数、升级/新增计划。

新接入任何上游,都必须按本规范在本目录下建立独立子目录并补齐必备文件。

## 1. 定位与边界

- 本目录回答的是「**某个具体上游怎么接、参数怎么转、怎么计费**」这一层事实。
- 本目录是上游适配事实的**权威来源**(authoritative source,即出现分歧时以此为准的源头)。
历史阶段文档(如 `docs/chapters/phase-10-*/DEEPSEEK_*_MAPPING.md`)在迁移后保留为**指针**
(只留一句话指向本目录),不再承载权威映射表。
- 不重复全局内容,改为**引用**：
  - 公开协议端点规范(OpenAI / Anthropic 协议本身)→ [docs/protocol/](../protocol/README.md)
  - 架构原则与商业语义 → [docs/production/DECISIONS.md](../production/DECISIONS.md)
  - 生产欠账(GAP)→ [docs/production/TODO_REGISTER.md](../production/TODO_REGISTER.md)
  - 阶段计划与历史 → [docs/chapters/](../chapters/README.md)
- 与代码冲突时,以「**代码 + 官方文档**」为准,并立即修正本目录文档。

### 与 `docs/protocol/` 的分工

- `docs/protocol/`：**公开协议**端点规范,只含 Unio 对外暴露的 OpenAI 与 Anthropic 两个协议族,
  与具体上游无关。每个端点两份文档:一份官方文档,一份依据官方整理的请求/响应参数说明。
- `docs/providers/<provider>/`：**某个上游**的适配事实,包括该上游相对标准协议的参数差异、
  转换逻辑、计费、专属行为。上游自身的协议/兼容性文档(如某上游的 Anthropic 兼容说明)也归这里,
  不放进 `docs/protocol/`。
- 一句话:协议本身归 `docs/protocol/`,某个上游怎么接归 `docs/providers/`。

## 2. 目录规范

- 每个 provider 一个独立目录,目录名用小写 slug(slug,即稳定的英文短标识,如 `deepseek`、`openai`)。
- 每个 provider 目录**必须**包含以下文件(职责见第 4 节)。其中
  **`protocol-and-params.md` 与 `adaptation.md` 这两份「协议适配」文档必须按协议族拆开,不允许把
  OpenAI 与 Anthropic 写在同一份里**:
  - 上游**只接一个协议族**时:这两份文件直接平铺在 provider 目录根。
  - 上游**接多个协议族**时(如 DeepSeek 同时提供 OpenAI 兼容与 Anthropic 兼容):按协议族建
    `openai/`、`anthropic/` 子目录,每个子目录各放一份 `protocol-and-params.md` + `adaptation.md`。
- `README.md` / `billing.md` / `upgrade-plan.md` 始终放在 provider 目录根(跨协议共用,不拆)。

单协议上游布局:

```text
docs/providers/<provider>/
  README.md                 # 概览 + 关键事实速查 + 术语表 + 本目录文档索引
  protocol-and-params.md    # 相对标准协议的参数差异:支持 / 废弃 / 转换 三类表
  adaptation.md             # 适配与转换逻辑:思考、工具、流式、usage、模型名、错误
  billing.md                # 计费:价格、token 口径、cache、reasoning 计费
  upgrade-plan.md           # 升级/新增计划(开头必须标官方文档最新日期 + 地址)
```

多协议上游布局(以 DeepSeek 为例):

```text
docs/providers/<provider>/
  README.md                 # 概览(列出本上游接受的所有协议族)
  billing.md                # 计费(跨协议共用)
  upgrade-plan.md           # 升级/新增计划(跨协议共用)
  openai/
    protocol-and-params.md  # OpenAI 格式:支持 / 废弃 / 转换
    adaptation.md           # OpenAI 格式:适配与转换逻辑
  anthropic/
    protocol-and-params.md  # Anthropic 格式:支持 / 废弃 / 转换
    adaptation.md           # Anthropic 格式:适配与转换逻辑
```

- 新接入上游时,复制一份现有 provider 目录作为骨架,逐项替换,不得删除必备小节。
- 缺一份必备文件,或把两个协议族的适配写在同一份里,均视为接入未完成。

## 3. 事实可追溯原则(不可捏造)

每一条事实都必须能追溯到来源。书写约定如下：

- **官方文档来源**：在所在小节或表格旁标注页面名、链接与查阅日期。
  - 格式：`来源：[官方·页面名](URL)(查阅 YYYY-MM-DD)`
- **代码来源**：标注文件路径(必要时含函数名)。
  - 格式：`代码：internal/core/adapter/openai/deepseek/chatcompletions/drop.go`
- **黑盒实测**：标注实测日期与验证方式。
  - 格式：`实测(YYYY-MM-DD)：<如何验证>`
- **不确定 / 官方未明确**：标 `⚠️ 待查证`,写明需要确认的官方页面,**不得**写成结论性表述。

禁止：凭印象或推测写入"上游应该是这样"的结论。拿不准就查官方文档或做黑盒验证。

## 4. 必备文件与小节

### README.md(概览)

- 上游定位与官方文档入口(链接 + 查阅日期)。
- 端点(Endpoint,即上游 API 地址)：按协议族列出 base URL 与路径。
- 模型清单：在售模型、默认模型、弃用模型及弃用时间。
- 关键事实速查表(协议族、认证、是否支持思考/工具/流式/缓存等,一眼可查)。
- 术语表(见第 5 节)。
- 本目录文档索引。

### protocol-and-params.md(协议与参数)

- 记录的是**该上游相对标准协议的差异**,不重抄标准协议本身(标准协议见 [docs/protocol/](../protocol/README.md))。
- **每个协议族一份**(多协议上游放在 `openai/`、`anthropic/` 子目录下,不混写)。每份给出三类表:
  - **支持参数**：可透传到上游、语义一致的参数。
  - **废弃参数**：上游已废弃或忽略,出站丢弃的参数。
  - **转换参数**：需要改名或改值才能进上游的参数(映射关系)。
- 每行注明：策略(透传 Pass / 转换 Adapt / 丢弃 Drop)+ 依据(官方或代码)。
- 「透传 / 转换 / 丢弃」的术语解释统一放术语表。
- 本文件是该上游某协议族的**权威逐字段映射**;历史阶段映射表(如 `chapters/phase-10-*/DEEPSEEK_*_MAPPING.md`)
  迁移后只保留为指针,不再承载权威表。

### adaptation.md(适配与转换逻辑)

- **每个协议族一份**,与同目录的 `protocol-and-params.md` 配对(多协议上游放在各自子目录下,不混写)。
- 请求(入口 → 上游)、响应(上游 → 客户)、流式三条链路的关键转换。
- 思考(reasoning / thinking)处理:开关、强度、跨轮回传规则。
- 工具调用(tool / function call)处理。
- usage(用量)字段到内部计费事实的映射。
- 模型名处理、错误映射、其它上游专属行为。
- 每个关键逻辑配上对应代码位置。

### billing.md(计费)

- 对外售价口径与上游成本口径(分开)。
- token 计量维度:输入(含缓存命中/未命中)、输出、reasoning。
- 计费公式 + 一个具体算例。
- 币种与单位(如 元/百万 token)。
- 所有价格必须标官方来源与查阅日期(价格会变)。

### upgrade-plan.md(升级/新增计划)

- 语义:**没有的上游是「新增(创建)计划」,已有的上游是「升级计划」**,同一份文件承载两种用途。
- **开头必须**标注「官方文档最新日期 + 地址」(用于判断本文档是否落后)。
- 新增(创建)阶段:正文为接入待办清单(待接端点、待实现转换、待配价格、待补黑盒)。
- 升级阶段:正文为升级项清单,每项含:现状、官方依据(链接)、影响、方案、优先级、状态。
- 项关闭后移入「已完成」区;若是生产欠账,同步 `TODO_REGISTER.md` 的 GAP。

## 5. 文档风格

- 读者画像:**懂技术、但不熟悉这个上游**的工程师。既不要堆砌术语,也不要写成大白话。
- 简写与行业专业名词**首次出现**时必须标注解释,格式：`术语(全称 / 一句话解释)`。
  - 例：`SSE(Server-Sent Events,服务器持续推送数据的流式响应格式)`。
- 每个 provider 的 `README.md` 末尾维护本目录「术语表」,汇总该上游文档用到的术语。
- 跨 provider 的通用术语放本文件末尾「通用术语表」,各 provider 可直接引用,不必重复解释。
- 结论优先用表格 + 短句;避免长段落;能给依据就附链接或代码路径。

## 6. 维护规则

- 上游官方文档变更,或我方适配代码变更时,同步更新对应文件,并刷新查阅日期。
- 升级项关闭后,从 `upgrade-plan.md` 的待办区移入已完成区;涉及上线阻断的同步 `RELEASE_BLOCKERS.md`。
- 价格、模型清单、弃用时间这类**有时效**的信息,每次查阅后更新日期标注,过期信息及时纠正。

## 通用术语表


| 术语                   | 全称 / 解释                                      |
| -------------------- | -------------------------------------------- |
| Provider             | 上游服务商(如 DeepSeek);是业务概念,不等于代码里的 adapter 接口   |
| Adapter              | 适配器:只做协议转换、请求发送、响应解析的纯代码能力,不读环境变量、不查库        |
| Ingress              | 入口协议:客户调用 Unio 时使用的对外协议族(OpenAI 或 Anthropic) |
| Upstream             | 上游:Unio 转发到的真实 provider 接口                   |
| Endpoint             | 端点:一个具体的上游 API 地址(base URL + 路径)             |
| Pass / 透传            | 参数名和语义一致,原样发给上游                              |
| Adapt / 转换           | 参数需要改名或改值后才发给上游                              |
| Drop / 丢弃            | 入口可以接收,但不发给上游(并记内部审计)                        |
| SSE                  | Server-Sent Events,服务器持续推送数据的流式响应格式          |
| Token                | 模型处理文本的最小计量单位,计费按 token 数                    |
| CoT                  | Chain-of-Thought,思维链,即模型在给出答案前的"思考过程"文本      |
| reasoning / thinking | 模型的思考模式;开启后会先产出思维链再给最终答案                     |
| usage                | 上游返回的用量统计(输入/输出 token 等),是计费事实来源             |


