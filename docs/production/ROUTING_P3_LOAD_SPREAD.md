# P3：同成本档分流与热渠道 TPM 治理

> 状态：**设计决议 / 待实现**（2026-07-18）  
> 前置：会话 sticky（P0）+ 队首 TPM/并发短等（P1）已落地。  
> 相关审计：[GATEWAY_LIFECYCLE_AUDIT.md](./GATEWAY_LIFECYCLE_AUDIT.md)

---

## 1. 问题边界

| 能力 | 解决什么 | 不解决什么 |
|------|----------|------------|
| **P0 sticky** | 同会话无谓跨渠道 → 断上游 prompt cache（「大 uncache」） | 便宜渠道容量不够 |
| **P1 队首短等** | 本地并发/TPM 瞬时满时先等再换 | 60s 级 TPM 窗口真打满 |
| **P3 本文件** | 同成本档死打最便宜一条 → 打爆 → 升舱到贵渠道 | 改售价公式；客户自选路由 |

计划评审 R1 已写明：sticky 解决「无谓换道」；容量问题靠 P3。短等主要收益在**并发满**；TPM 真满时升舱并 sticky 改绑是预期行为。

### 现状（代码）

[`PrepareCandidates`](../../internal/service/gateway/lifecycle/candidates.go) 流程：

1. `sortCandidatesByMode`：`cheapest` 按 `output_cost` → `uncached_input_cost` **稳定排序**，平手回落 SQL `priority`
2. 能力过滤 / 熔断可用性
3. 失败软冷却 demote
4. sticky 置顶（绝对优先）

同档内没有随机或剩余容量加权 → priority 靠前的最便宜渠道被持续打满。

---

## 2. 决议（默认方案）

未另行推翻前，按下列默认实现。

### 2.1 与 sticky 共存

**只在下列情况做同档分流：**

- sticky miss（无绑定 / 绑定渠道已硬摘除后重选）
- 无 `sessionKey`（本就不粘）
- 首轮成功前的候选顺序（bind 之前）

**sticky 命中置顶不变**：已粘住的会话不因分流主动打断 cache。

### 2.2 同成本档定义

与现有 `costSnapshotLess` 平手一致：

- 主键：`ChannelCost.OutputCost`
- 次键：`ChannelCost.UncachedInputCost`
- 两者均不可比（无效）时视为同档内可并列

第一期**不做**百分比分桶（如 ±X%）。若上线后同档过窄，再加 `spread_tier_epsilon` 类设置。

### 2.3 同档内如何摊

在**最便宜同档**（排序后队首连续平手的一段）内：

1. 读取各候选本地 **剩余 TPM / 并发**（与 admit 同源口径；读失败则该候选权重按均匀处理）
2. **加权随机**重排同档段；权重 ∝ `max(0, remaining)`，全 0 则均匀随机
3. 同档以外的更贵候选顺序不变，仍作 failover 后备

热渠道接近上限 → 权重趋近 0（软治理），**不替代**熔断 / 429 冷却 / 凭据失效硬摘除。

### 2.4 作用线路 mode

| mode | 是否做同档分流 |
|------|----------------|
| `cheapest` | 是（主场景） |
| `stable` | 否（健康序优先；后续可另议） |
| `random` | 否（已全局洗牌） |
| `fixed` | 否（单候选） |

---

## 3. 落点与顺序

仍在 `PrepareCandidates` 内，插入位置：

```text
mode 排序
  → [P3] 最便宜同档加权随机   ← 新增；仅 cheapest 且本请求允许 spread
  → 能力过滤 / Available
  → 软冷却 demote
  → sticky 置顶              ← 不变，压过 demote 与 spread 结果
```

调用方无需感知；fail-open：容量信号不可用时退化为同档均匀随机或保持稳定序（实现时二选一，推荐均匀随机以免回到「永远第一条」）。

---

## 4. 配置

建议 `app_settings` key（示意，实现时落入 `gateway_settings` 注册表）：

| Key | 字段 | 默认 | 说明 |
|-----|------|------|------|
| `gateway.routing_spread` | `enabled` | `true` | 总开关，热更新 |
| | `weight_by_remaining` | `true` | false 时同档均匀随机 |

线路级覆盖第一期不做（与 sticky 线路开关解耦，全局即可）。

---

## 5. 可观测

| 信号 | 用途 |
|------|------|
| `routing_spread_total{result=applied\|skipped_*}` | 是否对本请求做了同档重排 |
| `routing_spread_tier_size`（histogram） | 同档候选个数 |
| 已有 sticky「钉非 cheapest 占比」 | 升舱/成本漂移（R2） |
| 结构化日志：`spread_tier_size` / `spread_applied` | 单请求排查 |

请求级 `routing_trace` 落库仍为独立后置项，不阻塞 P3。

---

## 6. 验收

1. 同模型池内 ≥2 条**代表成本相同**的渠道：无 sticky 的负载下，首轮命中不再长期 100% 落在同一 `channel_id`
2. sticky 命中会话：`final_channel_id` 仍稳定；不因 P3 主动换道
3. 人为打满档内一条渠道的本地并发/TPM：权重下降，新 miss 流量偏向同档其他渠道；仍满则既有短等 → failover
4. `gateway.routing_spread.enabled=false` 热关闭后，行为回退为当前稳定 cheapest 序

---

## 7. 明确不做（本阶段）

- 百分比成本分桶 / 差价阈值
- sticky 命中后主动拆会话摊负载
- 客户 Key/请求级分流开关
- 替代上游 429 策略或熔断
- 改 DEC-026 售价公式

---

## 8. 实现拆分（回家继续时）

1. `lifecycle`：同档切段 + 加权随机 + 单测（平手 2+/唯一档/无容量信号）
2. 注入剩余 TPM/并发读取（复用 admit / ratelimit 侧只读接口；禁止在排序阶段预占）
3. `app_settings` + settings applier 热更新
4. metrics + 结构化日志字段
5. Admin：系统设置 typed 编辑器（可后置，JSON 可先改）

预估：核心排序与测试为主要工作量；配置与指标为收尾。
