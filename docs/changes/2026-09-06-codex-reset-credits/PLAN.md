# Codex 账号状态刷新：用量、重置卡（手动 / 自动）与上游台账

## 背景

号池的账号水位一直只能被动采集（请求 / 检测响应头）：暂停中的账号拿不到流量就没有新观测，管理员也看不到
账号持有几张 ChatGPT 发放的 rate-limit reset credit（一张卡同时重置 5h 与 7d 窗口）。sub2api 已有这套能力
（`openai_quota_service` / `openai_quota_auto_reset`），本次按其接口口径改造本项目，代码按本仓库契约重写。

同一批上游接口还能拿到账号台账（套餐、订阅到期 / 续订 / 取消、欠费、停用标记）与用户画像（地区、MFA、组织封禁），
这些之前要么手工维护（订阅到期日）、要么完全看不到。所以「查用量」升级为「刷新状态」：一次拉齐用量、重置卡、
台账、画像，首次导入 / 重新授权 / 令牌刷新后也自动拉一次。

「检测」保持不变：它发真实模型请求并流式展示模型的真实回复，回答的是「这个号的模型能不能用」；
「刷新状态」只问账号面，两者互补。

## 上游契约（真实样例，2026-09-06）

样例均在 `sandbox/codex/wire/samples/`，出站身份统一为 codex-tui 三头（`codexidentity.ApplyUsageHeaders`，
与推理面同源）+ `chatgpt-account-id`，实测 200；不需要 Codex Desktop 的 `openai-beta` 头。

- `upstream-wham-usage.json`：`GET /backend-api/wham/usage`，`rate_limit.primary_window / secondary_window`
  （`used_percent`、`limit_window_seconds`、`reset_after_seconds`、`reset_at`）、`plan_type`、
  `rate_limit_reset_credits.available_count`（另有 `applicable_available_count`，实测为 0 时消费照样成功，
  不代表「此刻能否用卡」，不解析不展示）、`credits`（按量余额）。
- `upstream-wham-reset-credits.json`：`GET /backend-api/wham/rate-limit-reset-credits`，`credits[]{id, reset_type, status,
  granted_at, expires_at, title}` + `available_count`。可用卡 = `reset_type=codex_rate_limits` 且 `status=available`。
- `POST /backend-api/wham/rate-limit-reset-credits/consume`：body `{"redeem_request_id": <uuid>, "credit_id"?: <id>}`，
  响应 `{code, windows_reset, credit}`（形状取自 sub2api，消费路径待真实账号验证）。
- `upstream-accounts-check.json`：`GET /backend-api/accounts/check/v4-2023-04-27`，**必须**带 `Origin: https://chatgpt.com`
  与 `Referer: https://chatgpt.com/`（缺任一 403）。按 `upstream_account_id` 取 `accounts[id]`，只有一项时兜底；
  `entitlement.expires_at` 是订阅到期的权威来源。
- `upstream-me.json`：`GET /backend-api/me`，邮箱、姓名、注册时间、MFA、国家 / 地区、`orgs.data[]{title, personal,
  is_default, role, banned}`。
- `GET /backend-api/subscriptions` 需要 `account_id` 查询参数（否则 400），信息与 entitlement 重叠，未采用。

## 实现

### 数据

- 迁移 `000086`：`subscription_accounts` 新增 `reset_credits_snapshot jsonb`（只存到期时刻，不存卡 id）、
  `auto_reset_credit_enabled`、`auto_reset_credit_mode`（`any` 任一 / `all` 同时）、
  `auto_reset_credit_5h_threshold_percent` / `7d`（NULL = 该窗口不参与触发，CHECK 1~100；开启时至少一个非空）、
  `auto_reset_credit_state jsonb`（脱敏状态机）。
- 迁移 `000087`：新增 `account_profile jsonb`（`quota.Profile` 的 JSON：`account / subscription / user / credits`
  四组 + `fetched_at` + 各来源的 `errors`）。
- sqlc（`sql/queries/shared/subscription_account_quota.sql`）：`UpdateAccountResetCreditsSnapshot`、
  `UpdateAccountProfileSnapshot`、`UpdateAccountSubscriptionFacts`（COALESCE 回写 `plan_type` /
  `subscription_expires_at`）、`UpdateAccountAutoResetCreditConfig`、`UpdateAccountAutoResetCreditState`、
  `ListAutoResetCreditAccounts`；`AdminListSubscriptionAccounts` 连表带出频道阈值与上述所有快照。

