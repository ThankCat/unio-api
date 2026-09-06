# wire — Codex 契约基线与证据

实现 Codex adapter 时对照这里，不要凭记忆或直接照抄 Sub2API。

## 证据分三层

| 层 | 位置 | 入库 | 用途 |
| --- | --- | --- | --- |
| 原始证据 | `../flows/*.jsonl` | 否（数十 MB） | 完整抓包摘要，本地排查用；`scripts/capture-all.sh` 可随时重建 |
| 契约基线 | `wire/<CLI 版本>.json` | 是 | 机器可比对的契约形状（端点、头名、body 字段名、事件类型）；`wire-snapshot.py diff` 的输入 |
| 代表性样例 | `wire/samples/*.json` | 是 | 带真实取值的脱敏样例，实现时逐字段对照 |

样例由 `python3 scripts/extract-samples.py` 从 `flows/` 重新生成；每份都带 `_source`（出自哪次抓包）
与 `_what`（这份证明什么）。

## 样例索引

| 文件 | 证明什么 | 实现时对照 |
| --- | --- | --- |
| `ingress-request.json` | 客户 CLI 经自定义 provider 打到 UnioAPI 的首个回合请求 | 入口解析、会话键提取、协议判定 |
| `ingress-request-tool-continuation.json` | 工具调用续跑：无状态重放全量历史，`previous_response_id` 恒空 | 多轮往返处理、Sticky 键稳定性 |
| `ingress-sse-sequence.json` | 客户入口收到的 SSE 事件序列 | 回包事件顺序 |
| `upstream-usage-headers.json` | 上游 HTTP 响应的全部用量头（primary=5h / secondary=7d） | 用量快照采集、账号水位、冷却时间 |
| `upstream-rate-limits-event.json` | 原生 WS 的 `codex.rate_limits` 事件（`allowed`/`limit_reached`/`reset_at`） | 限流判定（与响应头字段一一对应） |
| `upstream-usage-completed.json` | `response.completed` 的 `service_tier`、`usage`、缓存选项 | 结算、档位权威性、缓存归因 |
| `upstream-response-metadata.json` | 原生 WS 承载响应头的事件（含 `x-codex-turn-state`） | 回合状态处理 |
| `upstream-models.json` | 模型清单端点响应与该账号可见的 slug | 模型发现流程 |
| `upstream-wham-usage.json` | 主动查用量端点 `GET /backend-api/wham/usage`：5h/7d 窗口 + 重置卡计数 | 主动查用量、自动用卡判定 |
| `upstream-wham-reset-credits.json` | 重置卡明细端点 `GET /backend-api/wham/rate-limit-reset-credits`：每张卡的状态与到期 | 重置卡展示、自动用卡选卡 |
| `upstream-accounts-check.json` | 账号台账端点 `GET /backend-api/accounts/check/v4-2023-04-27`（需 `Origin`/`Referer`）：套餐、停用标记、订阅到期/续订/取消、计费、欠费、促销 | 刷新状态、订阅到期回写、异常账号标记 |
| `upstream-me.json` | 用户画像端点 `GET /backend-api/me`：邮箱、MFA、国家/地区、注册时间、组织与封禁标记 | 刷新状态、账号详情展示 |
| `upstream-ws-handshake.json` | 原生 WS 握手头与完整事件序列 | 仅参考，号池出站不用 WS |

## 使用纪律

- **实现前先看样例，不要直接照抄 Sub2API 源码。** 已发现 Sub2API 至少两处与真实行为不符：
  primary/secondary 窗口方向弄反、只解析 7 个用量头（实际远多于此）。
- 蓝图 `unio-blueprint/docs/architecture/account-pool.md` 的「Codex wire 实测」小节是结论层，
  每条结论都标注了来源等级（见该节图例）；本目录是它的证据支撑。
- CLI 升级后按沙箱 README 的流水线重跑，`diff` 有输出就意味着 adapter 可能要改。
