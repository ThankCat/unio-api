# 渠道凭据失效闸门 + 渠道自动检测 Worker（落地清单，2026-07）

> 本文只做**方案与落地清单**，含逐项可勾选步骤。凡标 **【待确认】** 的地方请先拍板，我据此再动代码。
> 这是 `DESIGN-channel-test.md` 预留的**阶段二**（阶段一「只报告不摘除」已上线：`channeltest` 服务 + `POST /admin/v1/channels/{id}/test` + `last_test_*` 四列）。

---

## 0. 落地状态：✅ 已全部实现（2026-07）

所有确认项（R1(b)+200/env、R2 默认开、C-1/2/3/4/5/6/7/8/9/10）均已按定稿落地：

- 迁移 `000062`（channels.credential_valid）+ `000063`（channel_test_logs），已 `migrate up` 到 63。
- 路由候选闸门 `channel_models.sql` 加 `AND c.credential_valid`；`retry.go` 放行 401 fallback；`breaker.go` 移除 auth（保留 permission）。
- `lifecycle/credential_gate.go` 连续 401 计数（默认 3，env），达阈值经 `bootstrap/credential_invalidator.go` 异步翻失效 + 写 `runtime_401` 日志；挂在两个 attempt runner。
- `channeltest` 公共翻牌+写日志（成功→有效 / credential_invalid→失效；R1(b) 口径，手动总写）；新增 `ListLogs`。
- `workers/channel_test_worker.go`（游标式逐渠道巡检，避免阻塞 runner）+ `bootstrap/worker_server.go` 注册（默认开）+ 每轮保留清理。
- config + `.env.example` 三个 env；admin `GET /channels/{id}/test-logs`；渠道 DTO `credential_valid`。
- 前端：列表「凭据状态」列（有效/凭据失效徽标）+ 沿用现有「检测」hover 最新结果 + 详情「检测」页新增检测日志表。

**验证**：前端 `tsc`+`vite build` 通过；后端 `gofmt`/`go vet`/`go build`/`go test ./...` 全绿；三个 server 二进制均构建通过；临时 admin-server 冒烟：`/channels/ops` 返回 `credential_valid`、手动 `POST /channels/48/test`=200 且写入一条 `manual` 日志、`GET /channels/48/test-logs` 正确分页返回。

> **上线提醒**：`CHANNEL_TEST_WORKER_ENABLED` 默认开，worker-server 一启动即每 30m 全量合成探测（探测与计费/统计隔离，不污染数据、不给客户计费；仅消耗上游额度/限流——R2 已知悉）。

---

## 1. 背景与目标

- 渠道打上游 401（凭据无效/过期/吊销/欠费）时，希望**持久摘除**该渠道，让后续请求自动绕过，且**跨实例、重启不丢**（进程内熔断做不到）。
- 恢复必须**经过一次真实检测通过**才放回，避免拿真用户流量去反复试坏渠道。
- 约束（用户确认）：`(服务商, Key)` 联合唯一 → **一条线路里的候选渠道 key 必然互不相同**，所以不存在「多渠道共用同一把死 key」的场景，无需凭据指纹去重/故障传播。

## 2. 总体设计：三层，各司其职

| 层 | 职责 | 触发 | 恢复 |
| --- | --- | --- | --- |
| **A. 请求内 401 fallback** | 让「发现坏 key 的当次请求」不失败，立即切到兄弟渠道（不同 key）成功 | 上游 401 | —（无状态） |
| **B. DB 凭据失效闸门** | 持久摘除坏渠道；路由候选层直接不选它；跨实例、重启不丢 | 连续 N 次 401（默认 3，env 可配） | 检测通过才翻回 |
| **C. 渠道自动检测 Worker** | 周期性对渠道发合成检测；通过则自动翻回有效、失败（凭据类）则翻为失效 | 定时 | 合成检测请求（非真用户流量） |

**状态轴**：新增「凭据有效性」轴，**与 `status`（enabled/disabled，管理员意图）正交**：
- `credential_valid = true`（默认）：正常，`status=enabled` 时可路由。
- `credential_valid = false`：系统判定凭据失效，**即使 `status=enabled` 也不可路由**，前端显示「凭据失效」。

**状态机**：

```
             连续 3 次 401 / 检测判定 credential_invalid
   valid ───────────────────────────────────────────▶ invalid
     ▲                                                   │
     └───────────────  检测通过（人工或 worker）  ───────────┘
```

---

## 3. 已定决策（用户已拍板）

