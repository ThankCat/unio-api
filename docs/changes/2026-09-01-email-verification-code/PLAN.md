# 邮件系统改造（验证码批次）— Gateway + Admin

> 蓝图依据：`unio-blueprint/docs/architecture/email-delivery-center.md`（2026-09-01 修订版）。
> 视觉原型：`unio-blueprint/docs/architecture/email-template-previews.html`（品牌珊瑚 #f17462）。
> 前提：开发环境；仅新增表，不动存量数据，不需要历史迁移。

## 已确认决策（本计划的边界）

- 验证码邮件由受理请求的进程（console-server）**同步有界发送**，不建队列、不用 Worker、不自动重试。
- 等待消费的验证码只存 Redis 挑战（HMAC 摘要 + TTL 10 分钟）；**重发间隔 60 秒**。
- 每次发送完成后（成功或失败）**写入邮件发送记录表**，含完整 HTML 正文（2026-09-01 修订，取代早期
  「不落库」约束）。
- SMTP 配置放**系统运行时配置**（app_settings：DB 权威 + 热更新免重启）；配置**可读可写**，Admin
  面板回显当前全部配置（含密码，与渠道凭据同权；2026-09-01 修订，取代同日早先的只写方案）。
- Admin **客户中心新增「邮件」菜单**：列表复用请求中心的 tablecn 组件（搜索/筛选/服务端分页），详情
  沙箱预览 HTML。
- SMTP 客户端库使用 `github.com/wneessen/go-mail`（MIT）；模板为内置中英双语（随代码维护），视觉按
  原型（单栏、内联样式、表格布局、#f17462）。
- SMTP 配置面板提供**测试邮件**（指定收件人真实发送，记录 `email_type=test`，与正式邮件同链路但可区分
  来源）；独立的「连接测试（只验证认证不发信）」后续补充。
- 本次不做：充值/告警邮件、任务队列、模板在线编辑、独立连接测试、退信 Webhook。

## 一、数据库（unio-gateway）

- [x] 迁移 `000066_email_messages`：

  ```sql
  CREATE TABLE public.email_messages (
      id            bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
      email_type    text NOT NULL,            -- verification_register / verification_login / verification_password_reset / test
      recipient     text NOT NULL,            -- 收件人地址
      sender        text NOT NULL,            -- 本封使用的发件地址（为未来多发件箱预留）
      subject       text NOT NULL,
      body_html     text NOT NULL,            -- 完整 HTML 正文（含验证码）
      status        text NOT NULL,            -- sent / failed（CHECK 约束）
      error_summary text,                     -- 失败时的安全错误摘要（不含凭据），成功为 NULL
      locale        text NOT NULL,            -- zh / en
      duration_ms   integer,                  -- SMTP 提交耗时
      sent_at       timestamptz,              -- SMTP 接受时间；failed 为 NULL
      created_at    timestamptz NOT NULL DEFAULT now()
  );
  -- 索引：created_at DESC（列表默认序）、recipient、email_type、status
  ```

  字段对照用户要求：收件人 → `recipient`；发件人 → `sender`；邮件类型 → `email_type`；邮件内容（含
  HTML）→ `subject` + `body_html`；发送时间 → `sent_at`（另有 `created_at` 记录写入时间）。补充字段：
  `status` / `error_summary`（失败可排查）、`locale`（中英模板）、`duration_ms`（SMTP 慢速可观测）。

- [x] sqlc 查询：
  - `sql/queries/console/emails.sql`：`InsertEmailMessage :one`（发送侧写入）。
  - `sql/queries/admin/emails.sql`：`ListEmailMessages :many`（不含 body_html；筛选 email_type /
    status / recipient ILIKE / from / to，`ORDER BY id DESC LIMIT/OFFSET`）、`CountEmailMessages
    :one`（同口径）、`GetEmailMessage :one`（详情，含 body_html）。
  - `sqlc generate` 后提交生成代码。

## 二、SMTP 系统配置（unio-gateway）

