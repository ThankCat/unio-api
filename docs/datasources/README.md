# 数据源文档规范(docs/datasources)

本目录记录 Unio 消费的**第三方数据源**——既不是 Unio 对外暴露的协议(那归 [docs/protocol/](../protocol/README.md)),
也不是被代理的上游 provider(那归 [docs/providers/](../providers/README.md)),而是为元数据/能力/价格等**提供种子数据**的外部来源。

## 1. 定位与边界

- 只放 Unio 当作**数据来源**消费的外部服务的接口参考、字段结构、license 与 attribution。
- 数据源**不是运行时事实源**:能力闸门、计费、路由的事实只以 Unio 自己的库为准,数据源仅作种子。
- 引用关系:阶段计划(`docs/chapters/`)在描述同步逻辑时**引用**本目录,不重抄接口结构。

## 2. 在册数据源

| 数据源 | 文档 | 用途 |
| --- | --- | --- |
| models.dev | [MODELS_DEV_API.md](MODELS_DEV_API.md) | 模型元数据 / 价格基线 / logo 种子源(能力架构 Layer 1,见 [DEC-015](../production/DECISIONS.md)) |
| models.dev | [MODELS_DEV_LICENSE.md](MODELS_DEV_LICENSE.md)([GAP-12-005](../production/TODO_REGISTER.md#gap-12-005)) | license 摘要(MIT)与 attribution;首次同步前必须确认 |

## 3. 事实可追溯

- 每条事实必须能追溯到来源:`来源:[名称](URL)(查阅 YYYY-MM-DD)`。
- 体量、字段、接口数量这类有时效的信息,更新后刷新查阅日期。
