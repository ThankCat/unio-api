# Gateway 审查报告处置计划

依据同目录 `REPORT.md`（2026-09-06 全维度只读审查）逐项处置。目标是把报告中 P0/P1/P2 与第十节清理
项全部收口；E-1 分层收敛、E-6 补测、D-4 预聚合按「可交付的最小闭环」实施。

## 范围

### P0

- A-1：`golang.org/x/image` 升至 ≥ v0.45.0；Go 工具链、Dockerfile、`DEVELOPMENT.md` 统一到 1.26.6；
  `govulncheck` 清零。
- E-3：`gofmt -w` 收口全部漂移文件。
- E-4 / C-4：stylua 格式化；`finish.lua` 删除未使用的 `interaction_evidence` 与 `apply_scope` 的
  `is_channel` 参数；`.luacheckrc` 把共享 helper `parse_circuit_breaker_payload` 纳入「按需使用」白名单。
  设计决策：finish 的交互证据校验由 Go 侧 `validateFinishInput` 强制，Lua 不重复裁决；三项事实仍作为
  permit 终态诊断字段落盘。

### P1

- A-2：Admin 登录限速改用可信代理感知的来源 IP。
- D-2：Console 时序 / 汇总接口按「桶数 = 窗长 ÷ 桶宽」设上限。
- D-1：路由 trace 改为收口时单次落库；规划期不再写 partial 行。
- C-1：worker 拆成账务组与探测组两个独立循环。
- B-1：账号终身统计增量改为实收（captured），与 000077 回填口径一致。
- A-5：`last_used_at` 按 Key 节流写入；写失败降级放行。
- D-3：迁移规范写入 `DEVELOPMENT.md`。

### P2 与第十节清理

- B-2 池型渠道成本倍率守卫；B-3 half-up 舍入收敛到 `core/fx`；B-4 capture 币种断言；B-5 汇率兜底计数。
- C-3 HTTP Shutdown 超时后仍限时执行 `app.Shutdown`；C-5 Sticky 脚本改 `redis.NewScript`；
  C-6 proxyclient 解析失败 fail-closed，并把内联的「账号代理 → 渠道代理」决策统一回 `RuntimeProxyURL`。
- A-4 `/metrics` 复用 `GATEWAY_INTERNAL_TOKEN` Bearer 校验；A-6 `seed` 二进制移出 git 并加入 `.gitignore`。
- 10.1 删除 20 条无调用方查询并 `sqlc generate`；`schema_health_checks` 特性连表一起退役（新增 drop 迁移）。
- 10.2 / 10.3(a) / E-5 删除死函数、被取代残留、staticcheck 告警；GAP-3-002 TODO 迁到真实创建路径。
- 10.6 legacy 注释修正。
- E-1 协议 DTO 下沉到 service 层 contract 包，app 层以类型别名过渡。
- E-2 在 AGENTS.md 为 workers 直连 sqlc 给出显式豁免。
- E-6 为 `service/admin/cdkey`、`service/admin/subscriptionaccount`、`app/consoleapi/auth` 补测试。
- D-4 `ChannelsOpsTable` 先分页再补指标。
- E-8 进行中提交收尾：`int8Ptr`、重复 `if err != nil`、to<from 一致性。

## 约束

- 不手改 sqlc 生成文件；改 `sql/queries` 或 `migrations` 后运行 `sqlc generate`。
- 不触碰用户本地 PostgreSQL / Redis 数据；DB / Redis 用例仅在提供隔离资源时运行。
- 保留工作树中已有的用户改动。

## 验证

- `go build ./...`、`go vet ./...`、`go test ./...`、`gofmt -l`、`staticcheck ./...`、`govulncheck ./...`、
  `sqlc diff`、`make check-lua`、`git diff --check`。

## 实施结果（2026-09-06）

全部条目已落地。验证快照：`go build` / `go vet` 通过；`go test -count=1 ./...` 仅单测 116 包全绿；
一次性 PostgreSQL 16 + Redis 7 容器（`unio-review-*`，从零跑完 83 个迁移，结束后已删除）上
`go test -count=1 -p 4 ./...` 全绿；`gofmt -l` 无漂移；`staticcheck` 0 项；`govulncheck` 0 可达漏洞；
`sqlc diff` 无漂移；`make check-lua` 绿；`git diff --check` 干净。

### 与报告结论不一致、按实际代码处置的点

- **10.1 死查询**：`ReserveUserBalance` / `UpdateUserBalance` / `ListProviders` / `ListChannelsByProvider` /
  `UpdateChannelCredential` 仍被 sqlc 层 DB 用例调用，未删，改为标注「仅测试使用」；连同报告列出的 5 条
  测试夹具查询一并标注。实际删除 15 条 + `users.sql`、`schema_health_checks.sql` 两个整文件。
- **10.4-2 写入证据链**：Go 侧并非「无生产调用方」——生产 adapter 经 `WithAttemptTransportTrace`
  （httptrace `WroteRequest`）打点，`MarkRequestWritten` 是文档声明的测试/自定义 transport 入口，
  且 `validateFinishInput` 已在 Go 边界强制「finish 必须携带交互证据」。因此只删 Lua 侧重复计算，
  三个 ARGV 仍作为 permit 终态诊断字段落盘。
- **10.3(a) `NewStateEpochRecoveryTransition`**：是 state_loss / restore 恢复迁移的协议构造器
  （seeder 与 bootstrap 观测都消费该 reason），保留并注明「状态丢失自动检测接线前只由用例驱动」。
- **D-2**：用量页已有 `maxBuckets=1500` 守卫，缺口只在请求汇总的 `compare`；已补 `maxSeriesBuckets`
  并把窗口/桶合法性判定移到任何查询之前（统一 400）。
- **A-2 附带修正**：原实现把 `RemoteAddr` 连端口一起哈希，同一来源的不同临时端口落在不同桶，
  来源维度的限速实际从未累计；现按 IP 分桶。
- **B-2**：除配置入口拒绝池型渠道非零倍率外，结算侧对「池型账号结算出非零服务商成本」加 Warn 告警。
- **E-1**：DTO 及其 JSON 编解码（`decode.go`）整体下沉到 `service/gateway/<protocol>/dto`，app 层以类型
  别名重新导出；service 层生产代码不再 import `internal/app/gatewayapi`（测试仍可为 e2e 装配而 import）。
- **`schema_health_checks`**：开发期迁移验证表，golang-migrate 自带 `schema_migrations` 承担同一职责，
  以 `000083` 退役（down 可重建）。

### 需要同步到 Blueprint 的事实

- 新配置项 `ADMIN_TRUSTED_PROXY_CIDRS`；`GATEWAY_INTERNAL_TOKEN` 非空时 `/metrics` 需 Bearer。
- 路由 trace 每请求单写（无 partial 行）；`partial` / `legacy_sampled` 为历史兼容位。
- worker-server 分 `settlement` / `maintenance` 两个并行 Runner；worker 单实例约束。
- 账号终身统计金额口径 = 客户实扣净额（与 000077 回填一致）。
- 迁移规范：已有大表新索引一律 `CREATE INDEX CONCURRENTLY`，单语句单文件。
- 新指标 `unio_gateway_settlement_recharge_rate_fallback_total`；新错误码 `ledger_currency_mismatch`；
  已删除错误码 `apikey_invalid_user_id` / `apikey_invalid_name` / `apikey_generate_failed` / `apikey_store_failed`。
- 已知边界：provider origin / 代理 host 允许内网地址（Admin 全信任模型下的 SSRF 面）。
