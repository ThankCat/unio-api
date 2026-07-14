# E2E 测试清单：渠道成本倍率（DEC-027）— 真实渠道 + Codex CLI + Claude CLI

> 配套 `DESIGN-channel-cost-multiplier.md`。这是一份**可照着执行的 runbook**，不是设计。
>
> **前置铁律**：本清单只有在 **DEC-027 倍率机制 + DEC-031 成本基数复用 `model_prices`** 均已实现并迁移后才能跑（`model_prices`（作成本基数，DEC-031）/ `channel_cost_multipliers` / `channel_recharge_factors` / `ScaleProviderCost` / `resolveSettlementCost` / `cost_snapshots` 新列 / 请求详情成本倍率展示 全部落地）。DEC-031 退役了独立的 `model_reference_costs` 表，成本基数改由 `model_prices`（模型基准价）复用——**售价与成本共用同一基数**。
>
> 事实基准（勘探自代码）：网关 `cmd/gateway-server` 监听 `:8520`（`GATEWAY_HTTP_ADDR`）、admin `cmd/admin-server` `:8521`、worker `cmd/worker-server`；对外端点 `/v1/models`、`/v1/chat/completions`、`/v1/responses`（Codex 用）、`/v1/messages`（Claude 用）；鉴权同时接受 `Authorization: Bearer` 与 `x-api-key`（`api_key_auth.go:61-68`）；`v1PathCompat` 会自动补/折叠 `/v1`。既有真实回归骨架见 `cmd/e2e-realtest/main.go`，清理样板见 `scripts/cleanup-agent-test-data.sql`。

---

## 0. 安全与隔离（务必先读）

- **花真钱（已接受）**：用户明确**不怕花钱**、且要求**跑长 prompt**。因此测试分两档：
  - **快检小 prompt**（`max_tokens` 16–64）：快速验证功能通路 + 费率恒等（费率与 token 量无关）。
  - **重负载长 prompt / 大输出 / 缓存 / reasoning / 流式 / 多轮**（见 §3.5、§4、§5）：小 prompt **测不出**的问题（缓存/推理**可空分项**的倍率、大 token 量下的精度舍入、流式异步收口、多轮长上下文累计）只有重负载能暴露。允许大 `max_tokens`（如 2000–4000）与数千 token 的输入。
  - 唯一红线：**别放无限循环/无上限自动 agent 任务**，给每个重负载用例设明确上界，避免失控烧钱。
- **数据隔离（关键）**：**不要**把测试定价直接挂到你的**生产渠道行**上。做法：**新建一条专用测试渠道**，`base_url`/`credential` 从你真实渠道**复制**过来——这样请求真实打到上游（真成本、真行为），但所有测试 DB 行都带 `e2e-costmult-*` 命名、可整体安全删除，**绝不碰生产配置**。
  - 命名约定：provider `e2e-costmult-provider-<ts>`、channel `e2e-costmult-ch-<ts>`、route `e2e-costmult-route-<ts>`、user email `e2e-costmult-<ts>@example.test`。
- **保留真实数据**：清理脚本按上述命名精准删除，绝不动 `user_id=3`（真账号）、生产渠道/服务商（同 `cleanup-agent-test-data.sql` 口径）。
- **全程记录本次创建的 id**（provider/channel/model/route/user/key/各定价行）到一个临时文件，供第 7 步精准清理。

---

## 1. 环境起服务

```bash
cd unio-api
source .env                       # 注入 DATABASE_URL / REDIS_ADDR / 加密密钥等
# 1) 迁移到最新（含 DEC-031 成本基数迁移 000037；迁移已 consolidation，编号以仓库实际为准）——用你平时的迁移方式
#    e.g. golang-migrate: migrate -path migrations -database "$DATABASE_URL" up
# 2) 起三个进程（各开一个终端 / 后台）
go run ./cmd/gateway-server      # 对外网关 :8520（Codex/Claude 打这里）
go run ./cmd/worker-server       # 结算补偿 + 流式异步收口（必须起，否则流式扣费不落）
go run ./cmd/admin-server        # :8521，若用 admin API/UI 配置则起
# 3) 健康检查
curl -s localhost:8520/healthz   # 期望 {"status":"ok"}
```

