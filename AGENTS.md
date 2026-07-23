# AGENTS.md — Unio API AI 工作规则

## 定位

`AGENTS.md` 只保留 AI 协作时必须长期遵守的稳定规则。

它不是知识库、阶段计划、任务清单或交接文档。详细内容必须写入：

- 当前状态：[docs/PROJECT_STATUS.md](docs/PROJECT_STATUS.md)
- 阶段计划：[docs/chapters/](docs/chapters/README.md)
- 生产欠账：[docs/production/TODO_REGISTER.md](docs/production/TODO_REGISTER.md)
- 重大决策：[docs/production/DECISIONS.md](docs/production/DECISIONS.md)
- 目录结构：[docs/architecture/PROJECT_STRUCTURE.md](docs/architecture/PROJECT_STRUCTURE.md)

## 项目边界

项目名：`Unio API`

定位：商业化多 provider、多 channel、可后台管理、可 fallback、可计费的 AI API 网关。

后端项目：

```text
unio-gateway = Gateway API + Admin API + Console API + Worker
```

前端项目：

```text
unio-web = 独立前端项目，不放在 unio-gateway 中
```

公开 API 边界：

- `/v1/*`：客户程序调用，OpenAI Chat Completions 与 Anthropic Messages 原生协议，opaque API key 认证。
- `/admin/v1/*`：平台管理员调用，管理 provider/channel/model/price/user/billing/audit，admin 认证。
- `/console/v1/*`：普通用户调用，管理 project/api key/balance/usage/request logs，user 认证。

## 架构原则

- 当前使用模块化单体，不做早期微服务拆分。
- `gateway-server`、`admin-server`、`console-server`、`worker-server` 是长期目标进程。
- Admin API、Console API 不能绕过 billing、ledger、routing 的业务规则直接改核心事实。
- 不同服务可以共享业务概念、错误码、OpenAPI contract 和 DTO 规范，但不要共享数据库 row struct 作为跨服务 API。
- 全服务目录、分层职责和依赖方向以 [docs/architecture/PROJECT_STRUCTURE.md](docs/architecture/PROJECT_STRUCTURE.md) 为准。

## 阶段规则

- 每个阶段进入实现后，必须按该阶段 `PLAN.md` 和 `ACCEPTANCE.md` 收口。
- `done` 只能表示实现完成、验证通过、文档同步，且没有本阶段必须关闭的 P0/P1 production TODO。
- 当前阶段内影响公开 API 契约、资金、安全、账务事实、数据一致性或上线能力的欠账，不能随意 deferred。
- 新增 production TODO 必须登记为 `GAP-*`，说明风险、触发时机和未来替换方向。
- 关闭阶段前必须扫描并复核当前阶段所有 `GAP-*`。

进入新阶段前必须执行：

```bash
rg -n "TODO|GAP-" AGENTS.md docs cmd internal migrations sql
```

进入新阶段前必须阅读：

```text
docs/PROJECT_STATUS.md
docs/chapters/README.md
当前章节 docs/chapters/phase-xx-*/PLAN.md
当前章节 docs/chapters/phase-xx-*/STATUS.md
当前章节 docs/chapters/phase-xx-*/ACCEPTANCE.md
docs/production/TODO_REGISTER.md
docs/production/RELEASE_BLOCKERS.md
```

## 文档治理

- 动态进度只写入 `docs/PROJECT_STATUS.md` 或章节 `STATUS.md`，不要写入 `AGENTS.md`。
- 章节任务、验收标准和阶段状态只写入对应 `docs/chapters/phase-xx-*`。
- 生产欠账只以 `docs/production/TODO_REGISTER.md` 为事实登记表。
- 上线阻断只写入 `docs/production/RELEASE_BLOCKERS.md`。
- 重大架构、商业语义、协议策略和安全策略写入 `docs/production/DECISIONS.md`。
- 协议原始参考或官方摘要快照放入 `docs/protocol/`，章节字段矩阵必须逐项消费。

## TODO 与 GAP

生产欠账 TODO 格式：

```go
// TODO(阶段X/production): [GAP-X-001] 风险；触发时机；未来替换方向。
```

