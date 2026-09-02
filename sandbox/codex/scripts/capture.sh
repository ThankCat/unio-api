#!/usr/bin/env bash
# 用沙箱 codex CLI 跑一次真实请求，全程经 mitmproxy 抓包。
#
# 产物（均在 flows/ 下，已被 .gitignore 排除）：
#   <stamp>[-label].mitm    原始流（含明文令牌，分析完即删，严禁外传）
#   <stamp>[-label].jsonl   脱敏摘要（请求/响应头、body 结构、SSE 与 WebSocket 事件序列），供 wire-snapshot.py 规范化
#
# 用法（参数原样透传给 codex.sh）：
#   scripts/capture.sh exec "Reply with exactly one word: pong"
#   scripts/capture.sh exec resume --last "Now reply with exactly one word: ping"
#   scripts/capture.sh                       # 交互式 TUI（由 capture-tui.sh 在 tmux 中驱动）
#
# 环境变量：
#   CAPTURE_LABEL=<name>        产物文件名后缀，便于识别场景
#   CAPTURE_PROVIDER=native|custom
#       native（默认）：CLI 直连官方 chatgpt.com 后端（原生 WebSocket 形态）
#       custom：CLI 指向本地假网关（scripts/fake-gateway.py），即客户接 UnioAPI 时的入口形态
#   CAPTURE_FAKE_ARGS="--tool-call"  传给假网关的参数（仅 custom）
#   CAPTURE_PORT=18080          mitmproxy 监听端口
#
# codex 的 HTTP 与 WebSocket 连接都遵守 HTTPS_PROXY，mitmproxy 会把 WebSocket 作为升级流记录并保存消息。
set -euo pipefail

SANDBOX_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
PORT="${CAPTURE_PORT:-18080}"
FAKE_PORT="${CAPTURE_FAKE_PORT:-18999}"
PROVIDER="${CAPTURE_PROVIDER:-native}"
LABEL="${CAPTURE_LABEL:+-$CAPTURE_LABEL}"
STAMP="$(date +%Y%m%d-%H%M%S)$LABEL"
OUT_DIR="$SANDBOX_DIR/flows"
RAW="$OUT_DIR/$STAMP.mitm"
JSONL="$OUT_DIR/$STAMP.jsonl"
MITM_LOG="$OUT_DIR/$STAMP.mitmdump.log"
MITM_CONFDIR="$SANDBOX_DIR/.mitmproxy"          # mitmproxy CA 与配置放在沙箱内（不入库）
CA="$MITM_CONFDIR/mitmproxy-ca-cert.pem"
WORK_DIR="$SANDBOX_DIR/home/work"
PROBE_HOME="$SANDBOX_DIR/.probe-home"           # custom 模式使用的隔离 CODEX_HOME（不入库）

command -v mitmdump >/dev/null || { echo "error: 需要 mitmdump（brew install mitmproxy）" >&2; exit 1; }
mkdir -p "$OUT_DIR" "$WORK_DIR" "$MITM_CONFDIR"
# work 目录独立成 git 仓库：避免 codex 沿目录向上把 unio-gateway 的 AGENTS.md 混进 instructions，
# 也让目录信任只作用于沙箱工作目录。
[[ -d "$WORK_DIR/.git" ]] || git -C "$WORK_DIR" init -q

# 令牌预检：已过期/即将过期则用 refresh_token 刷新（见 scripts/token.py）
python3 "$SANDBOX_DIR/scripts/token.py" refresh

# 端口已被占用说明有上一次遗留的代理，继续会把流量录进别人的文件；直接失败提示。
if nc -z 127.0.0.1 "$PORT" 2>/dev/null; then
  echo "error: :$PORT 已被占用（上一次抓包的 mitmdump 未退出？pgrep -fl mitmdump），或用 CAPTURE_PORT 换端口" >&2
  exit 1
fi

FAKE_PID=""
cleanup() {
  [[ -n "${MITM_PID:-}" ]] && kill "$MITM_PID" 2>/dev/null || true
  [[ -n "$FAKE_PID" ]] && kill "$FAKE_PID" 2>/dev/null || true
}
trap cleanup EXIT
trap 'exit 130' HUP INT TERM

