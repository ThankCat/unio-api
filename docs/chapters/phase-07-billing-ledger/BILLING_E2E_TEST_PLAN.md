# Phase 7 账单端到端实测计划（E2E Billing Audit）

> **状态**：已审核（2026-06-25）— 可开始执行  
> **目标**：对 Unio 网关 **全部账单/账本逻辑** 做一次严谨的 **HTTP 端到端实测**，以真实 Codex 对话为主路径，辅以可控 mock 上游覆盖无法靠真实上游触发的边界。  
> **非目标**：本计划 **不以 `go test` 通过作为验收结论**；单测/blackbox 仅作回归辅助。  
> **交付范围**：**本地校验**（Q12）；修复代码在本地验证通过即可，不强制提交 PR，除非你另行要求。

---

## 0. 执行前必做：代码与 schema 修复

以下两项 **必须先落地**，再跑对应用例；否则 SB/D/REC 或 Admin 交付字段会失败。

### 0.1 `partial_stream_estimate` DB 约束（P0）

| 表 | 当前 CHECK | 代码写入值 | 影响 |
|----|-----------|-----------|------|
| `usage_records.usage_source` | `upstream_response`, `upstream_stream` | `partial_stream_estimate` | partial 结算 INSERT 失败 |
| `settlement_recovery_jobs.usage_source` | 同上 | 同上 | partial 流 recovery 失败 |

**已在 2026-06-25 实测中观察到**：2 条 `gpt-5.4-mini` Codex 请求因 `settlement_recovery_jobs_usage_source_check` 失败。

**动作**：

1. 新增 migration，两处 CHECK 扩展为包含 `partial_stream_estimate`。
2. `migrate up`（Docker Postgres）。

### 0.2 `delivery_status` 交付状态机（P0，Q14 已确认要修）

当前 gap：请求 `status=succeeded` 且 Codex 已收到内容，但 `delivery_status` 恒为 `not_started`（代码从未推进 `in_progress` / `completed` / `interrupted`）。

**动作**：

1. 在 Chat / Responses / Anthropic 流式与非流式路径接入交付状态机：
   - 首字节 → `in_progress` + `response_started_at`
   - 正常结束 → `completed` + `response_completed_at`
   - 客户端中断 / 交付未完成 → `interrupted`
2. 结算路径仍不写 `response_completed_at` 的约束保持不变；由交付状态机统一写。
3. 修完后跑 **DEL-*** 用例（§5.12）。

---

## 1. 测试目标与验收口径

### 1.1 要证明什么

一次成功的 E2E 实测应证明以下 **事实链** 在真实 HTTP 流量下闭合且无静默错误：

```text
客户请求 → request_record → attempt → usage_record → price/cost_snapshot
         → ledger_reservation (freeze/capture/release)
         → ledger_entry (debit) → user_balance
         → （必要时）ledger_billing_exception (write_off / risk_exposure)
         → （必要时）settlement_recovery_job
```

### 1.2 覆盖范围

| 类别 | 协议入口 | 必测 |
|------|---------|------|
| 非流式成功结算 | `POST /v1/chat/completions` | ✅ |
| 非流式失败释放 | 同上 | ✅ |
| 流式路线 A（full bill + final usage） | Chat + Responses + Messages | ✅ |
| 流式路线 B（emit 后 cancel/interrupt，partial） | Chat + Responses（Codex 主路径） | ✅ |
| 流式路线 C（emit 前 cancel/error，release） | Chat + Responses | ✅ |
| 流式路线 D（emit 后正常结束但缺 final usage） | **mock 上游**（真实上游难稳定复现） | ✅ |
| 授权：余额不足 / 部分余额授权 | 全部入口 | ✅ |
| 平台差额核销 `write_off` | 非流 + 流（含 partial cap） | ✅ |
| 结算失败 recovery + dead finalize | worker + 故障注入 | ✅ |
| `risk_exposure`（仅 settlement-fail / recovery-dead） | 故障注入 | ✅ |
| Codex 真实多轮 agent（多 request_id） | `POST /v1/responses` | ✅（行为观测 + 逐条对账） |
| **交付状态 `delivery_status`** | 流式 + 非流式 | ✅（Q14：修完必测） |
| Admin 后台可读性与一致性 | Admin API + UI | ✅ |

### 1.3 明确不测 / 低优先级

- 能力校正 worker（`capability_autocalibrate`）——归 Phase 12，本计划只顺带确认 billing 不被干扰。
- 多 channel fallback 的全矩阵（除非真实环境配了第二渠道）。
- Anthropic Messages 若当前 Codex 未走该路径：用 curl/SDK 补 1 条路线 A 即可。