- **阈值默认连续 3 次 401**，写成环境变量（可配）。
- **渠道自动检测 Worker 本次一并完成**（上次改造遗留项）。
- **C-1 / C-9**：仅 **401** 触发「凭据失效」与请求内 fallback；403 不自动 ban（仍计渠道故障）。
- **C-5**：Worker 检测**所有 `status=enabled` 渠道**（天然覆盖失效渠道以恢复）；**间隔写 env**（`CHANNEL_TEST_WORKER_INTERVAL`）。
- **C-6**：Worker 开关写 env，**默认开**（`CHANNEL_TEST_WORKER_ENABLED=true`）。
- **C-8**：`channels` 表**只加 `credential_valid` 一个布尔**；「何时失效 / 失效原因 / 每次检测结果」全部记入**检测日志表**（见 §5.2b）。运行时 401 自动翻牌也写一条日志（`source=runtime_401`），保证纯布尔之外仍有 when/why 可查。
- **C-10**：渠道**列表加「凭据有效」列**；hover 显示**最新检测结果**（用现有 `last_test_*`）；**渠道详情页可查看检测日志**（worker/手动/运行时事件的历史）。

> **探测隔离性（已核实，非风险）**：`ProbeChannel`（`channel_probe.go:31`）直接调 adapter/HTTP，不走网关生命周期，**不产生 request_records/attempts/usage/ledger、不进成功率与 fault_party 统计**。故 worker 默认开不会污染统计或给客户计费。

## 3b. R1/R2（已定）

- **R1（已定）检测日志保留**：**(b) 只记「失败 + 状态跳变」**（健康 ok 不刷屏）+ **每渠道保留最近 200 条**，均 env 可配（`CHANNEL_TEST_LOG_RETENTION_PER_CHANNEL`，默认 200）。worker 每轮末尾按渠道裁到最近 200。
- **R2（已定）Worker 默认开**：保留默认开；知悉「每 30m 全量真实探测消耗上游额度/限流」这一成本（探测与计费/统计已隔离）。

---

## 4. C-2/C-3/C-4/C-7（已定，均按推荐）

- **C-2 连续计数重置语义**：成功 → 计数清零；401 → +1（到 3 翻失效并清零）；其它失败（超时/5xx/429/bad_request）→ 计数不变。只数「连续的 401」。
- **C-3 计数器位置**：**进程内、按渠道**（镜像熔断器）。多实例下第一个数到 3 的实例翻 DB flag，其余随后从 DB 看到失效即停选。
- **C-4 熔断口径**：**把 auth（401）从 `IsChannelFaultError` 移除**（401 归凭据闸门专管，不再喂进程内熔断）。⚠️ **permission（403）保留在 `IsChannelFaultError` 里**——因为 403 不进闸门（C-1）、也不 fallback（C-9），若也移除就没有任何摘除机制；保留则由熔断做瞬时摘除（20 次窗口/50% 比率触发、30s 冷却半开）。**fault_party 统计不变**（401/403 仍算渠道故障、计成功率，这是 DB 生成列，不动）。
- **C-7 手动检测失败也主动 ban**：管理员点「检测」返回 `credential_invalid` → `credential_valid=false`；通过 → `credential_valid=true`。人工/worker/运行时走同一套翻牌+写日志。

- **V-1【落地时验证，非决策】** `FindRouteCandidates` 是否被缓存？若网关每请求直查 DB 则 flag 即时生效；若有缓存需确认 TTL/失效策略。实现时先确认再定是否需主动清缓存。

---

## 5. 落地清单（分模块，可勾选）

### 5.1 迁移（Schema）— `migrations/000062_add_channels_credential_valid.{up,down}.sql`

> 最新迁移为 `000061_add_request_attempts_fault_party`，本次用 **000062**。
> C-8：`channels` **只加一个布尔**；when/why 全进检测日志表（§5.2b）。

- [ ] `up`：`ALTER TABLE channels ADD COLUMN credential_valid boolean NOT NULL DEFAULT true;`
- [ ] `up`：部分索引，供 worker 快速捞失效渠道：`CREATE INDEX idx_channels_credential_invalid ON channels (id) WHERE credential_valid = false;`
- [ ] `down`：删列 + 索引。

### 5.2 SQLC 查询 — `sql/queries/channels.sql`

