# Phase 13 Acceptance

## 功能验收

1. 管理员可以登录后台。
2. 可以管理用户、project、API key。
3. 可以管理 provider、带 protocol/adapter_key 的 channel、model、price、capability 声明与 channel 级 capability override。
4. 可以查看 request logs 和 billing logs。
5. 后台变更能影响 routing、`/v1/models` 与阶段 12 capability gating（无需重启）。
6. 可以触发并查看阶段 12 models.dev 同步状态与 conflict 列表。

## 生产验收

1. 后台 auth 使用安全 JWT 策略。
2. 密码使用 argon2id。
3. credential 不以长期明文保存。
4. 后台所有敏感操作有审计日志。
5. 后台展示的错误和密钥信息已脱敏。
6. capability 编辑只能由具备运营权限的 admin role 执行，且写入审计日志（含 before/after diff）。

## 测试验收

1. admin auth 测试覆盖登录、过期、权限不足。
2. provider/channel/model/price 管理测试覆盖创建、更新、禁用。
3. credential resolver 测试覆盖成功、失败、轮换。
4. request/billing 查询测试覆盖权限和过滤条件。
5. capability override 测试覆盖：override 写入后立即生效、删除 override 退回阶段 12 默认事实、override 写审计日志。

## 文档验收

1. 后台权限模型写入章节文档。
2. credential 管理方案写入决策文档。
3. capability 管理流程（同步触发、人工 patch、override）写入运营手册章节。
