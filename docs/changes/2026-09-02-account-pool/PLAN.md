# 订阅账号号池改造 — Gateway + Admin

> 蓝图依据：`unio-blueprint/docs/architecture/account-pool.md`（2026-09-02 定稿，全部议题已收敛）。
> wire 证据：`unio-gateway/sandbox/codex/wire/samples/*.json`（真实抓包脱敏样例，实现时逐字段对照）。
> 前提：开发环境；仅新增表与列，不动存量数据；现有 credential 型渠道行为必须逐字节不变。

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

- [ ] 候选查询：现有候选 SQL 增加 `supply_form`，并对池型渠道保证「至少一个可调度账号」才产出候选
      （零账号池允许存在但不成为候选，边界 12）。**顺延至批次三**：候选 SQL 的改造与候选快照的
      账号聚合是同一件事，分开做会产生一个「查得出候选但拿不到账号」的中间态。

> **批次一已知既有失败（非本次引入）**：`TestFindModelCandidatesFiltersByIngressProtocol` 与
> `TestFindModelCandidatesOrdersAndFilters` 在本地 dev 库上返回 0 候选而失败。已用严格对照确认——
> 代码与数据库同时回退到基线仍失败，切到 `develop` 分支仍失败——属既有问题，本批次未引入也未修复。
> 批次三改候选查询前需要先查清它（很可能是本地 dev 库缺少定价/汇率种子数据），否则无法用这两个
> 测试守住候选改造。

## 二、运行态与原子准入（unio-gateway）

- [ ] `internal/platform/breakerstore`：账号并发槽
  - 新键：账号并发 ZSET（成员为 permit id，分数为 Redis 服务端时间戳），TTL 自愈，沿用现有渠道并发槽
    的键命名与清理模式；
  - `lua/ops/gate_and_acquire.lua` 扩展：新增账号维度入参（account_id、账号并发上限、账号 revision），
    在现有 Channel 门禁（breaker / cooldown / permission / revision / 渠道并发）之后、创建 permit 之前，
    **同一次原子调用内**校验账号冷却与账号槽位并占位；
  - 账号槽满返回可区分的 denial 原因（`account_concurrency_full`），供路由换号重试；
  - `finish.lua` / `abort.lua` 同步释放账号槽；异常残留由 TTL 回收。
  - 现有 credential 型渠道传空账号维度，脚本走原路径，行为逐字节不变（用现有测试守住）。

- [ ] 账号运行态 control（Redis）：账号冷却至（`reset_at` 绝对时间戳）、临时不可调度至、用量暂停标记、
      疑似代理故障标记。与 Provider/Channel control 同一套读写与围栏风格。

- [ ] **账号配置热更新传播**（边界 20）：账号 `config_revision` 变化时提升所属 Channel 的
      `capacity_revision`，使新快照与 `AttemptPermit` 立即感知；在途 permit 按固化身份收口。

## 三、路由与选号（unio-gateway）

- [ ] `internal/core/routing`：候选快照对池型渠道额外拉取账号运行态列表，产出两项：
  - 资格：至少一个可调度账号；全部冷却 → 渠道按 cooldown 处理，`Retry-After` 取最早恢复时间；
  - 并发容量分输入：全池空闲槽位聚合 / 上限聚合（五项评分公式与权重**不变**）。

- [ ] 池内选号（新增，位于 lifecycle 候选执行层）：
  1. Sticky 绑定账号优先（命中后复验可用性）；
  2. 过滤链：账号 ∧ Channel ∧ Provider enabled、不在冷却、未临时不可调度、用量未达阈值；
  3. 排序：priority → 实时负载率 → 最久未使用（LRU）；**同档随机打散**；
  4. 按序逐号 `AttemptPermit`；账号槽满取下一个；
  5. 全部账号满 → 该 Channel 记 `concurrency_full` 跳下一候选（进入既有全池短等语义）。
  - 混合拒绝映射（边界 5）：池内存在「仅并发满」的账号即报 `concurrency_full`（可等待），
    全部因冷却才报 cooldown——全池短等的进入条件依赖该映射的确定性。

- [ ] **同渠道换号重试**（边界 6，修改 ADR-0016 既有契约）：放开「同 Channel 不同账号可重试」，设次数
      上限；同一账号在单请求内禁止重复。需要显式测试守住「同账号绝不重复」。

