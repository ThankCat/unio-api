# Phase 13 Status

状态：in_progress（admin-server，垂直切片推进；Slice 1 已交付）

阶段 13 只做 admin-server（`/admin/v1/*`）。模块分解、资源地图与推进顺序见 [ADMIN_MODULES_DRAFT.md](ADMIN_MODULES_DRAFT.md)；已交付契约见 [CONTRACT.md](CONTRACT.md)。

## 已交付（Slice 1，2026-06-09）

M1 静态 token 认证 + M3 provider/channel CRUD + M2 channel 凭据只写轮换，端到端打通并验证。

| 任务 | 状态 | 说明 |
| --- | --- | --- |
| TASK-13.01 | done | 静态 `ADMIN_API_TOKEN` 认证（常量时间比对）+ `AdminPrincipal` context 缝 + `/healthz`、`/admin/v1/ping`；JWT/RBAC/审计 deferred。 |
| TASK-13.03 | in_progress | provider CRUD + channel CRUD（List/Get/Create/Update）已交付；channel↔model 绑定、model provisioning、定价、能力管理待后续切片。 |
| TASK-13.02 | in_progress | channel 凭据只写轮换（`PUT /channels/{id}/credential`，AES-GCM 加密入库、不回读）已交付；凭据存储方案（master key vs KMS，GAP-6-001）待 13.02 规划定稿。 |

新增内部包：`internal/core/adminauth`、`internal/app/adminapi`(+`/middleware`)、`internal/service/admin/provider`、`internal/service/admin/channel`、`cmd/admin-server`、`internal/bootstrap/admin_server.go`+`admin_http.go`；新增 `sql/queries/providers.sql` 与扩展 `channels.sql`（sqlc 已生成）。

GAP 收口：**GAP-6-003 已关闭**——channel 写入路径用 adapter registry 校验 (protocol, adapter_key) 复合键，未注册返回 `admin_adapter_binding_unsupported`(422)。

## 验证（2026-06-09，真实 Postgres）

```bash
go build ./... ; go vet ./...                                   # 通过
DATABASE_URL=postgres://unio:***@localhost:5432/unio?sslmode=disable \
  go test ./...                                                 # 43 包全绿，0 失败
```

包含：adminauth / adminapi handler 状态码 / provider+channel service / DB 门控 provider+channel CRUD 集成测试。

> 运行 DB 测试前先把本地库重置到当前迁移（`migrate ... drop -f` + `up`）：源 migration 曾原地改表（如 `request_records.capability_check_result`），旧本地库 schema 会落后于迁移文件导致 ledger/requestlog/usage/settlement 等 DB 测试失败。

## 尚未开始

| 任务 | 状态 | 说明 |
| --- | --- | --- |
| TASK-13.04 | planned | request/usage/billing 只读查询台（M6）尚未开始。 |
| TASK-13.05 | planned | 客户/项目/预算/手工调额（M7）尚未开始。 |

后续切片（按 ADMIN_MODULES_DRAFT.md 推进顺序）：channel↔model 绑定 + model provisioning → 定价（M4）→ 能力管理（M5）→ 只读查询台（M6）→ 工作台看板（M9）。

## 进入阶段 13 前置条件（已满足）

1. 阶段 7 计费事实链路稳定。
2. 阶段 8 观测字段可支撑后台查询。
3. 阶段 10 双协议 gateway 链路稳定。
4. 阶段 11 OpenAI Responses 已收口（公开 API 表面冻结）。
5. 阶段 12 能力架构已交付（capability schema、运行时闸门、cap-tags API）。
6. credential resolver 生产方案：Slice 1 沿用 master key + 密文列；外部 KMS/secret manager 取舍留待 13.02（GAP-6-001）。
