# 改造设计:6 组 gateway 热路径 env → 运行时配置(可热改)

> 状态:**已实施(2026-07-08)。** 落地映射:注册表/codec=`appsettings/gateway_settings.go`;
> 消费方写入口=`lifecycle/breaker.go`、`lifecycle/cooldown.go`、`lifecycle/credential_gate.go`、
> `ratelimit/guard.go`、`routing/router.go`;applier=`bootstrap/settings_applier.go`(5s 周期);
> 启动 seed=`SettingsStore.SeedDefaults`(gateway/admin bootstrap 均调用);前端=admin
> 「系统 → 运行时配置」页(`RuntimeGatewaySettings.tsx`,含「已偏离代码默认」标记)。
> 建立:2026-07-08。依赖:`DESIGN-runtime-settings.md`(运行时配置系统:app_settings + Redis 实时缓存 + 注册表)。
> 决策(用户已定):**① 优先关系 = `db_only`**(这批从 `config.go` 移除,只留注册表默认 + DB);**② 范围 = 6 组一次性做完**;**③ 全部要求热改(免重启生效)**;**④ applyInterval=5s**;**⑤ 启动 seed(缺行写默认,DO NOTHING)**;**⑥ 熔断/限流均重构为共享单实例(§3.4 选 a)**;**⑦ 不做变更审计**。
>
> P4 后续修订：DEC-053 已把当前限流代码默认改为 `RPM=0、TPM=0、RPD=0`；DEC-054 又把
> `gateway.rate_limit_defaults` 拆为 `gateway.route_rate_limit_defaults` 与
> `gateway.channel_rate_limit_defaults` 两套独立 Redis revisioned control，并删除
> `failure_policy`，统一 fail-closed。下文关于限流共享 key、共享 Guard、由 settingsApplier
> 驱动限流的描述，以及 `60/0/0/fail_closed`，均只记录 2026-07-08 首次迁移时的历史基线，
> 不代表当前架构或默认值；settingsApplier 对其他非准入类设置仍继续使用。

---

## 0. TL;DR / 审核要点

把 6 组 gateway 热路径配置从 env 迁到运行时配置系统,并让它们**免重启生效**。核心难点:这 6 组现在**都是构造时把值冻结在结构体里**(唯一例外 `stream_idle_timeout` 已是包级 atomic),所以要热改必须给每个消费方加一个**线程安全的 `Reload`/`Set`**,再由 gateway 的一个**后台 applier(定时从 SettingsStore 拉最新值并推给各消费方)**统一驱动。

审核时请重点看:**§3 每组的消费点与 reload 方式是否准确**、**§5 `db_only` 带来的连锁改动(config.go / 只读面板 / .env)是否可接受**、**§7 风险(全是计费邻近热路径)**。

---

## 1. 范围

### 本批(6 组,全部 hot_reload)

| # | 配置 | 现 env | 默认 | 消费子系统 |
|---|---|---|---|---|
| 1 | 熔断器(5 项:enabled/window/min_requests/failure_ratio/open_duration) | `CIRCUIT_BREAKER_*` | 开/30s/20/0.5/30s | `lifecycle.ChannelCircuitBreaker` |
| 2 | 限流全局默认(rpm/tpm/rpd/failure_policy) | `RATE_LIMIT_DEFAULT_*` / `RATE_LIMIT_FAILURE_POLICY` | 60/0/0/fail_closed | `ratelimit.Guard` |
| 3 | 流式 idle 超时 | `GATEWAY_STREAM_IDLE_TIMEOUT` | 10m | `adapter`(包级 atomic) |
| 4 | 渠道 429 冷却 + 上限 | `GATEWAY_CHANNEL_RATELIMIT_COOLDOWN(_CAP)` | 5s/5m | `lifecycle.ChannelCooldownRegistry` |
| 5 | 凭据失效 401 阈值 | `GATEWAY_CHANNEL_CREDENTIAL_401_THRESHOLD` | 3 | `lifecycle.ChannelCredentialGate` |
| 6 | 默认渠道超时(渠道未配 timeout 时兜底) | *(无 env,硬编码)* `defaultChatRouteTimeout`/`defaultChannelTimeout` = 30s | 30s | `routing.Router.defaultTimeout` |