# custom 模式：起本地假网关，并准备指向它的隔离 CODEX_HOME（复用沙箱账号凭据）
if [[ "$PROVIDER" == "custom" ]]; then
  if nc -z 127.0.0.1 "$FAKE_PORT" 2>/dev/null; then
    echo "error: 假网关端口 :$FAKE_PORT 已被占用" >&2; exit 1
  fi
  # 输出重定向到日志：后台进程若继承 stdout，调用方管道（如 | tail）会一直等不到 EOF。
  # shellcheck disable=SC2086
  python3 "$SANDBOX_DIR/scripts/fake-gateway.py" --port "$FAKE_PORT" ${CAPTURE_FAKE_ARGS:-} \
    > "$OUT_DIR/$STAMP.fake-gateway.log" 2>&1 < /dev/null &
  FAKE_PID=$!
  mkdir -p "$PROBE_HOME"
  cp "$SANDBOX_DIR/home/auth.json" "$PROBE_HOME/auth.json"
  cat > "$PROBE_HOME/config.toml" <<EOF
# 由 capture.sh 生成：模拟客户把 codex 指向 UnioAPI（自定义 provider）
model_provider = "unio"
model = "gpt-5.5"
preferred_auth_method = "chatgpt"
disable_response_storage = true

[model_providers.unio]
name = "unio-fake-gateway"
wire_api = "responses"
requires_openai_auth = true
base_url = "http://127.0.0.1:$FAKE_PORT/v1"
EOF
  export CODEX_SANDBOX_HOME="$PROBE_HOME"
  for _ in $(seq 1 30); do nc -z 127.0.0.1 "$FAKE_PORT" 2>/dev/null && break; sleep 0.1; done
fi

mitmdump --listen-port "$PORT" --set confdir="$MITM_CONFDIR" --save-stream-file "$RAW" --set flow_detail=0 -q \
  > "$MITM_LOG" 2>&1 &
MITM_PID=$!

# 首次运行时 mitmdump 会在 confdir 生成 CA，监听就绪即表示 CA 已存在。
for _ in $(seq 1 50); do
  kill -0 "$MITM_PID" 2>/dev/null || break
  nc -z 127.0.0.1 "$PORT" 2>/dev/null && [[ -f "$CA" ]] && break
  sleep 0.1
done
{ kill -0 "$MITM_PID" 2>/dev/null && nc -z 127.0.0.1 "$PORT" 2>/dev/null && [[ -f "$CA" ]]; } \
  || { echo "error: mitmdump 未在 :$PORT 就绪或 CA 未生成，见 $MITM_LOG" >&2; exit 1; }

# exec 子命令在非 git 目录运行需要跳过检查；非 exec（交互式 TUI）保留 stdin。
STDIN_SRC=/dev/stdin
if [[ "${1:-}" == "exec" ]]; then
  shift
  set -- exec --skip-git-repo-check "$@"
  STDIN_SRC=/dev/null
fi

set +e
(
  cd "$WORK_DIR"
  HTTPS_PROXY="http://127.0.0.1:$PORT" HTTP_PROXY="http://127.0.0.1:$PORT" SSL_CERT_FILE="$CA" \
    "$SANDBOX_DIR/codex.sh" "$@" < "$STDIN_SRC"
)
CODEX_EXIT=$?
set -e

sleep 1
kill "$MITM_PID" 2>/dev/null || true
wait "$MITM_PID" 2>/dev/null || true
MITM_PID=""
if [[ -n "$FAKE_PID" ]]; then
  kill "$FAKE_PID" 2>/dev/null || true
  wait "$FAKE_PID" 2>/dev/null || true
  FAKE_PID=""
fi

mitmdump -nr "$RAW" --set confdir="$MITM_CONFDIR" -s "$SANDBOX_DIR/scripts/mitm_dump_addon.py" --set dump_out="$JSONL" -q

echo
echo "capture done (codex exit=$CODEX_EXIT, provider=$PROVIDER)"
echo "  raw:   $RAW"
echo "  jsonl: $JSONL"
exit "$CODEX_EXIT"