- [ ] 所有返回 channel 行的 SELECT 补 `credential_valid`（sqlc 结构体随之更新）。
- [ ] 新增 `SetChannelCredentialInvalid`（置 `credential_valid=false`；**幂等**：`WHERE id=$1 AND credential_valid`，只在 true→false 跳变时写；返回受影响行数以判断是否真的发生了跳变，跳变时才补一条日志）。
- [ ] 新增 `SetChannelCredentialValid`（置 `credential_valid=true`；同样 `WHERE id=$1 AND NOT credential_valid` 仅跳变时写）。
- [ ] 新增 `ListChannelsForCredentialTest`（worker 用；`status='enabled'`）。

### 5.2b 检测日志表（新）— `migrations/000063_create_channel_test_logs.{up,down}.sql` + `sql/queries/channel_test_logs.sql`

> C-5/C-8/C-10 的共同依赖：`channels` 只存当前布尔，**历史（何时/为何失效、每次检测结果）全落此表**；详情页读它。**保留策略见 R1，未定前此表 DDL 里的清理口径先留空。**

- [ ] 表 `channel_test_logs`：`id bigserial pk`、`channel_id bigint`（FK channels，`ON DELETE CASCADE`）、`created_at timestamptz default now()`、`source text`（`worker` / `manual` / `runtime_401`）、`success boolean`、`error_code text`（如 `credential_invalid`）、`http_status int`、`latency_ms int`、`tested_model text`、`credential_valid_after boolean`（本次事件后的状态，便于回放跳变）、`message text`。
- [ ] 索引：`(channel_id, created_at DESC)`（详情页分页查最新）。
- [ ] 查询：`InsertChannelTestLog`、`ListChannelTestLogsByChannel`(分页)、以及按 R1 决定的清理查询（`DeleteChannelTestLogsOlderThan` 或 `DeleteChannelTestLogsBeyondPerChannel`）。
- [ ] **runtime_401 也写这里**：运行时连续 401 翻失效时插一条 `source=runtime_401, success=false, error_code=credential_invalid, credential_valid_after=false`（异步 best-effort）。

### 5.3 路由候选闸门 — `sql/queries/channel_models.sql`（`FindRouteCandidates`）

- [ ] 在现有 `AND c.status = 'enabled'`（`channel_models.sql:131-136`）旁并列加 `AND c.credential_valid`（或 `IS NOT FALSE`）。
- [ ] 重新 `sqlc generate`；确认 `internal/core/routing/router.go` 候选构建不受影响。

### 5.4 请求内 401 fallback — `internal/service/gateway/lifecycle/retry.go`

- [ ] `ProviderErrorClassifier.IsRetryable` 的 `switch` 增加 `case adapter.UpstreamErrorAuth` → 返回 true（仅 401；permission/403 不放行，与 C-1/C-9 一致）。
- [ ] 更新/补 `retry_test.go`：auth 现在可 fallback；permission 仍不可。

### 5.5 运行时「连续 401 计数 + 翻失效」— lifecycle

- [ ] 新增 `CredentialGate`（进程内、按渠道计数），接口类似熔断器：`RecordResult(channelID int64, err error)`，内部：
  - 401 → `count[id]++`；`count[id] >= threshold` → 触发 `store.SetChannelCredentialInvalid`（**异步 best-effort**，别阻塞请求）+ 清零。
  - 成功 → `count[id]=0`。
  - 其它 → 不变（见 C-2）。
- [ ] 挂载点：`attempt_runner.go:230` 附近（`RecordChannelHealth(channelKey, err)` 同处）与流式 `attempt_runner_stream.go:462`，用 `candidate.Channel.ID` 作 key、`adapter.UpstreamCategoryOf(err)` 判类。
- [ ] 阈值取 `Config.Gateway.CredentialInvalid401Threshold`（见 5.7）。
- [ ] 翻失效**真跳变时**（`SetChannelCredentialInvalid` 影响行数=1）补写一条 `channel_test_logs`（`source=runtime_401`，异步 best-effort），保证纯布尔外有 when/why。
- [ ] 若采纳 **C-4**：从 `breaker.go:IsChannelFaultError` 移除 `UpstreamErrorAuth`，并改 `breaker_test.go`。
- [ ] 单测：连续 3 次 401 触发一次翻失效；中间穿插成功则不触发；非 401 失败不影响计数。

### 5.6 渠道自动检测 Worker — `internal/app/workers/channel_test_worker.go`

> 参照 `model_catalog_sync_worker.go` 的 `nextPollAt + interval` 节流（`Unit`/`RunOnce` 模式，非各自 ticker）。

