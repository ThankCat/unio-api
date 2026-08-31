# CDKEY v2 临时变更计划

## 范围

- 将 CDKEY 统一为 `UNIO-XXXX-XXXX-XXXX-XXXX`，后端拒绝无前缀旧格式。
- 更新 Admin 多面值批量生成、脱敏列表、兑换记录用户名称、500 行分页和稳定创建时间排序。
- 将导出收敛为 `selected`、`page`、`all` 三种范围，状态支持多选；完整值只通过受保护 CSV 流返回。
- 将 Console 兑换输入归一化为 16 格明文分组控件，并统一提交新规范值。
- 对本地 Dev 中已兑换的旧格式记录执行一次性事务修正，不改兑换事实、账本流水或余额。

## 不变量

- 普通 JSON、日志和前端状态不包含 `code_plaintext`；仅导出响应读取明文。
- 面值固定为 5、10、30、50、100、200、500 USD，单批总数量不超过 1000。
- 兑换仍在单事务内完成并保持幂等；已兑换 CDKEY 不可再次兑换、作废或删除。
- `code_prefix` / `code_suffix` 仅保存随机部分首尾各 4 位，哈希基于完整规范值。
- 创建时间排序使用同向稳定键：`created_at DESC, id DESC` 或 `created_at ASC, id ASC`。

## 验证

- 运行 Gateway 的 Go test、race、vet、build；运行 Admin/Console 的 test、typecheck、lint、build。
- 对 sqlc 变更执行 `sqlc generate`，Go 代码执行 `gofmt`，所有仓库执行 `git diff --check`。
- 数据库操作先留存快照，再在停止相关服务后执行受控事务，并只输出数量、状态和校验结果。

## 归档条件

该计划仅用于本次实施过程；待最终代码、Schema 和测试结果同步到 Blueprint 后按 Gateway 协作规则删除。