验收：三进程无 error 日志；`/healthz` 返回 ok。

---

## 2. 造测试数据（真实上游 + 三层成本）

> 下列用**直连 SQL**造数（也可用 admin API，等 DEC-027 的 admin 端点就绪）。金额 `NUMERIC(20,10)`，倍率无量纲。示例值刻意好算、且让新旧可辨。

设计示例（USD 为结算币种）：
- 基准价（`model_prices`，售价与成本**共用基数**，名义）：`gpt` 主模型 输入 `2.0` / 输出 `10.0`（每 1M）。
- **价格倍率** 默认 `1.20`；对某一个模型建**逐模型覆盖** `1.40`（验证覆盖优先）。
- **充值倍率** `0.50`（模拟"充得多、真实成本减半"；取明显值便于肉眼验证）。
- ⇒ 主模型**真实成本** 输入 `2.0×1.20×0.50=1.2` / 输出 `10.0×1.20×0.50=6.0`。
- 线路 `price_ratio=1.5` ⇒ 客户售价 输入 `3.0` / 输出 `15.0`（售价侧不受成本改造影响，用于对照毛利）。

```sql
-- 记下每个 RETURNING id，写进临时清理清单
-- provider + 复制真实上游的 channel
INSERT INTO providers (slug,name,status) VALUES ('e2e-costmult-provider-<ts>','e2e-costmult','enabled') RETURNING id;   -- :provider_id
INSERT INTO channels (provider_id,name,protocol,adapter_key,base_url,credential,status,priority,timeout_ms)
VALUES (:provider_id,'e2e-costmult-ch-<ts>','openai','openai','<真实上游 base_url，含 /v1>','<真实上游 key>','enabled',100,60000) RETURNING id;  -- :channel_id
INSERT INTO models (model_id,display_name,owned_by,status,max_output_tokens)
VALUES ('e2e-gpt-<ts>','e2e-gpt','openai','enabled',128000) RETURNING id;   -- :model_id
INSERT INTO channel_models (channel_id,model_id,upstream_model,status)
VALUES (:channel_id,:model_id,'<真实上游模型名>','enabled');

-- 成本基数 = model_prices（DEC-031：售价与成本共用同一基准价，退役 model_reference_costs）+ 渠道两表倍率
-- 注意：cache_read / cache_write_* / reasoning 基准价**必须配**，否则这些可空分项按 0 入账，
--       重负载缓存/推理场景就验证不到"倍率作用在分项上"。这里给全 6 个分项。
-- 这一行 model_prices 同时充当**售价基数**（× 线路倍率 1.5 ⇒ 售价 3.0/15.0）与**成本基数**
--   （× 价格倍率 × 充值倍率 ⇒ 成本 1.2/6.0），只录一次。
INSERT INTO model_prices (
    model_id,currency,pricing_unit,
    uncached_input_price,output_price,
    cache_read_input_price,cache_write_5m_input_price,cache_write_1h_input_price,cache_write_30m_input_price,reasoning_output_price,
    status,effective_from)
VALUES (:model_id,'USD','per_1m_tokens',
    2.0,10.0,
    0.2,2.5,4.0,3.0,10.0,          -- 缓存读取 0.2 / 5m 2.5 / 1h 4.0 / 30m 3.0 / reasoning 10.0
    'enabled',now()-interval '1 hour') RETURNING id;   -- :model_price_id
INSERT INTO channel_cost_multipliers (channel_id,model_id,multiplier,status,effective_from)
VALUES (:channel_id,NULL,1.20,'enabled',now()-interval '1 hour') RETURNING id;   -- :mult_default_id
-- （可选）逐模型覆盖，验证覆盖优先：
-- INSERT INTO channel_cost_multipliers (channel_id,model_id,multiplier,status,effective_from) VALUES (:channel_id,:model_id,1.40,'enabled',now()-interval '1 hour');
INSERT INTO channel_recharge_factors (channel_id,factor,status,effective_from)
VALUES (:channel_id,0.50,'enabled',now()-interval '1 hour') RETURNING id;   -- :recharge_id

-- 线路 + 用户 + 余额 + key（照搬 e2e-realtest 口径）
INSERT INTO routes (name,mode,pool_kind,status,price_ratio)
VALUES ('e2e-costmult-route-<ts>','cheapest','all','enabled',1.5) RETURNING id;   -- :route_id
INSERT INTO users (email,password_hash,display_name) VALUES ('e2e-costmult-<ts>@example.test','x','e2e-costmult') RETURNING id;  -- :user_id
-- EnsureUserBalance + AddUserBalance 充足余额（如 1000 USD）
INSERT INTO api_keys (user_id,name,key_prefix,key_hash,key_plaintext,route_id)
VALUES (:user_id,'e2e-costmult',:prefix,:hash,:plaintext,:route_id);   -- 记下 key 明文 :UNIO_KEY
```

