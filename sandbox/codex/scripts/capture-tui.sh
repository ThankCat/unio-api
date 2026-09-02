#!/usr/bin/env bash
# 驾驭交互式 codex TUI（originator=codex_cli_rs）跑一轮真实回合并抓包。
#
# 客户实际使用的是 TUI / IDE 插件而非 `codex exec`，两者 wire 可能不同，需要分别取证。
# TUI 需要一个会应答终端能力查询的真实终端模拟器，这里用 tmux（brew install tmux）承载：
# `capture-pane` 读屏幕判断界面状态，`send-keys` 注入按键。
#
# 用法：scripts/capture-tui.sh "Reply with exactly one word: pong" [回合最长等待秒数，默认 120]
set -euo pipefail

PROMPT="${1:?usage: capture-tui.sh <prompt> [max_wait_seconds]}"
MAX_WAIT="${2:-120}"
SANDBOX_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
SESSION="codexcap-$$"
mkdir -p "$SANDBOX_DIR/flows"
command -v tmux >/dev/null || { echo "error: 需要 tmux（brew install tmux）" >&2; exit 1; }

alive() { tmux has-session -t "$SESSION" 2>/dev/null; }
snap() { tmux capture-pane -p -t "$SESSION" 2>/dev/null | tr -s ' \n' ' ' || true; }
keys() { tmux send-keys -t "$SESSION" "$@"; }
wait_for() { # wait_for <regex> <seconds>
  local i; for ((i = 0; i < $2; i++)); do snap | rg -q "$1" && return 0; sleep 1; done; return 1
}

echo "启动 TUI（tmux 会话 $SESSION）…"
tmux new-session -d -s "$SESSION" -x 160 -y 50 \
  "env TERM=xterm-256color CAPTURE_PROVIDER='${CAPTURE_PROVIDER:-native}' CAPTURE_LABEL='${CAPTURE_LABEL:-}' CAPTURE_FAKE_ARGS='${CAPTURE_FAKE_ARGS:-}' '$SANDBOX_DIR/scripts/capture.sh'; sleep 2"
sleep 2
alive || { echo "error: tmux 会话未启动" >&2; exit 1; }

# 1) 首次进入目录的信任对话框：默认 "Yes, continue"，回车接受
if wait_for "trust the contents|Press enter to continue" 20; then
  echo "接受目录信任对话框"; keys Enter; sleep 2
fi
# 2) 等输入框就绪且 model 不再是 loading
wait_for "Ask Codex|\? for shortcuts" 40 || { echo "error: TUI 未出现输入框，当前屏幕："; snap | cut -c1-500; exit 1; }
wait_for "model: +[a-z0-9]" 30 || echo "warn: model 仍显示 loading，继续尝试"
sleep 2

# 3) 发送提示词并等待回合完成（运行中显示 esc to interrupt / Working）
echo "发送提示词"; keys -l "$PROMPT"; sleep 1; keys Enter
wait_for "esc to interrupt|Working" 20 || echo "warn: 未观察到运行状态，可能已瞬间完成"
for ((i = 0; i < MAX_WAIT; i++)); do
  snap | rg -q "esc to interrupt|Working" || break
  sleep 1
done
sleep 3
echo "回合结束时的屏幕摘要："; snap | cut -c1-500; echo

# 4) 退出 TUI：/quit → Ctrl-D → Ctrl-C×2；每步都等待 codex 自行退出，让 capture.sh 完成导出
echo "退出 TUI"; keys -l "/quit"; keys Enter
for ((i = 0; i < 20; i++)); do alive || break; snap | rg -q "capture done" && break; sleep 1; done
if alive && ! snap | rg -q "capture done"; then keys C-d; sleep 5; fi
if alive && ! snap | rg -q "capture done"; then keys C-c; sleep 1; keys C-c; sleep 5; fi
# 兜底：只结束沙箱自己的 codex 进程（按沙箱路径匹配），capture.sh 随后正常收尾
if alive && ! snap | rg -q "capture done"; then
  pkill -f "$SANDBOX_DIR/node_modules/@openai/codex" 2>/dev/null || true
fi
for ((i = 0; i < 30; i++)); do alive || break; snap | rg -q "capture done" && break; sleep 1; done
snap | rg -o "capture done.*" | cut -c1-80 || true
tmux kill-session -t "$SESSION" 2>/dev/null || true

echo "最新产物："; ls -t "$SANDBOX_DIR/flows"/*.jsonl 2>/dev/null | head -1