- [ ] 新建 `ChannelTestWorker`：`Name()="channel_test"`；`RunOnce`：到点（`nextPollAt + interval`）则取**所有 `status=enabled` 渠道**（含 `credential_valid=false` 的，以便恢复），逐个调 `channeltest.Service.Test`。
- [ ] 检测结果翻牌 + 写日志（走 §5.8 的公共翻牌函数）：`Success` → `SetChannelCredentialValid`；失败且 `ErrorCode==credential_invalid` → `SetChannelCredentialInvalid`；其它失败只落 `last_test_*`，不动 flag。
- [ ] **R1(b) 写日志口径**：只在「**检测失败** 或 **credential_valid 发生跳变**」时写 `channel_test_logs`（健康且状态未变的成功探测**不写**，避免刷屏）。
  - **【2026-07 修订】已改为每次检测都写一条**：原策略导致检测日志里自动巡检「只剩失败行」，被误读成「自动巡检老是异常」。现成功也留痕（每渠道每轮一条），总量由 `CHANNEL_TEST_LOG_RETENTION_PER_CHANNEL` 控制。
- [ ] **日志清理（R1）**：worker 每轮末尾调 `DeleteChannelTestLogsBeyondPerChannel`，每渠道保留最近 `CHANNEL_TEST_LOG_RETENTION_PER_CHANNEL`（默认 200）条。
- [ ] 并发/限速：逐个串行或小并发，避免瞬时打爆上游。
  - **【2026-07 修订】探测超时改为「渠道 timeout 钳到上限」**：默认 `CHANNEL_TEST_PROBE_TIMEOUT=30s`、上限 `CHANNEL_TEST_PROBE_TIMEOUT_MAX=60s`（旧值硬编码 15s，对慢上游/推理模型中转太紧，会误判超时）。
- [ ] 注册：`bootstrap/worker_server.go` 里仿 `ModelCatalogSync` 条件 append（`if cfg.ChannelTestWorker.Enabled`，**默认开**）；依赖 `channeltest.NewService(queries, adapterRegistry)`（同 `admin_server.go:104`）。
  - [ ] 确认 worker-server 是否已构建 `adapterRegistry`（gateway lifecycle 依赖）；若无需在 `bootstrap/worker_server.go` 补建。**【落地时确认】**
- [ ] 单测：假 prober，成功→翻有效、credential_invalid→翻失效、超时→只落 last_test；每次都写一条日志。

### 5.7 配置 / 环境变量 — `internal/platform/config/config.go` + `.env.example`

- [ ] `GatewayConfig` 加 `CredentialInvalid401Threshold int`，读 `GATEWAY_CHANNEL_CREDENTIAL_401_THRESHOLD`，默认 **3**，`<=0` 报 `CodeConfigInvalid`。
- [ ] 新增 `ChannelTestWorkerConfig{ Enabled bool; Interval time.Duration; ...保留策略参数(见 R1) }`：
  - `CHANNEL_TEST_WORKER_ENABLED`（bool，默认 **true**）
  - `CHANNEL_TEST_WORKER_INTERVAL`（duration，默认 **30m**）
  - 日志保留 env（**待 R1 定**，如 `CHANNEL_TEST_LOG_RETENTION_PER_CHANNEL` 或 `..._RETENTION_DAYS`）
- [ ] `.env.example` 补齐（仿 `MODEL_CATALOG_SYNC_*` 段）。

| Env | 类型 | 默认 | 说明 |
| --- | --- | --- | --- |
| `GATEWAY_CHANNEL_CREDENTIAL_401_THRESHOLD` | int | `3` | 连续多少次 401 翻「凭据失效」 |
| `CHANNEL_TEST_WORKER_ENABLED` | bool | `true` | 是否启用渠道自动检测 worker |
| `CHANNEL_TEST_WORKER_INTERVAL` | duration | `30m` | 巡检间隔 |
| 日志保留（待 R1） | — | — | 每渠道 N 条 / 保留天数 |

### 5.8 翻牌 + 写日志的公共入口 — `channeltest` / admin / worker 共用

- [ ] 抽一个公共「应用检测结果」函数：入参 = channelID + TestResult + source（`worker`/`manual`/`runtime_401`）；内部：按 C-7 翻 `credential_valid`（成功→valid、`credential_invalid`→invalid、其它失败不动 flag）+ 落 `last_test_*` + **按 R1(b)** 决定是否写 `channel_test_logs`（失败 或 flag 跳变才写）。worker、admin 手动检测走它。
- [ ] admin 手动检测 handler（现有 `POST /channels/{id}/test`）改为调用该公共函数（`source=manual`）。手动检测**总是写一条日志**（管理员显式操作，值得留痕），即便成功且状态未变——这是对 R1(b) 的一个例外，仅限手动。

