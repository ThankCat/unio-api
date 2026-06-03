# Unio API 文档索引

`docs/` 用于保存项目状态、阶段计划、生产欠账、架构决策和协议参考。根目录 [AGENTS.md](../AGENTS.md) 只保留 AI 协作时需要长期遵守的轻量规则。

## 当前阅读顺序

1. [PROJECT_STATUS.md](PROJECT_STATUS.md)：确认当前阶段、已完成阶段、上线阻断和下一步。
2. [chapters/README.md](chapters/README.md)：进入当前阶段的 `PLAN.md`、`STATUS.md`、`ACCEPTANCE.md`。
3. [production/TODO_REGISTER.md](production/TODO_REGISTER.md)：检查全局生产欠账和 GAP 编号。
4. [production/RELEASE_BLOCKERS.md](production/RELEASE_BLOCKERS.md)：检查公开生产前阻断项。
5. [production/DECISIONS.md](production/DECISIONS.md)：查看重大架构和商业语义决策。
6. [architecture/PROJECT_STRUCTURE.md](architecture/PROJECT_STRUCTURE.md)：查看全服务目标目录、分层职责和依赖方向。
7. [production/THIRD_PARTY_POLICY.md](production/THIRD_PARTY_POLICY.md)：新增依赖前使用。
8. [protocol](protocol)：实现公开协议字段时使用，并由对应阶段字段矩阵逐项消费。

## 文档职责

| 文档 | 职责 |
| --- | --- |
| [PROJECT_STATUS.md](PROJECT_STATUS.md) | 当前全局状态，只写当前阶段、完成度、阻断项和下一步。 |
| [chapters](chapters/README.md) | 阶段索引，以及每个阶段的计划、状态和验收标准。 |
| [production/TODO_REGISTER.md](production/TODO_REGISTER.md) | 全局生产欠账事实表。每个 production TODO 必须有 GAP 编号并链接回章节任务。 |
| [production/RELEASE_BLOCKERS.md](production/RELEASE_BLOCKERS.md) | 当前上线阻断项。 |
| [production/DECISIONS.md](production/DECISIONS.md) | 重大架构、商业规则和长期边界决策。 |
| [production/THIRD_PARTY_POLICY.md](production/THIRD_PARTY_POLICY.md) | 第三方库选择原则。 |
| [architecture/PROJECT_STRUCTURE.md](architecture/PROJECT_STRUCTURE.md) | 全服务目标目录结构、分层职责、依赖方向和部署关系。 |
| [protocol](protocol) | OpenAI、Anthropic 等公开接口的原始参考或带来源日期的官方摘要快照。 |

## 当前事实与历史档案

- 当前事实以 [PROJECT_STATUS.md](PROJECT_STATUS.md)、[production/TODO_REGISTER.md](production/TODO_REGISTER.md)、当前阶段 `PLAN.md` / `STATUS.md` / `ACCEPTANCE.md` 为准。
- 已完成阶段的 `PLAN.md` 可以保留当时的任务拆解和实现路径，作为历史档案使用。
- 代码目录经过后续阶段重构时，历史阶段文档只做必要断链修正，不重新承担当前架构说明职责。
- 动态进度不要写入 [AGENTS.md](../AGENTS.md)，必须写入本目录下对应状态文档。