触发条件为什么不是固定的「或」：一张卡同时重置 5h 与 7d，5h 几小时内自己就恢复，只因 5h 打满就用卡会浪费周重置的
价值；但 7d 打满账号会被锁到下周，纯「与」又会让账号干等。所以每个窗口是否参与、多个窗口是「或」还是「与」
由运营按账号决定（典型配置：只填 7d 90%，5h 留空）。

### 账号面（`internal/service/subscription/quota`）

- `Client`：五个端点的最小解码（按真实样例，不做多别名容错）；非 2xx 返回 `*UpstreamError`（状态码 + 截断正文）。
- `Profile`（`profile.go`）：把 accounts/check + me + usage.credits 归一为落库 / 展示模型；`Abnormal()` 归纳
  「上游已停用账号 / 所属组织已被封禁 / 订阅欠费 / 无有效订阅」供管理端状态列标红。
- `Service.QueryUsage`：窗口水位经 `health.Recorder.RecordAccountUsageObservation` 落 `usage_snapshot` 并按生效
  阈值评估暂停 / 恢复（与请求路径同一口径），卡数与到期明细写 `reset_credits_snapshot`；卡明细端点失败不阻断。
- `Service.Refresh`：`QueryUsage` + 台账 / 画像；两个画像端点各自失败只记入 `profile.errors`，成功则写
  `account_profile`，并用 `check.PlanType` / `entitlement.expires_at` 回写套餐与订阅到期（覆盖手工值，
  管理端以 `subscription_source = upstream / manual` 标出来源）。`RefreshMany` 供导入后后台并发刷新。
- `Service.ResetCredit`（手动，由上游挑卡）与 `ConsumeTargeted`（自动，定向卡 + 调用方给的幂等 redeem id）；
  消费后回读用量，新水位经观测链路解除用量暂停。
- `AutoResetWorker`（worker-server，单实例）：每分钟评估开启自动用卡的启用账号——本地快照触顶或陈旧
  （>10 分钟）才打上游；上游现查按 mode 组合参与窗口（any 任一 / all 同时）确认达到阈值且
  `available_count > 0` 才消费，选最早到期的卡；
  redeem id 由「账号 + 卡指纹 + 周期指纹（两个窗口的 reset_at）」确定性派生，同周期重试复用同一张卡与幂等键，
  原卡消失拒绝换卡；状态机 checking → available / resetting → success / no_credit / failed 落库供展示。

### 自动触发刷新

- 文件导入（`ImportFile`）：导入成功后后台 `RefreshMany`（并发 2，`context.WithoutCancel`），不阻塞响应。
- OAuth 完成（`CompleteOAuth`，新导入与重新授权）：同步 `Refresh` 后再回读账号，弹窗关闭时列表即有完整台账。
- 令牌刷新 worker（`RefreshWorker.WithAfterRefresh`）：每次成功换到新 access token 后顺手刷一次状态，
  台账随令牌周期（约 10 天）自然保鲜。

### 管理端（unio-gateway admin）

- `POST /subscription-accounts/{id}/refresh`（替代原 `usage/refresh`）：刷新状态，返回 `{report{usage, credits,
  profile, …}, account}`。
- `POST /subscription-accounts/{id}/reset-credit`：手动用一张卡，返回 `{outcome{result, report, refresh_error}, account}`；
  上游拒绝（无可用卡 / 窗口未打满）以 502 带上游正文返回；归档账号拒绝。
- `PUT /subscription-accounts/{id}/auto-reset-credit`：`{enabled, mode, threshold_5h_percent, threshold_7d_percent}`
  （阈值 null = 不参与；开启时至少一个参与）；仅 OpenAI 账号可开启。
- `PUT /subscription-accounts/{id}/usage-pause-threshold`：账号级用量暂停阈值（见 `2026-09-06-usage-pause-threshold`）。
- 账号视图新增 `reset_credits`（快照）、`auto_reset_credit`（配置 + 生效阈值 + state）、`profile`、`subscription_source`。

