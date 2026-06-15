# 阶段 14 状态 - models.dev 目录解耦

> 状态：in_progress（核心实现已落地并通过自动化验证；剩余少量测试/文档收口）。

## 已完成（done）

- **迁移（000027–000030）**：`model_catalog` / `model_catalog_capabilities` / `model_catalog_links` 建表；
  `models` 删 `canonical_id`/`removed_upstream_at`/`lab`、`source` 收敛为 `('manual','catalog')`；
  `model_capabilities` 删 `source`。`migrate up → down 4 → up` 可逆验证通过。
- **sqlc 查询**：`model_catalog.sql`、`model_catalog_links.sql` 新增；`models.sql` 改造（`CreateModel` 去 lab +
  可选元数据、`CreateModelFromCatalog`、`UpdateModel`/`RefreshAdoptedModelFromCatalog`、`ListModelsPage`/`CountModels`
  连带 `catalog` 追更字段 + `has_update_only`、`GetModelCatalogState`；退役 seed 三查询）；`model_capabilities.sql` 去 source +
  `DeleteModelCapabilitiesByModel`。`sqlc generate` 干净。
- **同步包 `internal/core/modelcatalog`**：`feed.go` 加单条目录指纹；`merge.go` 简化为目录 upsert/removal；
  `store.go` 改写为写 `model_catalog(+capabilities)` + 标记下架；`syncer.go` 结果口径改为 feed/upserted/removed/cap-hints。
- **采纳/刷新/提醒服务 `internal/service/admin/modelcatalog`**：目录浏览/详情；「从目录采纳」单事务（建模型 source=catalog +
  能力 + 关联，基线=当前指纹）；「从目录刷新」单事务（覆盖元数据 + 能力，更新基线、清 dismiss/snooze）；提醒 dismiss/mute/unmute/snooze。
- **admin API**：`GET /admin/v1/model-catalog`、`GET /admin/v1/model-catalog/*`（canonical_id 含 `/` 走通配段）、
  `POST /admin/v1/models/from-catalog`、`POST /admin/v1/models/{id}/catalog-refresh`、`POST /admin/v1/models/{id}/catalog-reminder`；
  模型 DTO 加 `catalog` 子对象、列表 `?has_update=true`；去能力 `source` 字段；bootstrap 装配。
- **前端 `unio-admin`**：`modelCatalog.ts` API 封装；独立「模型目录」页（浏览/搜索/采纳）；采纳弹窗（去前缀 model_id 预填 +
  元数据带入 + 能力增删改）；模型列表「有更新/已下架」徽章 + 目录差异面板 + 四个动作；模型表单去 lab + 可选元数据输入；
  能力对话框去 source 徽章；能力中心同步结果卡片改目录口径；侧栏新增「模型目录」。
- **验证**：后端 `go build/vet ./...` 干净、`go test ./...` 53 包全绿；前端 `tsc -b` / `eslint`（新增文件）/`vite build` 全绿；
  HTTP 端到端走通 list/get/adopt → 改指纹检测更新 → dismiss → refresh。

## 剩余项（todo）

- [ ] `internal/service/admin/modelcatalog` 的 DB 集成测试（采纳事务原子回滚、刷新覆盖+基线、提醒 mute/snooze/dismiss 真值矩阵）。
  目前这些路径已由 HTTP 端到端验证覆盖，但缺 Go 自动化回归。
- [ ] `ACCEPTANCE.md` 勾选到全绿并补 `PROJECT_STATUS.md`、必要时 `DECISIONS.md` 追加实现修正记录。
- [ ] 真实 models.dev 同步一次，确认目录页/采纳/追更在真实数据量下的搜索分页体验。

## 运行时不变（回归基线）

gateway `/v1/*`、`ListAvailableModelsForProject`、routing、capability gate 仍只读 `models` + `model_capabilities`；
目录三表运行时不参与。相关 DB 集成测试（capability store、model channel routing、delete cascade）已适配新 schema 并通过。