规则：

- 每个 production TODO 必须登记到 `docs/production/TODO_REGISTER.md`。
- 每个 GAP 必须链接到对应章节 `PLAN.md` 的具体任务锚点。
- 代码 TODO 只标记实现位置，完整上下文以 TODO register 和章节文档为准。
- 已完成测试不得保留临时 TODO。
- 新增或关闭 production TODO 时，必须同步代码 TODO、TODO register、章节 PLAN、章节 STATUS；若是上线阻断，同步 RELEASE_BLOCKERS。

## AI 写入权限

生产代码：

- 没有得到用户明确允许，不得私自修改生产代码。
- 默认由用户实现生产代码，AI 负责 review、解释、指出风险。
- 只有用户明确要求时，AI 才直接修改生产代码。

测试代码：

- 用户已授权：测试代码由 AI 直接编写、运行和修复。
- 不要求用户手动补测试。

文档：

- 用户要求整理或更新文档时，AI 可以直接修改对应文档。
- 文档修改必须保持职责边界，不把动态进度写入 `AGENTS.md`。

## 核心业务边界

- Gateway：请求编排层。
- Provider：业务服务商，例如 OpenAI、Anthropic、Gemini。
- ProviderEndpoint：某个 provider 下的唯一 API Root 与公共故障域，持有规范化 BaseURL。
- Channel：某个 ProviderEndpoint 下的一组账号级上游渠道事实，包含凭据、协议、adapter、优先级、运行状态、模型映射和价格策略，不持有 BaseURL。
- Adapter：纯代码能力，只做协议转换、请求发送、响应解析、stream parser、usage/error 映射。
- `channel.Runtime`：gateway/routing 选中 channel 后传给 adapter 的运行时参数，不等于数据库里的 channel 业务实体。

硬规则：

- Adapter 不读取 env，不查数据库，不保存业务状态。
- Gateway/routing 负责选择 model、provider、endpoint、channel，并把运行时 channel 参数交给 adapter。
- Provider、endpoint、channel、model、price、fallback、health、rate policy 属于业务数据，最终必须进数据库并由后台管理，不能设计成正式 env/config 来源。
- Billing/ledger 必须记录 request、model、provider、channel、price snapshot、cost snapshot 和 usage。
- 金额、余额、token 计费数据不要使用 float。
- Redis 不能作为金额或余额的最终事实来源。
- 公开 Gateway endpoint 不能直接透传上游原始错误 body。
- customer API key、admin auth、console auth 不能混用。

详细商业语义以 [docs/production/DECISIONS.md](docs/production/DECISIONS.md) 为准。

## 错误与安全

- 跨模块传播、日志记录、request/attempt error_code、retry/fallback 判断依赖的错误，必须使用 `internal/platform/failure`。
- `failure.Code` 是内部稳定错误身份，格式为 `<category>_<reason>`。
- 日志记录错误时使用 `failure.LogArgs(err)`。
- HTTP 层必须把内部 failure 映射为安全的协议原生错误响应，不能把 `err.Error()` 直接暴露给用户。
- 不要把 API key、credential、上游原始 body、SQL 细节、用户 prompt、完整响应正文放进日志、错误 fields 或默认审计表。

## 协议与 Adapter

- 对外维护 OpenAI Chat Completions 与 Anthropic Messages 两个独立协议族。
- OpenAI ingress 只命中 OpenAI channel，Anthropic ingress 只命中 Anthropic channel；禁止隐式跨协议 bridge。
- Adapter 一次调用只允许发起一次真实 upstream HTTP 请求；retry 和 fallback 归 lifecycle 管理。
- 生产 adapter 不使用官方 Go SDK；官方 SDK 只用于黑盒验收。
- ingress 禁止 silent drop；合法但 provider 无法转换的字段按已接受决策在 adapter 出站 Drop，并记录内部审计。
- 账务、settlement、recovery 只消费 adapter 同次解析产生的 `ResponseFacts` / `usage.Facts`，不反向解析公开响应。

详细双协议规则以 [docs/chapters/phase-10-dual-protocol-gateway/](docs/chapters/phase-10-dual-protocol-gateway/) 为准。