### unio-admin

- 行菜单固定顺序：编辑 / 停用（启用）/ 检测 / 刷新状态 / 用量阈值 / 自动重置 / 刷新令牌 / 重新授权 / 订阅台账 / 请求记录。
  手动「使用重置卡」不再占菜单位，放进「自动重置」弹窗底部（确认框说明消耗 / 剩余，无卡禁用）。
- 「检测」过程先打「请求：hi（模型）」再打「响应：…」：探测输入 `adapter.ProbeInputText` 随 `test_start` 事件
  （`prompt` 字段）透出，前端不再硬编码。
- 「重置卡」列取消，并入「套餐」列：第一行套餐徽章，第二行票券图标 + `重置卡 N 张`（0 张写「无重置卡」，
  未刷新不显示），开启自动用卡时追加带状态色的「自动」标记；详情给订阅计划、计费周期、订阅状态、促销、
  账号结构、曾付费、按量 credits、每张卡到期、自动用卡配置 / 状态 / 最近结果 / 失败码。
- 「订阅」列只显示 `MM/DD HH:mm`；剩余时长、来源（上游 / 手工）、续订时间、取消生效、宽限期、折扣到期都在详情。
- 「账号」列详情追加画像：姓名、地区、MFA、注册时间、邮箱类型、组织与角色、上游账号状态、算力驻留、拉取失败项。
- 「状态」列：画像异常（停用 / 封禁 / 欠费 / 无订阅）时显示 `启用 · <原因>` 并标红。
- 「用量暂停阈值」「自动使用重置卡」从账号编辑框拆成两个独立弹窗（行菜单入口），编辑框只保留调度参数
  （头尾固定、中间滚动、字段两列）。

## 验证

- Go：build / vet / test（含 `-race` 的导入后台刷新用例）/ gofmt / staticcheck / deadcode（无新增）/ `sqlc diff` /
  `make check-lua` 全绿；新增 quota 包单测（client httptest 含 Origin/Referer 断言、service 含画像落库与
  订阅事实回写、auto reset 状态机）与 admin 服务测试。
- dev：迁移 85 → 87（86 down/up 回放）；真实账号只读验证：`POST /subscription-accounts/1/refresh` 200，
  用量 0% / 71%，卡快照 `available_count=2`（两张、到期 2026-10-04），
  画像 Plus / 月付 VND / 促销 `plus-1-month-free` 100% / JP Tokyo / MFA 开 / 个人组织 owner，
  `subscription_expires_at` 由上游回写为 `2026-10-02T17:53:17Z`，`subscription_source = upstream`。
- unio-admin：`bun run check`（typecheck / lint / 302 tests）全绿。
- 真实消费（用户手动点「使用重置卡」，窗口 0% / 71%、`applicable_available_count = 0`）：上游接受，5h / 7d
  两个窗口从消费时刻重新起算（reset_at = 消费时刻 + 5h / + 7d），卡数 2 → 1（消耗的是较早到期那张），
  运行态无冷却 / 无暂停 / 无在途，worker 日志无告警；用户随后开启自动用卡（7d ≥ 100%），worker 在快照过期
  （10 分钟）后主动查了一次上游并回写 `usage_snapshot.captured_at` 与 state `available`（1 张、未触发），
  证明无流量账号的观测盲区由它补齐且不会每分钟打上游。
- **待人工验证**：自动用卡在窗口真实打满时的消费行为（账号 1 已开启：7d ≥ 100%，任一模式）。

## Blueprint 同步清单

- 号池：账号面第四个出站面（/wham/*、accounts/check、me）与 codex-tui 身份复用；刷新状态 vs 检测的分工。
- 订阅到期的权威来源改为上游 entitlement，手工值只在未刷新过时生效。
- 重置卡语义：一张卡同时重置 5h 与 7d，窗口未打满也能用（从消费时刻重新起算）；只看 `available_count`。
- 自动用卡规则：账号级开关 + 5h/7d 各自阈值（留空不参与）+ any/all 组合、最早到期优先、同周期不换卡、失败保持暂停。
- 自动刷新时机：导入后 / OAuth 完成 / 令牌刷新后。
- 新接口四条与账号视图四个新字段。
