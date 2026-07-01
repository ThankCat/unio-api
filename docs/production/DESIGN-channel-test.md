# 设计方案：渠道检测（Channel Test / 一键测渠道）

> 目标：给管理端每条渠道加「检测」能力——向该渠道的**真实上游**发一个最小请求，验证「连得上 + 凭据有效 + 模型可用」，并测出**延迟**、给出**可读的失败原因**。参考 new-api 的 Test / Test All，但按 unio 现有架构落地，先做**低风险的手动单测**，把「批量 / 自动禁用」列为需你拍板的第二阶段。
>
> - 撰写基准：对照当前工作区代码逐文件勘探，文件/行号来自真实代码。
> - 阅读约定：先看 §1（一句话）、§3（检测什么）、§8（分阶段），决策项集中在 §9。

---

## 1. 一句话

**检测 = 用渠道自己的 `base_url + 凭据`，挑一个该渠道绑定的模型，发一个「hi」的最小请求，看能不能在超时内拿到合法响应；成功则记录延迟，失败则把上游错误翻译成人话（凭据无效 / 模型不可用 / 超时 / 连不上）。**

---

## 2. new-api 是怎么做的（读源码结论）

来源：`QuantumNous/new-api` 的 `controller/channel-test.go`。

| 能力 | 做法 | 关键位置 |
|------|------|----------|
| 单渠道测试 | `GET /api/channel/test/:id?model=&stream=`：构造一个最小请求（`messages:[{role:user,content:"hi"}]`, `max_tokens:16`）走真实上游，计时，返回 `{success, message, time, error_code}` | `TestChannel` / `testChannel` / `buildTestRequest` |
| 测试模型选择 | 优先 query `model` → 渠道的 `TestModel` → 渠道模型列表第一个 → 兜底 `gpt-4o-mini` | `testChannel` |
| 端点自适应 | 按模型名猜端点：embedding/rerank/image/responses/compact/chat，用对应的最小请求体 | `buildTestRequest` |
| 延迟 | `time.Since(tik).Milliseconds()`，异步 `channel.UpdateResponseTime()` 落库 | `TestChannel` |
| 批量 + 自动 | `performChannelTests`：遍历所有渠道逐个测；错误达标（`ShouldDisableChannel`）或**响应时间超阈值**→ 自动禁用（渠道 `AutoBan` 开时）；已禁用渠道恢复成功→自动启用；产出 `{tested,succeeded,failed,disabled,enabled}` 汇总；作为周期系统任务运行 | `performChannelTests` |

**要点提炼**：检测的本质是**发一次真实的小请求**，其余（延迟、自动禁用、批量）都是围绕这一次调用的增强。

---

## 3. 检测什么（unio 版，明确清单）

一次成功的检测同时验证下面这些，失败时按错误归类给出原因：

| 检测项 | 如何验证 | 失败表现 → 归类 |
|--------|----------|-----------------|
| **连通性** | 能与 `base_url` 建立连接、拿到响应头 | 连接被拒/DNS/超时 → `unreachable` / `timeout` |
| **凭据有效** | 上游返回非 401/403 | 401/403 → `credential_invalid` |
| **模型可用** | 用该渠道绑定的 `upstream_model` 请求成功 | 404/400 model / "model not found" → `model_unavailable` |
| **协议正确** | 用渠道 `(protocol, adapter_key)` 对应 adapter 编解码成功 | 解码失败/协议不符 → `protocol_error` |
| **延迟** | 记录发起到拿到响应的毫秒数 | 超过设定阈值 → `slow`（阶段二才据此自动禁用） |
| **限额/过载**（顺带识别） | 上游 429 | → `rate_limited`（可重试，不代表渠道坏） |

> 说明：这是**主动探测**，与现有「被动熔断 / cooldown」正交——被动是等真实流量打到才发现坏渠道，主动是上线前/巡检时提前发现。`GATEWAY_LIFECYCLE_AUDIT.md` 已把「主动健康探测 + 一键测渠道」列为待补项，本方案正是补它。

---

## 4. 现状勘探（为什么 unio 落地很顺）