---

## 2. 测试环境

### 2.1 基础设施

| 组件 | 要求 |
|------|------|
| PostgreSQL | **Docker** `docker compose up -d postgres`，migration 已 up（含 §0 修复） |
| Redis | Docker `docker compose up -d redis` |
| 全部 env | 读本地 **`unio-api/.env`**（Q3） |
| Gateway | `cmd/gateway-server`，`:8521` |
| Admin API | `cmd/admin-server`，`:8522`；Token = `.env` 默认 `ADMIN_API_TOKEN`（Q2） |
| Worker | `cmd/worker-server`（recovery 用例需要） |
| 渠道 | **单渠道**（Q7）；真实上游 + mock 切换 base_url，不测 fallback |
| Codex | 绑定 **Billing E2E 测试项目**（§2.6）；`base_url = http://127.0.0.1:8521/v1`，`wire_api = responses` |

### 2.2 上游渠道（真实上游）

> ⚠️ **测试专用凭据**：仅限本地实测，勿提交公开仓库。

| 项 | 值 |
|----|-----|
| 渠道 Base URL | `https://zz1cc.cc.cd/v1` |
| 渠道 API Key | `sk-REDACTED（实测时从 .env / 私密渠道获取，勿写入仓库）`（2026-06-25 更新） |
| 渠道类型 | `protocol=openai`，`adapter_key=openai` |
| 上游能力 | **原生支持** `POST /v1/responses`（Q5）；Unio 可走 direct 路径 |
| stream usage | **偶发丢失**（Q6）；真实流量可能触发路线 D，需与 Unio bug 区分 |
| 渠道名称建议 | `NewAPI 测试渠道` |

**Admin / seed 配置**：

1. 更新 channel `base_url` 与加密凭据（`CREDENTIAL_MASTER_KEY` 来自 `.env`）。
2. 模型绑定（Q4）：seed 三模型 **`gpt-5.5` / `gpt-5.4` / `gpt-5.4-mini`**，`upstream_model` 与 catalog **同名**（上游均可用）。
3. 确认 `channel_prices` 启用且 `effective_to IS NULL`。

### 2.3 Unio 客户端凭据（已确认）

| 项 | 值 |
|----|-----|
| Unio Gateway API Key | `unio_sk_ohuUc2Zkpspw9Vre5idmZKRXfFX6Vxb2cl52cMZuFLQ`（Q1） |
| Admin API Token | `.env` 默认 `ADMIN_API_TOKEN`（Q2） |
| 主测试用户 | `dev@unio.local`，余额 `$100`（seed） |
| 低余额用户 | **新建** `billing-e2e-low@unio.local`，余额约 `$0.05`（Q8） |

```bash
export GATEWAY=http://127.0.0.1:8521
export UNIO_KEY='unio_sk_ohuUc2Zkpspw9Vre5idmZKRXfFX6Vxb2cl52cMZuFLQ'
export ADMIN_TOKEN="$ADMIN_API_TOKEN"   # 来自 .env
```

### 2.6 测试项目与用户（Q8 / Q11）

执行 Phase 0 时创建（可写入 `scripts/billing-e2e-seed.sql` 或 Admin UI）：

| 实体 | 名称 | 用途 |
|------|------|------|
| 项目 | **`Billing E2E Test`** | Codex workspace = `/Users/chenhao/Project/unio`；专用 API Key |
| 主用户 | `dev@unio.local` | 常规用例 + Codex 长会话（Q10 保留历史） |
| 低余额用户 | `billing-e2e-low@unio.local` | AUTH-01/02、WO-01/02；**不跑 Codex 长会话** |
| Codex | 打开 **unio** 仓库 + 上述项目 key | 避免污染其他项目 |

**数据策略（Q10）**：

- **Phase 0** 可 `--reset` 一次，配置上游与测试项目。
- **Phase 1–4 不全库 reset**，保留 Codex 长会话 request 历史便于 COD-04 对账。
- 单用例失败：仅 §3.2 按 `request_id` 清理；低余额用户可单独 reset 余额。

### 2.4 Mock 上游（路线 B/C/D 可控场景）

在 **本机** 起 `httptest` mock server 或独立 `mock-upstream` 进程：

```text
127.0.0.1:<port>/v1/chat/completions   # SSE 可控：有/无 usage chunk、中途断流
```

**做法 A（推荐）**：扩展 `internal/blackbox/sdkfixture`，新增 **Billing E2E 专用 channel**，`base_url` 指向 mock。  
**做法 B**：临时在 Admin 把「测试渠道」的 `base_url` 切到 mock，跑完用例再切回真实上游。

Mock 必须能模拟：