- [ ] Sticky（`internal/platform/stickysession`）：绑定值增加 `account_id`，CAS 同时比较
      `(channel_id, account_id, binding_version)`；换键空间让旧绑定自然 miss（TTL 30 分钟，无需迁移）。
  - 绑定账号冷却/并发满 → 临时绕行，保留绑定不续期，绕行优先落同渠道其他账号；
  - 绑定账号禁用/归档/吊销 → 确认失效，CAS 清绑。

- [ ] 会话键提取（`internal/core/sessionhint`）：在现有显式信号之上补**内容派生哈希兜底**——无
      `prompt_cache_key`/`session-id` 时对消息前缀做定长哈希。边界：只哈希有界前缀、只存哈希不落原文、
      相同前缀共享身份仅用于缓存亲和不赋业务语义。**反向桥接落地后该兜底会成为 chat 客户的主要来源**。

- [ ] 缓存画像口径（边界 10）：「池内换号」与「跨渠道切换」同口径排除出渠道缓存画像。

- [ ] Routing trace：只记实际尝试过的账号与池内选号事实，不记全池逐号评分（边界 14）。

## 四、Codex adapter（unio-gateway）

- [ ] `internal/core/adapter/openai/codex/responses/`：新 adapter，在 `bootstrap/adapters.go` 以
      `Key: "codex"` 注册到 OpenAI 协议族 registry，**只登记 responses 四槽**
      （Models / Responses / StreamResponses / ResponsesInputTokenizer + 原生 ResponsesCompact），
      不登记 chat 三槽。
  - 端点：`{Provider.origin}/backend-api/codex/responses`（`/compact`、`/input_tokens` 子路径），
    模型清单 `GET /backend-api/codex/models?client_version=<ver>`（带 ETag 条件请求）；
  - 请求头：`Authorization: Bearer <账号 access_token>`、`chatgpt-account-id`、Host、
    **按账号收敛的设备指纹**（`originator`/`User-Agent`/`version`，边界 26）；剥离入站鉴权头；
  - `previous_response_id` 一律 400 拒绝（边界 25，与上游 HTTP 行为一致）；
  - 防御性剥离客户回带的 `x-codex-turn-state`（跨账号串号防护）；
  - `reasoning.effort` 原样透传：上游自报档位为 low/medium/high/xhigh/max/ultra，多于标准 API 档位集，
    **不得按标准集做白名单校验**；
  - 响应：解析 `x-codex-*` 用量头（**primary = 5h、secondary = 7d，勿照抄 Sub2API 的反向映射**）、
    `usage`（含 `attribution`）、`service_tier`；`safety_identifier` 与 `x-codex-turn-state` 不回传客户。
  - 逐字段对照 `sandbox/codex/wire/samples/`：`ingress-request.json`、
    `upstream-usage-headers.json`、`upstream-usage-completed.json`、`upstream-models.json`。

- [ ] **按账号代理**（边界 29）：`channel.Runtime` 增账号维度字段（账号凭据、上游账号 ID、proxy_url）；
      bootstrap 提供按代理 URL 取 `*http.Client` 的解析器注入 adapter（transport 以代理 URL 为键缓存），
      不让各 adapter 自管 client 池。导入、令牌刷新、正式请求三条路径统一走账号代理。

- [ ] **Fast 档位**（边界 15，修订既有档位结算契约）：按 wire 引入「响应档位权威性」标记——credential
      渠道维持现状（按响应事实结算），Codex wire 标记为不权威、**按出站档位结算**（实测同一请求响应分别
      回 `auto`/`default`，均非发送值）。Fast 开关对池型渠道提供，仍要求人工确认。
- [ ] **`priority` 与 `fast` 归一为同一档（存量修正，不限号池）**：官方 2026-07-30 把 priority 改名
      fast，两值都接受；现有模型响应回 `priority`，**gpt-5.6 之后发布的模型响应回 `fast`**。现有档位
      解析若只认 `priority`，新模型上线即把 Fast 误判为 Standard 并少收费。需在档位归一化处同时接受
      两值，并补测试冻结该等价关系。

## 五、健康反馈与归因（unio-gateway）