- **凭据明文存储**（产品决策，`channels.credential` NOT NULL + CHECK `<> ''`）：测试时**无需解密**，直接取用。见 `internal/service/admin/channel/channel.go`、`router.go:344`。
- **adapter 单次调用现成**：`chatAdapter.ChatCompletions(ctx, ch channel.Runtime, req ChatRequest) (*ChatResponse, error)`（`internal/core/adapter/openai/chatcompletions/chat.go:50`）——一次真实上游调用、返回响应或 typed 错误，**天然适合一次性检测**，且与网关走的是同一份代码/HTTP client，检测结果=真实行为。
- **channel.Runtime 结构简单**：`{ID, BaseURL, APIKey, Timeout, ProviderSlug}`（`internal/core/channel/runtime.go`）——手动 new 一个即可，参考 `router.go:395` 的构造。
- **adapter 注册表可按 key 解析**：`registry.Chat(adapterKey)` / `AdapterRegistry`（`lifecycle/adapter_registry.go`）。
- **绑定模型可查**：`channel_models`（`upstream_model` + status）提供「该渠道支持哪些模型」，供选测试模型。
- **admin 路由/handler 现成**：`internal/app/adminapi/channels.go` + `channels_ops.go` + `router.go` 挂 `/admin/v1/channels/...`；前端 `unio-admin/src/components/channels/*` + `src/lib/api/channels.ts`。

---

## 5. 接口设计

**端点**：`POST /admin/v1/channels/{id}/test`

请求体（均可选）：
```json
{ "model": "gpt-5.4", "stream": false }
```
- `model` 省略：按 §6 选默认测试模型。
- `stream` 省略：默认 `false`（非流式最小请求足够，阶段一）。

响应：
```json
{
  "success": true,
  "latency_ms": 742,
  "tested_model": "gpt-5.4",
  "http_status": 200,
  "error_code": null,
  "message": ""
}
```
失败示例：
```json
{ "success": false, "latency_ms": 128, "tested_model": "gpt-5.4",
  "http_status": 401, "error_code": "credential_invalid",
  "message": "上游拒绝：凭据无效或无权限（401）" }
```

> 始终返回 HTTP 200（检测本身成功执行），用 body 的 `success` 表达渠道是否健康——与 new-api 一致，便于前端统一处理。

---

## 6. 后端实现

新增 `internal/service/admin/channeltest`（或并入 `channelops`）：

1. **加载渠道**：`GetChannel(id)` → 校验存在。
2. **选测试模型**：
   - 入参 `model` 非空 → 用它（需能映射到该渠道的 `upstream_model`）。
   - 否则取该渠道 **第一个 enabled 的 `channel_models` 绑定** 的 `upstream_model`。
   - 都没有 → 返回 `no_model_bound`「渠道未绑定任何模型，无法检测」。
3. **构造 Runtime**：
   ```go
   rt := channel.Runtime{
     ID: ch.ID, BaseURL: ch.BaseURL,
     APIKey: strings.TrimSpace(ch.Credential),
     Timeout: timeoutOrDefault(ch.TimeoutMs),  // 复用渠道超时，缺省用一个较短的探测超时（如 15s）
     ProviderSlug: ch.ProviderSlug,
   }
   ```
4. **解析 adapter + 发最小请求**：按 `ch.Protocol`：
   - `openai` → `registry.Chat(ch.AdapterKey).ChatCompletions(ctx, rt, probeReq)`
   - `anthropic` → 对应 messages adapter 的单次方法
   - `probeReq`：`messages:[{role:"user",content:"hi"}]`, `max_tokens:16`, 非流式。
5. **计时 + 归类**：
   - `tik := time.Now()` → 调用 → `latency := time.Since(tik)`。
   - 成功（拿到合法响应）→ `success:true`。
   - 失败：用现有 `failure` code + 上游 status 映射到 §3 的 `error_code` 与中文 `message`（401/403→credential_invalid；404/400 model→model_unavailable；ctx deadline→timeout；连接错误→unreachable；429→rate_limited；其余→upstream_error）。
6. **（阶段一可选）落库**：见 §7；若不落库，纯返回结果。

Handler：`internal/app/adminapi/channels.go` 加 `test`，`router.go` 注册 `POST /channels/{id}/test`。

**成本说明**：检测发一次真实小请求（约十几个 token），**不计入任何客户账单**（不走 settlement）；属于运营成本，可忽略。可在文档/UI 注明「检测会向上游发一次计费很小的真实请求」。

---

## 7. 数据库（可选，推荐做）

给 `channels` 加 4 列，持久化「最近一次检测结果」，用于列表健康点 + 延迟展示（对齐 new-api 的 `response_time`）：

