# sandbox — 上游订阅账号验证沙箱

号池（订阅账号）功能开发用的隔离环境。每个上游平台一个子目录，各自内置一份 **本地安装** 的官方
CLI 与 **独立的凭据/配置目录**，与开发机全局安装的同名 CLI 完全隔离——不读、不写、不影响开发者本机
的 `~/.codex`、`~/.claude` 等。

## 为什么存在

订阅账号号池需要真实上游响应（用量响应头、SSE 事件序列、限流/封号错误体、`service_tier` 回显等）
作为 adapter 的 wire 契约依据。这些数据源码里没有，只能对真实账号抓取。此沙箱提供一个可复现、可反复
运行的环境，用号池订阅账号驱动官方 CLI，产出真实流量供开发对照。

## 目录约定

```
sandbox/
  <platform>/
    home/            # 该平台 CLI 的隔离凭据/配置目录（CLI_HOME），运行态不入库
    node_modules/    # 本地安装的 CLI，不入库（由 package.json 重建）
    <platform>.sh    # 包装脚本：钉住本地 CLI + 隔离 HOME + 清除干扰环境变量
    scripts/         # 凭据注入、令牌刷新、抓包、假网关、快照与 diff、自检
    flows/           # 抓包产物，不入库（.mitm 含明文令牌，分析完即删）
    wire/            # 规范化 wire 契约快照，入库，作为 adapter 对照基线
    package.json     # 锁定 CLI 版本
    README.md        # 该平台的用法与已知问题
```

当前平台：

- [`codex/`](codex/README.md) — OpenAI Codex（ChatGPT 订阅），已就绪。

规划中：`claude-code/`（Anthropic 订阅）、`gemini/`（Google 订阅）。

## 应对 CLI 版本更新

上游 CLI 频繁迭代，wire 可能随版本变化。沙箱把「发现变化」做成固定流程，不必每次重新逆向：

```bash
cd <platform>
npm install @openai/codex@latest    # 只升级沙箱 CLI
scripts/capture-all.sh              # 重跑全部取证场景
python3 scripts/wire-snapshot.py build 'flows/*.jsonl' -o wire/<新版本>.json
python3 scripts/wire-snapshot.py diff wire/<旧版本>.json wire/<新版本>.json
```

`wire/*.json` 只含契约形状（端点、头名、body 字段名、事件类型集合），不含取值与敏感数据，入库作为
adapter 的对照基线；`diff` 逐项列出新增/移除的端点、头、字段与事件，每项对应 adapter 里可能要改的
一处逻辑，无差异时明确告知无需调整。

## 入库边界

沙箱脚手架（脚本、配置模板、版本锁、README）与 `wire/` 契约快照入库，方便团队复现与扩展到新平台。
**凭据与体积产物不入库**，由 `.gitignore` 精准排除：

- 各平台隔离 `home/`（含活令牌、会话、缓存）——只放行 `config.toml`；
- `**/node_modules/`、本地 CLI 二进制——体积大，由 `package.json` 重建；
- `flows/`（抓包产物，`.mitm` 含明文令牌）、`.mitmproxy/`（CA 私钥）、`.probe-home/`。

## 安全约束

- 沙箱使用的订阅账号凭据仅存于各自 `home/` 下、且被忽略，不进版本库、不进日志。
- 包装脚本清除 `OPENAI_API_KEY` 等环境变量，避免开发机的中转 Key 干扰订阅登录。
- 此环境仅用于内部 wire 验证；对上游的请求量保持在正常使用量级。
