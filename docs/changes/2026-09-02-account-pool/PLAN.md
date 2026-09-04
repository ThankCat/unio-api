# 订阅账号号池改造 — Gateway + Admin

> 蓝图依据：`unio-blueprint/docs/architecture/account-pool.md`（2026-09-02 定稿，全部议题已收敛）。
> wire 证据：`unio-gateway/sandbox/codex/wire/samples/*.json`（真实抓包脱敏样例，实现时逐字段对照）。
> 前提：开发环境；仅新增表与列，不动存量数据；现有 credential 型渠道行为必须逐字节不变。

## 进度总览（2026-09-02 晚）

| 批次 | 内容 | 状态 | 提交 |
| --- | --- | --- | --- |
| 一 | 数据库结构与查询 | ✅ | bdf04d2 / 2982989 |
| 二 | 账号级原子准入与运行态 control | ✅ | f7394f0 |
| 三 | 候选资格与池内选号 | ✅ | 05ef2dc |
| 四 | 换号重试 / Sticky 账号 / 会话键兜底 / trace | ✅ | d31c384 |
| 五/六 | Codex adapter / 归因分层 / 保活与导入 | ✅ | bbcb8e9 |
| 七/九A | 账号快照进账务 / Admin API / 供给形态 | ✅ | 636d072 |
| 十 | E2E 工具链与真实账号验证 | ✅ | 395330a |
| 八 | Chat→Responses 反向桥接 | ✅ | 4ef81fc |
| 九B | unio-admin 前端（账号页签 + 表单） | ✅ | unio-admin d1e960d |
| 收口 | Fast 结算例外 / 经营视图 / 供给预览 / 监测页 / 阈值热更 / CLI 实测（2026-09-03） | ✅ | 待提交 |
| — | **剩余未做**（仅 ADR 文档批次 + 429 真实样本） | | |

剩余未做清单（2026-09-03 收口批次后）：
1. **文档收口**（第十一节）：Blueprint ADR 批次（1 新增 + 6 修订 + Admin 设计文档）——实现已全部定型，
   可以开写；
2. **真实 429 样本**：需要把真实账号的 5h 窗口打满才能触发，刻意刷爆用户账号不可接受，
   等自然发生后回写蓝图证据等级（错误体判据维持 Sub2API 口径）。

以下原清单项已全部完成（见各节补章）：Fast 结算例外（边界 15，含 CHECK 约束迁移 000075）、
零成本毛利口径排查（边界 2，结论：口径成立无需改码）、缓存画像账号口径（边界 10，迁移 000074）、
号池并发监测页、账号下钻/按账号筛选、装配顺序提示、完整供给影响预览（边界 11 接 ADR-0020 预览）、
操作审计核查（边界 23，结论：无统一审计机制可接，可追溯性由 runtime_control_operations +
config_revision + 台账 created_by 承担，统一审计属平台级另立项）、用量暂停阈值 appsettings 热更新、
**codex CLI 真实端到端**（CLI 0.152.1 → 网关 → 池 → 真实上游，完整会话含 token 统计；
实测抓出并修复结算 CHECK 约束漏改的严重 bug）。

## 已确认决策（本计划的边界）

- **Channel 增加供给形态**：`credential`（现状，持单份 API Key）/ `pool`（持一池订阅账号）。池型渠道
  自身不持凭据，协议、adapter、模型绑定、定价、超时仍在 Channel 层。
- **账号恰好归属一个 Channel（1:N）**，不跨 Channel 复用、不引入独立池实体；同一套餐档位建一个池
  （池内同质性是评分与容量聚合的前提）。订阅上游用独立 Provider（origin 与故障域不同于 API 上游）。
- **路由两阶段**：Channel 层五项评分与确定性排序不动，池的并发容量分取全池空闲槽位聚合；池内第二阶段
  选号（Sticky 优先 → 过滤 → 优先级 → 负载率 → LRU → **同档随机打散**，明确不沿用 Channel 层「同分取
  更小 ID」的确定性决胜）。
- **Redis 故障保持 fail-closed**，不采用 Sub2API 的 fail-open。
- **Sticky 是一条绑定**，绑定值从 `(channel_id, binding_version)` 扩为含 `account_id`；CAS 三操作语义不变。
- **健康反馈分层**：429/401/风控归账号，5xx/超时/网络归 Provider/Channel breaker；用量达阈值提前移出调度。
- **生命周期用绑定式语义**：账号 status 只表达自身启停，可调度性 = 账号 ∧ Channel ∧ Provider 三层
  enabled；归档保持严格顺序，恢复统一落 disabled。
- **账务**：池型渠道配渠道级成本倍率 0，结算快照与毛利守卫机制不动；新增账号维度订阅台账；请求记录
  带 `account_id`，摊销与分摊离线计算。
- **Adapter**：OpenAI 协议族新增 `adapter_key = codex`，与 `openai`/`deepseek` 平级，只注册 responses
  四槽；账号维度经 `channel.Runtime` 注入，adapter 对号池无感知。
- **Chat Completions→Responses 反向桥接必做**（负责人拍板），chat 请求候选资格同步放开为
  `HasChat || HasResponses`。
- 本次不做：影子账号、按账号计费倍率、代理自动轮换、池内退避排队、`load_factor`、指纹伪装 enrichment、
  反代账号类型、WebSocket 传输、账号级 RPM/会话数上限。

## 官方文档核对（2026-09-02，动工前完成）

抓包只能证明「这一个账号在这一刻的行为」，枚举的完整取值必须查官方。核对结果如下，**均已回改到下文
对应条目**：

| 项 | 核对结论 | 对计划的影响 |
| --- | --- | --- |
| `plan_type` 取值 | 官方档位为 Free / Go / Plus / Pro（5x 与 20x 两档同名 pro）/ Business（旧名 Team，两者都可能出现）/ Enterprise / Edu / ChatGPT for Teachers。抓包账号为 `plus`，Sub2API 代码认识 free/plus/pro/team/enterprise | **`plan_type` 不加 CHECK 约束**，按自由文本存储 + 应用层白名单软校验；「同套餐一池」按实际字符串比对，不枚举 |
| 5h 窗口是否恒存在 | **Business Premium 座位「无五小时限制」**；Enterprise/Edu 弹性定价「无固定速率限制，随额度伸缩」 | 用量快照的 5h 窗口**可能缺失**，调度过滤必须容忍 `primary` 为空，不得默认按 0% 处理 |
| 窗口语义 | 5h 为滚动窗口持续刷新；7d 为**自首次使用起算的滚动周期**，非自然周 | 与实测 `reset_at` 绝对时间戳一致，沿用实测值 |
| 配额重置 | 2026-06 起支持**付费即时重置与可储存重置**（Plus/Pro），重置会把 `reset_at` **提前**并重启周期 | 用量恢复逻辑不能只处理「`reset_at` 到期」，必须处理 **`reset_at` 变小（提前重置）** 的情况，否则账号会被错误地继续暂停 |
| `service_tier` 取值 | 官方为 `auto` / `default` / `flex` / `priority`（2026-07-30 起**改名 fast**，两个值都接受）/ `scale`（企业遗留）。**现有模型响应仍回 `priority`；gpt-5.6 之后发布的模型响应改回 `fast`** | Fast 档位解析必须把 `priority` 与 `fast` **视为同一档**，否则新模型上线时会把 Fast 误判成 Standard。这是对现有 Fast 实现的修正，**不限于号池** |
| Codex 模型的 Fast 声明 | 抓包清单实测：`service_tiers: [{id:"priority", name:"Fast"}]`、`additional_speed_tiers: ["fast"]` | 两个名字在同一份响应里并存，印证上条 |
| reasoning 档位 | 抓包清单实测（上游自报）：`low` / `medium` / `high` / `xhigh` / `max` / `ultra`，多于标准 API 的档位集 | 透传即可，不要按标准 API 档位集做白名单校验 |
| 客户接入配置 | 官方参考确认：`wire_api` 默认且**唯一**取值 `responses`；`env_key` 提供 API Key；`requires_openai_auth` 默认 false。与沙箱实测一致 | 客户接入文档按此写 |
| **`wire_api = "chat"` 已移除** | Chat Completions 协议于 2025-12 弃用、**2026-02 从 Codex CLI 彻底移除**，设置该值会在启动时报错 | **Codex CLI 客户不可能向我们发 Chat Completions**。反向桥接的受益方不是 Codex CLI，而是通用 chat 客户端（OpenAI SDK、LangChain、第三方应用）。桥接照做，但**优先级排在 Codex 主链路之后**，且验收场景要改成通用 chat 客户端而非 codex CLI |

## 证据来源纪律

