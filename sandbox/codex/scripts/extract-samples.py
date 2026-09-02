#!/usr/bin/env python3
"""从抓包摘要中抽取代表性样例，落到 wire/samples/（体积小、已脱敏、入库）。

flows/*.jsonl 体积大（数十 MB）且不入库，改造者 clone 后看不到证据。本脚本把「实现 adapter 时真正
需要逐字段对照的那几条」固化成可入库的样例：客户入口请求、上游用量头、限额事件、usage、SSE 序列、
工具调用往返。

    python3 scripts/extract-samples.py                       # 从 flows/*.jsonl 抽取
    python3 scripts/extract-samples.py --out wire/samples    # 指定输出目录

样例保留真实取值（模型名、窗口分钟数、事件字段等），但令牌、邮箱、用户/账号 ID、会话 ID 已在抓包
导出阶段打码；本脚本额外裁剪超长文本（system prompt 等）。
"""
import argparse
import glob
import json
import os

MAX_TEXT = 200


def trim(obj, depth=0):
    """裁剪超长字符串与长列表，保留结构与短取值。"""
    if isinstance(obj, str):
        return obj if len(obj) <= MAX_TEXT else obj[:MAX_TEXT] + f"…<+{len(obj) - MAX_TEXT} chars>"
    if isinstance(obj, list):
        out = [trim(x, depth + 1) for x in obj[:8]]
        if len(obj) > 8:
            out.append(f"…<{len(obj) - 8} more>")
        return out
    if isinstance(obj, dict):
        return {k: trim(v, depth + 1) for k, v in obj.items()}
    return obj


def load_flows(pattern):
    for path in sorted(glob.glob(pattern)):
        for line in open(path, encoding="utf-8"):
            yield os.path.basename(path), json.loads(line)


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--flows", default="flows/*.jsonl")
    ap.add_argument("--out", default="wire/samples")
    args = ap.parse_args()
    os.makedirs(args.out, exist_ok=True)

    samples = {}

    for src, flow in load_flows(args.flows):
        url, method = flow.get("url", ""), flow.get("method")
        ws = flow.get("websocket")
        req_body = (flow["request"].get("body") or {})
        resp = flow.get("response") or {}
        resp_body = resp.get("body") or {}

        # 1) 客户入口请求（自定义 provider = 客户接 UnioAPI 的形态）
        if "18999" in url and method == "POST" and req_body.get("kind") == "json":
            j = req_body["json"]
            has_tool = any(i.get("type", "").startswith(("function_call", "custom_tool_call"))
                           for i in j.get("input", []) if isinstance(i, dict))
            key = "ingress-request-tool-continuation" if has_tool else "ingress-request"
            if key not in samples:
                samples[key] = {
                    "_source": src,
                    "_what": ("客户 CLI 经自定义 provider 打到 UnioAPI 的请求"
                              + ("（工具调用续跑：无状态重放全量历史）" if has_tool else "（首个回合）")),
                    "method": method,
                    "path": url.split("18999")[-1],
                    "request_headers": dict(flow["request"]["headers"]),
                    "request_body": trim(j),
                }

        # 2) 上游 HTTP 响应头（用量头集合）
        if "chatgpt.com" in url and "/codex/responses" in url and resp.get("headers") and not ws:
            if "upstream-usage-headers" not in samples:
                headers = {k: v for k, v in resp["headers"]}
                if any(k.lower().startswith("x-codex-") for k in headers):
                    samples["upstream-usage-headers"] = {
                        "_source": src,
                        "_what": "上游 HTTP 出站响应头：用量窗口（primary=5h / secondary=7d）与回合状态",
                        "response_headers": headers,
                    }

        # 3) 模型清单端点
        if "/codex/models" in url and resp_body.get("kind") == "json":
            if "upstream-models" not in samples:
                j = resp_body["json"]
                models = j.get("models", []) if isinstance(j, dict) else []
                samples["upstream-models"] = {
                    "_source": src,
                    "_what": "Codex 模型清单端点响应（发现流程的数据源）",
                    "model_count": len(models),
                    "slugs": [m.get("slug") for m in models if isinstance(m, dict)],
                    "first_model_fields": trim(models[0]) if models else None,
                }

        # 4) 原生 WS：限额事件、usage、SSE/事件序列
        if ws:
            for msg in ws.get("server_notable", []):
                if not isinstance(msg, dict):
                    continue
                mtype = msg.get("type")
                if mtype == "codex.rate_limits" and "upstream-rate-limits-event" not in samples:
                    samples["upstream-rate-limits-event"] = {
                        "_source": src,
                        "_what": "原生 WS 的 codex.rate_limits 事件：allowed/limit_reached 与两个窗口的 reset_at",
                        "event": trim(msg),
                    }
                if mtype == "response.completed" and "upstream-usage-completed" not in samples:
                    r = msg.get("response", {})
                    samples["upstream-usage-completed"] = {
                        "_source": src,
                        "_what": "response.completed 的账务相关字段（service_tier 谎报、usage、缓存选项）",
                        "service_tier": r.get("service_tier"),
                        "usage": trim(r.get("usage")),
                        "prompt_cache_options": r.get("prompt_cache_options"),
                        "prompt_cache_retention": r.get("prompt_cache_retention"),
                        "status": r.get("status"),
                    }
                if mtype == "codex.response.metadata" and "upstream-response-metadata" not in samples:
                    samples["upstream-response-metadata"] = {
                        "_source": src,
                        "_what": "原生 WS 中承载 HTTP 响应头的事件（含 x-codex-turn-state）",
                        "event": trim(msg),
                    }
            if "upstream-ws-handshake" not in samples:
                samples["upstream-ws-handshake"] = {
                    "_source": src,
                    "_what": "原生模式 WebSocket 握手请求头（含 openai-beta、routing-hint、turn-metadata）",
                    "request_headers": dict(flow["request"]["headers"]),
                    "server_event_sequence": ws.get("server_sequence"),
                }

        # 5) 客户入口的 SSE 事件序列
        if "18999" in url and resp_body.get("kind") == "sse" and "ingress-sse-sequence" not in samples:
            samples["ingress-sse-sequence"] = {
                "_source": src,
                "_what": "客户入口收到的 SSE 事件序列（假网关回放，形状与真实一致）",
                "event_sequence": resp_body.get("event_sequence"),
                "counts": resp_body.get("counts"),
            }

    for name, payload in sorted(samples.items()):
        path = os.path.join(args.out, f"{name}.json")
        with open(path, "w", encoding="utf-8") as f:
            json.dump(payload, f, ensure_ascii=False, indent=2, sort_keys=False)
            f.write("\n")
        size = os.path.getsize(path)
        print(f"  {name}.json  ({size:,} bytes)  ← {payload['_source']}")
    print(f"\n共 {len(samples)} 份样例写入 {args.out}/")


if __name__ == "__main__":
    main()
