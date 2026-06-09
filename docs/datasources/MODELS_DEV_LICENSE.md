# models.dev License 与 Attribution

来源：[models.dev](https://models.dev) / 仓库 [github.com/sst/models.dev](https://github.com/sst/models.dev)
（现组织迁移为 [anomalyco/models.dev](https://github.com/anomalyco/models.dev)，查阅 2026-06-09）。

本文件是 [GAP-12-005](../production/TODO_REGISTER.md#gap-12-005) 的关闭依据：在 [TASK-12.04](../chapters/phase-12-capability-architecture/PLAN.md#task-12-04-models-dev-sync)
每日同步使用 models.dev 数据**之前**，必须确认其 license 与 attribution 要求并记录在案。
接口字段结构见同目录 [MODELS_DEV_API.md](MODELS_DEV_API.md)。

## 1. License 事实

| 项 | 结论 |
| --- | --- |
| 仓库 license | **MIT License**，`Copyright (c) 2025 models.dev` |
| 数据载体 | 模型/价格数据以 TOML 文件存于该仓库（logo 为 SVG），随仓库一并 MIT 授权 |
| 数据本身是否另有独立 license | 否：仓库未对数据单列区别于代码的 license，数据文件适用仓库 MIT |
| 公开 API（`https://models.dev/*.json`）ToS / 隐私政策 | **截至查阅日无成文 ToS/隐私政策**（见仓库 issue #1639）；维护者在 issue 中非正式表示"不滥用即可商用"，但未落为书面条款 |
| 维护方 | SST（sst.dev）团队 |

MIT 授予的权利：自由使用、复制、修改、合并、发布、分发、再许可、出售，含商业用途。

MIT 的**唯一条件**：在软件（含数据）的副本或**实质性部分**中，保留版权声明与许可声明。

MIT 许可全文（仓库 `LICENSE`）：

```text
MIT License

Copyright (c) 2025 models.dev

Permission is hereby granted, free of charge, to any person obtaining a copy
of this software and associated documentation files (the "Software"), to deal
in the Software without restriction, including without limitation the rights
to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
copies of the Software, and to permit persons to whom the Software is
furnished to do so, subject to the following conditions:

The above copyright notice and this permission notice shall be included in all
copies or substantial portions of the Software.

THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE
SOFTWARE.
```

## 2. Attribution 要求与 Unio 落地

MIT 要求保留版权 + 许可声明。Unio 在**对外展示或分发**源自 models.dev 的实质性数据时，必须附 attribution：

> Model metadata sourced from [models.dev](https://models.dev) (© 2025 models.dev, MIT License).

落地位置：

- 本文件保留 MIT 全文与版权声明（满足"实质性部分"保留义务的事实底座）。
- 面向客户/控制台展示 models.dev 派生元数据的界面（如 `/console/v1/models`、文档站能力页）附上述 attribution 文案（[TASK-12.05](../chapters/phase-12-capability-architecture/PLAN.md#task-12-05-public-capability-surface)）。
- 价格基线字段仅作 catalog 展示，**绝不用于计费**（DEC-015）；展示处同样适用 attribution。

## 3. API ToS 模糊点：风险与缓解

公开 API 端点无成文 ToS 是事实模糊点。Unio 的合规姿态与缓解：

- **授权基础以仓库 MIT 为准**：我方消费的字段（元数据、价格、能力位）均来自 MIT 仓库的 TOML 数据，MIT 已足以覆盖使用/复制/分发权利；不依赖 API 端点的隐含 ToS 作为授权来源。
- **善意低频访问**：同步默认每日一次（cron），失败重试有上限退避，避免对 `models.dev` 服务造成压力（呼应 issue 中"不滥用"的期望）。
- **非运行时事实源**：能力闸门、计费、路由的事实只以 Unio 自有库为准，models.dev 仅作首次入库种子，下游不实时强依赖其可用性。
- **可切换取数源**：若未来 API 端点出现限制性 ToS，可改为直接消费 MIT 仓库的 release 工件 / TOML，授权基础不变。

## 4. 审计

- 首次启用 models.dev 同步、以及每次检测到 license 变化，必须写入 audit log（同步任务 `model_capability_sync_jobs.stats_json` 记录 license 版本指纹 + 本文件查阅日期）。
- 本文件更新（含查阅日期、license 文本、组织迁移）需同步刷新 §1 表格的查阅日期。

## 5. 关闭判据（GAP-12-005）

- [x] 确认 models.dev license 为 MIT 并记录全文与版权人。
- [x] 记录 attribution 文案与落地位置。
- [x] 记录公开 API 无成文 ToS 的事实、风险与缓解（授权基础回落到 MIT 仓库数据）。
- [ ] 同步代码在首次运行时把 license 指纹写入 sync_job 审计（随 [TASK-12.04](../chapters/phase-12-capability-architecture/PLAN.md#task-12-04-models-dev-sync) 实现）。
