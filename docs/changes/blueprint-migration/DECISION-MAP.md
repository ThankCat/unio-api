# Gateway 决策迁移映射

状态：待审核

基线日期：2026-07-25

## 1. 目的

本表覆盖 `docs/production/DECISIONS.md` 中 DEC-001 至 DEC-055。它用于把分散、追加修订的 Gateway
决策整理为 Blueprint 中少量、可维护的当前有效 ADR，同时保留来源编号、日期、原始状态和取代关系。

源文件中 DEC-028 排在 DEC-027 之前；本表按编号排序，不改变来源编号。来源未明确记录原始定稿日期的
条目标为“未记录”，不得根据 Git 以外的线索臆造日期。

迁移核验发现、但不能由本表自动裁决的冲突，统一进入 [`OWNER-REVIEW.md`](OWNER-REVIEW.md) 供 Owner
签署；推荐方案不改变来源 DEC 或 Blueprint ADR 的状态。

## 2. 导入规则

- `accepted`/`implemented` 是 Gateway 来源状态，不自动等同于 Blueprint `active`。
- 新建 Blueprint ADR 初始为 `proposed`；合并到普通设计的内容初始为 `draft`。
- 已完全取代的 DEC 不单独生成 ADR，只在有效 ADR 的“来源与取代历史”中保留。
- 部分取代的 DEC 必须逐条拆分“仍有效”和“已失效”范围。
- 每个目标 ADR 必须列出所有来源 DEC、来源日期（含“未记录”）和 supersession 关系。若某条 DEC 经分类
  属于跨仓库规范或候选架构原则而不单独形成 ADR，对应权威规范/原则文档必须保留同等来源谱系与状态。
- 目标 ADR 只写长期决策及理由，不复制阶段任务、代码路径、表名、Lua/SQL 结构或实施日志。

## 3. 拟议 ADR 集群

目标编号在正式创建时分配；这里使用稳定代号：

| 集群 | 拟议主题 | 拟议位置 |
| --- | --- | --- |
| `ADR-A` | 账户、API Key、请求身份与审计标识 | Gateway 或平台级 decisions；`G-ACCESS` 配套设计 |
| `ADR-B` | 公开协议、adapter 边界、直传/桥接和 Provider 映射 | Gateway decisions；`G-API`/`G-ADAPTER` 配套设计 |
| `ADR-C` | 第三方依赖选择原则 | 优先合并 `docs/specifications/coding.md`；必要时平台级 ADR |
| `ADR-D` | 预付余额、授权、结算、核销、成本和计费快照 | Gateway decisions；`G-BILLING` 配套设计 |
| `ADR-E` | 模型目录、能力字典和人工声明 | Gateway decisions；`G-CAP` 配套设计 |
| `ADR-F` | 分档线路产品、定价与内部供给边界 | Gateway/平台 decisions；`G-OV` 配套设计 |
| `ADR-G` | Admin 经营看板的事实口径 | Admin decisions 与页面设计 |
| `ADR-H` | 多实例准入、限流、Permit、过载和外部错误边界 | Gateway decisions；`G-ADMISSION` 配套设计 |
| `ADR-I` | Provider Origin 故障域、配置代际、凭据轮换和状态围栏 | Gateway decisions；`G-RUNTIME` 配套设计 |
| `ADR-J` | 流式 TTFT、客观路由信号与成本加权 Balanced | Gateway decisions；现有 `routing-load-balancing.md` |
| `ADR-K` | 上游责任归因、全局熔断与恢复退避 | Gateway decisions；`G-ROUTING` 配套设计 |
| `ADR-L` | 部署升级兼容和 Redis 拓扑支持边界 | Gateway/架构 decisions；`architecture/deployment.md` |
| `ADR-M` | Admin Provider/Origin/Channel 管理信息架构 | Admin decisions 与页面设计 |

## 4. 决策逐条映射（55）