> 注:第 6 组当前**没有 env**,是 `bootstrap/routing.go` 与 `core/routing/router.go` 两处 `30s` 硬编码常量。本次一并纳入运行时配置。

### 不在本批(留 env,理由见 runtime-settings §1)

启动型:`*_HTTP_ADDR`、`DATABASE_URL`、`POSTGRES_*`、`REDIS_*`、`ADMIN_API_TOKEN`、`LOG_LEVEL`、`OTEL_*`、`HTTP_*` 超时。
第二批候选(本次不做,登记备忘):worker 结算补偿参数、孤儿清扫、渠道巡检间隔/超时/保留、媒体 token 估算开关、健康判定阈值(0.95/0.80,散落 3 处)、SSE 4MB 上限、`partial_assumed_cache_read_ratio`、`max_output_tokens_fallback`、`max_upstream_response_bytes`。

---

## 2. 现状:6 组的"冻结"事实(逐一读代码确认)

| 配置 | 构造点 | 请求时怎么读 | 现状 | 实例数 |
|---|---|---|---|---|
| 熔断器 | 三协议 gateway 各自 `NewChannelCircuitBreaker(cfg)`(`gateway.go:68/143/214`)→ 存 `b.cfg` | 每请求读 `b.cfg.*`(持 `b.mu`) | **冻结** | **3 个**(chat/responses/messages 各一) |
| 限流默认 | ① `gateway_server.go:116` `NewRateLimitGuard`→ 传三协议 service;② `http.go:34` `NewHTTPHandler` 内**再建一个**→ HTTP 中间件 | 每请求 `effectiveLimit(override, g.defaults.X)` | **冻结** | **2 个**(service 侧 + 中间件侧) |
| stream idle | `adapter.SetStreamIdleTimeout(cfg)`(`gateway_server.go:75`) | 每流 `adapter.StreamIdleTimeout()` 读包级 atomic | **已现读** | 1(包级全局) |
| 429 冷却 | `lifecycle.NewChannelCooldownRegistry(cooldown, cap)`(`gateway_server.go:154`),三协议共享 | 命中 429 时读注册表 cooldown/cap | **冻结** | 1(共享) |
| 401 阈值 | `lifecycle.NewChannelCredentialGate(threshold,…)`(`gateway_server.go:164`),三协议共享 | 每次 401 读 `g.threshold`(持 `g.mu`) | **冻结** | 1(共享) |
| 默认渠道超时 | `routing.NewRouter(store, defaultChatRouteTimeout,…)`(`routing.go:16`),三协议共享同一 router | 路由时读 `r.defaultTimeout` | **冻结** | 1(共享) |

结论:
- 除 stream idle 外,5 组都需新增线程安全写入口才能热改。
- ⚠️ **熔断器 3 实例、限流 2 实例**:不是单点。已决(§3.4/§11):重构为**共享单实例**后再接 applier。
- ⚠️ **熔断器 `enabled` 的特殊限制**:熔断器**仅在 `Enabled` 时构造**,禁用时 service 持 nil `ChannelBreaker`(退化为始终放行)。因此**启动时禁用→运行期热启用**在当前架构下做不到(手里根本没有 breaker 实例)。见 §3.4 处理方案。

---

## 3. 方案:后台 applier + 各消费方 Reload

### 3.1 总体

在 gateway 进程加一个 **`settingsApplier`**:启动时同步拉一次初值构造各消费方,之后**每 `applyInterval`(建议 5s)从 `SettingsStore` 读这 6 组最新值,推给对应消费方的线程安全写入口**。消费方的热路径读取逻辑基本不动(仍读自己的 cfg 字段),只是那份 cfg 会被 applier 原子替换。