```sql
-- 000060_add_channels_test_result.up.sql
ALTER TABLE channels
    ADD COLUMN last_tested_at        TIMESTAMPTZ,
    ADD COLUMN last_test_ok          BOOLEAN,
    ADD COLUMN last_test_latency_ms  INTEGER,
    ADD COLUMN last_test_error       TEXT;
```
- 检测成功/失败后 `UPDATE` 这 4 列。
- 列表接口带出，前端渲染「● 正常 742ms / ● 异常 401」+ 悬浮显示 `last_tested_at`。
- 不做也行（纯即时返回），但持久化能让运营在渠道表一眼看健康度。**推荐做**。

> 迁移号 `000060`（现最大 `000059`，即上一改造的线路限流）。列全可空、非破坏性，不动现有数据。

---

## 8. 前端

- **渠道表 / 详情**：每行加「检测」按钮（`unio-admin/src/components/channels/*`）。
  - 点击 → 调 `POST /admin/v1/channels/{id}/test` → loading spinner → 成功 toast「正常 · 742ms」/ 失败 toast「异常 · 凭据无效」。
  - 可选「选择测试模型」下拉（默认自动选）。
- **健康列（配合 §7）**：新增一列展示 `last_test_ok` 圆点 + `last_test_latency_ms`，悬浮显示 `last_tested_at` 与 `last_test_error`。
- **API 层**：`src/lib/api/channels.ts` 加 `testChannel(id, {model?, stream?})`。

---

## 9. 分阶段与决策项（附推荐）

### 阶段一（推荐先做，低风险、高价值）
- 手动**单渠道检测**：端点 + 服务 + 前端按钮。
- 只返回结果，或顺带落 §7 的 4 列。
- **不做**任何自动禁用/启用——检测只报告，不改渠道状态。

### 阶段二（需你拍板，touches 线上路由）
- **「测试全部」** + **周期巡检**（系统任务）。
- **自动禁用/启用**：错误达标或延迟超阈值→禁用；恢复→启用。**风险**：上游偶发抖动可能误禁用正在服务的渠道。
  - **推荐**：默认**不自动禁用**；每条渠道加 `auto_ban` 开关（对齐 new-api），仅对显式开启的渠道生效；且要求「连续 N 次失败」而非单次，避免抖动误伤。

### 待决问题
1. **是否持久化最近检测结果（§7 四列）？** 推荐：**是**（列表健康度很实用）。
2. **是否要阶段二的批量/周期/自动禁用？** 推荐：先只做阶段一；阶段二等阶段一用顺后，按「opt-in `auto_ban` + 连续失败阈值」再上。
3. **默认测试模型来源？** 推荐：渠道第一个 enabled 绑定模型；允许前端下拉覆盖。（是否引入渠道级 `test_model` 字段可选，非必需。）
4. **流式检测？** 推荐：阶段一只做非流式；流式作为后续可选（能额外验证 SSE 通路）。
5. **超时**：检测用多长超时？推荐：`min(渠道 timeout, 15s)`，避免坏渠道把检测拖很久。

### 使用场景
- 新建渠道后点「检测」→ 立刻知道凭据/base_url 填对没、模型通不通，不用等真实流量。
- 换凭据/改 base_url 后一键复测。
- （阶段二）每小时巡检，第一时间发现上游挂掉的渠道并（可选）自动摘除。

---

## 10. 影响文件清单（实现时对照）

**后端**
- 新增 `internal/service/admin/channeltest/`（或并入 `channelops`）
- `internal/app/adminapi/channels.go`（加 `test` handler + DTO）、`router.go`（注册路由）
- 复用 `internal/core/adapter/*`、`internal/core/channel/runtime.go`、`lifecycle/adapter_registry.go`
- （§7）`migrations/000060_*`、`sql/queries/channels.sql`（更新最近检测结果 + 列表带出）、`internal/service/admin/channel/channel.go`

**前端**
- `src/lib/api/channels.ts`（`testChannel`）
- `src/components/channels/*`（检测按钮 + 结果 toast）、渠道列表健康列（配合 §7）

---

> 结论：unio 落地这个功能成本很低——**凭据明文 + adapter 单次调用现成**，阶段一基本是「加一个 admin 端点复用现有 adapter 发一次 hi 请求 + 前端一个按钮」。建议先做阶段一（不碰渠道状态），阶段二的自动禁用等你确认策略后再上。
