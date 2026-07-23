# P4 实施：自主决策与待确认事项

> 关联：[ROUTING_P4_GLOBAL_BREAKER_PROVIDER_PLAN.md](./ROUTING_P4_GLOBAL_BREAKER_PROVIDER_PLAN.md)、[ROUTING_P4_IMPLEMENTATION_LOG.md](./ROUTING_P4_IMPLEMENTATION_LOG.md)
>
> 日期：2026-07-22

本文档集中记录实施中已经作出的补充决策及其当前落地状态。动态进度以
[ROUTING_P4_IMPLEMENTATION_LOG.md](./ROUTING_P4_IMPLEMENTATION_LOG.md) 为准；本文件不再保留已经失真的阶段估算。

---

## 1. 自主决策记录

### D-A. BaseURL 语义：root vs 含 `/v1`
- **背景**：计划 §4.6 规定 ProviderEndpoint BaseURL 是 adapter root，adapter 追加完整标准路径（`/v1/chat/completions` 等）。但改造前 OpenAI adapter 是「base 含 `/v1` + 追加 `/chat/completions`」，Anthropic 是「base 不含 `/v1` + 追加 `/v1/messages`」，两者不一致。
- **决策**：按 §4.6 统一为 **BaseURL=root**，adapter 用结构化 helper 追加完整 `/v1/...`。相应把 adapter/service 测试的 mock BaseURL 去掉 `/v1`（最终 URL 与 mock 路径均不变）。
- **影响**：**StarAPI 等 Endpoint 的 `base_url` 应填 root**（如 `https://open.codex521.cc`，不带 `/v1`）。Phase H seed 与真实 E2E 按此填写。

### D-B. `provider_endpoints.base_url` 全局唯一 vs 真实上游 E2E
- **背景**：§4.2 要求规范化 base_url **全局唯一**。但 channel 复合外键要求 endpoint 与 channel 同属一个 provider；黑盒 fixture 每个测试建独立 provider，若多个真实上游 E2E 都指向同一 StarAPI root，会撞唯一约束。
- **决策**：保留全局唯一约束（生产正确性优先）。mock fixture 使用各自的 `httptest` URL；真实上游 E2E 每次运行使用专用隔离数据库，并在同一运行内复用唯一的 Provider + Endpoint，不用修改生产唯一约束迁就测试。

### D-C. `request_attempts` 冻结列 nullable → NOT NULL 的过渡
- **背景**：§4.7 要求 attempt 冻结的 `provider_endpoint_id`/两类 Endpoint revision/`channel_config_revision`/`routing_candidate_index`/`upstream_operation` 最终为 `NOT NULL`。但这些值在 Phase E 重写 attempt 创建后才有来源。
- **决策**：本阶段以 **nullable + 值存在时 CHECK** 落地，保持 B–D 期间 `go test` 全绿；Phase E 接线所有插入路径供值后，再收紧为 `NOT NULL`（开发库可随意 up/down 重建，无历史包袱）。

### D-D. `app_settings` 关键设置值形状与 epoch seed（后续由 DEC-054 修订）
- **背景**：§4.8 最初要求替换共享的 `gateway.rate_limit_defaults`（删 `failure_policy`）、`gateway.circuit_breaker`、`gateway.routing_balance`（删 `enabled/weight_by_remaining`）、`concurrency_defaults` 值形状，seed `gateway.runtime_state_epoch`，删 `admin_backend.channel_health_thresholds`。
- **决策**：DEC-054 已将共享限流进一步拆成 `gateway.route_rate_limit_defaults` 与 `gateway.channel_rate_limit_defaults`，因此当前关键运行态设置共五项；两套默认都为 `0/0/0`，分别承担请求入口 429 与候选渠道跳过/fallback。完整当前契约与落地状态以主计划、DEC-054 和实施日志为准，本条只保留早期实施背景。

### D-E. BreakerStore 准入参数来源（已落地）
- **背景**：P4-D11 要求四维限额只能由 Redis admission control 原子解析，调用方不得自报。
- **决策**：`AcquireAttempt` 已删除调用方提供的 effective limits、并发上限和 breaker `Config`；Lua 只读取 Redis committed active controls。`RequestAdmission` 仅保留来自可信认证快照的显式四维 override，`nil=继承 Redis 线路默认`、`0=不限`、正数为显式上限。control 缺失、pending、stale 或畸形均 fail-closed。

### D-F. Endpoint 跨 Channel/跨模型 500 证据的归属点
- **背景**：§2.4/§2.5.3 要求 Endpoint 的 500/首token/body-timeout 只有跨 ≥2 Channel 且 ≥2 模型才计入 Endpoint。
- **决策**：遵循 §2.5.8「Finish 前完成稳定 attribution」，把「是否升级为 Endpoint EligibleFailure」的证据判定放在**调用方（Phase E lifecycle）**，BreakerStore 的 Lua 忠实应用调用方给定的 per-scope outcome。证据集合（§5.1 的 distinct channel/model 短 TTL 集合）在 Phase E 接线时实现。

### D-G. 迁移合并策略（按你本轮新增要求）
- **决策**：迁移保持「一表一文件」，列变更**就地并入建表文件**（channels/request_attempts/request_records/channel_test_logs/app_settings 均如此），3 张新表各自独立建表文件；不新增独立 ALTER 迁移。开发库空库重建验证通过。符合你「把独立迁移合并进所属表建表迁移」的要求。

---

## 2. 当前实施口径

继续按计划 §11 的检查点实施，不用阶段估算替代完成定义。每个检查点必须同时满足：生产代码完成、风险相称的测试通过、实施日志同步；没有完成的能力必须明确写成剩余项，不能因已有骨架或 focused test 通过而标记完成。

---

## 3. 当前剩余工作

P4 生产主链路和 H10 的线路/渠道默认限流拆分均已完成。当前剩余项只以
[ROUTING_P4_IMPLEMENTATION_LOG.md](./ROUTING_P4_IMPLEMENTATION_LOG.md) 的 H8“仍未完成的发布门禁”为准，
主要包括 24 小时时间门禁、active-owner 完整恢复闭环、外部 StarAPI Compact/双 Endpoint、
Admin live 浏览器矩阵、性能与正式发布/回滚演练；这些不影响本次 H10 拆分完成，但在收口前
不得把整个 P4/Phase H 标记为发布完成。
