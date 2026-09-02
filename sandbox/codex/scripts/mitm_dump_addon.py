"""mitmproxy addon：把抓到的 HTTP 请求/响应与 WebSocket 消息导出为脱敏 JSONL。

在 mitmproxy 自带的 Python 环境中运行（只用标准库）。既可实时挂载，也可回放 .mitm 文件：

    mitmdump -nr flows/<stamp>.mitm -s scripts/mitm_dump_addon.py --set dump_out=flows/<stamp>.jsonl

脱敏规则：
- 请求头 authorization / chatgpt-account-id / cookie 等只保留长度；
- 响应头 set-cookie 只保留长度；
- x-codex-turn-state 等不透明 blob 只保留长度与前缀；
- x-codex-turn-metadata（JSON 头）保留键名，ID 类值打码；
- body 中 access_token / refresh_token / id_token 等键打码；
- 超长字符串（如完整 system prompt）截断并标注原始长度；tools 只保留 type/name/namespace。
"""

import json

from mitmproxy import ctx

TRUNC = 300
MAX_LIST = 40
MAX_NOTABLE = 60

MASK_REQUEST_HEADERS = {"authorization", "chatgpt-account-id", "cookie", "x-api-key", "openai-organization"}
MASK_RESPONSE_HEADERS = {"set-cookie"}
BLOB_HEADERS = {"x-codex-turn-state"}
JSON_HEADERS = {"x-codex-turn-metadata"}
SECRET_BODY_KEYS = {"access_token", "refresh_token", "id_token", "api_key", "authorization"}
# 账号身份类字段（上游用户/账号标识、邮箱）与会话/设备 ID 一律打码，只保留长度。
ID_LIKE_KEYS = {
    "installation_id", "session_id", "thread_id", "turn_id", "window_id", "conversation_id", "device_id",
    "user_id", "account_id", "email", "safety_identifier", "chatgpt_account_id", "chatgpt_user_id",
}


def mask(value: str) -> str:
    return f"<masked len={len(value)}>"


def blob(value: str) -> str:
    return f"<blob len={len(value)} prefix={value[:12]}>"


def summarize_json_header(value: str):
    try:
        obj = json.loads(value)
    except Exception:
        return blob(value)
    if not isinstance(obj, dict):
        return blob(value)
    return {k: (mask(str(v)) if k in ID_LIKE_KEYS else v) for k, v in obj.items()}


def headers_to_list(headers, mask_set):
    out = []
    for key, value in headers.items(multi=True):
        lowered = key.lower()
        if lowered in mask_set:
            out.append([key, mask(value)])
        elif lowered in BLOB_HEADERS:
            out.append([key, blob(value)])
        elif lowered in JSON_HEADERS:
            out.append([key, summarize_json_header(value)])
        else:
            out.append([key, value])
    return out


def tool_summary(tool):
    if not isinstance(tool, dict):
        return shrink(tool)
    summary = {"type": tool.get("type")}
    name = tool.get("name") or (tool.get("function") or {}).get("name") if isinstance(tool.get("function"), dict) else tool.get("name")
    if name:
        summary["name"] = name
    if "namespace" in tool:
        summary["namespace"] = tool.get("namespace")
    extra_keys = sorted(k for k in tool.keys() if k not in {"type", "name", "namespace", "description", "parameters", "function", "strict"})
    if extra_keys:
        summary["other_keys"] = extra_keys
    return summary


def shrink(obj):
    if isinstance(obj, str):
        if len(obj) <= TRUNC:
            return obj
        return obj[:TRUNC] + f"…<+{len(obj) - TRUNC} chars>"
    if isinstance(obj, list):
        items = [shrink(x) for x in obj[:MAX_LIST]]
        if len(obj) > MAX_LIST:
            items.append(f"…<{len(obj) - MAX_LIST} more items>")
        return items
    if isinstance(obj, dict):
        out = {}
        for key, value in obj.items():
            if key == "tools" and isinstance(value, list):
                out[key] = [tool_summary(t) for t in value]
            elif key in SECRET_BODY_KEYS and isinstance(value, str):
                out[key] = mask(value)
            elif key in ID_LIKE_KEYS and isinstance(value, str):
                out[key] = mask(value)
            else:
                out[key] = shrink(value)
        return out
    return obj


def is_notable_event(event_type: str) -> bool:
    if not event_type:
        return True
    if "delta" in event_type:
        return False
    return event_type not in {"response.in_progress"}


def compress_sequence(types):
    out = []
    for t in types:
        if out and out[-1][0] == t:
            out[-1][1] += 1
        else:
            out.append([t, 1])
    return [t if n == 1 else f"{t} ×{n}" for t, n in out]