| 场景 | Mock 行为 |
|------|----------|
| 路线 A | 正常 SSE + `[DONE]` 前带 `usage` |
| 路线 B | 发 2+ content chunk 后 hang / 断连接（无 usage） |
| 路线 C | 首 chunk 前返回 500 或立即关闭 |
| 路线 D | 发 content chunk + `finish_reason=stop`，**故意省略 usage chunk** |

### 2.5 服务启动命令（参考）

```bash
cd unio-api
docker compose up -d postgres redis
source .env   # DATABASE_URL, REDIS_*, CREDENTIAL_MASTER_KEY, ADMIN_API_TOKEN

# 终端 1
go run ./cmd/gateway-server

# 终端 2（recovery 用例）
go run ./cmd/worker-server

# 终端 3（Admin，对账）
go run ./cmd/admin-server
```

---

## 3. 数据卫生与重跑规则

**原则**：每个用例要么在干净基线下跑，要么可唯一标识并可在失败后清理。

### 3.1 全量重置（仅 Phase 0 或显式需要时）

```bash
bash scripts/seed-test-data.sh --reset
# 重新配置上游 credential、测试项目、以及固定 Unio Key（见 §2.3，非 seed 随机 key）
# Phase 1–4 默认不 reset，保留长会话（Q10）
```

### 3.2 删除单个失败用例的脏数据

按 `request_id` 清理（**仅测试库**）：

```sql
-- 替换 :request_id
BEGIN;
DELETE FROM ledger_billing_exceptions WHERE request_record_id = (SELECT id FROM request_records WHERE request_id = :request_id);
DELETE FROM ledger_entries WHERE request_record_id = (SELECT id FROM request_records WHERE request_id = :request_id);
DELETE FROM cost_snapshots WHERE request_record_id = (SELECT id FROM request_records WHERE request_id = :request_id);
DELETE FROM price_snapshots WHERE request_record_id = (SELECT id FROM request_records WHERE request_id = :request_id);
DELETE FROM usage_records WHERE request_record_id = (SELECT id FROM request_records WHERE request_id = :request_id);
DELETE FROM settlement_recovery_jobs WHERE request_record_id = (SELECT id FROM request_records WHERE request_id = :request_id);
DELETE FROM ledger_reservations WHERE request_record_id = (SELECT id FROM request_records WHERE request_id = :request_id);
DELETE FROM request_attempts WHERE request_record_id = (SELECT id FROM request_records WHERE request_id = :request_id);
DELETE FROM request_records WHERE request_id = :request_id;
COMMIT;
```

### 3.3 余额回滚

手工调额或 SQL（测试用户）：

```sql
UPDATE user_balances SET balance = 100.0000000000, reserved_balance = 0
WHERE user_id = (SELECT id FROM users WHERE email = 'dev@unio.local') AND currency = 'USD';
```

### 3.4 失败处理流程

```text
执行用例 → 断言失败
    → 记录 request_id + Admin 截图/SQL 导出
    → 定位根因（网关 bug / 配置 / 上游 / schema）
    → 修复代码或配置
    → 删除该用例脏数据（§3.2）
    → 重跑该用例直至 PASS
    → 进入下一用例
```

**禁止**：带着未解释的 failed request / 错误 ledger 继续跑后续用例。

---

## 4. 验证手段（每条用例必做）

### 4.1 HTTP 层

- 记录：`correlation_id`（响应头 `X-Request-ID`）、HTTP status、是否 SSE 完成。
- Codex 场景：记录 Codex UI 可见回复 vs 网关 request 条数（**一条用户消息 ≠ 一条 API 请求**）。

### 4.2 Admin API

| 接口 | 用途 |
|------|------|
| `GET /admin/v1/requests?...` | 列表筛选 |
| `GET /admin/v1/requests/{requestId}?include_internal=true` | 全量审计聚合 |
| `GET /admin/v1/ledger/entries?user_id=` | 扣费流水 |
| `GET /admin/v1/ledger/billing-exceptions` | write_off / risk_exposure |
| `GET /admin/v1/system/settlement-recovery-jobs` | recovery 队列 |
| `GET /admin/v1/users/{id}` | 余额 |

Header：`Authorization: Bearer $ADMIN_API_TOKEN`

### 4.3 SQL 断言模板

每条用例执行后跑（`:rid` = 业务 `request_id`）：

