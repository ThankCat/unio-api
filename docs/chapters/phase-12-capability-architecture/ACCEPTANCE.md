# Phase 12 Acceptance

## 功能验收

1. `models` / `model_capabilities` / `channel_capability_overrides` / `model_capability_sync_jobs` 四张表已建立、有业务注释、有可逆 down migration、有 sqlc query。
2. 三协议（OpenAI Chat / Anthropic Messages / OpenAI Responses）的请求都能正确推断 required_capabilities，且推断结果可在 `request_attempts.required_capabilities` 中查到。
3. routing 在 model + protocol + project policy 过滤后再做 capability filter；找不到候选时按三协议各自原生格式返回 `model_capability_unavailable` / `channel_capability_unavailable`。
4. models.dev daily cron 可以在不破坏 `source=manual` 行的前提下，更新 metadata、登记冲突、登记缺失模型；新模型默认 `enabled=false`。
5. `GET /v1/models` 输出向后兼容 OpenAI SDK，且可选返回 `capabilities` 字段，并支持 `?capability=...` 过滤。
6. `GET /console/v1/models` 输出含 cap-tags、价格、上游数量、状态，并受 console 认证保护。
7. capability 闸门支持 observe 与 enforce 两种模式，可按协议独立切换；切换不需要重启。

## 生产验收

1. capability_keys 注册表（`docs/protocol/CAPABILITY_KEYS.md`）已发布，并以语义化版本管理；只能新增不能删除。
2. enforce 模式切换前必须经过 observe 期（不短于 7 天），观察期内 `unio_gateway_capability_missing_total` 已被复核。
3. capability 错误响应不暴露 channel 名、credential、上游 base_url 等敏感字段。
4. 闸门拒绝路径不写 ledger、不写 cost_snapshot，不污染账务事实（capability 失败永远是 ingress 级 4xx，不进 reservation）。
5. models.dev 同步路径有 license 摘要与 attribution；同步失败不阻塞主流程；连续 3 次失败有告警。
6. capability override（channel 层）只能做减法（限制 / 关闭），不能在 channel 上声明模型层未声明的能力。
7. adapter `dropUnsupported` 与 capability 闸门一致；如果出现"闸门通过但 adapter 仍 Drop"，单元测试必须报错。

## 测试验收

1. `core/capability/inference` 对三协议每条规则有单元测试；fuzz 测试覆盖未知字段不污染 required set。
2. `core/capability/gate` 单元测试覆盖：模型缺能力、channel override 关闭、limits 不满足、多 channel 部分可用。
3. routing capability filter 集成测试覆盖：候选完全可用 / 部分可用 / 全部不可用 / project policy 与 capability 联合过滤。
4. 三协议 handler 集成测试断言 capability error 渲染格式与 OpenAI / Anthropic 原生错误对齐。
5. models.dev sync worker 集成测试覆盖：首次入库、metadata 更新、`source=manual` 不被覆盖、上游删除标记、license 变更审计。
6. `/v1/models` 与 `/console/v1/models` 集成测试覆盖：cap-tags 输出、`?capability=` 过滤、缓存失效、未授权拒绝。
7. observe → enforce 切换集成测试：observe 模式下闸门只记录不拒绝；enforce 模式下闸门正确拒绝。
8. 单元测试覆盖 adapter drop 与 capability 闸门的一致性（GAP-11-010 关闭判据之一）。

## 文档验收

1. `docs/protocol/CAPABILITY_KEYS.md` 已发布，列出全部稳定 capability_key，并标注版本。
2. `docs/protocol/MODELS_DEV_LICENSE.md` 已记录 models.dev license 摘要与 attribution。
3. `docs/production/DECISIONS.md` DEC-015 与本阶段所有任务相互可索引。
4. `docs/chapters/phase-11-openai-responses-api/CAPABILITY_MATRIX.md` 增加"本静态文档由 Phase 12 model_capabilities 表接管"说明（实现完成后）。
5. `docs/production/TODO_REGISTER.md` 中 GAP-11-010 在 TASK-12.06 完成后被关闭；GAP-12-001 ~ GAP-12-009 全部存在并指向本阶段任务锚点。
6. `docs/PROJECT_STATUS.md` 反映阶段 12 完成状态、enforce 切换时间、cap-tags 公开版本。

## 关闭条件

- 三协议 enforce 模式已上线且至少观察 7 天无误拒。
- TASK-12.01 ~ TASK-12.08 全部 done。
- GAP-12-001 ~ GAP-12-009 全部关闭或明确转入阶段 13 admin（仅与 UI 相关的 deferred 允许）。
- GAP-11-010 在 TASK-12.06 完成时一并关闭。
