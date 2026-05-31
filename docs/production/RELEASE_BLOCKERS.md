# Release Blockers

本文档只记录公开生产前必须解决的阻断项。

## 当前阻断项

当前无 P0 release blocker。Phase 9 GAP-9-001~004 已全部关闭。

下一阶段阻断可能来自 TASK-9.12/9.14 黑盒验收失败项，验收后按需登记。

## 使用规则

1. 任何 `P0` 且 `release_blocker=yes` 的 GAP 必须同步进入本文档。
2. blocker 关闭时，先完成代码和测试，再更新 TODO register，最后移出本文档。
3. 本文档不记录普通优化项，只记录影响公开生产、资金、安全、账务或用户契约的阻断项。