> 建议直接**扩展 `cmd/e2e-realtest`** 的 seed helper（`insertChannel`/`insertPricedModel`）加一个 `insertMultiplierPricedModel`，把上面这套写成代码，省得手敲 SQL。

---

## 3. Layer 1 · 自动化断言（扩展 `cmd/e2e-realtest`，原始 HTTP，可 CI）

在 `cmd/e2e-realtest` 加一组 `scenarioCostMultiplier*`（沿用 `assertBilling` 风格，**断费率不断 token 量**）：

- [ ] **成本 = 基准价 × 价格倍率 × 充值倍率**：发一条 happy 请求，查该请求 `cost_snapshots`：`uncached_input_cost::text == 1.2000000000`、`output_cost::text == 6.0000000000`（= 2×1.2×0.5 / 10×1.2×0.5）。
- [ ] **覆盖优先**：给该模型加逐模型价格倍率 1.40，新请求成本单价变 `2×1.4×0.5=1.4` / `10×1.4×0.5=7.0`。
- [ ] **改倍率不漂移（核心）**：请求 R1 → 记 `cost_snapshots(R1)`；把默认价格倍率**新建一条** 1.20→0.60（收口旧窗口）→ 请求 R2。断言：R1 快照**不变**、R2 单价 = 2×0.6×0.5=`0.6` / 10×0.6×0.5=`3.0`。
- [ ] **改充值倍率不漂移**：同上，充值倍率 0.50→0.25，新请求单价再减半，历史不变。
- [ ] **冻结不受成本影响**：成本倍率/充值倍率变更前后，同 token 估算的 `ledger_reservations.authorized_amount` 一致（authorization 只用售价）。
- [ ] **快照来源列**：`cost_snapshots` 的 `cost_multiplier` / `recharge_factor` / `cost_base_model_price_id` / `channel_cost_multiplier_id` / `channel_recharge_factor_id` 均有值且与配置一致。
- [ ] **绝对覆盖优先级**：给 (channel,model) 建一条 `channel_prices` 绝对成本 → 新请求走绝对值、`cost_snapshots.cost_price_id` 非空、`cost_multiplier/recharge_factor` 为 NULL。
- [ ] **未定价拦截**：删掉基准价（保留倍率）→ 该模型请求被路由排除 / 结算报错、不 2xx、不计费。
- [ ] **fallback 客价不变**（沿用现有场景）：命中更贵成本渠道，`price_snapshots` 售价费率不变。

跑：`go run ./cmd/e2e-realtest`（需放行外网 + 上游 env）。验收：新增用例全 PASS。

---

## 3.5 Layer 1.5 · 重负载 / 长上下文场景（**逼出小 prompt 测不到的问题**）