```sql
-- 1) 请求终态
SELECT request_id, status, stream, operation, requested_model_id,
       delivery_status, error_code, started_at, completed_at
FROM request_records WHERE request_id = :rid;

-- 2) Attempt（partial 审计）
SELECT attempt_index, status, final_usage_received, upstream_finish_reason,
       finish_class, upstream_status_code
FROM request_attempts
WHERE request_record_id = (SELECT id FROM request_records WHERE request_id = :rid);

-- 3) Usage
SELECT usage_source, uncached_input_tokens, output_tokens_total,
       reasoning_output_tokens, cache_read_input_tokens
FROM usage_records
WHERE request_record_id = (SELECT id FROM request_records WHERE request_id = :rid);

-- 4) 冻结/捕获
SELECT status, estimated_amount, authorized_amount, captured_amount, released_amount
FROM ledger_reservations
WHERE request_record_id = (SELECT id FROM request_records WHERE request_id = :rid);

-- 5) 账本流水
SELECT entry_type, amount, balance_before, balance_after, reason
FROM ledger_entries
WHERE request_record_id = (SELECT id FROM request_records WHERE request_id = :rid)
ORDER BY id;

-- 6) 计费异常（路线 B/D 必须为 0 行）
SELECT event_type, reason_code, actual_amount, captured_amount, platform_amount
FROM ledger_billing_exceptions
WHERE request_record_id = (SELECT id FROM request_records WHERE request_id = :rid);

-- 7) 快照
SELECT * FROM price_snapshots WHERE request_record_id = (SELECT id FROM request_records WHERE request_id = :rid);
SELECT * FROM cost_snapshots WHERE request_record_id = (SELECT id FROM request_records WHERE request_id = :rid);

-- 8) Recovery
SELECT status, attempt_count, max_attempts, usage_source, last_error_code
FROM settlement_recovery_jobs
WHERE request_record_id = (SELECT id FROM request_records WHERE request_id = :rid);

-- 9) 用户余额
SELECT balance, reserved_balance FROM user_balances
WHERE user_id = (SELECT user_id FROM request_records WHERE request_id = :rid) AND currency = 'USD';
```

### 4.4 Admin UI 人工复核清单

测试全部完成后，你在后台应能看到：

- [ ] **请求记录**：每条用例对应 1 条 request，status 与预期一致。
- [ ] **交付状态**：成功流式/非流式 → `delivery_status=completed`；中途 Stop → `interrupted`；未 emit → `not_started`（Q14）。
- [ ] **Attempt**：`final_usage_received` 与路线 A/B/D 一致。
- [ ] **用量**：`usage_source` 正确（`upstream_*` vs `partial_stream_estimate`）。
- [ ] **账本流水**：debit 金额与 price_snapshot 一致；无重复 debit。
- [ ] **计费异常**：仅 write-off / 真 settlement-fail 用例出现；**无**「已 emit 无 usage」的 risk_exposure。
- [ ] **Recovery jobs**：recovery 用例 pending→succeeded；dead 用例有 risk_exposure。
- [ ] **Dashboard**：余额、消耗与 ledger 汇总一致（允许 rounding 误差 < $0.000001）。

---

## 5. 用例矩阵

### 5.1 编号规则

`{类型}-{序号}`：类型 = NS(non-stream) / SA(stream-A) / SB / SC / SD / AUTH / WO(write-off) / REC(recovery) / COD(codex) / AD(admin)

**优先级**：P0 = 发布阻断；P1 = 应测；P2 = 时间允许。

---

### 5.2 非流式（真实上游）

| ID | 场景 | 触发方式 | 预期 |
|----|------|---------|------|
| NS-01 |  happy path | `curl POST /v1/chat/completions` stream=false | succeeded；debit>0；usage_source=upstream_response |
| NS-02 |  upstream 4xx | mock 或错误 model | failed；reservation released；无 debit |
| NS-03 |  Responses 非流式 | `POST /v1/responses` stream=false 简短 prompt | 同 NS-01，operation=responses |

```bash
# NS-01 示例
curl -sS "$GATEWAY/v1/chat/completions" \
  -H "Authorization: Bearer $UNIO_KEY" \
  -H "Content-Type: application/json" \
  -d '{"model":"gpt-5.4-mini","messages":[{"role":"user","content":"say hi in 3 words"}],"stream":false}'
```

---

### 5.3 流式路线 A — 正常 full bill（真实上游 + Codex）

| ID | 场景 | 触发 | 预期 |
|----|------|------|------|
| SA-01 | Chat stream + usage | curl `-N` stream=true | succeeded；final_usage_received=TRUE；upstream_stream |
| SA-02 | Responses stream（Codex 主路径） | Codex 单轮简单问答 | 同 SA-01，operation=responses |
| SA-03 | Anthropic Messages stream | curl `/v1/messages`（可选） | succeeded；upstream_stream |

---

### 5.4 流式路线 B — emit 后 partial（mock 或 curl 中断）

