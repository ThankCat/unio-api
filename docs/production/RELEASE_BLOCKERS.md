# Release Blockers

本文档只记录公开生产前必须解决的阻断项。

## 当前阻断项

当前无 P0 release blocker。Phase 9 GAP-9-001~004 已全部关闭。

阶段 10 双协议 Gateway 全链路改造进入实现后，任何影响公开协议、资金、审计、
settlement 或 recovery 的缺口都必须登记为 P0/P1 GAP，并按需进入本文件。

## 使用规则

1. 任何 `P0` 且 `release_blocker=yes` 的 GAP 必须同步进入本文档。
2. blocker 关闭时，先完成代码和测试，再更新 TODO register，最后移出本文档。
3. 本文档不记录普通优化项，只记录影响公开生产、资金、安全、账务或用户契约的阻断项。