> 用户不怕花钱、要求长 prompt。这些用例专打**大 token 量 + 可空分项（缓存/推理）+ 流式 + 多轮**——成本倍率恰恰最容易在这几处出错（分项漏乘、大数舍入、异步收口）。可加进 `cmd/e2e-realtest`，也可手动发 HTTP。**费率恒等**仍是主断言：无论 token 多大，`成本单价 = 基准价 × 价格倍率 × 充值倍率` 逐分项精确成立（下例默认倍率 1.20 × 充值 0.50 = 0.60）。

| 用例 | 怎么造 | 逼出什么 | 关键断言 |
|---|---|---|---|
| **大输入** | 塞几千 token 长文本（如粘一大段文档）+ `max_tokens` 小 | 大 `uncached_input_tokens` 下的金额精度 | `uncached_input_cost::text == 1.2000000000`；`uncached_input_cost_amount ≈ 1.2 × tokens/1e6`（`NUMERIC(20,10)` 无尾差） |
| **大输出** | `max_tokens` 2000–4000 + "写一篇长文" | 大 `output_tokens` 金额；输出/推理拆分 | `output_cost::text == 6.0000000000`；总额 = 各分项之和（`ck_cost_snapshots_total_amount`） |
| **缓存命中** | 同一大 system/context **连发两次**（TTL 内）；Anthropic 用 `cache_control` 断点，OpenAI ≥1024 token 自动缓存 | **可空分项 `cache_read_input_cost` 的倍率**是否生效（易漏） | 第二次 `usage_records.cache_read_input_tokens>0`；`cost_snapshots.cache_read_input_cost::text == 0.1200000000`（=0.2×1.2×0.5） |
| **缓存写入** | 首次写入大缓存 | 5m/1h/30m 三档写入分项各自倍率 | 命中档 `cache_write_*_input_cost` = 基准×1.2×0.5（5m=1.5 / 1h=2.4 / 30m=1.8） |
| **reasoning** | 用推理模型 + 难题（数学/多步推理），开启思考 | **可空分项 `reasoning_output_cost` 倍率** + 普通输出=总输出−推理 的拆分 | `usage_records.reasoning_output_tokens>0`；`cost_snapshots.reasoning_output_cost::text == 6.0000000000`（=10×1.2×0.5） |
| **流式长输出** | `stream=true` + 大 `max_tokens` | worker 异步收口下的倍率/快照落地 | 流结束后（等 worker）`cost_snapshots` 存在、单价 = 基准×倍率×充值、`debit` 增长 |
| **长多轮** | 6–10 轮累积长上下文（沿用 `scenarioOpenAILongConversation` 加长） | 累计 token 放大后费率仍恒等、无溢出 | 每轮 `price_snapshots` 售价费率 3/15 不变、`cost_snapshots` 成本费率 1.2/6 不变 |
| **改倍率×大 token** | 大输出请求 R1 → 改价格倍率 → 大输出 R2 | 大数下"改倍率不漂移"更显眼 | R1 大额快照改倍率后**分文不变**；R2 按新倍率 |

> 缓存能否命中依赖**上游是否支持 prompt caching** + 模型 + 断点写法，属真实世界不确定项。若某次 `usage_records` 无缓存 token，说明上游没缓存该次——换支持的模型/加 `cache_control` 重试；缓存分项验证以"确实产生了缓存 token 的那次"为准。

---

## 4. Layer 2 · Codex CLI（真实 CLI → `/v1/responses`）

> Codex 2026 起**只支持 `wire_api = "responses"`**（`chat/completions` 已弃用）；Unio 网关已实现 `/v1/responses`（Codex 兼容）。必须写在**用户级** `~/.codex/config.toml`（项目级会忽略 `model_providers`）。

```toml
# ~/.codex/config.toml
model = "e2e-gpt-<ts>"            # 第 2 步建的网关模型 id
model_provider = "unio_local"

[model_providers.unio_local]
name = "Unio Local Gateway"
base_url = "http://localhost:8520/v1"
env_key = "UNIO_KEY"             # 从环境变量读 key
wire_api = "responses"
```