“评审状态”均表示 Blueprint 迁移状态，而非 Gateway 实现状态。

| DEC | 标题 | 来源日期 | 来源状态 | 当前有效解释与取代关系 | 目标 | 评审状态 |
| --- | --- | --- | --- | --- | --- | --- |
| DEC-001 | 个人账户余额先落 user | 未记录 | accepted，当前实现部分超越 | 余额归 user 的结论仍有效；来源中的 Project 应用/API Key/用量边界未被当前实现保留。当前行为已折叠为 `User Account -> API Key -> Route`，且 Owner 于 2026-07-26 确认不重新引入 Project。未来 billing account 仍须新决定。 | `ADR-A`, `ADR-D` | Project 结论已确认；其余归属待复核 |
| DEC-002 | Adapter 不读取 provider/channel 配置 | 未记录 | accepted | 全部有效：adapter 只消费运行快照并负责协议/HTTP，不自行读取业务配置或保存业务状态。 | `ADR-B` | 待 owner 复核 |
| DEC-003 | Stream 无 final usage 暂不扣费 | 未记录；2026-06-25 修订 | accepted，部分修订 | 仅“首 token 前取消/无输出不扣费”仍有效；已输出但无 final usage 的路径由 DEC-025 partial settlement 取代。 | `ADR-D` | 谱系 + 有效残余待复核 |
| DEC-004 | request_id 与 correlation id 分离 | 未记录 | accepted | 全部有效：服务端业务 ID 不能被客户端 correlation ID 替代。 | `ADR-A` | 待 owner 复核 |
| DEC-005 | 第三方库选择不以“少用”为目标 | 未记录 | accepted | 原则有效；更适合作为跨仓库编码/依赖规范，是否需要 ADR 由架构 owner 决定。 | `ADR-C` | 待架构评审 |
| DEC-006 | 部分余额放行与平台差额核销 | 未记录 | accepted，已被后续 Owner 决定部分取代 | 部分余额授权和不产生用户负余额仍有效；其“客户实扣不超过原授权、差额全部 write-off”子句已被 2026-07-26 Owner 决定取代。当前行为是 capture 后以独立、幂等 overage debit 从剩余可用余额补扣，余额不足残差 write-off；`spend_limit` 是结算后计数、认证时拒绝的软上限。补齐 overage 与软上限测试是质量缺口，不覆盖当前行为。 | `ADR-D` | 当前口径已确认；测试缺口待补 |
| DEC-007 | Settlement 失败补偿归属 worker | 未记录 | accepted | 长期结果有效：上游已成功时不得简单 release；持久、幂等恢复负责收口。具体 worker 实现不入 ADR。 | `ADR-D` | 待账务 owner 复核 |
| DEC-008 | 第一版不支持倍率，金额快照属于账务事实 | 未记录 | accepted，部分超越 | “不支持倍率”由 DEC-026/027 超越；金额、用量和定价/成本快照作为账务事实仍有效。 | `ADR-D` | 谱系 + 有效残余待复核 |
| DEC-009 | OpenAI-first 公开契约与 adapter 响应翻译 | 未记录 | superseded by DEC-010 | 完全历史；双协议公开入口由 DEC-010 取代。不得恢复“仅 OpenAI 公开契约”。 | `ADR-B` | 仅谱系 |
| DEC-010 | 双协议公开入口、协议原生响应与统一事实 | 未记录 | accepted | 全部有效：公开协议分离、协议原生响应、统一请求/usage/计费事实和商业生命周期。 | `ADR-B` | 待 owner 复核 |
| DEC-011 | 生产 Adapter 不使用官方 Go SDK，retry 归 lifecycle | 未记录 | accepted | retry/lifecycle 归属是长期边界；“不用官方 Go SDK”需按当前维护风险复审，不应无条件固化为永恒禁令。 | `ADR-B`, `ADR-C` | 需重点复核 |
| DEC-012 | 协议为先与 Provider 映射 Drop 策略 | 未记录 | accepted | 公开协议校验与上游能力分离、无功能影响的不支持字段可 Drop 的方向有效；来源要求的“审计”当前仅在 DeepSeek Adapter 写字段名应用日志，尚无持久审计，Responses bridge 的诊断甚至未被消费。能力闸门部分以 DEC-024 为准。 | `ADR-B` | 待协议 owner 复核；审计实现有缺口 |
| DEC-013 | 协议 beta header 宽进接受与出站 Drop | 未记录 | accepted | ingress 宽进、出站按上游能力传递或 Drop、不因未登记 beta 自动 400 的方向有效；当前 beta Drop 仅写应用日志，来源要求的持久可关联审计尚未实现。 | `ADR-B` | 待协议 owner 复核；审计实现有缺口 |
| DEC-014 | OpenAI Responses ingress 下转 Chat Completions 桥接 | 未记录 | accepted | 桥接路径仅适用于 chat-only Adapter；DEC-018 补充 native passthrough 分流，OR-006 于 2026-07-26 选择 B 并批准该组合。bridge 支持矩阵、审计和协议验收仍未闭环。 | `ADR-B` | 路径选择已确认；实施门禁待完成 |
| DEC-015 | 能力架构三层模型与 models.dev 定位 | 未记录；2026-06-23 部分取代 | 部分 superseded | 外部目录与正式模型、模型层能力声明仍有效；Layer 3 由 DEC-023 移除，能力闸门/自动校正由 DEC-024 移除。 | `ADR-E` | 谱系 + 有效残余待复核 |
| DEC-016 | Responses reasoning opt-in 与 DeepSeek 归一 | 未记录 | accepted | 全部有效：reasoning 显式 opt-in，DeepSeek `reasoning_effort`/thinking 在 adapter 内归一。 | `ADR-B` | 待协议 owner 复核 |
| DEC-017 | 分档网关、绝不降级、透明市场顺延 | 未记录 | accepted，部分修订 | “线路内供给、绝不跨档降级、渠道不暴露”有效；档的载体由 DEC-026 改为 Route，BaseURL/故障域由 DEC-032 修订。 | `ADR-F` | 待产品 owner 复核 |
| DEC-018 | 上游 Responses 直传 + 第三方桥接分流 | 未记录 | accepted | OR-006 于 2026-07-26 选择 B：注册原生 Responses 能力的 Adapter 走 passthrough，chat-only Adapter 走 DEC-014 bridge，账务事实统一。批准路径不等于完整 bridge 兼容已经交付。 | `ADR-B` | 路径选择已确认；协议验收待完成 |
| DEC-019 | 可配置请求体上限 + Compact 双路径 | 未记录 | accepted | 全部有效：大小门禁、NativeCompact/SyntheticCompact 分流、错误和 fallback 边界。 | `ADR-B` | 待协议/安全复核 |
| DEC-020 | 被动证据式模型能力自动校正 | 未记录；2026-06-23 取代 | superseded by DEC-024 | 完全历史；不得迁入当前能力模型。 | `ADR-E` | 仅谱系 |
| DEC-021 | 经营驾驶舱三层架构，按币种拆卡不引汇率 | 未记录 | accepted | 事实口径有效；属于 Admin 产品领域，不应留在 Gateway ADR。 | `ADR-G` | 待 Admin/财务复核 |
| DEC-022 | 能力证据体系 v2 | 未记录；2026-06-23 取代 | superseded by DEC-024 | 完全历史；used_capabilities 自动证据闭环不得迁回当前能力模型。 | `ADR-E` | 仅谱系 |
| DEC-023 | 移除能力架构 Layer 3 渠道能力收紧 | 未记录 | implemented | 结论有效：无渠道能力收紧层；关于依赖自动校正的旧理由被 DEC-024 更新。 | `ADR-E` | 待 owner 复核 |
| DEC-024 | 移除自动校正与能力闸门，改能力字典 + 人工声明 | 2026-06-23 | accepted，来源标注待实施 | 保留已实现的能力字典、模型声明、移除自动学习和移除 ingress 能力闸门。当前声明还可由目录采纳、目录刷新 service 和 Adapter 画像物化写入，人工不是唯一来源；active ADR 按该代码事实接收。 | `ADR-E` | 当前事实已核验；OR-007 只读建议仍是未实现改造目标 |
| DEC-025 | Stream partial settlement | 2026-06-25 | accepted，来源标注待实施 | 修订 DEC-003：已经输出但没有 final usage 的流式路径按可审计部分结算收口；实现状态另行核验。 | `ADR-D` | 待实现核验与账务复核 |
| DEC-026 | 线路=分组 + 倍率定价 | 2026-06-29 | accepted | 修订 DEC-008/017：Route 是产品档位并绑定 Key；售价为模型基准价乘线路倍率，fallback 不改变锁定客户价。 | `ADR-F`, `ADR-D` | 待产品/账务复核 |
| DEC-027 | 渠道成本倍率 | 未记录；来源称已实现 | accepted，部分修订 | 成本倍率、充值倍率、绝对覆盖和快照有效；独立参考成本基数由 DEC-031 取代。 | `ADR-D` | 待账务复核 |
| DEC-028 | 缓存感知 TPM 与未结算释放 | 2026-07-01 | accepted，DEC-041 补充 | cache_read 不占 TPM、cache_write 保留，按真实 usage 对账；候选原子准入/资源所有权由 DEC-041 固定。该预占属于 admission 资源，不是 ledger reservation，原决策明确不改变 billing。 | `ADR-H` | 待运行复核 |
| DEC-029 | 在途并发上限 + 失败软冷却 | 2026-07-10 | accepted，大部分实现被替换 | 过载保护目标保留；入口并发/限流由 DEC-043、Channel 并发由 DEC-041 接管；本地失败软冷却由 DEC-045 废止。 | `ADR-H`, `ADR-K` | 谱系 + 有效目标待复核 |
| DEC-030 | 缓存写入 30m 独立计费维度 | 2026-07-10 | accepted | 全部有效：30 分钟缓存写入是独立 usage/price 维度，不并入 5m/1h。 | `ADR-D` | 待账务复核 |
| DEC-031 | 成本基数改用模型基准价 | 2026-07-14 | accepted，来源称已实现 | 修订 DEC-027：售价和成本共用模型基准价，倍率/绝对覆盖及快照保持。 | `ADR-D` | 待账务复核 |
| DEC-032 | ProviderEndpoint 承载 BaseURL 与公共故障域 | 2026-07-20 | accepted，来源标注待实现 | BaseURL + 公共故障域语义有效；术语必须服从 Blueprint active ADR-0001，导入时称 Provider Origin，不恢复旧 `ProviderEndpoint` 定义。 | `ADR-I`，关联现有 ADR-0001 | 需术语/实现重点核验 |
| DEC-033 | AttemptPermit 与状态代际隔离迟到结果 | 2026-07-20 | accepted，来源标注待实现 | 代际隔离、迟到结果 no-op 与资源仍需精确收口有效；由 DEC-036/038/040/041 补全。 | `ADR-I`, `ADR-H` | 待实现核验 |
| DEC-034 | 流式与非流式延迟信号隔离 | 2026-07-20；2026-07-21 取代 | superseded by DEC-035 | 完全历史且来源明确“不得实施”；不得生成独立目标 ADR。 | `ADR-J` | 仅谱系 |
| DEC-035 | 仅流式 FirstToken 生成唯一 TTFT EWMA | 2026-07-21 | accepted，来源标注待实现 | 替代 DEC-034：当前代码只让协议定义的流式 `FirstTokenEligible` 事件形成 Channel TTFT 样本；非流式不产生样本，无样本不伪造延迟。 | `ADR-J` | 当前代码事实已核验 |
| DEC-036 | Channel config revision 隔离迟到结果 | 2026-07-21 | accepted，来源标注待实现 | Channel 配置代际隔离有效；限额使用独立 admission revision，不混入 config revision。 | `ADR-I` | 待实现核验 |
| DEC-037 | 失效凭据轮换后自动检测 | 2026-07-21 | accepted，来源标注待实现 | credential_valid=false 的轮换/检测语义有效；DEC-039 扩展到原先有效凭据并统一响应。 | `ADR-I` | 与 DEC-039 合并复核 |
| DEC-038 | Origin status revision 与全局准入围栏 | 2026-07-21 | accepted，来源标注待实现 | 状态更新、围栏、全局故障域和迟到结果边界有效；导入时使用 Blueprint Provider Origin 术语。 | `ADR-I` | 待实现核验 |
| DEC-039 | 有效凭据轮换先暂停并自动检测 | 2026-07-21 | accepted，来源标注待实现 | 补充 DEC-037：保存新凭据先失效/暂停，检测成功后恢复；旧请求按代际规则收口。 | `ADR-I` | 待实现核验与安全复核 |
| DEC-040 | Redis/BreakerStore 故障统一 fail-closed | 2026-07-21 | accepted，来源标注待实现 | 当前 request admission、整批快照和 candidate Store 故障的分层 fail-closed 事实已核验；candidate 业务拒绝只跳当前候选。DEC-043/054 扩展具体 control，来源状态不改写。 | `ADR-H`, `ADR-I`, `ADR-L` | 当前核心事实已核验（OR-010/011）；组合恢复验收仍缺 |
| DEC-041 | 候选级资源原子准入与统一 AttemptPermit 所有权 | 2026-07-21 | accepted，来源标注待实现 | 当前代码已在每个真实 transport 前以新 `AttemptPermit` 原子取得候选 breaker/concurrency/RPM/RPD/TPM；拒绝发生在 attempt 与上游调用前，统一 permit 持有服务端资源 token。DEC-042/043 补充。 | `ADR-H` | 当前代码事实已核验（OR-010） |
| DEC-042 | Channel 限额热更新只影响新 Acquire | 2026-07-21 | accepted，来源标注待实现 | 当前每次 candidate Acquire 强读 integrity、ChannelRate、GlobalConcurrency 与 CircuitBreaker control facts；ChannelAdmission revision 来自冻结候选计划。已签发 permit 不因新限额追溯取消并按既有资源事实终结；DEC-043 定权威，DEC-044 定不限期计数。 | `ADR-H` | 当前代码事实已核验（OR-010） |
| DEC-043 | Redis admission control 是多 Gateway 当前限额权威 | 2026-07-21 | accepted，来源称已实现，部分修订 | 当前 request token、只读 `SnapshotMany` 和逐候选 `AttemptPermit` 均以 Redis control/revision 为执行权威；原共享 rate-control key/作用域只由 DEC-054 拆分，不改写本 DEC 其余谱系。 | `ADR-H`, `ADR-I` | 当前代码事实已核验（OR-010） |
| DEC-044 | 0=不限时仍持续记录准入用量 | 2026-07-21 | accepted，来源标注待实现 | 当前 Lua 中不限表示不触发拒绝门槛，不表示停止记录稳定窗口用量。 | `ADR-H` | 当前代码事实已核验（OR-010） |
| DEC-045 | 只有真实上游责任结果进入 breaker | 2026-07-21 | accepted，来源标注待实现 | 修订 DEC-029：删除本地软冷却；仅真实上游责任结果进入对应 Origin/Channel breaker。 | `ADR-K` | 待实现核验 |
| DEC-046 | 熔断阈值、样本边界与恢复退避 | 2026-07-21 | accepted，来源标注待实现 | 在 DEC-045 归因基础上定义连续/比例触发、样本排除、half-open 和退避。 | `ADR-K` | 待实现核验 |
| DEC-047 | Balanced 默认参数进入系统设置并热更新 | 2026-07-21 | accepted，来源称已实现，DEC-055 扩展 | 热更新参数与基础客观权重有效；最终公式加入 DEC-055 成本因子和过期错误窗口修正。 | `ADR-J` | 与 DEC-055 合并核验 |
| DEC-048 | 外部 429、503 与 model_not_found 边界 | 2026-07-21 | accepted，来源标注待实现 | 全部有效：业务容量不足、基础设施不可用和不存在/不可访问模型必须使用不同外部语义，内部 trace 保持完整。 | `ADR-H`, `G-ERROR` | 待实现/协议复核 |
| DEC-049 | Admin 删除主观健康标签，只展示客观事实 | 2026-07-21 | accepted，来源标注待实现 | 全部有效：不综合伪造主观“健康”，展示 breaker、错误率、TTFT、限额等客观事实。 | `ADR-J`, Admin 页面设计 | 待 Admin/实现核验 |
| DEC-050 | 当前开发库停机空库重建，不支持新旧版本混跑 | 2026-07-21 | accepted，来源标注待实现 | 范围仅是当前可重建开发环境。空库重建留在 Gateway `DEVELOPMENT.md`，不迁为平台生产迁移、备份或回滚规则；来源状态不改写。 | `ADR-L` 或 repository-only | 开发环境范围边界已核验（OR-011） |
| DEC-051 | P4 不支持 Redis Cluster | 2026-07-21 | accepted，来源标注待实现 | 当前代码只有单地址 Redis client；Gateway、Worker runner 与维护 CLI 拒绝 Cluster 并要求 Redis 7+，Admin 缺少同一预检。Sentinel、自动 failover 与其他高可用形态没有实现证明。 | `ADR-L`, `ADR-H` | 当前预检差异已核验（OR-011）；Admin/高可用缺口保留 |
| DEC-052 | Provider 列表直接展示 Origin 并提供行级创建 | 2026-07-23 | accepted，来源称已实现 | Admin 信息架构有效；导入时按 Blueprint ADR-0001 使用 Provider Origin 术语。 | `ADR-M` | 待 Admin/实现核验 |
| DEC-053 | 线路与渠道 RPM/TPM/RPD 默认均不限 | 2026-07-23 | accepted，来源称已实现，部分修订 | 两类默认均为 0/0/0 且不限仍计数有效；原共享 key/作用域由 DEC-054 取代，其余结论保留。 | `ADR-H` | 与 DEC-054 的修订边界已核验（OR-010/011） |
| DEC-054 | 线路默认限流与渠道默认限流完全拆分 | 2026-07-23 | accepted，来源称已实现 | 当前线路 request admission 与 Channel candidate admission 使用独立配置域、revision 和稳定 key；本 DEC 只修订 DEC-043/053 的旧共享 rate-control 作用域，不取代整个 DEC-043。 | `ADR-H` | 当前代码事实已核验（OR-010） |
| DEC-055 | Balanced 加入受限渠道成本因子并修正过期错误窗口 | 2026-07-23 | accepted，来源称已实现 | 扩展 DEC-047：最终权重加入受限成本因子；过期错误窗口不继续惩罚，成本为偏好而非确定首选。 | `ADR-J` | 待实现核验 |

