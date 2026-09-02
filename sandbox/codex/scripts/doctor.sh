#!/usr/bin/env bash
# 沙箱隔离自检：证明沙箱 codex 与开发机全局 codex 互不影响。
set -uo pipefail

SANDBOX_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
ok=0; bad=0
pass() { echo "  [ok]  $*"; ok=$((ok+1)); }
fail() { echo "  [!!]  $*"; bad=$((bad+1)); }

echo "== 沙箱 codex =="
SANDBOX_BIN="$SANDBOX_DIR/node_modules/.bin/codex"
if [[ -x "$SANDBOX_BIN" ]]; then
  pass "本地 CLI: $SANDBOX_BIN ($("$SANDBOX_BIN" --version 2>/dev/null))"
else
  fail "本地 CLI 未安装，请在 $SANDBOX_DIR 执行 npm install"
fi
[[ -f "$SANDBOX_DIR/home/config.toml" ]] && pass "隔离 CODEX_HOME: $SANDBOX_DIR/home" || fail "缺少 home/config.toml"
if [[ -f "$SANDBOX_DIR/home/auth.json" ]]; then
  pass "沙箱凭据已注入（home/auth.json，被 .gitignore 排除）"
else
  echo "  [..]  尚未注入凭据：python3 scripts/load-account.py <sub2api 导出文件>"
fi

echo "== 开发机全局 codex（只读对照，沙箱不会使用）=="
if command -v codex >/dev/null 2>&1; then
  HOST_BIN="$(command -v codex)"
  echo "  全局 CLI: $HOST_BIN ($(codex --version 2>/dev/null))"
  [[ "$HOST_BIN" != "$SANDBOX_BIN" ]] && pass "沙箱 CLI 与全局 CLI 是不同的二进制" || fail "沙箱 CLI 解析到了全局二进制"
else
  echo "  全局未安装 codex"
fi
echo "  ~/.codex 最近修改: $(stat -f '%Sm' "$HOME/.codex" 2>/dev/null || echo '不存在')（沙箱运行不应改变它）"

echo "== 干扰环境变量（包装脚本运行时会清除）=="
for v in OPENAI_API_KEY CODEX_API_KEY OPENAI_BASE_URL OPENAI_API_BASE; do
  if [[ -n "${!v:-}" ]]; then echo "  [..]  当前 shell 设有 $v，经 codex.sh 运行时会被清除"; fi
done
pass "codex.sh 清除 OPENAI_API_KEY/CODEX_API_KEY/OPENAI_BASE_URL/OPENAI_API_BASE"

echo "== 抓包依赖 =="
command -v mitmdump >/dev/null 2>&1 && pass "mitmdump: $(command -v mitmdump)（唯一的开发机级工具依赖，同 node/npm）" || fail "缺少 mitmdump（brew install mitmproxy）"
[[ -f "$SANDBOX_DIR/.mitmproxy/mitmproxy-ca-cert.pem" ]] && pass "沙箱内 CA: $SANDBOX_DIR/.mitmproxy（首次 capture 自动生成）" || echo "  [..]  沙箱内 CA 尚未生成，首次 scripts/capture.sh 会自动创建"

echo
echo "结果: $ok 项通过, $bad 项失败"
[[ $bad -eq 0 ]]