蓝图每条结论都标了来源等级。实现时：**【实测】**可直接照做；**【Sub2API】**必须与真实响应复核后再落地，
取得样本后回写蓝图升级标记并用 `sandbox/codex/scripts/extract-samples.py` 固化证据。已知 Sub2API 两处
与真实行为不符：用量窗口 primary/secondary 方向弄反、用量头集合不全。

---

## 一、数据库（unio-gateway）

- [x] 迁移 `000068_channel_supply_form`：`channels` 增列
  - `supply_form text NOT NULL DEFAULT 'credential'`，`CHECK (supply_form IN ('credential','pool'))`；
  - `account_default_concurrency integer`（池型的账号默认并发；NULL 继承全局，0 不限，正数上限）；
  - 约束：`supply_form='pool'` 时 `credential` 必须为空串；`supply_form='credential'` 时保持非空语义。
  - 存量行全部落 `credential`，行为不变。

- [x] 迁移 `000069_subscription_accounts`：

  ```sql
  CREATE TABLE public.subscription_accounts (
      id                  bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
      channel_id          bigint NOT NULL REFERENCES channels(id),
      platform            text   NOT NULL,              -- openai / anthropic / gemini（本期只实现 openai）
      credential_type     text   NOT NULL,              -- oauth（本期唯一取值，CHECK 约束）
      upstream_account_id text   NOT NULL,              -- 上游账号标识（Codex 的 chatgpt_account_id）
      display_name        text   NOT NULL,              -- 运营可读名（邮箱或备注，不作唯一键）
      plan_type           text,                         -- free/go/plus/pro/business(team)/enterprise/edu；
                                                    -- 官方档位会增改，**不加 CHECK**，应用层白名单软校验
      credentials         jsonb  NOT NULL,              -- access/refresh token、过期时间、client_id 等
      proxy_url           text,                         -- 账号绑定出口；NULL 表示直连
      concurrency_limit   integer,                      -- NULL 继承渠道默认，0 不限，正数上限
      priority            integer NOT NULL DEFAULT 50,  -- 池内选号优先级，越小越靠前
      status              text   NOT NULL,              -- enabled / disabled / archived
      disabled_reason     text,                         -- 受控值：manual / token_revoked / risk_control
      subscription_expires_at timestamptz,              -- 订阅到期（与令牌过期是两回事）
      usage_snapshot      jsonb,                        -- 5h/7d 水位与窗口重置时间（绝对时间戳）+ 采集时间；
                                                    -- Business Premium 无 5h 窗口、Enterprise/Edu 弹性额度无固定限，
                                                    -- 故任一窗口都可能缺失，消费方必须容忍空值
      last_success_at     timestamptz,
      config_revision     bigint NOT NULL DEFAULT 1,    -- 账号配置真变化时 +1（见第三节热更新）
      created_at          timestamptz NOT NULL DEFAULT now(),
      updated_at          timestamptz NOT NULL DEFAULT now()
  );
  -- 唯一：(platform, upstream_account_id) 全局唯一（边界 21，重复导入拒绝）
  -- 索引：(channel_id, status)、subscription_expires_at
  ```

  凭据存储沿用渠道凭据的明文口径（边界 22：要加密应连渠道凭据一起做，不单独为账号开新口径）。

- [x] 迁移 `000070_subscription_ledger`：订阅台账（账号维度，周期、金额、币种、生效区间、备注），
      用于离线算摊销单价与利用率。

- [x] 迁移 `000071_request_records_account`：`request_records` 增 `account_id bigint`（可空，快照列，
      不加外键约束以免影响归档），索引 `(account_id, created_at DESC)`。

- [x] sqlc 查询：
  - `sql/queries/gateway/subscription_accounts.sql`：`ListSchedulableAccountsByChannel :many`
    （候选快照用，只取调度必需列）、`GetAccountCredentials :one`、`UpdateAccountTokens :exec`、
    `UpdateAccountUsageSnapshot :exec`、`TouchAccountLastSuccess :exec`。
  - `sql/queries/admin/subscription_accounts.sql`：CRUD、列表（含聚合计数）、状态流转、台账读写。
  - `sqlc generate` 后提交生成代码。

- [x] 候选查询：现有候选 SQL 增加 `supply_form` 与 `account_default_concurrency`，并对池型渠道要求
      「至少一个 enabled 账号」才产出候选（零账号池允许存在但不成为候选，边界 12）。
      同时放宽 `buildChatRouteCandidate` 的凭据非空校验——池型渠道凭据恒为空串（DB CHECK 保证），
      供给单元换成了账号。

> **批次一记录的两个既有失败已定位并修复（批次三）**：`TestFindModelCandidatesFiltersByIngressProtocol`
> 与 `TestFindModelCandidatesOrdersAndFilters` 返回 0 候选，与本地 dev 库的种子数据无关——
> 是 `000065_provider_recharge_rates`（2026-08-31）给候选查询加了 D-02 严格拦截
> （`recharge.id IS NOT NULL`）之后，这两个用例的 fixture 从未补上 `provider_recharge_rates` 行。
> 修法是给测试加 `createProviderRechargeRateForTest` 并在两处调用（纯测试改动）。
> 这条对后续有约束：**任何新增的候选用例都必须给 provider 配一条当前生效充值汇率**，否则恒为 0 候选。

## 二、运行态与原子准入（unio-gateway）

- [x] `internal/platform/breakerstore`：账号并发槽
  - 新键：账号并发 ZSET（成员为 permit id，分数为 Redis 服务端时间戳），TTL 自愈，沿用现有渠道并发槽
    的键命名与清理模式；
  - `lua/ops/gate_and_acquire.lua` 扩展：新增账号维度入参（account_id、账号并发上限、账号 revision），
    在现有 Channel 门禁（breaker / cooldown / permission / revision / 渠道并发）之后、创建 permit 之前，
    **同一次原子调用内**校验账号冷却与账号槽位并占位；
  - 账号槽满返回可区分的 denial 原因（`account_concurrency_full`），供路由换号重试；
  - `finish.lua` / `abort.lua` 同步释放账号槽；异常残留由 TTL 回收。
  - 现有 credential 型渠道传空账号维度，脚本走原路径，行为逐字节不变（用现有测试守住）。
  - **计划外补的两处**：① `renew.lua` 也必须续期账号槽——原计划只列了 finish/abort，漏掉续期会让长流
    请求的账号租约先于渠道租约到期，在途数被系统性少算、账号并发上限形同虚设，且全程不报错；
    ② acquire 写阶段对账号 ZSET 补 `ZREMRANGEBYSCORE`，与渠道槽同样清理过期成员，否则热点账号的
    zset 只涨不减。
  - 账号身份（`concurrency_account_id`）与渠道身份同权进入 permit guard：认错账号的收口一律拒绝，
    且零资源变化。`account_config_revision` **只固化不设围栏**——改一次账号并发就掐断全部在途请求
    不可接受，新配置从下一次 acquire 起生效。

- [x] 账号运行态 control（Redis）：`SetAccountCooldown` / `MarkAccountUnschedulable` /
      `PauseAccountUsage` 三写 + 对应 clear，`AccountRuntimeMany` 批量读（供候选快照聚合空闲槽位）。
      三种状态共用一个 hash 且只由一个 Lua 脚本维护，键 TTL 取三者最晚到期——按单个状态各自 PEXPIRE
      会把另一个状态提前抹掉。
  - **与渠道 429 冷却的语义差异（实现时才发现，需回写蓝图）**：渠道冷却只增不减，账号冷却按最近一次
    观测**覆盖**。理由就是官方核对表里的那条——付费即时重置会让 `reset_at` 变小，只增不减会把配额
    已恢复的账号继续锁住数小时。用量暂停同理。本地判定的临时隔离仍取最晚到期（叠加语义）。
  - 「疑似代理故障」不单列状态，落为 `unschedulable_reason = proxy_suspect`，与
    `token_refresh` / `manual` 并列，运维界面按 reason 出徽章。
  - **用量暂停是路由过滤链上的一条，不是准入硬门槛**：已选中的请求继续放行，只是下一轮不再选它。
    这样阈值判断的时效性问题最多多发一个请求，而不会把请求打回客户。第三节过滤链据此实现。

- [x] **账号配置热更新传播**（边界 20）的平台侧原语：新增 `BumpChannelCapacityRevision`（CAS 推进
      `channels.capacity_revision`，不要求渠道并发真变化）。既有 `CommitChannelCapacityAtRevision`
      带 `concurrency_limit IS DISTINCT FROM` 条件，账号改动时渠道并发没变，用它推不动版本。
  - **服务层调用顺延至第九节**：账号写入须与 `BumpChannelCapacityRevision` 同事务，并经
    `runtimecontrol.Publisher` 两阶段发布，与渠道容量编辑同一条路径。账号管理服务尚不存在，
    在这里补会先造出一个没有调用方的编排层。