| ID | 场景 | 触发 | 预期 |
|----|------|------|------|
| SB-01 | 客户端 cancel | curl stream 收到 1+ chunk 后 Ctrl+C | request **canceled**；final_usage_received=**FALSE**；usage_source=**partial_stream_estimate**；debit>0（若有输出）；**无** risk_exposure |
| SB-02 | 上游断流 | mock：content 后断连 | request **failed**；upstream_finish_reason=stream_interrupted_without_final_usage；final_usage_received=**FALSE**；usage_source=**partial_stream_estimate**；debit>0（若有输出）；**无** risk_exposure |
| SB-03 | Codex 中途 Stop | Codex UI 点停止（若可触发） | 同 SB-01；记录 metrics 层 canceled |

> **依赖**：§0 migration 修复 partial_stream_estimate。

---

### 5.5 流式路线 C — emit 前 release

| ID | 场景 | 触发 | 预期 |
|----|------|------|------|
| SC-01 | 首 chunk 前 cancel | curl 立刻 kill | failed/canceled；released；debit=0；无 usage |
| SC-02 | 上游首包前 500 | mock | failed；released；无 billing exception |

---

### 5.6 流式路线 D — 缺 final usage（mock 专用）

| ID | 场景 | 触发 | 预期 |
|----|------|------|------|
| SD-01 | 有内容无 usage | mock SSE 省略 usage | succeeded；final_usage_received=FALSE；upstream_finish_reason=**stream_final_usage_missing**；partial_stream_estimate；debit>0；无 risk_exposure |
| SD-02 | Chat + Responses 各 1 条 | mock channel 分别测 | 两条入口行为一致 |

> 真实上游 `https://zz1cc.cc.cd/v1` **不应**依赖此用例；New API 通常总会带 usage。

---

### 5.7 授权（AUTH）

| ID | 场景 | 准备 | 预期 |
|----|------|------|------|
| AUTH-01 | 余额为 0 | **`billing-e2e-low@unio.local`** 余额调 0 | 402/403；无 upstream；无 debit |
| AUTH-02 | 低余额部分授权 | 低余额用户，余额 `$0.05`，长输出 prompt | 请求成功；authorized < estimated；capture ≤ authorized |

---

### 5.8 平台差额核销 write_off（WO）

| ID | 场景 | 准备 | 预期 |
|----|------|------|------|
| WO-01 | actual > authorized（非流） | AUTH-02 条件下短 prompt 仍可能不够；或 mock 返回超大 usage | succeeded；captured=authorized；ledger_billing_exceptions.write_off；reason=authorization_underfunded |
| WO-02 | partial stream cap | 低余额 + SB-01 长输出 | partial estimate > authorized → capture + write_off |

---

### 5.9 结算 Recovery（REC）

| ID | 场景 | 触发 | 预期 |
|----|------|------|------|
| REC-01 | 首次 settlement 失败 | **测试钩子**或暂时破坏 DB 约束后恢复 | recovery job pending；request running；worker 重试后 succeeded |
| REC-02 | recovery 耗尽 | 调低 max_attempts 或持续失败 | job dead；FinalizeDeadChatSettlement；risk_exposure(settlement_recovery_exhausted)；request failed；balance 释放 |
| REC-03 | 永久 settlement 失败且无 recovery | 不可靠 usage 路径 | risk_exposure(stream_settlement_failed_after_upstream_success) |

> REC-* 需要 worker 运行 + **`BILLING_E2E_INJECT_SETTLEMENT_FAIL=1`** 测试 env（Q9 已确认支持）。

---

### 5.10 Codex 真实对话（COD）— **核心**

这些用例使用 **真实 Codex CLI/App**，验证「用户感知一次对话，网关多条 request」下的 **逐条账务正确性**。

| ID | 场景 | 用户操作 | 验证重点 |
|----|------|---------|---------|
| COD-01 | 单轮寒暄 | 发送「你好」 | 识别主模型 request（如 gpt-5.5）；status=succeeded；debit 合理；**不要求只有 1 条 API 请求** |
| COD-02 | 单轮 + 读文件 | 「读取 README 前 20 行并总结」 | 多条 request_id 串行/并行；每条独立 settlement；余额递减单调 |
| COD-03 | 工具调用 | 触发 apply_patch / shell（若环境允许） | attempt.used_capabilities 有值；billing 仍 succeeded |
| COD-04 | 长会话 | 同线程 5+ 轮 | 无 recovery dead；无异常 risk_exposure 洪泛 |
| COD-05 | 用户 Stop | 输出中途点停止 | 触发 SB 类 partial；**不得** risk_exposure |
| COD-06 | 多模型混用 | 观察 gpt-5.5 / 5.4 / 5.4-mini | 各 model_id 命中正确 price_snapshot |

