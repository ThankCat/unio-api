# Unio API 文档索引

本文档目录用于管理项目进度、章节规划、生产欠账和架构决策。

## 阅读顺序

1. 先读根目录 [AGENTS.md](../AGENTS.md)，确认产品边界、技术栈、协作规则和 AI 行为规范。
2. 再读 [PROJECT_STATUS.md](PROJECT_STATUS.md)，确认当前项目整体状态和下一步。
3. 查看全服务目录方案时，阅读 [architecture/PROJECT_STRUCTURE.md](architecture/PROJECT_STRUCTURE.md)。
4. 进入具体章节时，从 [chapters/README.md](chapters/README.md) 进入对应阶段的 `PLAN.md`、`STATUS.md`、`ACCEPTANCE.md`。
5. 检查生产欠账时，阅读 [production/TODO_REGISTER.md](production/TODO_REGISTER.md)。
6. 涉及重大取舍时，阅读 [production/DECISIONS.md](production/DECISIONS.md)。
7. 选择第三方库或决定是否手写基础设施时，阅读 [production/THIRD_PARTY_POLICY.md](production/THIRD_PARTY_POLICY.md)。
8. 实现公开协议时，阅读 [protocol](protocol) 下保存的接口原始参考或官方摘要快照，并从对应章节的字段矩阵逐项收口。

## 目录职责

| 文档 | 职责 |
| --- | --- |
| [README.md](README.md) | 文档入口和阅读顺序。 |
| [PROJECT_STATUS.md](PROJECT_STATUS.md) | 当前全局状态。只记录阶段完成度、当前阻断项和下一步。 |
| [architecture/PROJECT_STRUCTURE.md](architecture/PROJECT_STRUCTURE.md) | 全服务目标目录结构、分层职责、依赖方向和部署关系。 |
| [chapters](chapters/README.md) | 每个阶段的详细计划、状态和验收标准。 |
| [production/TODO_REGISTER.md](production/TODO_REGISTER.md) | 全局生产欠账登记表。每个代码 TODO 必须有 GAP 编号并链接回章节任务。 |
| [production/DECISIONS.md](production/DECISIONS.md) | 重大架构和商业规则决策记录。 |
| [production/RELEASE_BLOCKERS.md](production/RELEASE_BLOCKERS.md) | 当前上线阻断项。 |
| [production/THIRD_PARTY_POLICY.md](production/THIRD_PARTY_POLICY.md) | 第三方库选择原则。 |
| [protocol](protocol) | OpenAI、Anthropic 等公开接口的原始参考或带来源日期的官方摘要快照，由阶段字段矩阵逐项消费。 |

## TODO 追踪规则

生产 TODO 必须同时满足：

1. 代码中有 `TODO(阶段X/production)` 注释。
2. 注释中包含稳定编号，例如 `[GAP-7-001]`。
3. [production/TODO_REGISTER.md](production/TODO_REGISTER.md) 中存在对应编号。
4. TODO register 必须链接到具体章节 `PLAN.md` 的具体任务锚点。

代码 TODO 只用于提醒实现位置，完整上下文以 TODO register 和章节计划为准。

## 章节文件约定

每个阶段目录至少包含：

| 文件 | 职责 |
| --- | --- |
| [PLAN.md](chapters/phase-07-billing-ledger/PLAN.md) | 本阶段任务拆解、任务编号、实现边界和风险。 |
| [STATUS.md](chapters/phase-07-billing-ledger/STATUS.md) | 本阶段当前状态，区分 done / in_progress / todo / deferred。 |
| [ACCEPTANCE.md](chapters/phase-07-billing-ledger/ACCEPTANCE.md) | 本阶段验收标准，区分功能验收、生产验收、测试验收和文档验收。 |

阶段专属技术参考（如 OpenAI 字段矩阵）可放在同目录额外 markdown 中。已完成阶段不再保留 HANDOFF 等临时交接文档。