```bash
export UNIO_KEY="<第2步的 :UNIO_KEY 明文>"
# (a) 快检小任务：
codex exec "print the string OK and nothing else"
# (b) 重负载真实任务（长上下文 + 多轮 + 大输出，最贴近生产；用户已接受花费）：
#     在一个真实小仓库里跑一个有明确上界的多步任务，让 Codex 反复读文件/改代码/自检。
codex exec "阅读本仓库结构，为 utils 目录补一份 README，说明每个函数用途，并给出使用示例；最后总结你改了什么"
# 上界建议：任务本身有限；避免"持续重构整个仓库"这类无上界指令。
```

验收：
- [ ] Codex 请求成功返回。
- [ ] 网关访问日志出现 `POST /v1/responses`。
- [ ] DB：该请求 `request_records` 存在，`cost_snapshots` 单价 = 基准价×价格倍率×充值倍率，`price_snapshots` 售价 = 基准价×线路倍率，余额下降。
- [ ] 请求详情费用处（admin `:8521` 或 SQL）显示「价格倍率 / 充值倍率 / 模型基准价（成本基数）」三行。

---

## 5. Layer 2 · Claude Code CLI（真实 CLI → `/v1/messages`）

> Claude Code 用 `ANTHROPIC_BASE_URL`（host 根，不带 /v1，CLI 自己拼 `/v1/messages`）+ `ANTHROPIC_AUTH_TOKEN`（走 `Authorization: Bearer`，网关接受）。留空 `ANTHROPIC_API_KEY` 防回落官方。`ANTHROPIC_MODEL` 把内部 sonnet/opus 名重映射到网关模型 id。
> 前置：需有一条 **Anthropic 协议**的测试渠道 + 模型（若你的真实上游是 Anthropic 系）。若只有 OpenAI 上游，则本层用一条指向 Anthropic 上游的测试渠道，同样按第 2 步造数（`protocol='anthropic'`, `adapter_key='anthropic'`, base_url 去尾 `/v1`）。

```bash
export ANTHROPIC_BASE_URL="http://localhost:8520"
export ANTHROPIC_AUTH_TOKEN="<第2步的 :UNIO_KEY 明文>"
export ANTHROPIC_API_KEY=""                 # 关键：留空
export ANTHROPIC_MODEL="e2e-claude-<ts>"    # 网关上的 Anthropic 测试模型 id
export ANTHROPIC_SMALL_FAST_MODEL="e2e-claude-<ts>"
# (a) 快检小任务：
claude -p "print the string OK and nothing else"     # /status 可确认当前 base_url 与凭据来源
# (b) 重负载真实任务（长上下文 + 多轮 + 大输出）：
claude "阅读本仓库，找出 3 处可改进的地方并逐一解释原因，给出修改示例；有明确结束点"
# 若上游/模型支持 extended thinking，出一道多步推理题以触发 reasoning token（验证 reasoning 分项倍率）。
```

验收：
- [ ] Claude Code 请求成功。
- [ ] 网关日志出现 `POST /v1/messages`。
- [ ] DB：`cost_snapshots` 成本 = 基准×价格倍率×充值倍率；`price_snapshots` 售价对照；余额下降；请求详情显示成本倍率/充值倍率。

---

## 6. 核对 SQL（把断言落到数据）