> **批次二交叉验证结论**：`go build` / `go vet` / `gofmt` 干净；真实 Redis 全量 `go test ./...` 通过，
> 带 `DATABASE_URL` 时失败集合与批次一记录的两个既有失败完全一致，未引入新失败。
> **`make check-lua` 在 HEAD 上就是失败的**（用 detached worktree 对照确认）：luacheck 5 warnings
> （2 处超长代码行 + 3 处未使用局部/参数，分布在 acquire / finish / observe_channels / snapshot_many）、
> stylua 8 个文件有格式差异。本批次新增的两个脚本 luacheck 与 stylua 全清，警告数与格式差异数一个没涨。
> 这道检查在修好之前无法当门禁用，后续批次仍以「警告数不增」为准；要修的话应单独一个 `style(lua)` 提交。

## 三、路由与选号（unio-gateway）

- [x] 候选快照对池型渠道额外拉取账号列表（Postgres）与账号运行态（Redis），产出两项：
  - 资格：至少一个可调度账号；全部被运行态挡住 → 候选按 cooldown 排除，`Retry-After` 取最早恢复时间；
  - 并发容量分输入：全池空闲槽位聚合 / 上限聚合（五项评分公式与权重**不变**，只换容量事实的来源）。
  - 落点是 `lifecycle`（`account_pool.go`）而非 `internal/core/routing`：routing 只做 SQL → 候选的映射，
    运行态读取与资格判定一直在 `Executor.PrepareCandidates`，放 routing 会把 Redis 依赖倒灌进 core。
  - 三种排除原因分开表达，不能合并：`account_pool_cooldown`（到点自愈，可给 Retry-After）/
    `account_pool_empty`（两次查询之间账号被停用）/ `account_pool_unavailable`（账号事实读不出来，
    与 Redis 运行态同样 fail-closed）。
  - **未注入账号读取器时池型候选一律排除**：既有装配与单测因此零改动，且不会出现「放行一个没有凭据的候选」。

- [x] 池内选号（`internal/core/accountpool`，纯函数 + `lifecycle/account_selection.go` 接线）：
  1. Sticky 绑定账号优先（`Order` 已支持 sticky 置顶，**调用方本期恒传 0**，见第四点顺延说明）；
  2. 过滤链：账号 enabled（SQL 保证）、不在冷却、未临时不可调度、用量未达阈值；
     Channel ∧ Provider 两层由候选查询的上层条件保证（绑定式语义）；
  3. 排序：priority → 实时负载率 → 最久未使用（LRU）；**同档随机打散**——实现为「先整体打散再稳定排序」，
     等价键之间保留的就是随机顺序，比排序后逐段打散少一层易错的分段逻辑；
  4. 按序逐号 `AttemptPermit`；账号槽满/账号冷却取下一个，**渠道级拒绝立即返回**（换号得到同一答案）；
  5. 全部账号满 → 该 Channel 记 `concurrency_full` 跳下一候选（进入既有全池短等语义）。
  - 混合拒绝映射（边界 5）已实现并有测试：池内存在「仅并发满」的账号即报 `concurrency_full`（可等待），
    全部因冷却/临时不可调度才报 cooldown 并带最早恢复时刻。
  - **单请求内单池最多试 5 个账号**（`accountAttemptBudget`）：池可能挂几十个号，逐个试到底会把一次
    请求的准入耗时放大成 N 次 Redis 往返；试过几个都没槽，换渠道比继续换号更快。
  - **并发上限回落链末端定为「不限」而非全局 `channel_limit`**（与批次二注释里的措辞不同，以此处为准）：
    那个全局默认限的是整条渠道，拿它去限每个账号会把「渠道最多 10 个在途」悄悄变成「每号各 10 个」。
    渠道级上限在 Lua 门禁里仍先于账号槽生效，池整体不失保护。要不要单开一个账号级全局默认，
    等运营真的需要时再加，不先造一个没人配的旋钮。

- [x] **同渠道换号重试**（边界 6，修改 ADR-0016 既有契约）：两半都已落地（批次三/四）。
  - 准入阶段：账号槽满/冷却 → 池内取下一个号；「同账号绝不重复」由请求级 `attemptedAccounts` 保证。
  - 传输失败后：实现方式是**候选列表预展开**（`expandPoolRetryCandidates`）——池型候选原位连续复制到
    传输预算次数（`poolChannelTransportBudget = 2`，1 原始 + 1 换号重试），`attemptedChannels` 布尔表
    换成 `channelTransports` 计数表。A(a1) 失败且可重试时，紧随其后的重试位以不同账号再试 A，然后才轮到
    B——A(a1)→A(a2)→B 成立，而 A→B→A 仍被禁止。credential 型预算恒 1，行为逐字节不变。
    没有用「循环内嵌重试环」的写法：两个 460+ 行候选循环里的裸 `continue` 语义会全部改变，预展开是
    改动面最小、且天然被现有扫描语义覆盖的做法。测试冻结了传输顺序与预算上限。
  - 池内试尽（本请求把可调度账号都试过）合成专属拒因 `account_pool_exhausted`：runner 跳过且**不进
    拒绝汇总**——每个号的真实拒因首次尝试时已记录，二次折叠会把「全员冷却应报 429」污染成 503。

- [x] Sticky（`internal/platform/stickysession`）：绑定值 schema 升 v2 增加 `account_id`，三个 CAS 脚本
      齐比 `(channel_id, account_id, binding_version)`；键空间从 `sticky:` 换到 `sticky:v2:`，
      旧绑定自然 miss（TTL 30 分钟，无需迁移）。真实 Redis CAS 契约测试补齐账号分量（含 credential 型
      account_id=0 的零分量行为冻结）。
  - `BindSuccessWithAccount`：同渠道换号成功 = 账号级临时绕行（保留原绑定不续期），与「绕到别的渠道」
    同一语义；真正的账号改绑只能来自确认失效后的清绑 + 新建。`BindSuccess` 保持原签名委托账号 0，
    全部既有调用点与测试零改动。
  - 运行时接线：runner 从 `params.Sticky.BoundAccountIDFor(channelID)` 取绑定账号传进池内选号置顶；
    成功后把 `permitAccount.ID` 写进绑定值。
  - `stickyTemporaryBypassReason` 认 `account_pool_cooldown` 为临时绕行——池内账号全部冷却与渠道
    429 冷却同属到点自愈，都不该清绑定。

- [x] 会话键提取（`internal/core/sessionhint`）：`ContentDerivedHint` 内容派生哈希兜底已落地并接入
      OpenAI 族四个调用点（chat 非流/流式取前两条消息，responses 非流/流式取 instructions + input 原文）。
      三条边界照办：只哈希 2KB 有界前缀、返回值即 SHA-256 哈希不落原文、来源标 `content_hash`。
      Anthropic 族未接兜底（Claude Code 的显式信号覆盖率足够，反向桥接受益方是 OpenAI chat 客户端）。
      E2E 实测：无显式信号的 chat 请求 sticky 以 `content_hash` 来源建绑（日志可查）。

- [x] 缓存画像口径（边界 10）：「池内换号」与「跨渠道切换」同口径排除出渠道缓存画像。
  迁移 000074 给 trace 增 `sticky_before_account_id`（规划开始时绑定的账号分量），StickyAudit/
  trace 写入链路补齐；`ProviderOpsChannels` 缓存 CTE 的排除条件扩为「渠道不同 OR 账号不同」
  （COALESCE 写法保证 credential 型 final_account_id 为 NULL 时不触发排除）。

- [x] Routing trace：只记实际尝试过的账号与池内选号事实，不记全池逐号评分（边界 14）。
  - 迁移 `000072`：`routing_decision_traces` 增 `attempted_account_ids bigint[]`（真实传输过的账号）与
    `selected_account_id`（部分索引），upsert 同步更新；`AcquireOutcome.TriedAccountIDs` 记每次扫描
    真实向 Redis 发起过 acquire 的账号，进 trace_payload.acquire_results。
  - **顺带修正池型渠道的选路诊断**：`ModelRuntimePool` 的 `has_credential = credential <> ''` 会把
    池型渠道诊断成 `credential_missing`。诊断增加 `supply_form` 与 `has_schedulable_account`，
    池型的排除原因改为 `account_pool_empty`（池空 ≠ 缺凭据 ≠ 熔断，三个事实各自可见）。