## 5. 已知冲突与迁移注意点

### 5.1 术语优先级

Blueprint 已有 active 的 `docs/products/gateway/decisions/adr-0001-domain-terminology.md`：

- Protocol = API 格式族；
- Endpoint = 网关公开 API 操作/路径；
- Provider Origin = 上游 `base_url`/host 和公共故障域。

因此 DEC-017、DEC-032、DEC-038、DEC-045、DEC-052 等来源中的 `ProviderEndpoint` 只能作为旧代码/旧
文档名称保留在来源说明，目标正文必须使用 Provider Origin。DEC-032 的“BaseURL 与公共故障域归同一
实体”仍可迁移，但不能覆盖 Blueprint 已接受的术语决策。

### 5.2 决策状态不等于实现状态

大量 DEC 在同一状态行中同时写“accepted”和“待实现”，少数写“已实现”。迁移必须分别记录：

- 决策是否仍有效；
- Blueprint 是否已经批准；
- Gateway 当前是否实现；
- 测试是否证明关键行为。

目标 ADR 不写动态完成百分比。未实现差距进入 `G-ROADMAP` 或 `A-RISK`，代码仓库的单次改造步骤进入
临时 change plan。

### 5.3 来源决策与当前证据的冲突登记

以下证据只用于防止迁移失真，不会自动取代已接受决策：