```sql
-- 最近这条测试请求的成本快照全貌（换成你的 :user_id）
SELECT rr.id, rr.requested_model_id,
       cs.uncached_input_cost::text, cs.output_cost::text,
       cs.cost_multiplier::text, cs.recharge_factor::text,
       cs.cost_base_model_price_id, cs.channel_cost_multiplier_id, cs.channel_recharge_factor_id,
       cs.cost_price_id, cs.total_cost_amount::text
FROM request_records rr JOIN cost_snapshots cs ON cs.request_record_id = rr.id
WHERE rr.user_id = :user_id ORDER BY rr.id DESC LIMIT 5;

-- 售价侧对照（应不受成本改造影响）
SELECT ps.uncached_input_price::text, ps.output_price::text, ps.price_ratio::text
FROM request_records rr JOIN price_snapshots ps ON ps.request_record_id = rr.id
WHERE rr.user_id = :user_id ORDER BY rr.id DESC LIMIT 5;

-- 改倍率不漂移：改倍率后对比 R1（旧）与 R2（新）两行 cost_multiplier / 单价

-- 重负载：缓存 / reasoning 分项确实产生 token 且成本 = 基准价×价格倍率×充值倍率
SELECT rr.id,
       u.cache_read_input_tokens, u.reasoning_output_tokens, u.output_tokens_total,
       cs.cache_read_input_cost::text,   -- 期望 0.1200000000（=0.2×1.2×0.5）
       cs.reasoning_output_cost::text,   -- 期望 6.0000000000（=10×1.2×0.5）
       cs.cache_read_input_cost_amount::text, cs.reasoning_output_cost_amount::text, cs.total_cost_amount::text
FROM request_records rr
JOIN usage_records u  ON u.request_record_id = rr.id
JOIN cost_snapshots cs ON cs.request_record_id = rr.id
WHERE rr.user_id = :user_id AND (u.cache_read_input_tokens > 0 OR u.reasoning_output_tokens > 0)
ORDER BY rr.id DESC LIMIT 10;

-- 总额守恒（DB 有 ck_cost_snapshots_total_amount 约束，这里再肉眼核）：
--   total_cost_amount = 各分项 *_cost_amount 之和；大 token 量下无尾差。
```

验收：单价与"基准价×价格倍率×充值倍率"逐项吻合（含 cache/reasoning 分项）；大 token 量下金额无尾差、总额守恒；历史行改倍率后不变；覆盖路径 `cost_price_id` 非空且倍率列为 NULL。

---

## 7. 清理测试记录（跑完必做）

**DB**：在 `scripts/cleanup-agent-test-data.sql` 基础上，按本次命名 `e2e-costmult-*` 精准清，并**补 DEC-027 新表**。顺序（先子后父，避 FK）：