- [ ] 归因分层改造（修订 ADR-0014）：
  - 429 → 写**账号**冷却（优先取 `x-codex-primary-reset-at` / `codex.rate_limits.reset_at` 绝对时间戳，
    解析不到用可配置秒级兜底）；不喂渠道 breaker、不进渠道错误率样本；
  - 401/403 → 归**账号**（失效令牌缓存 + 临时不可调度给刷新窗口，确认吊销才禁用）。
    **注意存量行为**：现有 `classifyChannelSampleError` 把 401/403 计入渠道错误率分子，池型渠道必须
    改为归账号并从渠道评分样本排除，否则单号令牌失效会拖垮整池评分（边界 4）；
  - 5xx / 超时 / 网络 → 照旧归 Provider/Channel breaker；
  - 传输错误持久/瞬态二分（边界 16，**【Sub2API】需复核**）：持久（代理认证失败、连接拒绝、DNS 失败、
    无路由）→ 账号临时不可调度约 10 分钟 + 换号；瞬态（超时、重置、EOF）→ 只换号不处置账号；
  - 用量自动暂停：快照达阈值（默认 90%，可配置）提前移出调度；窗口重置或快照过期自动恢复。
    **两个易漏边界**：① 官方支持付费即时重置与可储存重置，`reset_at` 可能**提前**，恢复判定必须处理
    「reset_at 变小」而非只处理「到期」；② 5h/7d 任一窗口可能缺失（见官方核对表），缺失时视为不限，
    不得按 0% 或 100% 臆断。

- [ ] half-open 探测同样经池内选号取健康账号，不绕过账号层（边界 8）。

- [ ] **池空 ≠ 熔断**（边界 7）：无可调度账号表现为「无候选资格」，运维界面必须与 breaker open 可区分。

- [ ] 429/封号错误体（边界 27，**【Sub2API】**）：先按 Sub2API 方式实现，冷却时间来源已实测确定；
      取得真实样本后校准错误体判据并回写蓝图。

## 六、OAuth 导入与令牌保活（unio-gateway）

- [ ] `internal/service/subscription/oauth`（新包，按平台可插拔，本期实现 openai/codex）：
  - PKCE 授权链接生成 → 回填 code → 常量时间校验 state → 换令牌 → 解析 id_token 得
    邮箱/账号 ID/套餐 → 落库为 `disabled`，显式启用；换码请求走账号绑定代理。
  - 不做指纹伪装类 enrichment。

- [ ] `internal/service/subscription/refresh`（后台服务）：
  - 定时分页扫描将过期账号；每账号持分布式锁（防多实例重复刷）；每平台限速 + 并发上限；
  - 失败指数退避，重试耗尽 → 临时不可调度 N 分钟（**不禁用**）；确认吊销才置 disabled；
  - **新 refresh token 非空才覆盖**旧值；
  - 请求时兜底：取凭据发现不新鲜则带锁同步刷一次再出站（长流请求凭据只在 transport 开始时取一次，
    流建立后不受影响，边界 13）。
  - 参考实现：`sandbox/codex/scripts/token.py` 已验证刷新流程可用。

- [ ] **批量文件导入**：格式解析器可插拔，本期唯一支持 Sub2API `sub2api-data` v1
      （`{type, version, proxies[], accounts[]}`；账号字段映射到实体，`proxies[]` + `proxy_key` 映射到
      代理绑定）。导入落库 disabled；带 refresh token 的账号直接进入保活；按上游账号标识去重，
      重复导入拒绝并提示已存在于哪个池，另提供「重新授权更新凭据」显式操作（边界 21）。

## 七、账务与供给联动（unio-gateway）

- [ ] 池型渠道配渠道级成本倍率 0（`channel_cost_multipliers.multiplier = 0`，schema 与毛利守卫均已核实
      允许）；订阅 Provider 结算币种设为与售价同币种，避免零成本仍因缺汇率被剔除候选（边界 1）。
- [ ] 排查「毛利/成本」类除零算式，零成本渠道毛利率口径统一为 `(售价−成本)/售价`（边界 2）。
- [ ] 请求记录写入 `account_id` 快照。
- [ ] **供给不变量联动**（边界 11）：停用/归档池内最后一个可调度账号可能让模型失去最后供给，账号操作
      必须接入 ADR-0020 影响预览，不得静默绕过。

## 八、Chat Completions → Responses 反向桥接（unio-gateway）

