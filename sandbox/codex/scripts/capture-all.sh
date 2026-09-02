#!/usr/bin/env bash
# 一次跑完全部取证场景，用于 CLI 版本升级后快速重建 wire 契约。
#
#   npm install @openai/codex@latest      # 升级沙箱 CLI（不影响开发机）
#   scripts/capture-all.sh                # 跑完所有场景
#   python3 scripts/wire-snapshot.py build flows/*.jsonl -o wire/<版本>.json
#   python3 scripts/wire-snapshot.py diff wire/<旧版本>.json wire/<新版本>.json
#
# 场景矩阵：
#   native-exec-single   原生（官方后端）× exec × 单回合
#   native-exec-resume   原生 × exec × 续聊（多回合）
#   native-exec-tool     原生 × exec × 工具调用
#   native-tui-single    原生 × TUI（originator 与 exec 不同）
#   custom-exec-single   自定义 provider（= 客户接 UnioAPI）× 单回合
#   custom-exec-tool     自定义 provider × 工具调用续跑
#   custom-tui-single    自定义 provider × TUI
set -euo pipefail

SANDBOX_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$SANDBOX_DIR"

echo "### 清理旧抓包产物"
rm -f flows/*.mitm flows/*.mitmdump.log flows/*.fake-gateway.log
mkdir -p flows

run() { # run <label> <provider> <mode> <prompt> [fake_args]
  local label="$1" provider="$2" mode="$3" prompt="$4" fake_args="${5:-}"
  echo
  echo "### [$label] provider=$provider mode=$mode"
  if [[ "$mode" == "tui" ]]; then
    CAPTURE_LABEL="$label" CAPTURE_PROVIDER="$provider" CAPTURE_FAKE_ARGS="$fake_args" \
      scripts/capture-tui.sh "$prompt" 90 || echo "warn: [$label] 未正常结束"
  else
    CAPTURE_LABEL="$label" CAPTURE_PROVIDER="$provider" CAPTURE_FAKE_ARGS="$fake_args" \
      scripts/capture.sh exec "$prompt" || echo "warn: [$label] 未正常结束"
  fi
}

run native-exec-single native exec "Reply with exactly one word: pong"

echo
echo "### [native-exec-resume] provider=native mode=exec resume（续聊上一会话）"
CAPTURE_LABEL=native-exec-resume scripts/capture.sh exec resume --last "Now reply with exactly one word: ping" \
  || echo "warn: [native-exec-resume] 未正常结束"

run native-exec-tool native exec "Run the shell command: echo wire-probe and report its exact output"
run native-tui-single native tui "Reply with exactly one word: pong"
run custom-exec-single custom exec "Reply with exactly one word: pong"
run custom-exec-tool custom exec "Run the shell command: echo wire-probe and report its exact output" "--tool-call"
run custom-tui-single custom tui "Reply with exactly one word: pong"

echo
echo "### 全部场景完成，产物："
ls -1 flows/*.jsonl
echo
echo "下一步："
echo "  python3 scripts/wire-snapshot.py build 'flows/*.jsonl' -o wire/\$(node -p \"require('./node_modules/@openai/codex/package.json').version\").json"
echo "  python3 scripts/wire-snapshot.py diff wire/<旧版本>.json wire/<新版本>.json"