> 为什么用 applier 轮询而不是每请求现读 SettingsStore:这 6 组每请求都要用,直接每请求读 store(即便有 3s 本地缓存)也会给热路径加锁竞争;用 applier 把"读配置"与"用配置"解耦,热路径只读本地 cfg,零额外开销。5s 生效延迟对这些运维阈值完全够。

### 3.2 各消费方要加的写入口

| 消费方 | 新增 | 并发安全 |
|---|---|---|
| `ChannelCircuitBreaker` | `SetConfig(cfg ChannelCircuitBreakerConfig)`:持 `b.mu` 替换 `b.cfg`(复用已有兜底校验) | 复用已有 `b.mu` |
| `ratelimit.Guard` | `SetDefaults(DefaultLimits)` + `SetFailOpen(bool)`:把 `defaults`/`failOpen` 改为 atomic 或加 `mu` 保护 | 新增字段保护 |
| `adapter` stream idle | 复用现有 `SetStreamIdleTimeout` | 已 atomic |
| `ChannelCooldownRegistry` | `SetCooldown(cooldown, cap)`:持锁替换 | ⚠️ 当前 `defaultCooldown`/`cap` **不在 `r.mu` 下**(只有 `until` map 被锁),加 setter 须把这两字段一并纳入锁/atomic,否则读写竞态 |
| `ChannelCredentialGate` | `SetThreshold(int)`:持 `g.mu` 替换 `g.threshold` | 复用已有 `g.mu` |
| `routing.Router` | `SetDefaultTimeout(d)`:把 `r.defaultTimeout` 改为 atomic 或加锁 | 新增保护 |

> 全部只"替换标量/小结构",不影响进行中的计数/状态机(如熔断窗口计数、冷却在途条目)。替换后下次判定即用新阈值。

### 3.3 注册表登记(`appsettings/registry.go`)

新增 6 条 `Definition`(key / 分类=`gateway` / 说明 / 默认 / 校验 / `HotReload:true`),各带独立 codec(参考 beta 的 doc/decode/validate):

| key | 值形状(JSON) | 默认 | 校验 |
|---|---|---|---|
| `gateway.circuit_breaker` | `{enabled,window_ms,min_requests,failure_ratio,open_duration_ms}` | 见 §1 | ratio∈(0,1]、时长>0、min>0 |
| `gateway.rate_limit_defaults`（已废止，历史） | `{rpm,tpm,rpd,failure_policy}` | 60/0/0/fail_closed | ≥0;policy∈{fail_closed,fail_open} |
| `gateway.stream_idle_timeout_ms` | `600000`(int 毫秒) | 10min | >0 |
| `gateway.channel_ratelimit_cooldown` | `{cooldown_ms,cap_ms}` | 5000/300000 | ≥0 |
| `gateway.credential_401_threshold` | `3`(整数,次) | 3 | >0 |
| `gateway.default_channel_timeout_ms` | `30000`(int 毫秒) | 30s | >0 |

> 单位约定(2026-07-08 用户拍板,替代最初的 duration 字符串方案):时长一律 **int 毫秒**,
> 字段/key 带 `_ms` 后缀(对齐 `channels.timeout_ms` 惯例);解码 `DisallowUnknownFields`,
> 旧字符串格式直接报错不静默。前端用「数字+时间单位下拉」编辑,TPM/RPD 复用渠道页的
> 「数字+K/M/B」组件(`rate-limit-input.tsx`)。格式改版迁移:`000070_app_settings_duration_ms`。

### 3.4 多实例 / enabled 的处理(自审补)

自审发现两处非单点,方案如下:

**熔断器(3 实例 + 可禁用)**。当前 chat/responses/messages 各建一个 breaker,且仅 `Enabled` 时才建。两个子问题:
- 多实例:**已决(用户选 a):把 breaker 改成三协议共享的单实例**(和 429 冷却/凭据闸门一样,在 `gateway_server` 建一个、分别 `Set*` 注入三个 service),applier 只更新这一个。此重构小且降低长期复杂度。
- enabled 热切换:为支持"运行期启/停熔断",**始终构造 breaker 实例**,把 `enabled` 变成 breaker 内部一个原子开关(禁用时 `Allow` 恒 true、不记状态),而不是"禁用就传 nil"。这样 `enabled` 也能热改。
- ~~备选 (b):保持 3 实例、`enabled` 标非热改~~(已否)。

**限流(2 实例)**。service 侧与 HTTP 中间件侧各建一个 `Guard`(都经 `NewRateLimitGuard`)。
- **已决(用户选 a)**:`gateway_server` 建**一个** `Guard`,同时给 service 与 `NewHTTPHandler` 用(改 `NewHTTPHandler` 签名接收现成 guard,而非内部再建);applier 只更新这一个。
- ~~备选 (b):applier 持有 2 个 guard 引用都更新~~(已否)。

---

## 4. 初始化顺序(db_only 下如何拿初值)+ 启动 seed(用户决策)

`db_only` 意味着 bootstrap 不再从 `config.Config` 拿这些值。顺序:

1. 建 `SettingsStore`(已在)。
2. **启动 seed(用户方案)**:对注册表全部 key 逐项检查 DB,**缺行则把注册表默认值写入**(含 description)。
   - **必须用 `INSERT … ON CONFLICT (key) DO NOTHING` 语义**——绝不能复用现有 `UpsertAppSetting`(它是 DO UPDATE,会覆盖运维改过的值)。需新增 sqlc 查询 `SeedAppSetting`。
   - gateway 与 admin 启动都跑 seed(幂等,DO NOTHING 天然并发安全);先起哪个都行。
   - seed 后 DB 即完整配置清单:运维直接查表/看面板即见全量配置与说明,不再有 `source=default` 的虚拟态。
   - **默认值升级语义(明确记录)**:seed 过的行即固化;将来代码注册表默认值升级,**不会自动改 DB 已有行**(无法区分"运维故意设的"与"当年 seed 的")。可见性补偿:`GET /admin/v1/settings` 同时返回 `default`(代码当前默认)与 `value`(DB 当前值),前端对二者不一致的项显示「已偏离代码默认」标记,由运维自行决定是否跟进。不加额外 schema。
3. 对 6 个 key 各同步 `Raw()` 拿初值 → 构造 breaker / guard / cooldown / gate / router,并 `SetStreamIdleTimeout`。
4. 启动 `settingsApplier` 后台 goroutine(每 5s 刷新推送),随进程生命周期运行,shutdown 时停止。

> 现网核对(2026-07-08):`.env` 中这 6 组**全部等于默认值**(`CIRCUIT_BREAKER_*`/`GATEWAY_STREAM_IDLE_TIMEOUT` 未设置,其余显式值均与默认一致),故 seed 默认值对当前部署**无损**,无需额外迁移。

---

## 5. `db_only` 的连锁改动(审核重点)

选择 `db_only` 后,除新增外还要改动以下现有代码,请确认可接受:

1. **`config.go`**:移除 `CircuitBreakerConfig`、`RateLimitConfig`、`GatewayConfig` 里这 6 组对应字段与其 env 解析 + 校验(约 §539–629、§343–380 等段落)。`config.Config` 变小。
   - ⚠️ 风险:凡引用这些字段的地方都要改。已知引用:`bootstrap/gateway_server.go`、`bootstrap/gateway.go`(`NewMessagesGateway` 传 `deps.Config.CircuitBreaker`/`Gateway`)、`bootstrap/http.go`(`NewRateLimitGuard(cfg)`)、`bootstrap/routing.go`(常量)、`admin_server.go`(只读面板)。