def summarize_sse(text: str):
    sequence, notable, counts = [], [], {}
    for block in text.split("\n\n"):
        event_type, data_lines = None, []
        for line in block.splitlines():
            if line.startswith("event:"):
                event_type = line[6:].strip()
            elif line.startswith("data:"):
                data_lines.append(line[5:].strip())
        if event_type is None and not data_lines:
            continue
        data = "\n".join(data_lines)
        if event_type is None:
            try:
                event_type = json.loads(data).get("type", "?")
            except Exception:
                event_type = "?"
        counts[event_type] = counts.get(event_type, 0) + 1
        sequence.append(event_type)
        if is_notable_event(event_type) and len(notable) < MAX_NOTABLE:
            try:
                notable.append(shrink(json.loads(data)))
            except Exception:
                notable.append({"type": event_type, "raw": data[:TRUNC]})
    return {"kind": "sse", "event_sequence": compress_sequence(sequence), "counts": counts, "notable": notable}


def summarize_body(content_type: str, raw: bytes):
    if not raw:
        return None
    text = raw.decode("utf-8", "replace")
    content_type = content_type or ""
    if "text/event-stream" in content_type or text.lstrip().startswith("event:"):
        return summarize_sse(text)
    if "json" in content_type or text[:1] in "{[":
        try:
            return {"kind": "json", "len": len(text), "json": shrink(json.loads(text))}
        except Exception:
            pass
    return {"kind": "raw", "len": len(text), "prefix": text[:TRUNC]}


def summarize_websocket(ws):
    client_messages, server_sequence, server_notable, counts = [], [], [], {}
    for message in ws.messages:
        if message.is_text:
            text = message.content.decode("utf-8", "replace")
        else:
            text = None
        parsed = None
        if text is not None:
            try:
                parsed = json.loads(text)
            except Exception:
                parsed = None
        if message.from_client:
            client_messages.append(shrink(parsed) if parsed is not None else {"raw": (text or "<binary>")[:TRUNC]})
            continue
        event_type = parsed.get("type", "?") if isinstance(parsed, dict) else ("<binary>" if text is None else "<non-json>")
        counts[event_type] = counts.get(event_type, 0) + 1
        server_sequence.append(event_type)
        if is_notable_event(event_type) and len(server_notable) < MAX_NOTABLE:
            server_notable.append(shrink(parsed) if parsed is not None else {"raw": (text or "<binary>")[:TRUNC]})
    return {
        "message_count": len(ws.messages),
        "client_messages": client_messages,
        "server_sequence": compress_sequence(server_sequence),
        "server_counts": counts,
        "server_notable": server_notable,
    }


class DumpFlows:
    def __init__(self):
        self.out = None
        self.seen = set()

    def load(self, loader):
        loader.add_option("dump_out", str, "", "脱敏 JSONL 输出路径")

    def configure(self, updates):
        if "dump_out" in updates and ctx.options.dump_out and self.out is None:
            self.out = open(ctx.options.dump_out, "a", encoding="utf-8")

    def emit(self, flow):
        if flow.id in self.seen or self.out is None:
            return
        self.seen.add(flow.id)
        record = {
            "id": flow.id,
            "ts": flow.timestamp_start,
            "method": flow.request.method,
            "url": flow.request.pretty_url,
            "status": flow.response.status_code if flow.response else None,
            "error": flow.error.msg if flow.error else None,
            "request": {
                "headers": headers_to_list(flow.request.headers, MASK_REQUEST_HEADERS),
                "body": summarize_body(flow.request.headers.get("content-type", ""), flow.request.content),
            },
            "response": None,
            "websocket": None,
        }
        if flow.response:
            record["response"] = {
                "headers": headers_to_list(flow.response.headers, MASK_RESPONSE_HEADERS),
                "body": summarize_body(flow.response.headers.get("content-type", ""), flow.response.content),
            }
        if flow.websocket is not None:
            record["websocket"] = summarize_websocket(flow.websocket)
        self.out.write(json.dumps(record, ensure_ascii=False) + "\n")
        self.out.flush()

    def response(self, flow):
        # WebSocket 升级响应（101）先不导出，等 websocket_end 拿到全部消息后再导出。
        if flow.response and flow.response.status_code == 101:
            return
        self.emit(flow)

    def websocket_end(self, flow):
        self.emit(flow)

    def error(self, flow):
        self.emit(flow)

    def done(self):
        if self.out:
            self.out.close()


addons = [DumpFlows()]