- **Project 层级**：DEC-001 只确定个人余额先归 user，不能单独证明 Project 仍是当前身份层。当前 schema
  与认证代码已折叠为 `User Account -> API Key -> Route`；Owner 于 2026-07-26 确认不重新引入 Project。
  未来 Project 或 billing account 是独立优化/产品决定，不能改写当前身份、Route 或账本归属。
- **API Key 完整明文**：当前 `api_keys.key_plaintext` 保存完整值，已注册的 Admin API Key 运维列表、请求
  列表/详情、更新与吊销响应可重复回显，认证按 hash 查询；Owner 于 2026-07-26 确认这是当前行为。
  细粒度权限、脱敏、显式 reveal、一次展示、轮换与历史数据处置是安全优化，
  必须另立改造项，不得写成已经实施或覆盖当前事实。
- **超额结算**：DEC-006 将客户实扣封顶于原授权；当前行为是先 capture 授权、再从剩余可用余额以独立幂等
  overage debit 补扣、最后核销残差。Owner 于 2026-07-26 确认该行为部分取代 DEC-006；`spend_limit` 是软上限。
  overage/replay/soft-limit 测试完善是质量缺口，不改变现有结算口径。
- **`GET /v1/models` 线路可见性**：Phase 15 的 fixed/explicit/all 待办已被后续 P3 实施记录、当前
  Route 显式 Channel 池、SQL 线路过滤和数据库测试取代。当前代码只保证模型来自 API Key 的 Route
  Channel 池，不按 Channel 协议过滤；OpenAI-compatible 列表可能包含只绑定 Anthropic Channel 的模型，
  而真实请求候选会按 ingress protocol 过滤。不得把“线路池内”进一步写成“同协议一定可调用”。