- [x] `internal/service/appsettings/email_settings.go` 新增 `email` 域（Category=key 前缀，满足域单测）：
  - 单一 key `email.smtp`（DedicatedControl + HotReload，走标准 SettingsStore 路径：DB 权威 +
    Redis 镜像 + 本地短缓存）：`{enabled, host, port, username, password, tls_policy, sender_name,
    sender_address}`；`tls_policy ∈ {implicit, starttls}`（不提供明文降级选项）；校验 port 1–65535、
    sender_address 为合法邮箱、enabled=true 时 host/sender_address 必填。
    默认 `{enabled:false, port:465, tls_policy:"implicit", sender_name:"UnioAPI", ...}`。
  - 访问器：`EmailSMTP(ctx, store)`（完整配置，含密码，发信与面板共用）。
- [x] `appsettings.Service` 增加 typed 方法：`GetEmailSMTP`（回显完整当前配置，含密码）、
  `SetEmailSMTP(cfg)`（整体校验并保存）。
- [x] Admin API（`internal/app/adminapi/system/email_smtp.go`）：
  - `GET /v1/settings/email-smtp` → 完整当前配置（含密码，可读可写）。
  - `PUT /v1/settings/email-smtp` → 校验并保存；响应同 GET。
  - `POST /v1/settings/email-smtp/test` → `{recipient}`：用当前已保存配置发送测试邮件（内置测试模板），
    记录 `email_type=test`；响应返回 sent/failed 与错误摘要。
  - 通用 `GET /settings` 面板因 DedicatedControl 不出现该 key，统一走专用面板（现有机制，无需改动）。

## 三、发信服务与认证流程接入（unio-gateway）

- [x] 依赖：`go get github.com/wneessen/go-mail`（MIT；补 THIRD_PARTY_NOTICES）。
- [x] 新包 `internal/service/email`：
  - `templates.go`：内置中英模板（注册验证 / 登录验证码 / 密码重置），`html/template` 渲染主题 +
    正文；单栏、表格布局、内联样式、#f17462，与原型一致；变量 `{{code}}`、`{{expires_minutes}}` 等
    默认转义。
  - `mailer.go`：`Mailer.SendVerificationCode(ctx, {Recipient, Purpose, Code, Locale})` 与
    `Mailer.SendTestMail(ctx, recipient)`（admin 测试邮件，模板固定、type=test）：
    1. 现读 `email.smtp`（含密码）；`enabled=false` 返回 `ErrNotConfigured`。
    2. go-mail 构建客户端：隐式 TLS 或 STARTTLS（`TLSMandatory`），整体超时默认 10s（有界）。
    3. 提交后无论成败写 `email_messages`（记录写入用独立短超时 context，发送超时不吞记录）；
       `error_summary` 截断且不含凭据。
    4. 返回发送结果；指标：发送计数/成功率/时延（现有 metrics 体系）。
- [x] 认证流程（`internal/service/console/auth`）：
  - `verification.go`：`Issue` 增返明文验证码（仅内存传递给发信；Redis 仍只存 HMAC 摘要）；
    `ResendAfter` 30 → **60**。
  - `service.go`：`SendChallenge` 增加 `locale` 参数；签发成功后同步调用 Mailer：
    - 发送成功 → 返回 Challenge；
    - `ErrNotConfigured` 且配置了 dev 固定验证码 → 跳过发送（开发环境现状不变）；
    - 其余失败 → 新错误 `auth_verification_delivery_unavailable`（503，可重试），不回滚挑战（用户
      60 秒后重发自动作废旧挑战）。
  - `errors.go` 新增上述错误码；`consoleapi/auth` handler 从 `Accept-Language` 解析 zh/en（缺省 en，
    与蓝图「无语言信息用英文」一致）；同步更新接口签名与三处测试 fake。
  - 限流默认值：`SendEmailPurpose` 首条规则 `{30s,1}` → `{60s,1}`（新部署生效；已 seed 环境需在
    系统设置面板手动同步，计划完成后提示）。
- [x] 装配（`internal/bootstrap/console_server.go`）：构建 Mailer（settingsStore + queries + logger），
  注入 authService（`WithCodeMailer`，不破坏现有构造签名）。

## 四、Admin 邮件列表 API（unio-gateway）

- [x] `internal/service/admin/emaillog`：`List(params) ([]Item, total, error)`（无 body_html）、
  `Get(id) (Detail, error)`（含 body_html）。