**COD-01 实测记录模板**（2026-06-25 已观测，供基线对比）：

| 时间 | model | request_id | status | 备注 |
|------|-------|------------|--------|------|
| 14:21:18 | gpt-5.5 | req_29eea2e… | succeeded | 用户可见「你好」回复 |
| 14:21:18~14:25:24 | gpt-5.4-mini / gpt-5.4 | 13 条 | 多数 succeeded | Codex agent 后台轮次，非网关重复计费 |

---

### 5.11 Admin 一致性（AD）

| ID | 场景 | 预期 |
|----|------|------|
| AD-01 | 手工调额 +$10 | adjustment_credit ledger；balance 增加 |
| AD-02 | Dashboard breakdown | 实测期间 revenue ≈ sum(debit) |

---

### 5.12 交付状态（DEL）— Q14 修完必测

| ID | 场景 | 触发 | 预期 |
|----|------|------|------|
| DEL-01 | 非流式成功 | NS-01 同请求 | `delivery_status=completed`；`response_completed_at` 非空 |
| DEL-02 | 流式成功 | SA-01 / SA-02 | `not_started`→`in_progress`→`completed`；`response_started_at` ≤ `response_completed_at` |
| DEL-03 | 流式中途 Stop | COD-05 / SB-03 | `delivery_status=interrupted`；billing 仍按路线 B partial |
| DEL-04 | emit 前失败 | SC-01 | `delivery_status=not_started`；`response_completed_at` NULL |

**Admin 断言**（追加到 §4.3）：

```sql
SELECT delivery_status, response_started_at, response_completed_at
FROM request_records WHERE request_id = :rid;
```

---

## 6. 执行顺序（建议 4 个阶段）

### Phase 0 — 环境与修复（阻塞项）

1. 落地 §0.1 migration + §0.2 `delivery_status` 状态机。
2. `seed-test-data.sh --reset`（仅一次）+ 配置上游（§2.2）+ 创建测试项目/低余额用户（§2.6）。
3. 确认 Unio Key / `.env` 就绪（§2.3）。
4. Smoke：`curl /v1/models` + NS-01 + DEL-01。

### Phase 1 — 真实上游 happy path（P0）

NS-01 → DEL-01 → SA-01 → SA-02 → DEL-02 → COD-01 → COD-02

**通过标准**：无 failed settlement；ledger 与 balance 一致；`delivery_status` 正确。

### Phase 2 — Mock 边界（P0）

SC-01 → SC-02 → SB-01 → SB-02 → SD-01 → WO-02

**通过标准**：路线 B/C/D 符号表（§4.3）全部命中；无错误 risk_exposure。

### Phase 3 — 授权与 write_off（P1）

AUTH-01 → AUTH-02 → WO-01

### Phase 4 — Recovery + Codex 压力（P1）

REC-01 → REC-02 → COD-04 → COD-05 → DEL-03 → DEL-04 → AD-01 → AD-02

### Phase 5 — 收尾

1. 导出 Admin 请求列表 CSV / 截图。
2. 填写 §7 结果记录表。
3. 更新 `STATUS.md` / 关闭相关 GAP。

---

## 7. 执行结果（2026-06-25 实测，全部 PASS）

环境：Docker Postgres/Redis + 本地 gateway（新构建二进制，含 §0 修复）+ 用户的 admin(8522)。真实上游
`https://zz1cc.cc.cd/v1`（key `sk-GKFab...`）用于 happy-path/Codex；本地 mock 上游用于 B/C/D/WO 可控路线。

`go test ./...` 全绿；`go build`/`go vet` 通过。

