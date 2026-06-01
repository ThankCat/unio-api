# Chapters

章节目录记录每个阶段的具体规划、状态和验收标准。

`AGENTS.md` 只保存全局规则、调度原则和协作规范；具体阶段目标、任务清单、验收标准和当前状态必须写在本目录下的章节文档中。

## 阶段完成原则

项目不采用“先做半成品、以后再补完整”的默认策略。

每个阶段一旦进入实现，就必须按本阶段 `PLAN.md` 和 `ACCEPTANCE.md` 收口到可验收状态。不能做着做着丢掉能力，然后把当前阶段该完成的事情长期留成 TODO。

阶段完成要求：

- `done` 只能表示该阶段目标已经实现、测试通过、文档同步，且没有本阶段必须关闭的 P0/P1 production TODO。
- `partial` 表示阶段未完成，不能当作已经交付的里程碑。
- `deferred` 只能用于明确不属于当前阶段边界、且已登记到后续阶段计划的事项。
- 当前阶段内影响公开 API 契约、资金、安全、账务事实、数据一致性或上线能力的欠账，不能为了推进章节而随意 deferred。
- 关闭阶段前必须扫描并复核当前阶段所有 `GAP-*`。

## 章节文档职责

每个阶段目录必须维护：

```text
PLAN.md
= 阶段目标、任务编号、任务锚点、实现边界和关联 GAP。

STATUS.md
= 当前完成状态，区分 done / partial / in_progress / todo / deferred / planned。

ACCEPTANCE.md
= 功能、生产、测试、文档验收标准。
```

章节内可以写详细技术路线、字段语义、分步任务和阶段专属边界；这些内容不要上提到 `AGENTS.md`。

## 阶段目录

| 阶段 | 计划 | 状态 | 验收 |
| --- | --- | --- | --- |
| 阶段 1 | [PLAN](phase-01-go-web/PLAN.md) | [STATUS](phase-01-go-web/STATUS.md) | [ACCEPTANCE](phase-01-go-web/ACCEPTANCE.md) |
| 阶段 2 | [PLAN](phase-02-infrastructure/PLAN.md) | [STATUS](phase-02-infrastructure/STATUS.md) | [ACCEPTANCE](phase-02-infrastructure/ACCEPTANCE.md) |
| 阶段 3 | [PLAN](phase-03-identity-api-key/PLAN.md) | [STATUS](phase-03-identity-api-key/STATUS.md) | [ACCEPTANCE](phase-03-identity-api-key/ACCEPTANCE.md) |
| 阶段 4 | [PLAN](phase-04-openai-compatible-api/PLAN.md) | [STATUS](phase-04-openai-compatible-api/STATUS.md) | [ACCEPTANCE](phase-04-openai-compatible-api/ACCEPTANCE.md) |
| 阶段 5 | [PLAN](phase-05-adapter-boundary/PLAN.md) | [STATUS](phase-05-adapter-boundary/STATUS.md) | [ACCEPTANCE](phase-05-adapter-boundary/ACCEPTANCE.md) |
| 阶段 6 | [PLAN](phase-06-model-channel-routing/PLAN.md) | [STATUS](phase-06-model-channel-routing/STATUS.md) | [ACCEPTANCE](phase-06-model-channel-routing/ACCEPTANCE.md) |
| 阶段 7 | [PLAN](phase-07-billing-ledger/PLAN.md) | [STATUS](phase-07-billing-ledger/STATUS.md) | [ACCEPTANCE](phase-07-billing-ledger/ACCEPTANCE.md) |
| 阶段 8 | [PLAN](phase-08-observability-stability/PLAN.md) | [STATUS](phase-08-observability-stability/STATUS.md) | [ACCEPTANCE](phase-08-observability-stability/ACCEPTANCE.md) |
| 阶段 9 | [PLAN](phase-09-openai-protocol-parity/PLAN.md) | [STATUS](phase-09-openai-protocol-parity/STATUS.md) | [ACCEPTANCE](phase-09-openai-protocol-parity/ACCEPTANCE.md) |
| 阶段 10 | [PLAN](phase-10-dual-protocol-gateway/PLAN.md) | [STATUS](phase-10-dual-protocol-gateway/STATUS.md) | [ACCEPTANCE](phase-10-dual-protocol-gateway/ACCEPTANCE.md) |
| 阶段 11 | [PLAN](phase-11-admin/PLAN.md) | [STATUS](phase-11-admin/STATUS.md) | [ACCEPTANCE](phase-11-admin/ACCEPTANCE.md) |

## 状态定义

| 状态 | 含义 |
| --- | --- |
| done | 当前阶段目标已经实现、测试通过、文档同步，且没有本阶段必须关闭的 P0/P1 production TODO。 |
| partial | 阶段未完成；可能已有部分能力，但仍有本阶段必须收口的生产欠账。 |
| in_progress | 当前正在实现或收口。 |
| todo | 当前阶段内必须处理，但尚未开始。 |
| deferred | 明确不属于当前阶段边界，且已登记到后续阶段计划。 |
| planned | 尚未正式进入。 |