- **历史 `adapter_seed`**：DEC-024 移除了自动校正和运行时能力闸门，但“模型能力只由人工声明”不符合
  当前代码。Admin 仍暴露画像物化入口，可逐 key upsert 并覆盖模型能力声明；目录采纳和目录刷新 service
  也能写入同一张表，schema 没有来源字段。Owner 于 2026-07-26 通过 OR-007 选择的“画像只读建议、人工
  确认写入”仍是未实现改造目标，只能留在迁移计划或未来 Gateway change plan，不得写入 active ADR 现状。
- **能力协议 scope**：33-key 字典为每个 key 保存 `shared`、`openai` 或 `anthropic` 分类；
  `model_capabilities` 没有协议列，人工、目录和 Adapter 画像写入均不检查跨 scope。当前 DeepSeek
  Anthropic 画像声明多个 `openai` scope key，且 `/v1/models` 不按 scope 或 Channel 协议过滤模型与
  cap-tags；真实请求候选才按 ingress protocol 过滤。Owner 于 2026-07-26 确认只按该代码事实迁移，
  不新增写入硬边界、override 或例外审计语义。
- **TTFT 统计归属**：当前 Gateway 只按 Channel 保存并评分 stream-only TTFT EWMA；Provider Origin
  继续承载 base URL、围栏和公共 breaker 故障域，生产写入链路不为其生成 TTFT EWMA。Owner 于
  2026-07-26 确认只按该代码事实迁移，因此已修正 ADR-0001 中把 TTFT 归 Origin 的旧迁移表述；不批准
  双指标、Origin 聚合或其他未来架构，也不建立新的 ADR 取代关系。请求级客户首帧 TTFT、attempt 上游
  TTFT 和 Channel 路由 EWMA 必须保持不同口径。
