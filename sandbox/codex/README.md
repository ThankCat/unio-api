# codex — OpenAI Codex 沙箱（ChatGPT 订阅）

隔离的 Codex CLI 与抓包工具链，用于取证 Codex 的真实 wire 并在 CLI 升级后快速发现契约变化。
与开发机全局 `codex` 完全隔离：本地安装的 CLI、独立 `CODEX_HOME=./home`、沙箱内 mitmproxy CA、
清除干扰环境变量。

## 初始化

```bash
cd unio-gateway/sandbox/codex
npm install                                                  # 本地 codex（版本锁定于 package.json）
python3 scripts/load-account.py <sub2api-accounts.json> [序号]  # 注入订阅账号凭据到 home/auth.json
scripts/doctor.sh                                            # 隔离自检
```

## 日常使用

```bash
./codex.sh exec "Reply with exactly one word: pong"   # 非交互
./codex.sh                                            # 交互式 TUI
python3 scripts/token.py status                       # 查看 access_token 有效期
python3 scripts/token.py refresh [--force]            # 用 refresh_token 刷新（抓包脚本会自动预检）
```

## 抓包

```bash
scripts/capture.sh exec "..."                          # 原生模式（官方后端）× exec
scripts/capture-tui.sh "..." 90                        # 原生模式 × 交互式 TUI（tmux 驱动）
CAPTURE_PROVIDER=custom scripts/capture.sh exec "..."  # 自定义 provider 模式（= 客户接 UnioAPI）
CAPTURE_PROVIDER=custom CAPTURE_FAKE_ARGS=--tool-call scripts/capture.sh exec "..."  # 诱导工具调用续跑
scripts/capture-all.sh                                 # 一次跑完全部 7 个场景
```

环境变量：`CAPTURE_LABEL`（产物名后缀）、`CAPTURE_PROVIDER`（native/custom）、`CAPTURE_FAKE_ARGS`、
`CAPTURE_PORT`、`CAPTURE_FAKE_PORT`。

产物在 `flows/`（不入库）：`<stamp>.mitm` 原始流**含明文令牌，分析完即删**；`<stamp>.jsonl` 脱敏摘要
（头名与结构、SSE/WebSocket 事件序列、usage；令牌、邮箱、用户 ID、会话 ID 均已打码）。

## CLI 升级后如何快速跟进（本沙箱的核心用途）

```bash
npm install @openai/codex@latest        # 只升级沙箱 CLI，不动开发机
scripts/capture-all.sh                  # 重跑全部场景
python3 scripts/wire-snapshot.py build 'flows/*.jsonl' -o wire/<新版本>.json
python3 scripts/wire-snapshot.py diff wire/<旧版本>.json wire/<新版本>.json
```

`wire/*.json` 是**规范化的契约快照**：只含端点、请求/响应头名、body 字段名、input 条目种类、工具类型、
事件类型集合，不含任何取值或敏感数据，因此入库作为 adapter 的对照基线。`diff` 输出的每一项
（新增/移除端点、头、body 字段、事件类型）都对应 adapter 里可能要改的一处逻辑；无差异时明确告知
"adapter 无需调整"。

## 工具清单

| 文件 | 用途 |
| --- | --- |
| `codex.sh` | 包装脚本：钉住本地 CLI、隔离 `CODEX_HOME`、清除干扰环境变量 |
| `scripts/load-account.py` | 从 sub2api 导出文件注入订阅账号凭据 |
| `scripts/token.py` | 令牌有效期查看与 `refresh_token` 刷新（只在带回新值时覆盖） |
| `scripts/capture.sh` | 单场景抓包（原生/自定义 provider × exec） |
| `scripts/capture-tui.sh` | 交互式 TUI 抓包（tmux 驱动，自动应答信任对话框） |
| `scripts/capture-all.sh` | 全场景矩阵，一次跑完 |
| `scripts/fake-gateway.py` | 本地假网关，模拟 UnioAPI 入口以观察客户侧 wire |
| `scripts/mitm_dump_addon.py` | mitmproxy 回放插件，导出脱敏 JSONL |
| `scripts/wire-snapshot.py` | 规范化 wire 快照与跨版本 diff |
| `scripts/doctor.sh` | 隔离自检 |

## 隔离保证

- 只使用 `node_modules/.bin/codex` 与 `CODEX_HOME=./home`，不读不写开发机 `~/.codex`；
  mitmproxy CA 与配置在沙箱内 `.mitmproxy/`（不入库），不使用 `~/.mitmproxy`。
- 自定义 provider 模式使用沙箱内 `.probe-home/`（不入库），凭据从 `home/auth.json` 复制。
- 开发机级依赖只有 `node/npm`、`mitmdump`、`tmux`（TUI 场景需要），沙箱不改动它们的配置。
- `scripts/doctor.sh` 打印以上事实并对照开发机全局 codex 的路径与版本。

## 已核实的环境坑

- **`CODEX_API_KEY` / `OPENAI_API_KEY`**：开发机常设为中转 Key，codex 会优先按 API Key 模式打
  `api.openai.com` 并 401。`codex.sh` 已统一清除；务必经包装脚本运行。
- **macOS 系统代理**：codex 遵守系统代理（本机曾观察到 `127.0.0.1:10011`），无法转发到本机回环地址；
  `capture.sh` 用 `HTTPS_PROXY` 显式覆盖。
- **`codex exec` 读 stdin**：stdin 为管道时会等待 EOF，脚本内已用 `< /dev/null` 关闭。
- **TUI 需要真实终端**：`TERM=dumb` 会被拒绝启动，且首次进入目录有信任对话框；`capture-tui.sh` 用 tmux
  提供伪终端并自动应答。
- **后台进程继承 stdout**：假网关等后台进程需重定向输出，否则调用方管道（`| tail`）收不到 EOF。

## 两种 wire 形态

同一个 CLI 对不同 provider 发出的 wire **不是同一套**，完整实测见蓝图
`unio-blueprint/docs/architecture/account-pool.md` 的「Codex wire 实测」小节：

- **自定义 provider 模式**（客户接 UnioAPI）：HTTP SSE、单回合单请求、无预热、无 `previous_response_id`，
  工具续跑重放全量历史；顶层 `instructions` + 标准 `function` 工具。
- **原生模式**（CLI → 官方后端）：WebSocket、每回合先预热再发真实回合、`previous_response_id` 链式续跑、
  `codex.rate_limits` 事件、`additional_tools` 命名空间 + `custom` 工具。
