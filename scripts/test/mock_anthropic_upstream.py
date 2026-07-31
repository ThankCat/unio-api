#!/usr/bin/env python3
"""Deterministic Anthropic-compatible upstream for local V1 acceptance tests.

The server intentionally emits protocol prelude events before the first
non-empty text delta. It logs request metadata only and never records headers,
credentials, prompts, or response bodies.
"""

from __future__ import annotations

import argparse
import json
import time
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from typing import Any


RESPONSE_TEXT = "V1 Anthropic local E2E passed."
UPSTREAM_MODEL = "claude-sonnet-4-local-v1"


class Handler(BaseHTTPRequestHandler):
    protocol_version = "HTTP/1.1"

    def do_GET(self) -> None:
        if self.path != "/healthz":
            self.send_error(404)
            return
        self._write_json(200, {"status": "ok"})

    def do_POST(self) -> None:
        if self.path != "/v1/messages":
            self.send_error(404)
            return

        length = int(self.headers.get("Content-Length", "0"))
        try:
            body: dict[str, Any] = json.loads(self.rfile.read(length))
        except (ValueError, json.JSONDecodeError):
            self._write_json(
                400,
                {
                    "type": "error",
                    "error": {"type": "invalid_request_error", "message": "invalid JSON"},
                },
            )
            return

        stream = body.get("stream") is True
        self.log_message(
            "messages stream=%s model=%s",
            stream,
            str(body.get("model", ""))[:128],
        )
        if stream:
            self._write_stream()
        else:
            self._write_json(
                200,
                {
                    "id": "msg_local_v1_nonstream",
                    "type": "message",
                    "role": "assistant",
                    "model": UPSTREAM_MODEL,
                    "content": [{"type": "text", "text": RESPONSE_TEXT}],
                    "stop_reason": "end_turn",
                    "stop_sequence": None,
                    "usage": {"input_tokens": 8, "output_tokens": 7},
                },
                extra_headers={"request-id": "req_local_v1_nonstream"},
            )

    def _write_stream(self) -> None:
        self.send_response(200)
        self.send_header("Content-Type", "text/event-stream")
        self.send_header("Cache-Control", "no-cache")
        self.send_header("Connection", "close")
        self.send_header("request-id", "req_local_v1_stream")
        self.end_headers()

        self._event(
            "message_start",
            {
                "type": "message_start",
                "message": {
                    "id": "msg_local_v1_stream",
                    "type": "message",
                    "role": "assistant",
                    "model": UPSTREAM_MODEL,
                    "content": [],
                    "stop_reason": None,
                    "stop_sequence": None,
                    "usage": {"input_tokens": 8, "output_tokens": 0},
                },
            },
        )
        self._event(
            "content_block_start",
            {
                "type": "content_block_start",
                "index": 0,
                "content_block": {"type": "text", "text": ""},
            },
        )
        self._event("ping", {"type": "ping"})

        # Prelude frames above must not stop the first-token clock.
        time.sleep(0.15)
        self._event(
            "content_block_delta",
            {
                "type": "content_block_delta",
                "index": 0,
                "delta": {"type": "text_delta", "text": RESPONSE_TEXT},
            },
        )
        self._event("content_block_stop", {"type": "content_block_stop", "index": 0})
        self._event(
            "message_delta",
            {
                "type": "message_delta",
                "delta": {"stop_reason": "end_turn", "stop_sequence": None},
                "usage": {"output_tokens": 7},
            },
        )
        self._event("message_stop", {"type": "message_stop"})
        self.close_connection = True

    def _event(self, event_type: str, payload: dict[str, Any]) -> None:
        frame = (
            f"event: {event_type}\n"
            f"data: {json.dumps(payload, separators=(',', ':'))}\n\n"
        )
        self.wfile.write(frame.encode("utf-8"))
        self.wfile.flush()

    def _write_json(
        self,
        status: int,
        payload: dict[str, Any],
        *,
        extra_headers: dict[str, str] | None = None,
    ) -> None:
        encoded = json.dumps(payload, separators=(",", ":")).encode("utf-8")
        self.send_response(status)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(encoded)))
        self.send_header("Connection", "close")
        for key, value in (extra_headers or {}).items():
            self.send_header(key, value)
        self.end_headers()
        self.wfile.write(encoded)
        self.close_connection = True

    def log_message(self, message: str, *args: Any) -> None:
        print(f"mock-anthropic {self.address_string()} {message % args}", flush=True)


def main() -> None:
    parser = argparse.ArgumentParser()
    parser.add_argument("--host", default="127.0.0.1")
    parser.add_argument("--port", type=int, default=18991)
    args = parser.parse_args()

    server = ThreadingHTTPServer((args.host, args.port), Handler)
    print(f"mock-anthropic listening on http://{args.host}:{args.port}", flush=True)
    try:
        server.serve_forever()
    except KeyboardInterrupt:
        pass
    finally:
        server.server_close()


if __name__ == "__main__":
    main()