> **批次三交叉验证结论**：`go build` / `go vet` 干净；改动文件 `gofmt` 全清
> （`internal/bootstrap/gateway_server.go` 在 HEAD 上就未格式化，已用 stash 对照确认，未顺手改）。
> 带 `REDIS_ADDR` + `DATABASE_URL` 的全量 `go test ./...` **108 个包全绿**——批次一、二记录的两个
> 既有失败随本批次的 fixture 修复一并消失，失败集合现在是空的。后续批次可以直接用「全绿」当门禁。
> `make check-lua` 未跑：本批次没有改 Lua，其既有失败状态与批次二记录一致。
>
> **本批次没做、留给下一批的**：传输失败后的同渠道换号、Sticky 账号维度、会话键兜底、缓存画像口径、
> routing trace 账号事实。这五项都要动同两个候选循环或 sticky 绑定值，合并成一个批次比拆开安全。

## 四、Codex adapter（unio-gateway）

- [x] Codex adapter 落地为 **base responses adapter 的 Wire 钩子装配**，不是复制实现
      （与计划的「新 adapter 包」有实现取向差异，已记录）：
  - `internal/core/adapter/openai/responses/wire.go` 新增 `Wire` 钩子（路径覆盖 / 请求头装饰 /
    出站前守卫 / 按账号 client / 响应头事实 / wire 专属 Retry-After），**零值 Wire = 官方现状**，
    credential 渠道路径逐字节不变；协议解析、SSE 循环、超时与错误分类一处不复制。
  - `internal/core/adapter/openai/codex/responses/` 只做装配与 codex 专属逻辑：
    路径 `/backend-api/codex/responses`（compact 子路径同前缀）；模型清单 lister 走
    `GET /backend-api/codex/models?client_version=<ver>`（models[] + slug 形态，无分页；
    ETag 条件请求未做——发现是低频操作，留给后续优化）；
    `chatgpt-account-id` / `originator` / `User-Agent` / `version` 按账号收敛（全局固定一组值即满足
    收敛，边界 30 明确不做指纹伪装 enrichment）；防御性 `Del("x-codex-turn-state")`；
    `previous_response_id` 出站前拦截为 400 语义的上游错误（与上游行为一致且不消耗账号出站）；
    `x-codex-*` 用量头解析进 `adapter.ResponseFacts.AccountUsage`（primary=5h、secondary=7d，
    有单测冻结方向）；429 的 `x-codex-primary-reset-after-seconds`/`reset-at` 折算进
    metadata.RetryAfter 供账号冷却取时。`reasoning.effort` 天然透传（直传 body 不做白名单）。
  - 注册 `Key: "codex"` 只登记 responses 四槽 + compact，不登记 chat 三槽。
  - `safety_identifier` 回传客户：**与计划有偏差**——直传路径零转换透传响应体，剥它需要逐事件改写；
    风险是客户可见一个上游侧标识，不构成串号（turn-state 头从不透传）。留给后续单独决定。

- [x] **按账号代理**（边界 29）：`channel.Runtime.Account`（ID / UpstreamAccountID / ProxyURL）+
      `internal/platform/proxyclient`（按代理 URL 缓存 `*http.Client`）。导入换码、令牌刷新、正式请求
      三条路径都经同一解析器注入；adapter 不自管 client 池。
      账号凭据在 permit 固化后由 `lifecycle.applyAccountOutbound` 注入（APIKey=access token），
      解析失败归还 permit 换下一个候选（`account_credentials_unavailable`）。

- [x] **Fast 档位**（边界 15）：「响应档位权威性」例外已落地——responses Wire 新增 `FinalizeFacts`
      钩子（非流式/流式终态/compact 三处统一收尾），codex wire 按**出站请求体的 service_tier** 定档
      （`servicetier.ResolveWireOutboundAuthoritative`，新 resolution `wire_outbound_authoritative`，
      响应原始值保留在 UpstreamRaw 供审计）；settlement 侧零改动（既有 `Actual==Fast` 门 + Fast 价格
      已配置闸门照常生效，未配置回落 Standard 少收不多收）。迁移 000075 放宽 request_records 与
      settlement_recovery_jobs 的 resolution CHECK。**该约束漏改曾导致池型请求结算全挂
      （流在 response.completed 前被切断）——由 codex CLI 真实实测抓出，教训：新增受控值必查全部 CHECK。**
      单测冻结：出站 priority/fast → Fast、缺省/default → Standard、坏 body 不覆写。
- [x] **`priority` 与 `fast` 归一为同一档（存量修正，不限号池）**：`servicetier.ResolveOpenAIResponse`
      的 `case "priority", "fast"` 已合并，gpt-5.6 后新模型回 `fast` 不再被误判成 Standard。
      请求侧 `NormalizeOpenAIRequest` 本就接受两值，无需改动。

## 五、健康反馈与归因（unio-gateway）

- [x] 归因分层改造（修订 ADR-0014）——集中在 permit owner 的 `recordAccountRuntimeFeedback`
      （`lifecycle/account_feedback.go`），permit 带账号即整体切到账号归因分支：
  - 429 → 账号冷却（时长取 codex wire 折算进 metadata.RetryAfter 的重置头，缺失用渠道 429 策略的
    秒级兜底）；不喂渠道 breaker、不写渠道冷却；
  - 401/403 → 账号临时不可调度 5 分钟（token_refresh 窗口）；**确认吊销才禁用**由刷新路径完成
    （见第六节）。存量修正同步落地：`classifyChannelScoringSample` 对账号维度传输的 401/403
    从渠道错误率样本整体排除（边界 4），`RecordCredentialResult` 对池型渠道跳过——单号令牌失效
    不得翻渠道 `credential_valid`；
  - 5xx / 超时 / 网络 → 照旧走 Finish outcome 归 Provider/Channel breaker（未动）；
  - 持久/瞬态二分（边界 16，已按 Go net 语义复核而非照抄 Sub2API）：DNS 失败、ECONNREFUSED、
    EHOSTUNREACH/ENETUNREACH、proxyconnect/407 → proxy_suspect 隔离 10 分钟；
    超时/重置/EOF（以及一切拿到了响应头的错误）→ 只换号不处置账号；
  - 用量自动暂停（`service/subscription/health`）：成功传输后消费 `facts.AccountUsage`，
    快照落库 + LRU touch + 阈值暂停（默认 90%；暂停用覆盖语义，reset_at 提前即刻缩短；
    低于阈值显式 Resume，不等旧暂停自然到期）；任一窗口缺失按不限处理。
    阈值已接 appsettings 热更新（`gateway.account_usage_pause_threshold_percent`，默认 90，1~100，
    每次用量观测现读设置快照）；构造参数保留为降级默认。
  - **E2E 实证（真实账号）**：上游真 401（token_revoked）→ 账号隔离 token_refresh 5 分钟、渠道评分
    与 breaker 零污染、同请求透明 fallback 到 credential 渠道成功结算；随后手动刷新确认吊销 →
    账号 disabled(token_revoked)。95% 用量头 → 自动暂停 → 下一请求池被过滤，全链路可复现。

- [x] half-open 探测经池内选号（边界 8）：天然成立——探测请求与普通请求同一条
      `acquireCandidatePermit` 路径，账号维度门禁在同一次原子调用内。
- [x] **池空 ≠ 熔断**（边界 7）：候选 SQL 排除零账号池（`account_pool_empty` 不产生候选）、
      选路诊断给出 `account_pool_empty` 专属原因、Admin 账号页签在池空且渠道启用时显式提示
      「这不是熔断」。三处各自可见。
- [x] 429/封号错误体（边界 27）：按上游标准形状实现（error.code=token_revoked 已实测拿到真实样本，
      见 E2E）。**429 真实样本已取得并校准（2026-09-04）**：PlusPool 账号连撞 5h 窗口上限，
      渠道检测触发真实 429，错误体为 `{"error":{"type":"usage_limit_reached","resets_at":<unix>,
      "resets_in_seconds":<sec>}}`，与既有 `RetryAfterFromBody` 判据（识别 usage_limit_reached /
      rate_limit_exceeded、优先 resets_in_seconds 再 resets_at）完全吻合，Sub2API 口径无需修正。
      样本已归档 `sandbox/codex/wire/samples/upstream-rate-limits-429-body.json`（channel_test_logs id 12/13）。

## 六、OAuth 导入与令牌保活（unio-gateway）