## Migration 与 SQL

Migration：

- `migrations/` 按“一张表一组 up/down 文件”组织，文件直接平铺。
- 一个表 migration 只能创建、约束、索引和删除当前表。
- 与该表相关的 index 必须放在该表自己的 `.up.sql` 文件中。
- 开发阶段字段或约束变更直接修改源 migration。
- 每个表和字段必须有业务注释。
- 改表 migration 后，必须先对当前本地库执行 down/drop，再修改文件，最后 up 验证。
- `sqlc.yaml` 的 schema 路径只读取 `migrations/*.up.sql`。

SQL query：

- `sql/queries` 按“一张表一个文件”组织。
- 每个 query 文件只放该表作为主表的查询。
- `-- name:` 下一行必须写以生成 Go 方法名开头的方法注释。
- 重整 query 后必须执行 `sqlc generate`，删除旧生成物，并运行测试。

## 编码风格

- 先定业务边界，再写实现。
- 代码保持直接、清晰、可审计，不为了“通用”提前抽象。
- 新增抽象必须能减少真实重复、隔离稳定边界或降低调用方复杂度。
- 遵循现有包结构、命名、错误处理和测试 helper，不引入局部异质风格。
- 业务核心避免隐式行为；状态迁移、金额计算、credential 处理、审计事实必须显式表达。
- HTTP 层只处理协议、认证、DTO、错误渲染和调用 service。
- service 层负责业务编排、事务、状态推进、审计和跨 core 协作。
- core 层表达稳定业务能力和领域事实，不依赖 app 层 DTO 或 sqlc row 暴露给外部。
- platform 层只放基础设施能力，不反向引用业务层。
- 接口放在文件前部类型声明区；结构体定义后紧跟构造函数。
- 方法专属参数结构体紧贴对应方法实现。
- 同一个结构体的方法默认不要拆散到多个文件。
- HTTP 请求/响应使用 DTO；Provider 请求/响应使用独立 DTO；数据库 row 不直接暴露为 API response。
- optional scalar 字段需要保留显式零值时使用指针类型。

## 测试规则

- 默认先完成可运行的完整行为，再在模块或阶段收口时补测试。
- 高风险 bug、安全、计费、事务、stream、credential、状态机边界必须及时补测试。
- 编写测试前必须只读检查已有 helper、依赖装配和同类测试写法。
- 测试断言优先覆盖稳定事实、状态、错误码和审计结果，不依赖脆弱字符串。
- 涉及新外部接口、新第三方库、新协议细节、复杂并发、事务或流式处理时，先用独立验证代码确认行为。
- 验证代码必须与生产代码分离，不能保存真实密钥，不能写入生产数据库，不能依赖生产计费或真实余额。

## 第三方库

- 商业项目不以“少用第三方库”为目标。
- billing、ledger、routing、settlement、request/attempt 状态机、price snapshot、adapter contract 等业务核心自己写，保证可审计。
- migration runner、JWT、argon2id、Prometheus、OpenTelemetry、decimal、UUID/ULID、SSE parser、Redis Lua/限流组件优先评估成熟库。
- 新增依赖前必须检查 license、维护状态、失败模式、测试替身能力，以及是否会把第三方类型泄漏到核心业务边界。
- 具体规则见 [docs/production/THIRD_PARTY_POLICY.md](docs/production/THIRD_PARTY_POLICY.md)。

## 回答风格

- 默认使用中文。
- 直接、实用、清楚、技术严谨。
- 不拍马屁，不过度复杂化。
- 涉及取舍时说明原因。
- 能给建议时给明确建议。

## Git 提交信息

中文提交信息格式：

```text
<type>(<scope>): <subject>

<body>

<footer>
```

`type` 必须是以下之一：

```text
feat / fix / docs / style / refactor / test / chore
```

规则：

- `scope` 可选，表示影响范围。
- `subject` 一行总结，不超过 50 字符，不以句号结尾。
- `body` 可选，描述变更原因和细节，每行不超过 72 字符。
- `footer` 可选，用于标注 BREAKING CHANGE 或 issue。
