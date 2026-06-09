# Phase 13 Admin API — Slice 1 契约

> 范围：本文件只记录**已实现并通过测试**的 `/admin/v1` 端点（Slice 1，2026-06-09）。
> 全景资源地图（含未实现部分）见 [ADMIN_MODULES_DRAFT.md](ADMIN_MODULES_DRAFT.md)。
> `unio-admin` 前端只应消费本文件已列出的端点。

## 通用约定

- 认证：除 `GET /healthz` 外，所有端点要求 `Authorization: Bearer <ADMIN_API_TOKEN>`。
- Content-Type：写入端点要求 `application/json`，否则 `http_unsupported_content_type`(400)。
- 时间：响应中 `created_at` / `updated_at` 为 RFC3339 UTC；id 为 int64。
- 凭据：channel 上游凭据**只写不回**——任何响应都不含明文/密文。

### 成功信封

- 资源端点：`{ "data": <对象 | 数组> }`。
- 探针端点（`/healthz`、`/admin/v1/ping`）：裸 `{ "status": "ok" }`（不包 `data`）。
- 凭据轮换：`204 No Content`，无响应体。

### 错误信封（当前实现）

复用 gateway 的 OpenAI 兼容错误体（`httpx.WriteError`）：

```json
{ "error": { "message": "...", "type": "api_error", "param": null, "code": "admin_not_found" } }
```

- 4xx：`message` 为安全摘要（不含 cause/上游 body/SQL/凭据）。
- 5xx：`code` 一律 `internal_error`、`message` 一律 `internal error`，不透传内部细节。

| code | HTTP | 含义 |
| --- | --- | --- |
| `adminauth_missing_token` | 401 | 缺 `Authorization: Bearer` |
| `adminauth_invalid_token` | 401 | token 与 `ADMIN_API_TOKEN` 不匹配 |
| `admin_invalid_argument` | 400 | 参数非法（格式/枚举/范围/路径 id/query） |
| `admin_not_found` | 404 | 目标资源不存在（含 channel 的 provider 外键不存在） |
| `admin_conflict` | 409 | 唯一约束冲突（provider slug、同 provider 内 channel 名） |
| `admin_adapter_binding_unsupported` | 422 | (protocol, adapter_key) 未在 adapter registry 注册 |
| `http_*` | 400 | 请求解码/Content-Type 错误 |
| 其他（含 `admin_store_failed`） | 500 | 内部错误，统一通用文案 |

> 待定决策：草稿曾设想 admin 专属信封 `{"error":{"code","message","details"}}`，Slice 1 实际复用 OpenAI 信封。是否切换到带 `details` 的 admin 专属信封，留待后续切片统一定（影响前端错误解析）。

## 端点

### 探针

```text
GET /healthz            # 免鉴权 → 200 {"status":"ok"}
GET /admin/v1/ping      # 鉴权探针 → 200 {"status":"ok"}（用于校验 token 有效）
```

### Provider（M3）

```text
GET   /admin/v1/providers            → 200 {"data":[Provider]}
POST  /admin/v1/providers            → 201 {"data":Provider}
GET   /admin/v1/providers/{id}       → 200 {"data":Provider}
PATCH /admin/v1/providers/{id}       → 200 {"data":Provider}
```

`Provider`：

```json
{ "id": 1, "slug": "deepseek", "name": "DeepSeek", "status": "enabled",
  "created_at": "2026-06-09T12:00:00Z", "updated_at": "2026-06-09T12:00:00Z" }
```

- POST body：`{ "slug", "name", "status" }`。
- PATCH body：`{ "name", "status" }`（slug 不可变，不接受修改）。
- 校验：`slug` 匹配 `^[a-z0-9][a-z0-9-]{0,63}$`；`name` 非空；`status ∈ {enabled, disabled}`。slug 重复 → `admin_conflict`。

### Channel（M3 + M2 凭据）

```text
GET   /admin/v1/channels?provider_id=         → 200 {"data":[Channel]}
POST  /admin/v1/channels                      → 201 {"data":Channel}
GET   /admin/v1/channels/{id}                 → 200 {"data":Channel}
PATCH /admin/v1/channels/{id}                 → 200 {"data":Channel}
PUT   /admin/v1/channels/{id}/credential      → 204（凭据只写轮换）
```

`Channel`（不含凭据）：

```json
{ "id": 1, "provider_id": 1, "name": "deepseek-main", "protocol": "openai",
  "adapter_key": "deepseek", "base_url": "https://api.deepseek.com", "status": "enabled",
  "priority": 0, "timeout_ms": null,
  "created_at": "2026-06-09T12:00:00Z", "updated_at": "2026-06-09T12:00:00Z" }
```

- POST body：`{ "provider_id", "name", "protocol", "adapter_key", "base_url", "credential", "status", "priority", "timeout_ms?" }`。
- PATCH body：`{ "name", "base_url", "status", "priority", "timeout_ms?" }`（protocol、adapter_key、credential 不在此修改）。
- 凭据轮换 body：`{ "credential": "<明文>" }`，加密入库后返回 204。
- 校验：
  - `provider_id` 为正整数且 provider 存在（否则 `admin_not_found`）。
  - `protocol ∈ {openai, anthropic}`；`adapter_key` 非空。
  - **(protocol, adapter_key) 必须在 adapter registry 注册**，否则 `admin_adapter_binding_unsupported`(422)。
  - `base_url` 为合法 http(s) URL；`status ∈ {enabled, disabled}`；`priority >= 0`；`timeout_ms` 置值时必须 > 0。
  - `credential` 创建时必填；同 provider 内 channel 名重复 → `admin_conflict`。
- `GET /channels` 的 `provider_id` query 可选；置值时必须为正整数。

## Slice 1 未实现（前端勿调）

provider/channel `DELETE`、channel `/health` 子资源、channel↔model 绑定、model CRUD、定价、能力管理、只读查询台、工作台看板——见 [ADMIN_MODULES_DRAFT.md](ADMIN_MODULES_DRAFT.md) 推进顺序，按后续切片逐步落地。
