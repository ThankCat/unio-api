# Origin → Provider：已冻结决断（摘要）

> **权威全文：** 同目录 [`PLAN.md` §4](./PLAN.md)（若冲突以 PLAN §4 为准）  
> 日期：2026-07-27  
> 前提：开发库可清库 / 清 Redis

## 产品

| ID | 决断 |
| --- | --- |
| 模型 | `Provider → Channel`；删除 `provider_origins` |
| 约束 | 一 Provider 恰好一个 `origin`；官方与中转 = 两个 Provider |
| 生命周期 | 继续 **归档**（`status=archived`）；本变更不做硬删+账本自包含 |

## 技术决断

| ID | 决断 |
| --- | --- |
| **D1** | 归档腾坑：`origin := origin \|\| '__archived_' \|\| id`；立即清非在途 Redis 状态，permit / 并发租约 / 计数桶留待收口或 TTL；恢复只剥离当前 ID 的精确后缀，URL 冲突 → 409 |
| **D1b** | Channel 在**任意** `route_channels` → **409**（不静默拆线）；Provider 下仍有未归档 Channel 或非终态 routing operation → **409**；删除两侧 Cascade / WithReplacement |
| **D2** | 删除 Channel Duplicate / CopyChannelsToOrigin（Gateway + Admin + 合同测试） |
| **D3** | `Channel=enabled` ⇒ `Provider=enabled`；disabled Provider 下可创建 / 编辑 Channel 但只能为 disabled；archived Provider 禁止配置 |
| **D3b** | Provider disable 有 enabled Channel → **409**，须先停用渠道；不级联；Provider enable 不自动启用 Channel |
| **D4** | 清库直建终态表 `provider_routing_operations`（不合入 `runtime_control_operations`）；**readiness 查询同步改表名** |
| **D5** | Provider 创建后普通编辑不能改 `origin` / `status`；地址专用端点要求 `expected_origin_revision`，有 enabled Channel 时再要求 `confirm_enabled_channels=true`，不用 token |
| **D6** | 字段/API 用 **`origin`**，不用 `base_url`；`origin` 仅指地址字符串，不是实体 |
| **D7** | 删除 `/admin/v1/provider-origins*`（9 条）；能力并入 `/providers`；不留代理。**provider runtime / reset breaker 是新建端点**，需带 404 存在性护栏 |
| **D8** | 一次切换；Gateway → Admin → 最后 Blueprint，三仓分别正常 commit / push、不建 PR；新旧 Gateway/Admin 不混用 |
| **D9** | Provider 上保留**两条** revision：`origin_revision`（地址）+ `status_revision`（启停）。Lua 双轨结构（独立 fence generation / pending / stale 子原因）原样保留，只改名与作用域 |
| **D10** | **丢弃** `provider_origins.name`；Provider.name 为唯一展示名；DTO/UI 去 `provider_origin_name` 与「源站」列 |
| **D11** | **删除** combined routing 组合端点（`POST .../routing`）；Provider 只留「改 `origin`」「改 `status`」两条写路径，连 combined Lua / 类型 / kind 一起删。换地址后重新启用按 D5 分步做 |

## 审计补充（原计划漏项，详见 PLAN §5/§6/§7）

- `channel_test_logs` 两个 origin revision 列
- `request_attempts.breaker_origin_disposition`
- `circuit_breaker` 设置文档 5 个 origin 键；`origin_status_batch_max` 成死设定须删
- Gateway readiness 查询硬编码 `origin_routing_operations`
- `DeleteProvider` 硬删 CTE 会清 origins
- integrity fault-clear per-origin proof 结构
- 403 permission hash / 401 credential gate 固化 origin 两 revision
- `GET /providers/ops` 内嵌 `origins[]` 与 `affected_origin_count`
- p4fault harness 直接 seed `provider_origins` 与 `breaker:v2:origin:` 键
- 迁移删两个文件后需重新编号为连续

## 开写条件

- [x] D1–D11 已决并回写 `PLAN.md`  
- [x] 代码审计核对完成（含上方漏项）  
- [ ] 你确认「可以按 PLAN 开工」  