- [x] OAuth PKCE 导入（本期实现 openai/codex）：实现落在 `internal/service/subscription/oauth.go`
      （与令牌保活同包，不拆子包——refresh/outbound/oauth 共享 credentials 编解码与账号代理解析）：
  - PKCE 授权链接生成 → 回填 code → 常量时间校验 state → 换令牌 → 解析 id_token 得
    邮箱/账号 ID/套餐 → 落库为 `disabled`，显式启用；换码请求走账号绑定代理。管理面向导已接
    （`subscriptionaccount.StartOAuth`/`CompleteOAuth`，同渠道同上游账号走重授权覆盖）。
  - 不做指纹伪装类 enrichment。

- [x] 令牌保活（`internal/service/subscription`，refresh 与 outbound 同包不再拆子包）：
  - `RefreshWorker` 实现 workers.Unit，接入 worker-server runner：周期扫描
    `expires_at < now()+1h` 的未归档 oauth 账号（disabled 也刷——启用瞬间就要有新鲜令牌），
    逐号限速 2s；
  - 每账号分布式锁：Redis SET NX 30s（跨实例）+ singleflight（进程内）；拿不到锁时短等后重读——
    对方实例大概率正在刷，refresh token 会轮换，并发刷会把对方的新令牌作废；
  - 失败处置：**明确拒绝**（400/401/403 → RefreshRejectedError）= 确认吊销 →
    disabled(token_revoked)；网络失败 → 临时不可调度 10 分钟（不禁用），下一轮周期天然构成重试
    （计划里的指数退避简化成固定隔离窗口 + 周期重扫，行为等价且少一套状态）；
  - **新 refresh token 非空才覆盖**（`MergeRefreshed`，有单测冻结）；
  - 请求时兜底：`Outbound.ResolveAccountOutbound` 发现过期前 5 分钟内即带锁同步刷一次再出站；
    长流请求凭据只在 transport 开始时取一次（边界 13，天然成立）。
  - **E2E 实证**：真实账号的 refresh token 已被上游作废（`refresh_token_invalidated`——该账号在
    导出后于别处重新登录过），我们的刷新路径正确判定为确认吊销并 disabled(token_revoked)。

- [x] **批量文件导入**（`subscription.ParseSub2APIData` + `ImportAccounts`）：sub2api-data v1
      逐字段对照真实导出文件解析（`proxies[]`+`proxy_key` → 账号代理绑定；凭据归一为单一 schema，
      上游账号 ID 缺失时从令牌 claims 兜底）；导入落库 disabled；重复导入按 (platform,
      upstream_account_id) 被 DB 唯一键拒绝，**单条拒绝不拖垮整批**，提示已存在于哪个池；
      「重新授权更新凭据」的 SQL（AdminReauthorizeSubscriptionAccount）已备好，Admin 入口未挂
      （OAuth complete 当前只走新建，重授权分支留给后续）。

## 七、账务与供给联动（unio-gateway）

- [x] 池型渠道成本倍率 0 + Provider 结算币种同售价币种：运营配置口径，E2E 种子已按此装配并实测
      毛利守卫通过、成本快照恒 0、结算正常（charged_amount 按售价收）。
- [x] 零成本渠道毛利率口径排查（边界 2）：巡检结论**口径成立，无需改码**——
      毛利率均为 margin/revenue（除法全有 rev>0 守卫或 NULLIF，无「除以 cost」处），成本 SUM 口径
      对 0 合法；池型请求 100% 毛利是实时结算口径的正确表达（边际成本 0），真实盈利 = 收入 −
      订阅费摊销，数据源为台账、离线计算（设计即此）。
- [x] 请求记录写入账号快照：`request_records.final_account_id` 随 MarkRequestSucceeded /
      MarkSettledRequestFailed / MarkSettledRequestCanceled 三条终态路径写入；
      settlement 补偿任务加 `account_id` 列（迁移 000073），重放收口不丢账号归因。
      E2E 实证 final_account_id 正确落库。
- [x] **供给不变量联动**（边界 11，已升级为 ADR-0020 完整预览）：停用/归档最后一个可调度账号时
      复用 `supply.ChannelImpact`（受影响集合 = 以本渠道为最后一条运行候选的模型）+ 指纹确认
      （`Kind=account_last_supply` 独立指纹域）；集合为空（其它渠道仍可服务）直接放行不打断。
      前端接共享 `SupplyImpactConfirmDialog`（模型清单展示，不提供连带停用勾选——账号操作不改模型
      配置），旧简化 409 保留为预览未注入时的降级路径。

## 八、Chat Completions → Responses 反向桥接（unio-gateway）

> **受益方已由官方核对澄清**：Codex CLI 自 2026-02 起移除 `wire_api = "chat"`，**不可能**向我们发
> Chat Completions，因此本桥接的受益方是**通用 chat 客户端**（OpenAI SDK 的 `chat.completions`、
> LangChain、各类第三方应用）——它们想用号池模型时只会说 chat 协议。桥接照做（已拍板），但排期上
> **应在 Codex 主链路（批次一至七）跑通之后**，验收场景也要用通用 chat 客户端而非 codex CLI。

- [x] 候选资格放开（`lifecycle/adapter_registry.go` OpenAI 分支三处，与 responses 侧对称）；
      Anthropic 分支未动。chat 服务的 AdapterRegistry 接口补 responses 三槽查询供桥接分流。
- [x] 桥接实现落在 **adapter DTO 层**（`internal/core/adapter/openai/chatbridge`，新包），
      不是 service 层的镜像转换——比计划预估的三个大文件镜像小一个量级：桥接实现
      `chatcompletions.ChatAdapter`/`StreamChatAdapter` 接口，背后调 responses adapter，
      chat service 的 ResolveAdapter 兜底分流（HasChat 直转 / 否则 HasResponses 桥接），
      两个 service 的 Invoke/Stream 闭包零改动。估算路径同样走桥接
      （BuildResponsesBodyForEstimate + responses tokenizer）。
- [x] 四个必答题的落地：
  - **权威首字**：只有 `response.output_text.delta` 产出携带 Content 的 chunk，
    created/in_progress/output_item.done 一律不 emit（有单测冻结）；
  - **工具调用**：chat tools→responses function tools、assistant.tool_calls→function_call item、
    role=tool→function_call_output（call_id 稳定对应）；回程 function_call/custom_tool_call→
    tool_calls（流式按 item_id→index 稳定映射增量）；tool_choice 四形态齐备；
  - **usage 降级**：结算只消费 responses adapter 同一次解析的 Facts（attribution 不进 chat usage、
    不影响结算取值）；流式终态 usage 以独立 chunk 下发，与 chat 上游 usage 尾帧同形态，
    include_usage 尾帧实测正常；
  - **会话键**：chat 客户无显式信号时经内容派生哈希命中 sticky（E2E 日志 source=content_hash）。
  - Fast 在桥接路径的表达随边界 15 的结算例外一起做（当前桥接透传 service_tier 请求值）。
- [x] 移植纪律：字段映射语义与 Sub2API apicompat 对照，代码按本仓库契约全部重写（包头已标注来源）。
- **E2E 实证（假 codex 上游 + 全真实网关链路）**：非流式 chat → 池 → "pong-fake" + usage 映射正确；
  流式 chat → 内容增量 chunk + finish + usage 尾帧 + [DONE]；`store` 恒 false、
  `max_completion_tokens→max_output_tokens` 等映射有单测。

## 九、Admin API 与前端（unio-gateway + unio-admin）

- [x] Admin API `internal/app/adminapi/subscriptionaccount` + `service/admin/subscriptionaccount`：
      列表（含聚合 + Redis 运行态合并）、启停/归档/恢复（含供给确认门）、编辑（并发/优先级/代理/
      备注名）、手动刷新令牌、OAuth 导入向导（start→授权链接 / complete→回填 code 落库）、
      批量文件导入、订阅台账读写。**关键机制**：全部调度参数与状态写入经
      `runtimecontrol.Publisher` 两阶段发布承载——BusinessCommit 事务内执行账号变更 +
      `BumpChannelCapacityRevision`，与渠道容量编辑同一条传播路径（边界 20 的服务层收口）。
      操作审计核查（边界 23）结论：仓库无统一 admin 操作审计机制（cdkey 的 audit 是业务台账，非
      平台审计）；账号操作可追溯性由 runtime_control_operations（两阶段发布审计行）+ config_revision
      单调版本 + 台账 created_by 承担，统一审计属平台级特性另立项，不在号池改造内造半个。
- [x] **管理面主动出站的账号身份化（全接口二次实测补章）**：池型渠道不持凭据，渠道检测/模型发现/
      逐模型验证/403 权限复检此前全部以空凭据出站必败。新增 `subscription.ProbeIdentityResolver`
      （未指定账号取 enabled 中 priority 最小者，与调度同向；指定 account_id 允许测停用账号——
      先测后启用），admin 与 worker 两处 bootstrap 注入。`ProbeChannel` 补 responses-only 分支
      （codex 只注册 responses 四槽，探测走流式 responses 等终态；实测 codex wire 契约：
      max_output_tokens 被拒、store 必须显式 false）。检测结果带 tested_account_id/name。
      实测：真实上游检测通过（1.5s）、发现 8 模型、验证 succeeded。
