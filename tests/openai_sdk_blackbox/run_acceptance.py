#!/usr/bin/env python3
"""OpenAI Python SDK 实机黑盒验收（Phase 9 TASK-9.12）。

用法:
  export UNIO_BASE_URL=http://127.0.0.1:8520/v1
  export UNIO_API_KEY=unio_sk_...
  export UNIO_MODEL=deepseek-v4-pro   # 可选

  # macOS/Cursor 若 NO_PROXY 含 ::1，需 unset 后再跑（httpx 解析 bug）
  env -u NO_PROXY -u no_proxy python3 run_acceptance.py

依赖: pip install openai
"""

from __future__ import annotations

import os
import sys

from openai import OpenAI


def main() -> int:
    base = os.environ.get("UNIO_BASE_URL", "").strip()
    key = os.environ.get("UNIO_API_KEY", "").strip()
    model = os.environ.get("UNIO_MODEL", "deepseek-v4-pro").strip()

    if not base or not key:
        print("UNIO_BASE_URL and UNIO_API_KEY are required", file=sys.stderr)
        return 2

    if not base.startswith("http"):
        base = "http://" + base
    if not base.endswith("/v1"):
        base = base.rstrip("/") + "/v1"

    client = OpenAI(base_url=base, api_key=key)
    failures: list[str] = []

    def ok(name: str, detail: str = "") -> None:
        print(f"PASS {name}" + (f" — {detail}" if detail else ""))

    def fail(name: str, detail: object) -> None:
        print(f"FAIL {name}: {detail}")
        failures.append(name)

    def msg_text(msg) -> str:
        content = (msg.content or "").strip()
        if content:
            return content
        rc = getattr(msg, "reasoning_content", None)
        return (rc or "").strip()

    disabled = {"thinking": {"type": "disabled"}}

    try:
        resp = client.chat.completions.create(
            model=model,
            messages=[{"role": "user", "content": "Reply with exactly: pong"}],
            temperature=0,
            max_tokens=32,
            extra_body=disabled,
        )
        text = msg_text(resp.choices[0].message)
        if text:
            ok("non_stream_basic", repr(text[:80]))
        else:
            fail("non_stream_basic", "empty message")
    except Exception as e:
        fail("non_stream_basic", e)

    try:
        stream = client.chat.completions.create(
            model=model,
            messages=[{"role": "user", "content": "Say hello briefly"}],
            stream=True,
            max_tokens=32,
            extra_body=disabled,
        )
        parts = []
        for chunk in stream:
            d = chunk.choices[0].delta if chunk.choices else None
            if d and d.content:
                parts.append(d.content)
        text = "".join(parts).strip()
        if text:
            ok("stream_basic", text[:60])
        else:
            fail("stream_basic", "no content delta")
    except Exception as e:
        fail("stream_basic", e)

    try:
        stream = client.chat.completions.create(
            model=model,
            messages=[{"role": "user", "content": "Hi"}],
            stream=True,
            stream_options={"include_usage": True},
            max_tokens=16,
            extra_body=disabled,
        )
        saw_content = False
        usage_chunk = None
        for chunk in stream:
            d = chunk.choices[0].delta if chunk.choices else None
            if d and d.content:
                saw_content = True
            if getattr(chunk, "usage", None) and chunk.usage.total_tokens:
                usage_chunk = chunk.usage
        if not saw_content:
            fail("stream_include_usage", "no content")
        elif usage_chunk is None:
            fail("stream_include_usage", "no usage tail")
        else:
            ok("stream_include_usage", f"total={usage_chunk.total_tokens}")
    except Exception as e:
        fail("stream_include_usage", e)

    try:
        client.chat.completions.create(
            model=model,
            messages=[{"role": "user", "content": "hi"}],
            extra_body={"service_tier": "auto"},
        )
        fail("reject_service_tier", "expected error")
    except Exception as e:
        if "400" in str(e) or "unsupported" in str(e).lower():
            ok("reject_service_tier")
        else:
            fail("reject_service_tier", e)

    try:
        resp = client.chat.completions.create(
            model=model,
            messages=[{"role": "user", "content": "What is 2+2? One word answer."}],
            max_tokens=128,
            extra_body={"thinking": {"type": "enabled"}},
        )
        msg = resp.choices[0].message
        rc = getattr(msg, "reasoning_content", None)
        content = (msg.content or "").strip()
        if rc or content:
            ok("deepseek_thinking", f"reasoning={bool(rc)} content={bool(content)}")
        else:
            fail("deepseek_thinking", "empty")
    except Exception as e:
        fail("deepseek_thinking", e)

    try:
        resp = client.chat.completions.create(
            model=model,
            messages=[{"role": "user", "content": "Return JSON object with key answer=4"}],
            response_format={"type": "json_object"},
            max_tokens=64,
            extra_body=disabled,
        )
        content = (resp.choices[0].message.content or "").strip()
        if content.startswith("{") and "answer" in content:
            ok("response_format_json_object", content[:80])
        else:
            fail("response_format_json_object", repr(content))
    except Exception as e:
        fail("response_format_json_object", e)

    print("\n--- summary ---")
    if failures:
        print("FAILED:", ", ".join(failures))
        return 1
    print("ALL PASS (6 cases)")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
