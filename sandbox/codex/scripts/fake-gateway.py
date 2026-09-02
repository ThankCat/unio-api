#!/usr/bin/env python3
"""本地假网关：模拟 UnioAPI 入口，用来观察 codex CLI 在「自定义 provider 模式」下发给我们的 wire。

只实现 CLI 会调用的两个端点：GET /v1/models 与 POST /v1/responses（SSE）。默认每次返回一句文本；
--tool-call 时对每个线程的首个请求返回一次 exec_command 函数调用，诱导 CLI 发出工具续跑请求。

    python3 scripts/fake-gateway.py --port 18999 [--tool-call]
"""
import argparse
import http.server
import json
import threading

STATE = {"seen_threads": set(), "lock": threading.Lock(), "tool_call": False}


def sse(w, name, data):
    w.write(f"event: {name}\ndata: {json.dumps(data)}\n\n".encode())
    w.flush()


class Handler(http.server.BaseHTTPRequestHandler):
    def log_message(self, *_):
        pass

    def do_GET(self):
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.end_headers()
        self.wfile.write(b'{"object":"list","data":[{"id":"gpt-5.5","object":"model"}]}')

    def do_POST(self):
        length = int(self.headers.get("Content-Length", 0))
        body = json.loads(self.rfile.read(length) or b"{}")
        thread = self.headers.get("thread-id") or body.get("prompt_cache_key") or "default"
        first_for_thread = False
        with STATE["lock"]:
            if thread not in STATE["seen_threads"]:
                STATE["seen_threads"].add(thread)
                first_for_thread = True
        has_tool_output = any(i.get("type") in ("function_call_output", "custom_tool_call_output") for i in body.get("input", []))
        emit_tool = STATE["tool_call"] and first_for_thread and not has_tool_output

        self.send_response(200)
        self.send_header("Content-Type", "text/event-stream")
        self.end_headers()
        rid = "resp_fake"
        base = {"id": rid, "object": "response", "status": "in_progress", "output": [], "model": body.get("model")}
        sse(self.wfile, "response.created", {"type": "response.created", "response": base})
        if emit_tool:
            item = {"id": "fc_fake_1", "type": "function_call", "status": "completed", "call_id": "call_fake_1",
                    "name": "exec_command", "arguments": json.dumps({"cmd": "echo fake-gateway-tool-probe"})}
            usage = {"input_tokens": 10, "output_tokens": 20, "total_tokens": 30}
        else:
            item = {"type": "message", "id": "msg_fake", "role": "assistant", "status": "completed",
                    "content": [{"type": "output_text", "text": "fake-gateway-ok", "annotations": []}]}
            usage = {"input_tokens": 10, "output_tokens": 2, "total_tokens": 12}
        sse(self.wfile, "response.output_item.added", {"type": "response.output_item.added", "output_index": 0,
                                                     "item": {**item, "status": "in_progress"}})
        sse(self.wfile, "response.output_item.done", {"type": "response.output_item.done", "output_index": 0, "item": item})
        sse(self.wfile, "response.completed", {"type": "response.completed",
                                               "response": {**base, "status": "completed", "output": [item], "usage": usage}})


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--port", type=int, default=18999)
    ap.add_argument("--tool-call", action="store_true")
    args = ap.parse_args()
    STATE["tool_call"] = args.tool_call
    http.server.ThreadingHTTPServer(("127.0.0.1", args.port), Handler).serve_forever()


if __name__ == "__main__":
    main()