- [x] **按号检测**：`POST /channels/{id}/test` 带 `account_id`；前端账号行「检测该账号」按钮。
- [x] **账号删除**：`DELETE /subscription-accounts/{id}`（仅归档可删、台账级联、有请求历史回 409
      提示保持归档）；渠道级联删除补齐 subscription_accounts/ledger/runtime_control_operations。
- [x] **重新授权**：OAuth complete 对同渠道同上游账号自动走 `AdminReauthorizeSubscriptionAccount`
      （覆盖凭据、保留调度参数与台账），异渠道拒绝（一号一池）；前端账号行「重新授权」入口复用
      OAuth 向导。响应 `{account, reauthorized}`。
- [x] **订阅到期时间可维护**：PATCH 账号带 `subscription_expires_at`（上游无机读来源，运营录入），
      「7 天内到期」聚合与行内展示随之生效；编辑弹窗加到期日期字段。
- [x] **一批守卫与修正**（全接口实测扫出）：池型渠道拒绝渠道级凭据轮换（PUT credential 400）；
      credential 渠道拒绝 account_id 检测；账号列表排序改为 enabled→disabled→archived（原字母序
      归档最前）；归档账号拒绝手动刷新（刷新会轮换上游会话）；OAuth 向导会话过期清扫（防内存泄漏）；
      渠道编辑支持改 account_default_concurrency（候选快照按请求读库即热生效）；渠道列表/运维表
      带 supply_form（前端「号池」标识）；disabled_reason 前端中文化；并发「继承」显示实际继承值。
- [x] **第二轮 UI→路由链审计修正**：
      ① 池型检测失败不再翻渠道 credential_valid（候选 SQL 按它硬过滤——原实现一个账号 401 会把
      整条池踢出路由；实测坏账号 401 后渠道仍 credential_valid=true）；
      ② 自动巡检 worker 排除池型渠道（每次巡检都是对真实订阅账号的真请求，周期性打白烧用量窗口
      且行为像机器人；实测修后 75s 零新增巡检日志）；
      ③ 检测失败 message 前缀被测账号名（last_test_error / 检测日志可定位坏号；成功不写避免污染）；
      ④ OAuth 裸 code 粘贴放行（session_id 已绑定 PKCE 会话，缺省 state 视为同会话；带 state 仍严格校验）；
      ⑤ 列表行菜单：池型隐藏「凭证」（原为死路 400）换「账号」入口；凭据状态列池型显示「账号池」
      而非误导性的「有效」；
      ⑥ 停用账号可直接删（UI 自动串「归档→删除」两步，确认文案说明）；
      ⑦ 账号行显示出口代理（直连/代理 host）；聚合行加池并发容量合计（可算时）；
      ⑧ 装配引导：按「导号→启用账号→绑定模型→启用渠道」缺哪步提示哪步；
      ⑨ 检测弹窗池型可选测试账号（自动选号=在役优先级最高，停用账号可选——先测后启用）；
      ⑩ 行内按号检测后同步刷新渠道 last_test；重复导入提示中文化并给出两条出路；
      渠道删除 409 文案提及池内账号历史。
- [x] 渠道创建表单增「供给形态」（credential/pool），池型不填凭据、可配账号默认并发；
      Fast 开关本就按协议出现，对池型同样可见。后端校验双向严格（credential 必填 ↔ 必空）。
- [x] `ChannelDetailPage` 新增 `section=accounts` 页签（仅池型渠道出现，池型隐藏「凭据」页签）：
      账号行含状态/停用原因、运行态徽章（冷却/令牌刷新中/疑似代理故障/用量暂停，文字+剩余时间）、
      用量水位（5h/7d 负载条配文字百分比 + 采集时间）、在途/上限、优先级、令牌过期与可续期性、
      最近成功时间；行操作：编辑/台账/启停/恢复/手动刷新令牌。聚合行：可调度/总数、在途、冷却、
      用量暂停、订阅将到期。「池空」提示明确写出「这不是熔断」。
- [x] 账号三个操作弹窗（Admin API 全接口人工实测后补齐，消除 dead export）：
      `AccountConfigDialog`（并发/优先级/代理/备注名，空并发=继承渠道默认）、
      `AccountOAuthDialog`（生成授权链接→回填回调 URL 或裸 code，state 缺失由后端会话校验兜底）、
      `AccountLedgerDialog`（按期录入订阅费用 + 台账列表，离线摊销数据源）。
- [x] 全接口人工实测修出的两处后端错误面：导入坏文件 `CodeConfigInvalid` 漏成 500 → 改回
      `admin_invalid_argument` 400；手动刷新失败漏 `routing_credential_resolve_failed` 500
      「internal error」→ 确认吊销回 409（账号已停用 token_revoked）、上游不可达回 502
      （新增 `admin_upstream_unavailable`，账号临时隔离等下一轮保活）。
- [x] 装配顺序提示（导号→启用账号→绑定模型→启用渠道）：账号页签内的 `SetupGuidance` 引导条，
      缺哪步提示哪步（第二轮审计批次落地，见上文 ⑧）。
- [x] **号池并发监测**（实时监控页「号池并发」区块）：`GET /monitoring/account-pools`
      （全部未归档池渠道 + 账号运行态，与账号页签同一条读路径：Postgres 事实 + Redis 批量读，
      不进 DB 经营聚合）；前端 `AccountPoolsPanel` 挂在渠道泳道下方，10s 轮询、随页面暂停：
      池头（渠道名链接到账号页签 + 状态 + 可调度/在途/冷却/暂停 + 池空提示「这不是熔断」）、
      账号行（在途/上限 + 负载条配文字百分比 + 文字状态徽章：禁用/冷却倒计时/用量暂停/隔离）。
      无池型渠道时区块不渲染。等待事实仍只有渠道维度短等计数（本方案无队列，与 Sub2API 的差异保留）。
- [x] 经营视图（账号维度）：请求列表/计数 SQL 增 `account_id` 过滤 + `final_account_id`/
      `final_account_name` 列；请求中心 API/前端 `RequestsList` 支持 `fixedAccountId`；
      账号行新增「账号请求记录」下钻弹窗（复用请求中心组件，费用/用量/线路同一套口径）。
      零成本毛利口径见第七节排查结论（无需改码）。
- [x] Console 客户侧完全不引入账号维度：本次改造未触碰 Console 任何代码，天然成立。

## 九C、与 sub2api 的号池逻辑对齐（2026-09-03 审计批次）

逐块通读 sub2api（gateway_scheduling / ratelimit_service / token_refresh_service /
openai_account_runtime_block_fastpath / openai_oauth_service）后对照本实现，**采纳 5 项**：

- [x] **刷新终局错误码清单**：令牌端点非常规状态码（5xx 等）但正文命中
      {invalid_grant, invalid_refresh_token, refresh_token_reused, refresh_token_invalidated,
      token_expired, app_session_terminated, invalid_client, unauthorized_client, access_denied}
      同样判定为确认拒绝（RefreshRejected）——只按 HTTP 状态分类会把「5xx 包着 invalid_grant」
      误判成网络问题反复退避。单测冻结清单。
- [x] **请求路径确认吊销短路**：401/403 错误体命中 {token_revoked, token_invalidated}（实测样本）
      时经 AccountRevocationSink（subscription.Outbound 实现）立即 disabled(token_revoked)，
      不再等下一轮保活刷新去确认；禁用失败退回 5 分钟隔离路径。语义不明确的 401
      （如 {"detail":"Unauthorized"}）仍走隔离+刷新确认，不直接禁用。
- [x] **429 错误体重置时刻兜底**：x-codex 重置头缺失时解析 body
      {"error":{"type":"usage_limit_reached"|"rate_limit_exceeded","resets_at"/"resets_in_seconds"}}
      （Wire 新钩子 RetryAfterFromBody），解析不出才落秒级兜底冷却。
- [x] **失败响应的用量观测回写**：429 的 x-codex 头携带最新水位（通常 100%），经
      UpstreamMetadata.AccountUsage → AccountUsageObserver（health.Recorder 新方法，
      不 touch LRU）落快照——冷却期内管理页也能看到真实水位与重置时刻。
- [x] **PreferSoonestReset（use-it-or-lose-it）**：池内排序在优先级之后可选插入
      「5h 窗口最早重置优先」（订阅额度过期作废，快过期的先烧），appsettings
      `gateway.account_pool_prefer_soonest_reset` 热更新，默认关闭（与 sub2api 默认一致），
      无活跃窗口观测的账号排后。排序行为有单测冻结。