> **受益方已由官方核对澄清**：Codex CLI 自 2026-02 起移除 `wire_api = "chat"`，**不可能**向我们发
> Chat Completions，因此本桥接的受益方是**通用 chat 客户端**（OpenAI SDK 的 `chat.completions`、
> LangChain、各类第三方应用）——它们想用号池模型时只会说 chat 协议。桥接照做（已拍板），但排期上
> **应在 Codex 主链路（批次一至七）跑通之后**，验收场景也要用通用 chat 客户端而非 codex CLI。

- [ ] 候选资格放开（`lifecycle/adapter_registry.go` OpenAI 分支，**三处**，与 responses 侧对称）：
      `AdapterCapabilityNonStream` 由 `HasChat` → `HasChat || HasResponses`；
      `AdapterCapabilityStream` 由 `HasStreamChat` → `HasStreamChat || HasStreamResponses`；
      `AdapterCapabilityInputTokenizer` 由 `HasChatInputTokenizer` → `|| HasResponsesInputTokenizer`。
      Anthropic 分支不动。
- [ ] 桥接实现：镜像现有 `responses→chat`（`internal/service/gateway/openai/responses/` 下
      `responses_chat_map.go` 527 行 + `responses_response_map.go` 217 行 + `responses_stream.go` 642 行，
      由 `create_response.go` 按候选能力分流），在 chat completions service 侧按候选能力分流
      「chat 直转 vs responses-only 反向桥接」。
- [ ] 落地必须回答（Sub2API `internal/pkg/apicompat/` 有现成答案可移植，须与本仓库契约对齐）：
  - 权威首字：译回的 chat chunk 与 Responses `output_text.delta` 对齐，不得把 `response.created` /
    `in_progress` 误判为首字（ADR-0017），否则 TTFT 样本失真影响五项评分 25% 权重项；
  - 工具调用双向映射：chat `tools`/`tool_calls` ↔ Responses `function_call`/`custom_tool_call`
    （含 Codex 私有 namespace），`call_id` 稳定对应；
  - usage 降级口径：Responses `usage`（含 `attribution`）译回 chat usage 时的字段损失与结算取值侧；
  - Fast 档位在桥接路径上的表达，与边界 15 响应档位不权威结论共存；
  - 会话键：chat 客户通常不发显式信号，依赖第三节的内容派生哈希兜底。
- 移植纪律：遵守采信口径三条护栏（分发红线、来源清单标注出处 + commit、集中在协议边缘）。

## 九、Admin API 与前端（unio-gateway + unio-admin）

- [ ] Admin API `internal/app/adminapi/subscriptionaccount`：账号 CRUD、启停、归档/恢复、编辑
      （并发/优先级/代理）、手动刷新令牌、按号检测、OAuth 导入向导、批量文件导入、订阅台账。
      全部操作接入现有操作审计（边界 23，审计记录不含令牌内容）。
- [ ] 渠道创建/编辑表单增加「供给形态」；池型不填凭据，可配账号默认并发；
      「允许 OpenAI Fast」开关对池型同样提供（启用语义见边界 15）。
- [ ] `ChannelDetailPage` 新增 `section=accounts` 页签（现有 section 机制走 URL 参数）：
  - 账号行：状态（含父级遮蔽标注）、运行态（冷却至 / 临时不可调度 / 用量暂停 / 疑似代理故障）、
    用量水位（5h/7d + 采集时间）、在途并发/上限、优先级、代理、令牌状态、最近成功时间；
  - 概览区池聚合：可调度/总账号数、聚合在途/上限、冷却中、用量暂停、令牌即将过期、**订阅即将到期**；
  - 「池空」与 breaker open 必须显示为两个可区分的事实。
- [ ] 装配顺序提示：建池（disabled）→ 导号 ≥1 → 模型发现/绑定 → 逐模型验证（验证请求经池内选号）→
      启用 Binding/Channel → 启用账号。手动检测下沉为按账号检测，渠道级聚合展示。
- [ ] **号池并发监测**（实时监控页新增区块，内容参考 Sub2API 运维并发卡，样式自定）：
      维度钻取 Provider → 池 → 账号，另有独立用户并发视图；汇总行含可用/总数、冷却数、异常数、
      聚合在途/上限与负载条、该渠道当前全池短等请求数；账号行含在途/上限、负载条、状态徽章
      （冷却剩余倒计时 / 用量水位 / 刷新中 / 疑似代理故障 / 已禁用 / 父级遮蔽），异常与冷却优先排序。
      **与 Sub2API 的差异**：无逐号等待队列指标（本方案无队列），等待事实只有渠道维度短等计数。
      数据源为 Redis 实时运行态，与实时监控页同源边界，不进 DB 经营聚合。
      样式遵循现有无障碍口径：负载条必须配文字百分比、状态以文字表达不单靠颜色、不给主观健康标签。