- **排序与候选原子准入时序**：当前 Gateway 先读取容量快照并生成候选顺序，容量为零的候选仍可能进入
  压力排序或 fallback；`SnapshotMany` 只读且不预占资源。每个真实 transport 前才以新的 `AttemptPermit`
  原子取得候选并发、RPM、RPD、TPM、breaker 与围栏资源，拒绝发生在 attempt/transport 前。Owner 于
  2026-07-26 确认只按该代码事实迁移，不批准快照预占、全量锁定 fallback 池或“容量为零必先摘除”等
  未来语义。快照阶段判定为 runtime-sync/pending/stale revision/config 时整批失败；Acquire 阶段除 Store 错误或
  `breaker_store_unavailable` 外，其他 denied reason 当前按候选继续 fallback，目标文档不得把两者混写。
- **Anthropic 按次工具计量**：OR-005 于 2026-07-26 选择 B，批准将 `web_search_requests` 与
  `web_fetch_requests` 作为两个独立按次计价项。当前完整 usage 只能保存通过校验的正数，recovery 将
  缺失值默认为零，partial stream 不携带这些维度，现有价格快照和公式也未表达独立按次售价/成本。
  Gateway 改造计划仍须定义三态、授权估算、公式版本、partial/recovery 补达、unknown 口径和防重复收费；
  不能因目标已批准或已有解析字段就宣称按次计费闭环完成。