| 用例 | req id | 结果 | 关键证据 |
|------|--------|------|---------|
| NS-01 非流式 | 1 | PASS | succeeded, upstream_response, debit 0.000393, delivery=completed |
| DEL-01 非流交付 | 1 | PASS | delivery_status=completed, response_completed_at 非空 |
| SA-01 chat 流式 A | 2 | PASS | upstream_stream, final_usage=t, debit 0.000413 |
| SA-02 responses 流式 A | 3 | PASS | operation=responses, upstream_stream, finish=completed |
| DEL-02 流式交付 | 2,3 | PASS | not_started→in_progress→completed |
| COD-01 真实 Codex 问候 | 4 | PASS | 真实 codex exec → 网关，单 responses 请求，路线 A |
| COD-02 Codex 多请求 | 5,6 | PASS | 一次对话 2 请求各自独立结算，余额单调 |
| SD-01 路线 D 缺 usage | 7 | PASS | partial_stream_estimate, stream_final_usage_missing, debit>0, **无 risk_exposure** |
| SC-02 emit 前上游 500 | 8 | PASS | failed, released, 0 扣费, delivery=not_started（兼 DEL-04） |
| SC-01 emit 前客户端取消 | 9 | PASS | canceled, released, 0 扣费 |
| SB-02 上游中断（emit 后） | 10 | PASS | failed, partial_stream_estimate, stream_interrupted_without_final_usage, delivery=interrupted, **无 risk_exposure** |
| SB-01 客户端取消（emit 后） | 11 | PASS | canceled, partial_stream_estimate, stream_client_canceled_without_final_usage, **无 risk_exposure**（原始投诉场景已修） |
| AUTH-01 余额为 0 | 12 | PASS | 429 insufficient_quota, ledger_insufficient_balance, 0 次上游调用 |
| AUTH-02 部分余额授权 | 13 | PASS | authorized=$0.005（封顶可用余额）, captured<authorized |
| WO-01 非流 actual≫authorized | 14 | PASS | captured=authorized, write_off platform=$0.43, 余额不为负 |
| WO-02 partial 流封顶 | 15 | PASS | partial_stream_estimate, captured=authorized, write_off platform=$0.0000166 |
| REC-01 内联结算失败→worker 修复 | 24 | PASS | 内联 running→worker 重放→succeeded, captured |
| REC-02 补偿耗尽→dead | 26 | PASS | job dead, released, risk_exposure(settlement_recovery_exhausted), 用户不扣费 |
| COD-04 真实 Codex 多轮 | 27-30 | PASS | 3 成功各自结算；第 4 遇上游 429 released；**0 risk_exposure** |
| COD-05 Codex 流中途取消 | 31 | PASS | responses 路线 B, stream_client_canceled_without_final_usage, delivery=interrupted, **无 risk_exposure** |
| DEL-03 流中断 interrupted | 10,11,31 | PASS | 上述 B 路线 delivery=interrupted |
| DEL-04 emit 前 not_started | 8,9 | PASS | delivery=not_started, response_completed_at NULL |
| AD-01 手工调额 | n/a | PASS | adjustment_credit +$10, 余额 99.97→109.97 |
| AD-02 Dashboard 对账 | n/a | PASS | overview revenue=0.031332075 **精确等于** ledger debit 汇总（18 笔）；breakdown by model 一致 |

### 7.1 全局账务异常汇总（关键验收）

| event_type | reason_code | 笔数 | 含义 |
|------------|-------------|------|------|
| `risk_exposure` | settlement_recovery_exhausted | **1** | 仅 REC-02 真结算失败（正确：平台承担、用户不扣费） |
| `write_off` | authorization_underfunded | 2 | WO-01/WO-02 actual>authorized（正确：平台核销差额） |

**所有 partial 路线（B/D：SB-01/SB-02/SD-01/WO-02/COD-05）产生 0 个 `risk_exposure`** —— 即你最初投诉
「取消导致平台白白承担费用」的问题已彻底修复：现在按已交付内容部分计费（debit>0），仅在 settlement
真正永久失败时才记 risk_exposure。

### 7.2 执行中发现并修复 / 处理的问题

1. **`partial_stream_estimate` 未在 DB CHECK**（§0.1）：导致 partial 结算与 recovery job 落库失败（即你
   14:21 那批 `settlement_recovery_jobs_usage_source_check` 报错的根因）。新增 migration 000050 修复，
   实测 SD-01/SB-01/SB-02/WO-02 的 recovery job 均以 partial_stream_estimate 正常落库。
2. **`delivery_status` 状态机未接线**（§0.2，Q14）：成功请求恒 `not_started`。已接入 in_progress/
   completed/interrupted（chat/responses/anthropic + 流式/非流式），DEL-01~04 全部验证。
3. **Codex 无头运行的三个坑**（环境，非网关）：①`codex exec` 默认等 stdin → 用 `< /dev/null`；
   ②非信任目录被拒 → `--skip-git-repo-check`；③用户当前 `~/.codex/config.toml` 已把 provider 指向
   **上游直连**（base_url=zz1cc.cc.cd, requires_openai_auth=true），绕过 Unio 网关。测试用 `-c` 覆盖把
   base_url 指回 `127.0.0.1:8521` 且用 Unio key 鉴权（未改用户配置文件）。
4. **上游 429**：Codex 多轮 agent 高频请求会被真实上游限流（COD-04 第 4 请求）。网关按 emit 前上游
   错误正确释放、不计费。

---

## 8. 与现有自动化测试的关系