**不采纳 8 项**（对照后明确拒绝，理由如下）：池内等待队列/sticky 排队（蓝图明确无队列设计，
等待事实只有渠道维度短等）；账号级 RPM/最大会话数（蓝图「本次不做」清单）；WindowCost 金额上限
（我们有官方 x-codex 用量水位，数据源更权威）；模型级限流标记（codex 限流是账号级窗口，无模型维度）；
Transient 429 同号重试窗口（我们的同渠道换号重试等价且不浪费预算）；主动用量探测轮询
（烧配额+风控风险，被动采集+检测回填已覆盖）；调度器快照缓存（单池规模不需要）；
Redis fail-open（我们 fail-closed 是蓝图硬决策）。

已知 sub2api 两处证伪维持不变（用量窗口方向、用量头集合），不回退。

## 九D、出站代理实体化（2026-09-03 批次，对齐 sub2api 代理一等实体并扩展到渠道级）

**动机**：代理是被复用的出口资产（多账号/渠道共用住宅代理，换地址应一处改处处生效），
sub2api 将其建为一等实体（`Proxy` + 账号 `proxy_id` 引用 + 下拉选择）。我们对齐并超出其范围：
代理不仅号池账号能用，**渠道也能用**（sub2api 无渠道级代理）。

**模型**（迁移 000076）：
- `proxies` 表：name（唯一）/protocol（http/https/socks5）/host/port/username/password（明文，
  与渠道凭据同口径）/`url`（写入时组装的规范 URL，凭据转义；运行时只读本列，热路径不拼串）
  /status（enabled/disabled）/note。
- `channels.proxy_id`、`subscription_accounts.proxy_id`：FK RESTRICT（被引用不可物理删除，
  管理面降级 409 并附引用计数）。账号保留 legacy `proxy_url` 作回退（存量 + 文件导入兼容）。

**出站回退链**（唯一决策点在 adapter 的 client 选择）：**账号代理 → 渠道代理 → 直连**。
- 解析在 SQL JOIN 完成（只认 `status='enabled'`，停用实体 → NULL → 回退下一级）：
  候选（FindModelCandidates→`Runtime.ProxyURL`）、出站凭据（GetAccountOutboundCredential）、
  保活扫描（ListAccountsNeedingTokenRefresh）、探测/发现/验证/轮换四类快照。
- adapter 层：openai chatcompletions / openai responses（wire.ClientFor 缺省分支）/ anthropic
  messages / deepseek 两包装 / modeldiscovery 两 lister 统一注入 `proxyclient.Resolver.ClientFor`
  （与账号代理共用同一份按 URL 缓存的 client 池）；codex wire.ClientFor 改为账号代理缺省时
  回退渠道代理。
- 管理面：`/proxies` CRUD + 启停 + 删除护栏；OAuth start / 账号编辑 / 渠道创建编辑收 `proxy_id`
  （绑定校验存在且 enabled；账号绑实体时清空 legacy 裸 URL，单一真相）。
- 前端：网关中心新增「代理」页（列表/表单/启停/删除护栏提示）；OAuth 导入、账号编辑、渠道表单
  三处裸 URL 输入替换为 `ProxySelect` 下拉（直连 + 实体，停用实体标注「回退直连」）；账号行出口
  显示实体名，渠道列表带 proxy_name。

**真机验证**（2026-09-03）：CRUD/409 护栏/密码留空保持/凭据转义（`p@ss:word`→`p%40ss%3Aword`）
全过；三级回退链用假代理实测——账号绑假代理→探测 unreachable；解绑账号只留渠道假代理→仍
unreachable；渠道也解绑→直连探测成功（gpt-5.4）。证明三级出站选择真实生效。

**已知限制**：sub2api 数据文件导入（`proxies[]`/`proxy_key`）仍落 legacy `proxy_url` 裸串
（回退链保证生效）；如需导入即实体化，后续在导入器里按 URL upsert 实体并绑 `proxy_id`。

## 九E、账号列表重构（2026-09-03 定稿）与 codex 正式请求契约新发现

**列表九列**（原型 `tmp/account-list-final.html`，行内结论 + HoverCard 详情）：
账号（备注名/邮箱 + 套餐徽章前置，副行出口代理）/ 状态（生死+运行态合成徽章：启用 · 正常/冷却/隔离/暂停，
叠加取剩余最长，副行统一倒计时）/ 订阅（年月日时分 + 剩余，≤7 天黄、过期红）/ 令牌（四态两字徽章：
正常绿/待续黄/无续黄→过期红/吊销红）/ 负载（进度条 + 在途/上限 · P 优先级）/ 水位（双条一行，
**窗口标签按 window_minutes 动态生成**——官方 weekly 窗因号而异不写死 7d，副行重置时间）/ 24H
（请求数 · 成功率，副行 token · 售卖额）/ 最近成功 / 操作。倒计时统一格式：天+小时 / 小时+分 / 分+秒 / 秒。

**新增数据**：列表 SQL 带 `credentials->>'email'`；三个 24h 聚合查询（AdminChannelAccountsUsage24h /
Sale24h / LastFailure24h：请求/成功/失败/token/按币种净扣费/平均延迟/首字/最近失败），List 批量挂载，
聚合失败不阻断列表。真机验证：3 笔真实流式请求后聚合返回 4 请求/60 tokens/0.00033425 USD ✓。

**codex 正式请求契约（真机实测新发现，2026-09-03）**：
- `stream` 必须为 true：非流式（stream:false/缺省）直接 400——CLI 恒流式，探测也是流式所以从未暴露；
- `input` 必须结构化数组（`[{role, content:[{type:input_text,...}]}]`）：字符串简写 400；
- 叠加已知：store 必须显式 false、max_output_tokens 拒收、previous_response_id 拒收。

**已修复（2026-09-03，出站规范化 + 强制流式聚合）**：
- Wire 新增两钩子（`NormalizeRequest`/`ForceStreaming`），官方 wire 均为零值——credential 直传
  路径逐字节不变，零转换纪律只在 codex wire 内开例外；
- `normalizeCodexRequest`（codex wire 独有，出站前统一改写，探测/CLI 合规请求零转换直返）：
  store 强制 false、stream 置为出站形态、剔除不支持字段清单（max_output_tokens/temperature/top_p/
  penalty/stream_options 等，sub2api 同款）、字符串 input 原文包装成单条 user message（含空白，
  只修形状不改语义）、role:system 文本并进 instructions + 消息降级 developer 留在 input、
  启用 reasoning 时补 `include:["reasoning.encrypted_content"]`（store=false 下多轮回放 reasoning
  依赖加密思维链随响应带回；CLI 每笔自带，SDK 客户在此补齐；include 非法形态不遮掩交上游拒）；
- 非流式入站 → `createResponseViaStream`：以流式出站，完全复用 `StreamResponse` 的 SSE 解析/
  超时/错误分类/头部水位采集，聚合终态 response 对象还原非流式 JSON。**关键实测坑**：codex 的
  `response.completed` 事件 `output` 恒为空数组（与官方语义相反，内容只在过程事件里），聚合必须
  用 `response.output_item.done` 的 item 原文回填 output（终态已带 output 时零改动，绝不重排）；
- chat→responses 桥接免费受益：桥接产物（max_output_tokens/temperature/非流式）经同一规范化 +
  聚合，非流式桥接从必 400 变为可服务。
**真机证据（channel 2 / account 3，request_records 11-17、121-122 全 succeeded、归因正确、零计费异常）**：
字符串+非流式（output 回填后 text=pong、usage 18）✓；system 提升（上游回显 instructions、
store:true/temperature/max_output_tokens 未 400）✓；chat 桥接非流式（content=pong、finish=stop）✓；
chat 桥接流式（usage 尾帧）✓；字符串+流式 ✓；池型渠道探测回归（自动选号 account 3、success）✓；
reasoning include 注入两轮闭环（turn1 无 include → 响应 reasoning item 带回 1336B encrypted_content；
turn2 原样回放 → 上游接受并延续推理上下文答对）✓。
单测冻结：规范化全分支（`normalize_test.go`）、聚合/回填/终态缺失/429 透传/官方 wire 不受影响
（`force_stream_test.go`）、wire 装配集成（`wire_normalize_test.go`）。全量 `go test ./...` 111 包绿。

## 十、验证

验收矩阵实际状态（✅=已验证 · 🧪=单测覆盖 · ⏳=未验证）：

