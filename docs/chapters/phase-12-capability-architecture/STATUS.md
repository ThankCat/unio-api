# Phase 12 Status

状态：planned

进入条件：阶段 11 OpenAI Responses 收口（ingress 表面冻结，CAPABILITY_MATRIX 静态文档可作为本阶段种子）。

## 任务表

| 任务 | 状态 | 说明 |
| --- | --- | --- |
| TASK-12.01 capability schema | planned | models / model_capabilities / channel_capability_overrides / sync_jobs 表 + capability_keys 注册表。 |
| TASK-12.02 ingress capability inference | planned | 三协议（Chat/Messages/Responses）请求体 → required_capabilities 推断纯函数。 |
| TASK-12.03 routing capability filter | planned | routing 加 capability filter；三协议各自原生 capability error 渲染。 |
| TASK-12.04 models.dev daily cron | planned | 每日同步 metadata；source=manual 不覆盖；新模型默认 disabled；license 审计。 |
| TASK-12.05 public capability surface | planned | `/v1/models` 扩展 cap-tags（保持 SDK 兼容），新增 `/console/v1/models` 给前端 console。 |
| TASK-12.06 adapter drop 对齐 | planned | adapter `dropUnsupported` 与 `CAPABILITY_MATRIX` 数据沉淀为 model_capabilities 种子；解决 GAP-11-010 `reasoning_effort` doc/code drift。 |
| TASK-12.07 observability + audit | planned | cap_check 指标、required/missing 计数、`request_records.capability_check_result`、sync metrics。 |
| TASK-12.08 灰度迁移 | planned | observe → enforce 切换；config 开关 `capability.enforce_mode` 按协议独立可控。 |

## 风险与关注点

1. capability_keys 注册表是公开稳定契约：发布即冻结、只能新增不能删除；命名前需要在 `docs/protocol/CAPABILITY_KEYS.md` review。
2. models.dev license：同步前必须确认 license 与 attribution 要求；首次同步与每次 license 变更入审计。
3. enforce 模式切换前必须完成观察期 + adapter 对齐（TASK-12.06），避免误拒生产请求。
4. 不引入跨 provider 拼接（DeepSeek 缺能力时不去外部 provider 拼接）；Unio 是网关不是 agent 平台。

## 与上下游阶段

```text
依赖：Phase 11 CAPABILITY_MATRIX 静态版本（迁入运行时）
影响：Phase 13 admin 直接基于本阶段表做 CRUD；不需要再设计能力表 schema
不影响：Phase 7 账务事实 / Phase 10 lifecycle / Phase 11 公开 API 表面
```

## 验证步骤（实现期对照）

```bash
sqlc generate
go build ./internal/... ./cmd/...
go vet ./internal/... ./cmd/...
go test ./internal/... ./cmd/...
git diff --check
```

同步 worker 启动后用 `--dry-run` 校验 conflict 列表；observe 模式上线后用 metrics 看 `unio_gateway_capability_missing_total` 分布再切 enforce。