- **孤儿预授权恢复**：当前 sweeper 扫描超时、仍 `authorized`、请求仍 running 且没有 recovery job 的
  reservation，释放冻结、记录 risk exposure 并将请求收口为 failed；Owner 于 2026-07-26 确认按此迁移。
  “无 recovery job”不是未 dispatch 的证明。持久执行意图、未执行/已执行/结果未知状态模型和 unknown
  隔离是优化缺口，须另立改造项，不能作为当前行为的前置门禁。

### 5.4 需要重点领域审核的集群

- `ADR-D`：金额、缓存计费、partial settlement、write-off 和成本快照必须由账务 owner 审核。
- `ADR-C`：DEC-005 已进入编码规范和候选架构原则；除非架构 owner 认为仍需固定一次性取舍，否则不为满足
  编号形式而新建空洞的全局 ADR。
- `ADR-H`/`ADR-I`/`ADR-K`：DEC-032~055 交叉修订密集，必须用当前代码、schema 和测试做只读核验。
- `ADR-B`：只迁 Unio 特有兼容行为，不能把第三方协议快照变成内部权威。
- `ADR-F`：线路产品模型应与 Blueprint 当前平台概览一致，不能把历史“档=model_id”带回。
- `ADR-G`/`ADR-M`：属于 Admin 领域，应由 Admin owner 接收，而不是继续归 Gateway 文档所有。
- API Key 明文与 Project 层级虽不形成独立来源 DEC，也必须作为安全/产品与产品/数据审核门禁。

## 6. 决策迁移完成门禁

- DEC-001 至 DEC-055 在本表和 Blueprint 对应目标文档的来源段中均可检索，且没有重复或缺号；目标文档
  可以是 ADR，也可以是已明确分类的权威规范/候选原则。
- 每个 `superseded`/部分取代关系在目标 ADR 中可沿链接理解。
- 每个目标 ADR 明确“当前决策”，不要求读者自行拼接 55 条追加记录。
- Blueprint owner 对每个目标 ADR、规范或原则完成状态评审：批准后进入相应 active 状态，或保持
  `proposed`/`draft` 并记录阻断。
- Gateway `DECISIONS.md` 只在所有目标文档、配套设计和链接通过评审后删除。