| 场景 | 状态 | 证据 |
| --- | --- | --- |
| 多实例抢同一账号槽 | 🧪 | 批次二真实 Redis 原子准入测试（并发上限、TTL 回收、Finish/Abort 释放、renew 续账号槽） |
| 同渠道换号重试 | 🧪 | `TestRunNonStreamRetriesSameChannelWithDifferentAccount`（A(a1)→A(a2)→B）+ 预算上限 + 同账号绝不重复 |
| 池内选号分布 | 🧪 | `TestOrderShufflesEqualTier`（同档随机打散）+ LRU/负载率排序测试 |
| Sticky 三分支 | 🧪 | 账号三分量 CAS 真实 Redis 契约测试 + BindSuccessWithAccount 分支（同渠道换号=账号级绕行） |
| 归因分层 | ✅ | **真实账号 E2E**：上游 401(token_revoked) → 账号隔离、渠道零污染、透明 fallback；单测覆盖 429/401 样本排除 |
| 全员冷却 | 🧪 | `TestPrepareCandidatesTreatsFullyCooledPoolAsRateLimited`（Retry-After 取最早恢复） |
| 池空 vs 熔断 | ✅ | 候选 SQL 排除 + 诊断 `account_pool_empty` + 前端「这不是熔断」提示 |
| 供给联动 | 🧪 | 最后可调度账号停用 → 409 确认门（简化实现，完整 ADR-0020 预览待补） |
| Fast 结算 | ✅ | priority/fast 归一（存量修正）+ Codex 出站档位权威例外（wire_outbound_authoritative）；CLI 实测请求落库 resolution 正确；单测冻结三分支 |
| 账务 | ✅ | E2E：成本快照 0、毛利守卫过、`final_account_id` 落库；台账 API 就位（离线摊销未跑） |
| OAuth | ✅ | 导入落 disabled（测试）；**真实吊销账号** → RefreshRejected → disabled(token_revoked)；分布式锁 + singleflight |
| 配置传播 | 🧪 | Admin 账号写入经两阶段发布 + BumpChannelCapacityRevision（批次二 CAS 测试）；E2E 未专项验证 |
| 零账号池 | ✅ | `TestFindModelCandidatesRequiresSchedulableAccountForPoolChannel` + E2E（账号禁用后池即失去候选） |
| 重复导入 | 🧪 | ImportAccounts 单条拒绝 + 提示所在池；「重新授权」入口未挂 |
| 反向桥接 | ✅ | E2E（假 codex 上游）：非流/流式/usage 尾帧；权威首字有单测 |
| **Admin API 全接口** | ✅ | 登录后真 HTTP 实测：建池（双向 credential 校验 400）、列表聚合、文件导入（重复拒绝+坏文件 400）、编辑、启停/归档/恢复、供给确认门（无确认 409→带确认成功）、手动刷新（真账号成功续期；失败面 409/502 修复后复测）、台账读写、OAuth start（S256 链接）/complete（坏会话 400）、非法 id 400、不存在 404 |
| **池型检测/发现/验证** | ✅ | 真实上游实测：渠道检测自动选号成功（1.5s，回显账号）；按号检测 account_id=200 成功；空池 409 文案清晰；跨渠道账号 404/400；模型发现拉回 8 个真实 slug；逐模型验证 succeeded（worker 账号身份出站）；池型拒 credential 轮换、credential 渠道拒按号 |
| **删除/重授权/到期** | ✅ | 真 HTTP：未归档删除 409→归档后 204（台账级联）；归档渠道级联删账号+审计行 204；归档账号刷新 409；到期时间 PATCH 后 expiring_soon=1；排序 enabled 在前 |
| **池型不被误伤** | ✅ | 真 HTTP：坏账号按号检测 401（message 带账号名）后渠道 credential_valid 仍 true；worker 巡检排除池型（75s 观察零新增）；credential_valid 建渠道默认 true（migration DEFAULT） |
| **codex CLI 端到端** | ✅ | 真实 codex CLI 0.152.1（独立 CODEX_HOME）→ 网关 → 池选号（账号 200）→ 真实上游 → 完整会话（内容 + token 统计尾，零重连）；结算 succeeded、final_account_id=200、resolution=wire_outbound_authoritative；补偿队列零滞留（398 succeeded）。实测抓出结算 CHECK 约束漏改 bug 并修复（迁移 000075） |
| **存量回归** | ✅ | 全量 `go test ./...` 全绿（110+ 包）；credential 路径的行为由既有测试冻结 |

- [x] `sqlc generate` 干净；`go build ./...`、`go vet`、`go test ./...`（110 包）全部通过。
- [x] 前端 `tsc -b` / eslint 通过（unio-admin）。
- [x] Dev 端到端（两段式，因上传账号令牌已被上游吊销）：
  - **真实上游段**：导入真实账号 → 建池（seed 工具）→ 请求经网关打到 `chatgpt.com/backend-api/codex/responses`
    → 上游回 401 token_revoked → 归因/隔离/fallback/禁用全链路符合设计；
  - **成功链路段**（假 codex 上游 `sandbox/codex/e2e/fakecodex`，wire 形状对照真实样例）：
    请求命中号池 → 账号身份出站 → 用量头解析 → 快照落库 + LRU + 95% 自动暂停 →
    `final_account_id` / trace 账号事实落库 → 暂停后池被过滤降级到 credential 渠道。
  - **codex CLI 实测已补**（健康账号 200，见验收矩阵）；真实 429 冷却取重置时刻仍未验证——
    需要打满 5h 窗口才能触发，不刻意刷爆账号，等自然发生。
    E2E 工具链固化在 `sandbox/codex/e2e/`（seed / fakecodex / verify / newkey）。

## 十一、文档收口

**唯一剩余批次**：实现已全部定型（Fast 结算例外、经营视图、按号检测、完整供给预览均已落地），
ADR 可以按最终代码行为开写。以下清单待执行：

- [x] 新增网关 ADR「订阅账号实体与账号级准入」（实体与绑定式生命周期、两阶段选号、`AttemptPermit`
      账号维度、同渠道换号重试）。
      （2026-09-04：unio-blueprint `docs/products/gateway/decisions/adr-0022-subscription-account-pool.md`，
      已登记决策索引，架构底稿「相关决策」已指向。）
- [x] 修订既有决策并显式记录关系（2026-09-04：各 ADR 增设「账号池修订/扩展/沿用」区块并互链 0022）：
  - ADR-0016（容量分输入、Sticky 绑定值、单请求不重复 Channel 的换号例外）；
  - ADR-0014（429/401 归因下沉到账号）；
  - ADR-0012（账号生命周期采用绑定式语义的差异）；
  - ADR-0020（账号操作接入模型供给影响预览）；
  - 服务档位与 OpenAI FastMode（「按响应事实结算」增加 wire 级响应档位权威性例外，记录于 ADR-0022 第 11 条）；
  - ADR-0006 / ADR-0017（反向桥接：chat 候选资格放开、桥接路径的权威首字与 usage 口径）。
  - ADR-0007 的原子准入、Redis 权威与 fail-closed 结论**原样沿用**，不修订（ADR-0022 引用确认）。
- [x] Admin 侧补账号管理页面设计文档。
      （2026-09-04：unio-blueprint `docs/products/admin/pages/subscription-account-management.md`，已登记页面索引。）
- [x] 取得 429/封号真实样本后，回写蓝图证据等级并更新 `sandbox/codex/wire/samples/`。
      （2026-09-04：真实 429 错误体样本已归档 `upstream-rate-limits-429-body.json`，判据校准见第五节。
      封号 token_revoked 样本此前 E2E 已实测。）

## 风险与注意事项

1. **存量不回归是硬约束**。所有改动都要保证 credential 型渠道走原路径：Lua 脚本传空账号维度、
   资格判断保持原分支、结算口径不变。现有测试是护栏，不允许为适配新逻辑而放宽既有断言。
2. **Sub2API 不可尽信**。已证伪两处（用量窗口方向、用量头集合）。凡标【Sub2API】的实现点，
   先与 `wire/samples/` 或真实响应复核。
3. **反向桥接的权威首字**最容易埋雷：判错会让 TTFT 样本失真，进而影响五项评分中 25% 权重的项，
   表现为路由缓慢偏移且难以定位。这一项必须有独立测试。
4. **CLI 版本漂移**：Codex CLI 升级可能改 wire。沙箱已固化流水线
   （`capture-all.sh` → `wire-snapshot.py build` → `diff`），发布前跑一次，diff 有输出即评估 adapter。
5. **账号数增长的快照成本**：候选快照读账号运行态随号数线性增长，Redis 批量读可控，但需留意单池挂
   数百个号的场景，必要时加池内分片。
