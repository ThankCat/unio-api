#!/usr/bin/env bash
# Codex CLI 沙箱包装脚本。
#
# 钉住沙箱本地安装的 codex（而非开发机全局 brew/npm 的那份），
# 使用隔离的 CODEX_HOME，并清除可能干扰 ChatGPT 订阅登录的环境变量。
#
# 用法：
#   ./codex.sh login          # 首次用订阅账号登录（或用 scripts/load-account.py 注入凭据）
#   ./codex.sh exec "..."     # 非交互跑一次
#   ./codex.sh                # 交互式 TUI
set -euo pipefail

SANDBOX_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

# 默认使用沙箱 home；探测类实验可用 CODEX_SANDBOX_HOME 指向沙箱内另一个隔离目录（仍不许指向 ~/.codex）。
export CODEX_HOME="${CODEX_SANDBOX_HOME:-$SANDBOX_DIR/home}"
case "$CODEX_HOME" in
  "$SANDBOX_DIR"/*) ;;
  *) echo "error: CODEX_SANDBOX_HOME 必须位于沙箱目录内: $SANDBOX_DIR" >&2; exit 1 ;;
esac

CODEX_BIN="$SANDBOX_DIR/node_modules/.bin/codex"
if [[ ! -x "$CODEX_BIN" ]]; then
  echo "error: 沙箱 codex 未安装，请先在 $SANDBOX_DIR 执行 'npm install'" >&2
  exit 1
fi

# 开发机的中转 Key 会让 codex 走 API Key 模式而非订阅模式，必须清除。
# 注意 CODEX_API_KEY：codex 0.152 会优先认它走 API Key 模式（开发机常设为中转 Key）。
unset OPENAI_API_KEY CODEX_API_KEY OPENAI_BASE_URL OPENAI_API_BASE 2>/dev/null || true

exec "$CODEX_BIN" "$@"