2. **只读配置面板 `adminapi/system_config.go`**:它现在展示这 6 组(熔断/限流默认/stream idle/429 冷却)。`db_only` 后这些不再来自 env——**从只读面板移除,改由新的「可编辑设置」面板承载**(避免同一项两处显示且一处过时)。`system_config.go` 只保留真·启动型 env(HTTP 超时/worker/授权兜底等)。
3. **`.env` / `.env.example`**:删除这 6 组 env(或标注"已迁运行时配置,此处失效")。⚠️ **部署影响**:现有部署 `.env` 里这些值将不再生效;若运维依赖过非默认值,需在后台重新设置(否则回落注册表默认)。这是 `db_only` 的既定代价(用户已确认)。
4. **`admin_server.go` 只读面板依赖**:`RouterDeps` 里 `CircuitBreakerConfig`/`RateLimitConfig` 等若仅供只读面板用,随面板调整一并清理。

---

## 6. Admin UI

- 通用面板:`GET /admin/v1/settings` 已能列出全部注册项(含 6 组)+ 生效值 + 来源;`PUT /admin/v1/settings/{key}` 已能通用写入。**框架就绪,新增项自动出现在列表**。
- 前端「系统设置 → Provider/运行时配置」:从注册表渲染每项(标签/说明/来源/生效值 + 编辑框)。duration/数值/枚举给合适输入控件。
- 每项显示 `hot_reload` 标签;本批 6 组均为「改后约 5s 生效」。
- 响应已含 `default`(代码当前默认)与 `value`(DB/Redis 当前值):前端对二者不同的项显示「已偏离代码默认」标记——这是 §4 seed 固化语义下,发现"代码默认值已升级但 DB 还是旧值"的唯一可见途径。

---

## 7. 风险分析(全是计费邻近热路径,重点)

| 风险 | 说明 | 缓解 |
|---|---|---|
| R1 热路径并发 | breaker/guard/router 每请求读,applier 并发替换 | 用已有锁 / atomic 替换;只换标量,不动进行中状态;加竞态测试(`-race`) |
| R2 坏配置放大故障 | 后台误填(如 failure_ratio=0、rpm=1)即时影响全量流量 | 每项 `Validate` 严格校验;非法值拒绝写入(PUT 返回 400),不落库不推送 |
| R3 db_only 丢 env | 现有 .env 值迁移后失效 | §5 第 3 点明确;启动 seed 写入默认值(§4),且已核对现网 `.env` 全为默认值,无自定义值可丢 |
| R4 初值依赖 DB/Redis | 启动同步读 store,若 DB 挂 | store 已回退注册表默认(不阻断启动);记 warn |
| R5 applier 泄漏/panic | 后台 goroutine | 随 app shutdown 退出;recover + 记日志;失败只跳过本轮 |
| R6 限流 failure_policy 热改 | fail_open/closed 影响安全姿态 | 校验枚举;变更时服务端记一条 info 日志(key+新值)。专门审计表:用户已决不做(§11.7) |
| R7 只读面板/字段移除面大 | 编译期断裂 | `db_only` 改动逐文件编译验证;`go build ./...` 全绿方可 |
| R8 多实例更新不一致 | 熔断 3 实例 / 限流 2 实例,applier 若漏更某个→行为分裂 | 已决(§11.4/§11.5):重构为共享单实例,applier 只更新单点,风险消除 |
| R9 熔断 enabled 热切换 | 禁用时无实例,无法运行期启用 | 已决(§11.4):改"始终构造 + 内部原子开关",enabled 可热改 |
| R10 冷却字段非锁保护 | `defaultCooldown`/`cap` 现不在锁下 | setter 须一并纳入锁/atomic(见 §3.2 注) |

---

## 8. 落地步骤(顺序;虽 6 组一次做,但按可编译/可测的小步推进)

> 每步结束都要 `go build ./...` + 相关包 `go test`(含 `-race`)绿。