```sql
BEGIN;
CREATE TEMP TABLE _t_users AS SELECT id FROM users WHERE email LIKE 'e2e-costmult-%@example.test';
CREATE TEMP TABLE _t_routes AS SELECT id FROM routes WHERE name LIKE 'e2e-costmult-route-%';
CREATE TEMP TABLE _t_channels AS SELECT id FROM channels WHERE name LIKE 'e2e-costmult-ch-%';
CREATE TEMP TABLE _t_providers AS SELECT id FROM providers WHERE slug LIKE 'e2e-costmult-provider-%';

-- 请求链 + 账本（同 cleanup-agent-test-data.sql 的表集）
DELETE FROM ledger_billing_exceptions WHERE user_id IN (SELECT id FROM _t_users);
DELETE FROM ledger_reservations       WHERE user_id IN (SELECT id FROM _t_users);
DELETE FROM ledger_entries            WHERE user_id IN (SELECT id FROM _t_users);
DELETE FROM settlement_recovery_jobs  WHERE request_record_id IN (SELECT id FROM request_records WHERE user_id IN (SELECT id FROM _t_users));
DELETE FROM cost_snapshots            WHERE request_record_id IN (SELECT id FROM request_records WHERE user_id IN (SELECT id FROM _t_users));
DELETE FROM price_snapshots           WHERE request_record_id IN (SELECT id FROM request_records WHERE user_id IN (SELECT id FROM _t_users));
DELETE FROM usage_line_items          WHERE usage_record_id IN (SELECT id FROM usage_records WHERE request_record_id IN (SELECT id FROM request_records WHERE user_id IN (SELECT id FROM _t_users)));
DELETE FROM usage_records             WHERE request_record_id IN (SELECT id FROM request_records WHERE user_id IN (SELECT id FROM _t_users));
DELETE FROM channel_cost_exposures    WHERE channel_id IN (SELECT id FROM _t_channels);   -- 000074，若命中断开计费敞口
DELETE FROM request_attempts          WHERE request_record_id IN (SELECT id FROM request_records WHERE user_id IN (SELECT id FROM _t_users));
DELETE FROM request_records           WHERE user_id IN (SELECT id FROM _t_users);
DELETE FROM api_keys                  WHERE user_id IN (SELECT id FROM _t_users);
DELETE FROM user_balances             WHERE user_id IN (SELECT id FROM _t_users);
DELETE FROM users                     WHERE id IN (SELECT id FROM _t_users);

-- 线路
DELETE FROM route_channels WHERE route_id IN (SELECT id FROM _t_routes);
DELETE FROM routes         WHERE id IN (SELECT id FROM _t_routes);

-- 渠道 + 三层成本（DEC-027 新表）
DELETE FROM channel_recharge_factors  WHERE channel_id IN (SELECT id FROM _t_channels);
DELETE FROM channel_cost_multipliers  WHERE channel_id IN (SELECT id FROM _t_channels);
DELETE FROM channel_test_logs         WHERE channel_id IN (SELECT id FROM _t_channels);
DELETE FROM channel_models            WHERE channel_id IN (SELECT id FROM _t_channels);
DELETE FROM channel_prices            WHERE channel_id IN (SELECT id FROM _t_channels);
DELETE FROM channels                  WHERE id IN (SELECT id FROM _t_channels);

-- 基准价（按测试模型；先删引用它的快照已在上面完成）
DELETE FROM model_prices WHERE model_id IN (SELECT id FROM models WHERE model_id LIKE 'e2e-gpt-%' OR model_id LIKE 'e2e-claude-%');
DELETE FROM models WHERE model_id LIKE 'e2e-gpt-%' OR model_id LIKE 'e2e-claude-%';

DELETE FROM providers WHERE id IN (SELECT id FROM _t_providers);
COMMIT;
```

> 校验：`SELECT count(*) FROM channels WHERE name LIKE 'e2e-costmult-%';` 等应全为 0；生产渠道/`user_id=3` 不受影响。

**CLI 配置**：
- Codex：删掉 `~/.codex/config.toml` 里 `[model_providers.unio_local]` 块与 `model`/`model_provider` 覆盖（或还原备份）。
- Claude：`unset ANTHROPIC_BASE_URL ANTHROPIC_AUTH_TOKEN ANTHROPIC_API_KEY ANTHROPIC_MODEL ANTHROPIC_SMALL_FAST_MODEL`；若写进了 `~/.claude/settings.json` 的 `env` 块则移除。
- `unset UNIO_KEY`。

---

## 8. 通过标准（总）

- [ ] 三进程起、`/healthz` ok。
- [ ] Layer 1 自动化用例全 PASS（尤其**改倍率不漂移** + **冻结不受影响** + **覆盖优先** + **快照来源列**）。
- [ ] Layer 1.5 重负载全 PASS：大输入/大输出/**缓存分项倍率**/**reasoning 分项倍率**/流式异步收口/长多轮/大 token 下改倍率不漂移；金额无尾差、总额守恒。
- [ ] Codex CLI 真实跑通（含重任务）→ `/v1/responses` → 成本快照 = 基准价×价格倍率×充值倍率、请求详情三行倍率可见。
- [ ] Claude CLI 真实跑通（含重任务）→ `/v1/messages` → 同上。
- [ ] 售价侧、冻结、扣费方向不受成本改造影响。
- [ ] 清理后测试数据归零、生产数据无损、CLI 配置还原。

---

> 注：DEC-027（倍率机制）+ DEC-031（成本基数复用 `model_prices`）均已实现并迁移（迁移至 `000037`）。Layer 1 用例、`cost_snapshots` 成本来源列、请求详情倍率展示均已就绪，可直接按 1→8 顺序跑；E2E 花真钱，保持 prompt 与 `max_tokens` 最小。