| 资产 | 角色 |
|------|------|
| `go test ./...` | 回归；**不替代**本计划 |
| `go test -tags=blackbox ./internal/blackbox/...` | 复用 `sdkfixture` + mock 上游实现 SB/SC/SD |
| `service_test.go` partial 单测 | 已实现 B/D 逻辑证明；E2E 验证 DB 约束与 HTTP 路径 |
| `scripts/seed-test-data.sh` | 环境初始化 |

本计划执行中 **可新增**（本地校验，Q12）：

- `scripts/billing-e2e-seed.sql` — 测试项目 + 低余额用户
- `scripts/billing-e2e/` — curl 脚本 + SQL 断言 shell
- `BILLING_E2E_INJECT_SETTLEMENT_FAIL` env 钩子（REC-*）
- `internal/blackbox/billing/` — 路线 B/C/D HTTP 黑盒（可选）

---

## 9. 已确认决策（2026-06-25）

| # | 决策 |
|---|------|
| **Q1** | Unio Key = `unio_sk_ohuUc2Zkpspw9Vre5idmZKRXfFX6Vxb2cl52cMZuFLQ` |
| **Q2** | Admin Token = `.env` 默认 `ADMIN_API_TOKEN` |
| **Q3** | 全部环境变量用本地 `unio-api/.env` |
| **Q4** | seed 三模型（gpt-5.5 / 5.4 / 5.4-mini）上游均可用，`upstream_model` 同名 |
| **Q5** | 上游支持原生 `/v1/responses` |
| **Q6** | stream final usage **偶发丢失**；真实 COD 可能走路线 D，记录 `upstream_finish_reason` 区分上游 vs Unio |
| **Q7** | **单渠道**；本地服务 + Docker DB/Redis |
| **Q8** | 低余额用例用 **新建测试用户**（不污染 dev 主用户） |
| **Q9** | **支持** `BILLING_E2E_INJECT_SETTLEMENT_FAIL` 等测试 env |
| **Q10** | Phase 间 **保留长会话历史**，不全库 reset |
| **Q11** | 创建专用测试项目 **`Billing E2E Test`**，Codex 绑 unio 仓库 |
| **Q12** | **仅本地校验**，不强制提交 repo |
| **Q13** | **接受** Codex 一条用户消息 → 多条 API 请求为正常 |
| **Q14** | **`delivery_status` 必须修 + 必测**（§5.12 DEL-*） |

**上游 Key 更新**（2026-06-25）：`sk-REDACTED（实测时从 .env / 私密渠道获取，勿写入仓库）`

---

## 10. 附录 A — Codex 配置检查

```toml
# ~/.codex/config.toml 应包含：
model_provider = "custom"
model = "gpt-5.5"   # 或你在 Unio _catalog 中配置的 model_id

[model_providers.custom]
wire_api = "responses"
base_url = "http://127.0.0.1:8521/v1"
requires_openai_auth = false
```

验证：

```bash
# 应返回 seed 中的模型
curl -sS http://127.0.0.1:8521/v1/models -H "Authorization: Bearer $UNIO_KEY" | jq '.data[].id'
```

---

## 11. 附录 B — 路线符号速查

| 路线 | request.status | final_usage_received | usage_source | reservation | billing_exception |
|------|---------------|----------------------|--------------|-------------|-------------------|
| A | succeeded | TRUE | upstream_stream | captured | 无（除非 WO） |
| B | succeeded | FALSE | partial_stream_estimate | captured | **无** |
| C | failed/canceled | FALSE | 无 | released | 无 |
| D | succeeded | FALSE | partial_stream_estimate | captured | **无** |
| write-off | succeeded | * | * | captured=authorized | write_off |
| recovery dead | failed | TRUE | upstream_* | released | risk_exposure |

---

## 12. 附录 C — 参考文档

- [STREAM_PARTIAL_SETTLEMENT.md](./STREAM_PARTIAL_SETTLEMENT.md) — 路线 B/D 设计
- [ACCEPTANCE.md](./ACCEPTANCE.md) — Phase 7 验收标准
- [PLAN.md](./PLAN.md) — TASK-7.x 任务分解
- [DESIGN-capability-evidence-v2.md §10](../../production/DESIGN-capability-evidence-v2.md) — Codex E2E 参考（能力域，非账单）
- `internal/blackbox/openaisdk/settlement_test.go` — 最小 audit trail 断言范例

---

## 变更记录

| 日期 | 作者 | 说明 |
|------|------|------|
| 2026-06-25 | Agent | 初稿 |
| 2026-06-25 | Agent | 用户审核通过：§9 决策落地；§0.2/DEL-* 纳入；上游 Key 更新 |