1. **注册表 + codec**:`registry.go` 加 6 条 `Definition` + 各 codec + 校验 + 单测(编解码/校验/默认)。此步不接线,零风险。
2. **各消费方加写入口**:breaker `SetConfig` / guard `SetDefaults`+`SetFailOpen` / cooldown `SetCooldown` / gate `SetThreshold` / router `SetDefaultTimeout`(stream idle 复用现有)。各加并发/竞态单测。此步仅新增方法,老路径不变。
3. **settingsApplier**:新增后台组件(拉 6 key → 调各写入口),含 interval、shutdown、recover;单测用假 store 验证"改库→applier→消费方生效"。
4. **启动 seed(§4/§11.2)**:新增 sqlc 查询 `SeedAppSetting`(`INSERT … ON CONFLICT (key) DO NOTHING`)+ `SettingsStore.SeedDefaults(ctx)`(遍历注册表,为缺行写入默认值与 description);gateway 与 admin 的 bootstrap 都调用;单测覆盖"已有行不被覆盖 / 缺行被补齐"。
5. **bootstrap 接线(db_only 切换)**:构造改为从 store 读初值;启动 applier;移除 `config.go` 6 组字段与 env 解析;修所有引用;调整 `system_config.go` 只读面板;熔断/限流按 §3.4 重构为共享单实例。**此步改动面最大,重点编译验证。**
6. **前端**:运行时配置面板渲染 6 组 + 生效值/来源;duration/枚举控件;对 `value ≠ default` 的项显示「已偏离代码默认」标记(§4 默认值升级可见性)。
7. **文档**:更新 `DESIGN-runtime-settings.md`(第一批已落地)、`.env.example`(标注失效)、本文件转"已实施"。
8. **全量验证**:`go build ./...`、`go test ./... -race`(改动包)、前端 `npm run build`;真机重启 gateway+admin,后台改一项→观测 `source=redis`→行为在 ~5s 内变化。

---

## 9. 测试计划

- **单元**:每 codec 的编解码/校验/默认;每消费方 `Set*` 的并发安全(`-race`)与"替换后下次判定用新值";applier 的"库变→推送"闭环(假 store)。
- **回归**:现有 breaker/guard/router/gate 测试保持绿(老读取路径不变)。
- **手动验收**:后台改熔断 `failure_ratio`、限流 `rpm`、`stream_idle_timeout`,各观测 5s 内生效 + `/settings` 的 `source=redis`。

---

## 10. 回退方案

- 代码未接线前(步骤 1–3)零影响,可随时停。
- 接线后如需回退:因 `db_only` 已移除 env,回退 = `git revert` 本批提交(恢复 env 路径)。故**建议本批用独立 commit/PR**,便于整体回退。
- 运行期误配:后台改回或删除该 `app_settings` 行(回落注册表默认),≤5s 生效,无需重启。

---

## 11. 决策记录(2026-07-08 用户已全部拍板)

| # | 事项 | 决策 |
|---|---|---|
| 1 | `applyInterval` = 5s | ✅ 接受 |
| 2 | 现网 env 值迁移方式 | ✅ **改为启动 seed(用户方案)**:所有注册配置必须有默认值;进程启动时逐 key 检查 DB,缺行则写入默认值(`ON CONFLICT DO NOTHING`,绝不覆盖已有行)。详见 §4。现网 `.env` 已核对全部为默认值,无损 |
| 3 | 只读面板移除这 6 组、改由可编辑面板承载 | ✅ 同意 |
| 4 | 熔断器方案 | ✅ 选 (a):重构为共享单实例 + `enabled` 内部原子开关,全热改 |
| 5 | 限流方案 | ✅ 选 (a):`gateway_server` 建单个 Guard 供 service + 中间件共用 |
| 6 | 接受 §3.4 两处共享单实例重构进本批 | ✅ 同意 |
| 7 | 配置变更审计(谁/何时/旧值→新值) | ❌ 不需要(不建审计表;变更可通过面板生效值 + 服务日志观察) |
