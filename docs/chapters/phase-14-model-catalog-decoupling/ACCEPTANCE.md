# 阶段 14 验收 - models.dev 目录解耦

## 功能验收

- [x] models.dev 同步只写 `model_catalog` + `model_catalog_capabilities`，**不再写运行时 `models`**。
- [x] 同步对 feed 全量 upsert，feed 不含的目录条目标记 `removed_upstream_at`（不删本地行）。
- [x] 独立「模型目录」页可浏览/搜索/分页目录条目，显示能力提示数与已采纳次数。
- [x] 「从目录采纳」：`model_id` 默认去前缀、可改；元数据带入；能力清单可增删改；单事务建模型(source=catalog)+能力+关联。
- [x] 同一目录条目可被采纳成多个模型（`model_catalog_links.canonical_id` 非唯一）。
- [x] 采纳模型的 `catalog` 子对象正确推导 `update_available` / `should_remind`（指纹比对 + mute/snooze/dismiss）。
- [x] 「从目录刷新」用目录最新值覆盖元数据 + 能力，更新基线指纹、清 dismiss/snooze，`model_id` 不变。
- [x] 更新提醒四动作：从目录刷新 / 忽略本次更新 / 永久忽略 / 稍后提醒（snooze）。
- [x] 空白手建模型支持可选元数据（上下文/价格基线/发布日期）；表单不再有 `lab`。
- [x] `model_capabilities` 去 `source`，前端去来源徽章。

## 生产/数据验收

- [x] 迁移 `up → down → up` 可逆；开发库重置后无 seed 污染。
- [x] `models` 删除级联清理 `model_catalog_links`（ON DELETE CASCADE）。
- [x] 运行时契约不变：gateway `/v1/*`、routing、capability gate 不读目录三表。

## 测试验收

- [x] 后端 `go build/vet ./...` 干净；`go test ./...` 全绿（53 包）。
- [x] 同步落目录 + 指纹 + 下架的 DB 集成测试（`sync_db_test.go`）。
- [x] 单条目录指纹稳定性/敏感性单元测试（`fingerprint_test.go`）。
- [x] 前端 `tsc -b` / `eslint`（新增文件）/ `vite build` 全绿。
- [ ] 采纳/刷新/提醒的 service DB 集成测试（当前 HTTP 端到端已验证，待补 Go 回归）。

## 文档验收

- [x] `PLAN.md` 任务勾选与状态更新。
- [x] `STATUS.md` / `ACCEPTANCE.md` 建立。
- [x] chapters `README.md` 索引登记阶段 14。
- [ ] `PROJECT_STATUS.md` / `DECISIONS.md` 收口时补充。
