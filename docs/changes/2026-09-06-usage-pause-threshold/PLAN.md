# 账号用量暂停阈值：三层继承 + 即时生效

## 背景

号池的用量自动暂停原来只有一个全局阈值（`gateway.account_usage_pause_threshold_percent`），且拦截只看
Redis 里观测时写下的 `usage_pause` 标记。实测踩到的问题：把阈值从 90 调到 100 后，一个 90% 水位的账号
仍然被旧标记锁到窗口重置——被暂停的账号拿不到流量，也就不会再有新观测去触发 Resume，阈值调整形同没改。

本次把阈值改成「账号 → 池型渠道 → 全局」三层继承，拦截改为按账号最近快照与生效阈值实时判定，并在任一
层阈值变更后按同一规则重算 Redis 标记，让展示与调度一致。

## 口径（已与产品确认）

- 值域：账号、渠道两层 `NULL`（继承上一层）或 `1~100`；全局 `1~100`。**不接受 0**，「基本不拦」= 设 100
  （只在完全打满时暂停）。
- 优先级：账号阈值 > 渠道阈值 > 全局阈值。渠道阈值只对 `supply_form = pool` 有意义，credential 渠道不显示、
  后端拒绝、DB CHECK 兜底。
- 拦截以「账号 `usage_snapshot` 水位 ≥ 生效阈值 且 `now < reset_at`」实时判定（比较用 `>=`）；Redis
  `usage_pause` 标记降级为展示缓存，不再参与调度。
- 任一层阈值变更后立即重算运行态：全局 → 全部启用中的池内账号；渠道 → 该渠道账号；账号 → 该账号。
  重算失败不回滚已保存的阈值（拦截不依赖它），只在响应里以 `runtime_refresh_error` 报出。
- 账号阈值是独立接口（不随调度参数 PATCH 提交），创建账号时默认 `NULL`。

## 实现

### 数据

- 迁移 `000085_usage_pause_thresholds`：`channels.account_usage_pause_threshold_percent`、
  `subscription_accounts.usage_pause_threshold_percent`，均 `integer NULL`，CHECK 1~100；渠道列另加
  `supply_form = 'pool' OR IS NULL` 的 pool-only CHECK（与 `account_default_concurrency` 同规则）。
- sqlc：`ListSchedulableAccountsByChannel` 增选两列（热路径随账号行一起读出）；新增
  `GetAccountUsagePausePolicy`（观测时解析生效阈值）、`AdminListUsagePauseReconcileAccounts`
  （重算范围列表，可按渠道/账号收窄）、`UpdateChannelAccountUsagePauseThreshold`、
  `AdminUpdateSubscriptionAccountUsagePauseThreshold`；`CreateChannel` / `ListChannelsPage` /
  `AdminListSubscriptionAccounts` 带出新列。

### 判定器（`internal/core/accountusage`）

纯函数，三处共用：`ParseSnapshot`（`usage_snapshot` 的唯一 JSON 定义）、`ResolveThreshold(account, channel,
global)`（三层继承 + 来源）、`Evaluate(snapshot, threshold, now)`（主窗口优先；`reset_at <= now` 视为不暂停；
缺失窗口视为不限）。

### 网关拦截（`lifecycle/account_pool.go`）

`loadAccountPool` 对每个账号行按快照与生效阈值派生 `UsagePauseRemainingMs`，**覆盖** Redis 的用量暂停字段
（冷却 / 临时不可调度 / 在途仍以 Redis 为准）。全局阈值经 `WithAccountUsagePauseThreshold` 注入
（appsettings 本地缓存 3s），阈值改动对下一次请求即生效。

### 观测（`subscription/health.Recorder`）

- 观测时按账号读两层覆写 + 全局解析生效阈值（读库失败退回全局并 warn）。
- 快照落库时把相对重置秒数换算为绝对 `reset_at`：调度侧按快照实时判定，缺绝对时刻会把高水位误判为已重置。

### 重算（`subscription/health.Reconciler`）

`ReconcileAll / ReconcileChannel / ReconcileAccount` 读出范围内账号的快照与两层覆写，按 `Evaluate` 结果
覆盖式写 Pause 或 Resume；无快照跳过、单账号 Redis 失败计 `Failed` 不中断；返回
`{scanned, paused, resumed, skipped, failed}`。

### 管理端（unio-gateway admin）

- 全局：`appsettings.Service.WithWriteHook` 在普通 setting 写入成功后触发；bootstrap 为阈值 key 注册
  `ReconcileAll`，结果附在 `PUT /settings/{key}` 响应的 `runtime_refresh`。
- 渠道：`POST /channels`、`PATCH /channels/{id}` 增 `account_usage_pause_threshold_percent`
  （PATCH 缺省=不改、`null`=继承全局、1~100=覆写；仅池型）；真变化才写库并 `ReconcileChannel`。
- 账号：`PUT /subscription-accounts/{id}/usage-pause-threshold`，body
  `{"usage_pause_threshold_percent": null | 1..100}`，响应 `{account, runtime_refresh, runtime_refresh_error}`。
  普通列更新 + `config_revision + 1`，不经渠道容量 control 两阶段发布（不是容量围栏参数，候选每请求读库）。
- 账号视图新增 `usage_pause_threshold_percent`、`effective_usage_pause_threshold_percent`、
  `usage_pause_threshold_source`（account / channel / global）。

### unio-admin

- 渠道表单：池型显示「账号用量暂停阈值（%）」（留空继承全局，1~100），credential 不渲染也不提交。
- 账号配置对话框：独立小节「用量暂停阈值」，自己的保存按钮 / 回车调独立接口，显示当前生效值与来源，
  保存后提示重算结果。
- 账号列表水位条与提示改用每账号生效阈值与来源，不再读全局 setting。
- 系统设置保存提示带重算统计。

## 验证

- `go build / vet / test ./...`、gofmt、staticcheck、deadcode（无新增）、`sqlc diff`、`make check-lua` 全绿。
- dev 库迁移 84 → 85 并 down/up 回放；现场复现：账号 1（90% 水位）在全局 100 下仍被旧标记锁定 →
  保存全局阈值触发 `runtime_refresh{scanned:1, resumed:1}`，Redis 标记清除；账号阈值设 80 → 立即
  `paused:1` 并写回标记；设回 `null` → `resumed:1`；渠道阈值 85 → 账号生效来源变为 `channel` 并暂停；
  `0` 在三层均被 400 拒绝。
- unio-admin：`bun run check`（typecheck / lint / 282 tests）全绿。

## Blueprint 同步清单

- 号池用量暂停：三层继承语义（账号 > 渠道 > 全局，NULL 继承、1~100、无 0）。
- 拦截口径：按快照 + 生效阈值实时判定，Redis `usage_pause` 仅为展示缓存；阈值改动对下一次请求即生效。
- 新接口：`PUT /subscription-accounts/{id}/usage-pause-threshold`；渠道 create/update 的
  `account_usage_pause_threshold_percent`；`PUT /settings/{key}` 响应新增 `runtime_refresh`。
- 账号视图字段：`usage_pause_threshold_percent` / `effective_usage_pause_threshold_percent` /
  `usage_pause_threshold_source`。