- [x] `internal/app/adminapi/email`：`GET /v1/emails`（`page/page_size/email_type/status/recipient/
  from/to`，`WriteList` 信封）+ `GET /v1/emails/{id}`；`router.go` 与 `bootstrap/admin_server.go`
  注册装配（依赖 queries，无新基础设施）。

## 五、Admin 前端（unio-admin）

- [x] 菜单与路由：`AppLayout.tsx` 客户中心组新增「邮件」（`/emails`，MailIcon）；`App.tsx` 懒加载
  `EmailsPage`。
- [x] `lib/api/emails.ts`：`listEmails` / `getEmail` 类型与调用（`{data, meta}` 信封）。
- [x] `pages/EmailsPage.tsx` + `components/openstatus-table/emails-os-columns.tsx`：
  - 列：时间、类型（Badge）、收件人、发件人、主题、状态（sent/failed Badge）、耗时；
  - 筛选：类型 / 状态（select facet）、收件人（text）；nuqs URL 状态 + 服务端分页（对齐
    RequestsList 模式）；
  - 行点击 → 详情 Sheet：事实字段 + 失败原因 + HTML 正文预览（`<iframe sandbox srcDoc>`，隔离脚本，
    符合蓝图「隔离方式预览」）。
- [x] `SystemPage.tsx` 新增「邮件」Tab → `components/system/EmailSmtpPanel.tsx`：
  启用开关 / host / port / TLS 策略 / 用户名 / 密码（回显当前值，默认掩码显示，带显示/隐藏切换）/
  发件人名称 / 发件地址；保存调 `PUT /settings/email-smtp`，热生效无需重启。面板内提供「发送测试邮件」：
  收件人输入 + 按钮，调 `POST /settings/email-smtp/test`，展示 sent/failed 与错误摘要。

## 六、验证

- [x] `sqlc generate` 干净；`go build ./...`、`go vet`、`go test ./...` 全部通过；新增 email 包单测
  （模板中英渲染/转义/回退、失败落库、测试邮件结果编码）。
- [x] 前端 `tsc` / eslint 通过。（列表筛选分页、详情预览、SMTP 面板保存待人工过一遍。）
- [ ] Dev 环境配置真实 SMTP 后端到端：注册发码 → 收件箱收到 → 记录表出现 `sent` 行 →
  Admin 列表可见（可选，需要真实 SMTP 账号）。

## 决策答复（2026-09-01 已确认）

1. 邮件类型用三个细分值：`verification_register` / `verification_login` / `verification_password_reset`
   （另有 `test` 标记测试邮件）。
2. 发送失败的邮件也落库（status=failed，含错误摘要），Admin 列表可筛出失败行。
3. 限流窗口 30→60：改代码默认值后，开发库执行
   `DELETE FROM app_settings WHERE key = 'auth.verification_rate_limits';` 并重启，由 seed 以新默认值重写。
4. 测试邮件本次交付（SMTP 面板内，指定收件人发送）；独立连接测试后续补充。

## 收尾

- Blueprint 已随本计划同步修订（2026-09-01），无需二次收口。
- 开发库已执行：迁移 000066 已应用（版本 65 → 66）；`auth.verification_rate_limits` 旧行已删除，
  下次 console-server 启动由 seed 以 60 秒窗口默认值重写。
- [x] 测试邮件端到端：Resend 通道送达 Gmail 收件箱，模板渲染完整，发送记录可查。
- [ ] Console 注册验证码闭环（发码 → 收码 → 完成注册 → 列表出现 `verification_register` 记录）。

## 服务商接入备注（2026-09-01 实测）

- 最终选型 **Resend**：`smtp.resend.com`，用户名固定 `resend`，密码为 `re_` API Key，域名记录挂
  `send` 子域与 `resend._domainkey`，不与根域收信记录冲突；免费档 3,000 封/月（100/天）。
- 开发机代理会拦截标准 SMTP 出站端口（25/465/587），须用服务商备用端口（Resend `2465` 隐式 TLS，
  实测可用）；生产服务器无此限制。
- SendCloud（aurorasendcloud 国际版）尝试后放弃：免费仅 50 封/天；控制台退订注入无法可靠关闭；
  对 `X-SMTPAPI` 头的后端重组会破坏 multipart 结构（折行则整封静默丢弃）——教训已记录在
  `internal/service/email/mailer.go` 注释中，勿再给 SendCloud 附加该头。
