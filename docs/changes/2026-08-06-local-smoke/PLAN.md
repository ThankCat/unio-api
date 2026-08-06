# 本地上线前冒烟测试计划

## 目标

使用本机 Docker PostgreSQL、Redis 和本机进程完成发布前运行验证；不写入或清理现有 `unio` 开发业务数据。

## 范围

1. 创建独立 PostgreSQL 冒烟数据库并从当前 43 个迁移建库，使用独立 Redis namespace。已完成。
2. 启动 Gateway、Admin、Worker，验证健康检查、鉴权、运行时初始化和 Admin 会话。
3. 使用 CLI 覆盖 OpenAI Chat Completions、OpenAI Responses、Anthropic Messages、模型列表、账务、路由、Sticky/fallback 和错误路径。
4. 在隔离 Redis namespace 验证状态丢失恢复；验证 settlement recovery 的真实触发与收口。
5. 将每项证据、发现和对应修改建议持续记录在 `REPORT.md`。

## 约束

- 真实上游请求仅使用本机已有的测试配置，使用无敏感的最小提示词；不记录凭据、完整响应或上游原始错误正文。
- 所有本次创建的数据库、Redis key、进程和临时文件在结束时清理；若审计事实使应用数据不能安全删除，则保留独立数据库而不触碰现有开发库。
- 发现生产代码问题时只提出修复建议；是否修改生产代码由用户后续决定。

## 后续修复（已获授权）

针对冒烟测试发现的 dead settlement recovery 状态不一致：

1. 在 dead finalizer 的同一事务内，将 recovery job 关联且仍为 running 的 request attempt 收口为 failed，并保留 job 已固化的上游事实。
2. 增加数据库回归测试，验证 request、指定 attempt 与 reservation 一致收口；重放不重复释放或写入风险异常，且无关历史 attempt 保持原状。
3. 运行 lifecycle、worker、runtime-control 相关定向 Go 测试；根据结果更新本次 smoke 报告的结论。
