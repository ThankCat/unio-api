# AGENTS.md - Unio Gateway 协作规则

## 文档权威边界

- 当前实现行为以本仓库代码、数据库 Schema 和测试为唯一事实依据。
- Gateway 的长期产品、架构、契约和决策文档统一维护在
  [Unio Blueprint](https://github.com/unioapi/unio-blueprint/tree/main/docs/products/gateway)。
- 本仓库只保留 `README.md`、`DEVELOPMENT.md`、第三方许可声明和正在实施的临时改造计划。
- 不在本仓库重新建立长期阶段、状态、风险、决策、协议或 Provider 文档树。

## 改造与归档

1. 改造开始前，在 `docs/changes/<change-id>/PLAN.md` 编写临时计划。
2. 按计划修改代码并完成相应测试。
3. 只按最终代码、Schema 和测试证明的行为更新 Blueprint；不把计划、评价或未实现目标写成当前事实。
4. Blueprint 更新并校验通过后删除临时计划，实施过程由 Git 历史保留。

## 修改权限

- 未得到用户明确允许时，不修改生产代码。
- 用户已授权直接编写、运行和修复测试代码。
- 文档任务可以直接修改对应文档，但必须遵守上述权威边界。
- 保留工作树中已有的用户修改；不得 reset、checkout 或覆盖无关改动。

## 数据与测试

- 默认测试不得连接或清理用户现有 PostgreSQL、Redis 或其他本地业务数据。
- 需要外部依赖的测试使用隔离的临时资源，并在完成后清除由本次测试创建的资源。
- 不在测试、日志、错误或文档中写入真实 API key、credential、用户 prompt 或完整响应正文。
- 数据库测试优先使用事务回滚；Redis 测试使用独立 namespace。

## 实现约定

- 遵循现有包结构、命名、错误处理和测试 helper。
- HTTP 层处理协议、认证、DTO 和错误渲染；service 层负责编排；core 层表达领域能力；platform 层提供基础设施。
- 跨模块错误使用 `internal/platform/failure`，公开响应不得透传上游原始错误正文。
- 修改 `migrations/` 或 `sql/queries/` 后运行 `sqlc generate`，不手改 sqlc 生成文件。
- Go 代码运行 `gofmt`，验证范围与变更风险相符。

## 沟通与提交

- 默认使用中文，回答直接、清楚、技术严谨。
- 中文提交信息使用 `<type>(<scope>): <subject>`；`type` 为 `feat`、`fix`、`docs`、`style`、
  `refactor`、`test` 或 `chore`。

本地构建和测试说明见 [DEVELOPMENT.md](DEVELOPMENT.md)。
