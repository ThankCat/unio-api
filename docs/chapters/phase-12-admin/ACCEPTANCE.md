# Phase 12 Acceptance

## 功能验收

1. 管理员可以登录后台。
2. 可以管理用户、project、API key。
3. 可以管理 provider、带 protocol/adapter_key 的 channel、model、price。
4. 可以查看 request logs 和 billing logs。
5. 后台变更能影响 routing 和 `/v1/models`。

## 生产验收

1. 后台 auth 使用安全 JWT 策略。
2. 密码使用 argon2id。
3. credential 不以长期明文保存。
4. 后台所有敏感操作有审计日志。
5. 后台展示的错误和密钥信息已脱敏。

## 测试验收

1. admin auth 测试覆盖登录、过期、权限不足。
2. provider/channel/model/price 管理测试覆盖创建、更新、禁用。
3. credential resolver 测试覆盖成功、失败、轮换。
4. request/billing 查询测试覆盖权限和过滤条件。

## 文档验收

1. 后台权限模型写入章节文档。
2. credential 管理方案写入决策文档。