### 5.9 DTO / 前端

- [ ] 后端渠道 DTO（`internal/app/adminapi/channels_ops.go` 等）增 `credential_valid`（布尔）。**不加** `_invalid_at`/`_reason`（在日志里）。
- [ ] **新增 admin 接口**：`GET /admin/v1/channels/{id}/test-logs`（分页，返回 `channel_test_logs`）；service + handler + 路由注册。
- [ ] 前端 `channelsOps.ts`：渠道行类型补 `credential_valid`；新增 `getChannelTestLogs(id, page)` API + 类型。
- [ ] **列表**：加「凭据有效」列（`credential_valid` → 徽标；区别于 `status` 的「停用」）；hover 显示**最新检测结果**（用现有 `last_test_ok`/`last_test_error`/`last_tested_at`）。
- [ ] **详情页**：新增「检测日志」区块（表格，分页），展示 `created_at / source / success / error_code / http_status / latency_ms / tested_model`；「重新检测」按钮调 `POST /channels/{id}/test`，通过即恢复。
- [ ] `tsc` + build 绿。

### 5.10 观测

- [ ] 翻失效/翻有效各记一条结构化日志（channel_id、reason、触发来源=runtime/worker/manual）。
- [ ] 可选：metrics 计数器 `channel_credential_invalidated_total` / `..._recovered_total`。

---

## 6. 测试与验证

- [ ] 后端：`gofmt` / `go vet` / `go build` / `go test ./...` 全绿。
- [ ] 迁移：本地 `migrate up` 到 000063，`\d channels` 确认 `credential_valid`，`\d channel_test_logs` 确认日志表 + 索引。
- [ ] 端到端（手动/脚本）：
  - 构造一条 key 故意填错的渠道 + 同模型另一条好渠道 → 请求：当次 fallback 成功；连续 3 次后坏渠道 `credential_valid=false`、路由不再选它、日志出现 `source=runtime_401` 一条。
  - 改好 key → 触发检测（手动或等 worker）→ `credential_valid=true`、重新可选、日志出现 `source=manual/worker` 成功记录。
- [ ] 前端：渠道列表「凭据有效」列 + hover 最新结果；详情页检测日志区块有记录；「重新检测」可恢复。

---

## 7. 上线顺序（建议）

1. 迁移 000062（channels 加 `credential_valid`）+ 000063（`channel_test_logs`）+ sqlc（5.1/5.2/5.2b）——纯加列/加表，无行为变化。
2. 路由闸门（5.3）——加过滤，默认所有 `credential_valid=true` 不影响现网。
3. 运行时计数 + 翻失效（5.5）+ 401 fallback（5.4）+ 公共翻牌/写日志（5.8）——核心行为。
4. Worker（5.6）+ 配置（5.7）——**默认开**：上线即每 30m 全量探测（探测已验证与计费/统计隔离；R1/R2 敲定后再合此步）。
5. DTO/前端（5.9，含检测日志接口与详情页日志区块）+ 观测（5.10）。

每步 `go build`/`go test`；全部完成后跑一遍端到端。
**注**：步骤 3–5 依赖 R1（日志保留）已定；R2 为默认开的成本知情项。R1 未定前先做 1–2。

---

## 8. 影响面清单（改动文件预估）

- 迁移：`migrations/000062_add_channels_credential_valid.{up,down}.sql`（channels 加布尔）、`migrations/000063_create_channel_test_logs.{up,down}.sql`（日志表）
- SQL：`sql/queries/channels.sql`、`sql/queries/channel_models.sql`、新增 `sql/queries/channel_test_logs.sql`
- lifecycle：`retry.go`、`breaker.go`（若 C-4）、新增 `credential_gate.go`、`attempt_runner.go`、`attempt_runner_stream.go`、`request_lifecycle.go`
- worker：新增 `internal/app/workers/channel_test_worker.go`、`bootstrap/worker_server.go`
- config：`internal/platform/config/config.go`、`.env.example`
- channeltest：`internal/service/admin/channeltest/channeltest.go`（公共翻牌 + 写日志入口）
- admin 接口：`internal/app/adminapi/channel_testing.go`（新增 test-logs handler）、`router.go`（注册 `GET /channels/{id}/test-logs`）、日志查询 service
- DTO/前端：`internal/app/adminapi/channels_ops.go` 等、`unio-admin/src/lib/api/channelsOps.ts`、渠道列表列 + 详情页检测日志区块
- 测试：对应 `_test.go`