- [ ] 经营视图：驾驶舱首屏 KPI 与实时监控页不引入账号维度；**毛利/利润率卡需处理零边际成本口径**
      （标注订阅成本不在请求成本内，先例为「缓存贡献（估算）」）；渠道分析中心对池型渠道提供账号下钻
      与台账摊销后的有效成本；请求记录与 routing trace 支持按账号筛选。
- [ ] Console 客户侧完全不引入账号维度（客户只看模型，与移除 Route 的口径一致）。

## 十、验证

对照蓝图「验收要点」，最低验收矩阵：

| 场景 | 目标结果 |
| --- | --- |
| 多实例抢同一账号槽 | 不超限；崩溃残留租约由 TTL 回收；Finish/Abort 正确释放 |
| 同渠道换号重试 | 不同账号可重试且有上限，**同账号绝不重复** |
| 池内选号分布 | 同档账号流量分布接近均匀（随机打散有效），LRU 生效，无稳定捶打单号 |
| Sticky 三分支 | 绑定账号命中续期 / 冷却绕行不改绑 / 确认失效清绑后新号重建 |
| 归因分层 | 账号 429/401 不进渠道评分样本与 breaker；5xx/超时照旧归渠道；代理建连失败归账号 |
| 全员冷却 | 渠道表现为 cooldown，`Retry-After` 取最早恢复时间 |
| 池空 vs 熔断 | 两种不可用在运维界面可区分 |
| 供给联动 | 停用/归档最后一个可调度账号触发模型供给影响预览 |
| Fast 结算 | Codex wire 按出站档位结算（响应 `default` 不降档）；credential 渠道与现状完全一致 |
| 账务 | 成本快照恒 0 且毛利守卫通过；请求记录带 `account_id`；台账可离线算出每号摊销单价 |
| OAuth | 导入落库 disabled 后显式启用；后台刷新与请求时兜底刷新互斥正确；确认吊销才禁用 |
| 配置传播 | 改账号并发/优先级/代理后新请求立即生效，在途 permit 按固化身份收口 |
| 零账号池 | 不成为候选；启用时提示 |
| 重复导入 | 被拒并提示已存在于哪个池；「重新授权」可更新凭据 |
| 反向桥接 | chat 请求可命中池型渠道；权威首字与 usage 口径符合定义 |
| **存量回归** | credential 型渠道的路由、准入、结算、评分行为**逐字节不变** |

- [ ] `sqlc generate` 干净；`go build ./...`、`go vet`、`go test ./...` 全部通过。
- [ ] 前端 `tsc` / eslint 通过。
- [ ] Dev 端到端：导入真实 Codex 账号 → 建池型渠道 → 发现绑定验证模型 → 客户用 UnioAPI Key 经 codex CLI
      调用 → 请求打到号池 → 账号用量水位更新 → Admin 监测页可见。

## 十一、文档收口

- [ ] 新增网关 ADR「订阅账号实体与账号级准入」（实体与绑定式生命周期、两阶段选号、`AttemptPermit`
      账号维度、同渠道换号重试）。
- [ ] 修订既有决策并显式记录关系：
  - ADR-0016（容量分输入、Sticky 绑定值、单请求不重复 Channel 的换号例外）；
  - ADR-0014（429/401 归因下沉到账号）；
  - ADR-0012（账号生命周期采用绑定式语义的差异）；
  - ADR-0020（账号操作接入模型供给影响预览）；
  - 服务档位与 OpenAI FastMode（「按响应事实结算」增加 wire 级响应档位权威性例外）；
  - ADR-0006 / ADR-0017（反向桥接：chat 候选资格放开、桥接路径的权威首字与 usage 口径）。
  - ADR-0007 的原子准入、Redis 权威与 fail-closed 结论**原样沿用**，不修订。
- [ ] Admin 侧补账号管理页面设计文档。
- [ ] 取得 429/封号真实样本后，回写蓝图证据等级并更新 `sandbox/codex/wire/samples/`。

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
