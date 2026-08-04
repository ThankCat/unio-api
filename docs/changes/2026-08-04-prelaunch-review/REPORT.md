# 上线前检查报告（精简版）

检查日期：2026-08-04 · 更新：同日精简  
范围：`unio-gateway`、`unio-blueprint`、`unio-admin`  
说明：已关闭项只保留一行索引；正文只列**仍待处置**的问题。

---

## 已关闭 / 已接受（不再展开）

| 原编号 | 状态 | 处置摘要 |
| --- | --- | --- |
| A-1 依赖漏洞 | 已修复 | `toolchain go1.26.5` + grpc/text 升级；`govulncheck` 可达漏洞 0 |
| A-2 指标未采集 | 风险接受 | 埋点保留、暂不 scrape；后续见蓝图 [metrics.md](https://github.com/unioapi/unio-blueprint/blob/main/docs/specifications/metrics.md) |
| A-3 硬编码 admin token | 代码已修 | 改为用户名口令 + Redis 会话；**仍需你侧**：轮换口令、重建前端产物、清理 git 历史中的旧 token |
| A-4 三表排序 400 | 已修复 | routes/channels/sync-jobs 白名单与 ORDER BY 对齐；前端 `models` 列已可点 |
| B-1 预扣搁浅泄漏 | 已修复 | `stranded_reservation_sweeper` + 巡检 SQL；Blueprint `billing-settlement.md` 已记三方边界 |
| B-2 孤儿清扫提前返回 | 已修复 | reservation 缺失时仍 `MarkRequestFailed` |
| B-3 真实上游 E2E 断言列名过期 | 已关闭 | `internal/blackbox` 整树已从项目移除，断言助手与 starapi 套件不再存在 |
| C-6 blackbox `error.code` 期望不一致 | 已关闭 | 同上，随 blackbox 套件移除 |
| 死代码（§八） | 已删除 | 零调用 settings 读取器、credential 错误码、`CleanupTerminal`、7 条 sqlc 查询、`console/` 占位等；`deadcode`/`staticcheck U1000` 当前干净 |
| 迁移「漂移」 | 非缺陷 | 工作树把 `000040` 索引内联进原表迁移，与开发库 schema 等价；**提交时需对齐已迁移环境的 version=40** |
| 长上下文阶梯计价 | 无缺陷 | 真实 27 万 token 请求 + 单测双向确认 |

---

## 一、仍待处置：高风险

### B-4 `breakdown_ledger_test` 永远过不了

`priority = 1` 违反 `priority % 10 = 0`。守护的「账本行数不放大 dashboard 统计」属性本身成立，自动化保护失效。

### B-5 流式已结算仍返回失败 outcome

`attempt_runner_stream.go`：`settleStreamFacts` 成功后 `params.Stream` 仍报错时，`Outcome` 保持 Failed 并 `return err`。可能 metrics/trace 记失败而 DB 已 capture，且未 `Sticky.BindSuccess`。

### B-6 长上下文预授权用估算、结算用真实 token

估算 ≤ 门槛而真实 > 门槛时，冻结按短单价、扣费按长单价，差额走 overage。上游系统提示会放大偏差（实测 3 token 提问报 `prompt_tokens: 4388`）。

### B-7 长请求 TPM 桶 TTL 到期后 Finish 静默 no-op

`finish_request_admission.lua` 桶键过期直接返回；跨度 ≳450s 的请求可使 TPM 计数偏低。

### B-8 Anthropic 非流式不校验 usage

缺失/null usage 当 0 入账；OpenAI chat 非流式会拒。Responses/chat 流式也有「无 usage 空 outcome」路径，依赖 lifecycle 兜底。

### B-9 Admin 列表批量返回明文凭据

`ChannelsOpsTable` 选 `c.credential`；`ListRequestRecordsPage` 返回 `ak.key_plaintext`。扩大会话泄露后果面。

### B-10 half-open 展示自相矛盾

总分强制 0 但分项保留 → UI 出现 `25+20+…=0`；`probe_only` 与「检查全绿 / 有候选资格」并存。DTO 未暴露 breaker state，前端无法单独修好。

---

## 二、仍待处置：中风险

### C-1 `ReserveIfPresent` 在 session 缺失时静默放过

六条生产路径全走 `ReserveIfPresent`（缺失则 `return nil`）。装配回归时 TPM 预留会静默跳过。

### C-2 成本快照分项舍入与总额 CHECK

单价小数位 > 4 时，分项分别舍入与「未舍入和再舍入」可能不一致，触发 `ck_cost_snapshots_total_amount`。当前库单价均 ≤3 位，未触发。

### C-3 账户级 403 按 (渠道, 模型) 逐对暂停

欠费等渠道级失格只能一对一对停；比例规则最终会开熔断，故非阻断。

### C-4 上游错误原文不进请求审计

设计如此（`ResponseSnippet` 仅渠道检测用）。OpenAI responses adapter **不抓** snippet，与 chat/messages 不一致。

### C-5 `Code.Category()` 对限流码产出枚举外值

`rate_limit_exceeded` → `rate`，`channel_rate_limited` → `channel`，影响日志聚合与兜底文案分支。

### C-7 路由 DB 读失败误报「线路未配置」

非 `ErrNoRows` 被吞，统一成 `ErrRouteNotConfigured`。

### C-8 Partial 结算幂等校验与写入矛盾

写入 `FinalUsageReceived=false`，幂等校验要求为真 → 重放稳定冲突。

### C-9 Admin 前端「前 100 当全量」

`page_size` 硬夹 100；渠道详情 / 毛利表 / 计价器 / 线路表单多处按全量使用，健康渠道或字母序靠后模型会消失。

### C-10 排除原因码只翻译约一半

`reasonLabel` 覆盖不全；`channel_cost_missing` → `渠道cost_missing`。被排除渠道还会多一条虚假「毛利 · not_evaluated」。

### C-11 模型详情「缓存命中率」含 cache write

后端 read+write / input；前端无构成说明，预热期会虚高。

### C-12 会话 token 存 localStorage + CORS `*`

会话可吊销/过期已缓解静态 token 问题，但 XSS/扩展仍可读 localStorage；`Access-Control-Allow-Origin: *` + Bearer 头允许跨源带 token 调 API。无 CSP。

---

## 三、SQL / 性能（待规划）

按流量增长优先：

1. `UpsertRoutingDecisionTrace`（每请求必写，JSONB 反复更新）
2. `FindRouteCandidates`
3. Dashboard `Radar()` / `percentile_cont` 全量排序
4. `ChannelsOpsTable` 分页前重聚合
5. `ListRequestRecordsPage` 的 `COUNT(*) OVER()`
6. 缺索引：`(provider_id, created_at)` on `request_attempts`、`(route_id, created_at)` on `request_records`
7. **口径错误**：部分 Admin 聚合用 `api_keys.route_id`（当前绑定）而非 `request_records.route_id`（请求快照）

补索引应使用 `CONCURRENTLY`。多个 down 迁移是 `DROP TABLE ... CASCADE`。

---

## 四、蓝图缺口（仍待改）

**必须**：`error-semantics.md` 把 concurrency 满误写为 429（实为 503）；Sticky TTL「绝对过期」是**代码注释错**（实现是滑动续期，蓝图对）。

**覆盖缺口**：服务商级熔断共享故障域后果；partial 在缺 `cache_read` 单价时静默退化；长上下文 threshold/乘数字段与输入合计构成。

**编辑**：partial 60/40 仍在多文档重复；`quality.md`/`roadmap.md` 仍 draft。孤儿/搁浅/recovery 三方边界与默认参数已写入 `billing-settlement.md`（lifecycle/ADR 交叉引用）。

---

## 五、风险接受项（产品决策）

- 渠道 credential 明文、API Key `key_plaintext`（Schema 注释已标明）
- 预授权低估 → overage / `authorization_underfunded` 平台核销
- DeepSeek 无法转换字段静默 Drop（DEC-012）
- 指标不采集、告警不投递（见已关闭 A-2）

---

## 六、运维上线清单（未完成项）

- 必配：`DATABASE_URL`（建议 `sslmode=require`）、`ADMIN_USERNAME` / `ADMIN_PASSWORD`、`ADMIN_SESSION_TTL`、`REDIS_*`、`GATEWAY_ENV=production`、`UNIO_SKIP_DOTENV=true`
- 前端生产构建必须提供非 localhost 的 `VITE_ADMIN_API_BASE`（已有构建期断言）
- 迁移用外部工具执行；服务启动**不校验** schema 版本；`000040` 合并后对齐已迁移环境 version
- 探针：Gateway `/healthz`+`/readyz`；Admin 仅 `/healthz`；Worker 无 HTTP 探针
- Redis：**不支持 Cluster**；建议 AOF；state-loss 恢复流程已由 P4 演练验证
- `HTTP_SHUTDOWN_TIMEOUT` 默认 10s，长流式需调大 drain
- 每条线路至少两个不同服务商渠道（今早事故教训）
- A-3 残留：轮换已泄露口令/旧 token、重建 `dist/`、清理 git 历史

---

## 七、验证快照（审查当时）

| 层次 | 结果 |
| --- | --- |
| build / vet / gofmt | 干净 |
| 纯单测 | 73 包 ok |
| 集成（隔离库） | 唯一稳定失败为 B-4 |
| blackbox / starapi | 已整树移除（原 B-3/C-6 随之关闭） |
| P4 故障演练 | 15/15（审查当时；套件随后随 blackbox 移除） |
| unio-admin tsc/eslint/vitest | 全绿 |

审查期间用真实上游手动验证过：OpenAI chat/responses（aihub）与 Anthropic messages（VIP-Claude）端到端成功且计费链完整。
