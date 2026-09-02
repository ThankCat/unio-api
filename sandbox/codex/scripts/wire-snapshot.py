#!/usr/bin/env python3
"""把抓包摘要（flows/*.jsonl）规范化成稳定的 wire 契约快照，并支持跨 CLI 版本对比。

快照只保留「契约形状」——端点、请求头名、body 字段名、事件类型序列、响应头名——
不含任何取值、令牌、会话内容，因此可以安全入库，作为 adapter 实现的对照基线。

    python3 scripts/wire-snapshot.py build flows/*.jsonl -o wire/0.152.1.json
    python3 scripts/wire-snapshot.py diff wire/0.152.1.json wire/0.160.0.json

典型用法（CLI 升级后）：
    npm install @openai/codex@latest && scripts/capture-all.sh
    python3 scripts/wire-snapshot.py build flows/*.jsonl -o wire/<新版本>.json
    python3 scripts/wire-snapshot.py diff wire/<旧版本>.json wire/<新版本>.json
diff 输出的每一项都对应 adapter 里可能要改的一处逻辑。
"""
import argparse
import glob
import json
import os
import re
import sys

# 关注的上游主机（其余如 github/oaiusercontent 等噪声端点忽略）
INTERESTING_HOSTS = ("chatgpt.com", "auth.openai.com", "api.openai.com", "127.0.0.1")
# 端点路径归一：把 ID 段替换成占位符
ID_PATTERNS = [
    (re.compile(r"/resp_[A-Za-z0-9_-]+"), "/{response_id}"),
    (re.compile(r"/[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}"), "/{uuid}"),
]
# 每次请求都不同、不属于契约的头
VOLATILE_HEADERS = {
    "content-length", "date", "cf-ray", "sec-websocket-key", "sec-websocket-accept",
    "x-oai-request-id", "x-client-request-id", "report-to", "nel", "set-cookie", "cookie", "etag",
    "x-models-etag", "age", "expires", "last-modified", "server-timing", "alt-svc", "cf-cache-status",
}


def norm_path(url: str) -> str:
    without_query = url.split("?")[0]
    for pattern, repl in ID_PATTERNS:
        without_query = pattern.sub(repl, without_query)
    return without_query


def is_interesting(url: str) -> bool:
    return any(h in url for h in INTERESTING_HOSTS)


def header_names(pairs) -> list:
    return sorted({k.lower() for k, _ in pairs} - VOLATILE_HEADERS)


def body_shape(body, depth=0):
    """递归提取 body 的字段名结构（不含取值）。"""
    if body is None or depth > 3:
        return None
    if isinstance(body, dict):
        return {k: body_shape(v, depth + 1) for k, v in sorted(body.items())}
    if isinstance(body, list):
        merged = {}
        for item in body[:20]:
            shape = body_shape(item, depth + 1)
            if isinstance(shape, dict):
                for k, v in shape.items():
                    merged.setdefault(k, v)
        return [merged] if merged else "[]"
    return type(body).__name__


def input_item_kinds(body) -> list:
    """response.create / responses 请求里 input[] 的条目类型与内容种类。"""
    kinds = set()
    for item in (body or {}).get("input", []) if isinstance(body, dict) else []:
        if not isinstance(item, dict):
            continue
        base = f"{item.get('type')}/{item.get('role', '-')}"
        meta = item.get("internal_chat_message_metadata_passthrough") or {}
        for kind in meta.get("content_item_kinds", []) or ["-"]:
            kinds.add(f"{base}:{kind}")
    return sorted(kinds)


def collect(paths) -> dict:
    endpoints = {}
    for path in paths:
        for line in open(path, encoding="utf-8"):
            flow = json.loads(line)
            url = flow.get("url", "")
            if not is_interesting(url):
                continue
            transport = "websocket" if flow.get("websocket") else "http"
            key = f"{flow['method']} {norm_path(url)} [{transport}]"
            entry = endpoints.setdefault(key, {
                "request_headers": set(), "response_headers": set(),
                "request_body_keys": set(), "input_item_kinds": set(),
                "tool_types": set(), "client_message_types": set(),
                "event_types": set(), "response_body_keys": set(), "statuses": set(),
            })
            if flow.get("status"):
                entry["statuses"].add(flow["status"])
            entry["request_headers"].update(header_names(flow["request"]["headers"]))
            if flow.get("response"):
                entry["response_headers"].update(header_names(flow["response"]["headers"]))

            req_body = flow["request"].get("body") or {}
            if req_body.get("kind") == "json":
                j = req_body["json"]
                if isinstance(j, dict):
                    entry["request_body_keys"].update(j.keys())
                    entry["input_item_kinds"].update(input_item_kinds(j))
                    for tool in j.get("tools", []) or []:
                        if isinstance(tool, dict):
                            entry["tool_types"].add(f"{tool.get('type')}:{tool.get('name')}")

            resp_body = flow.get("response", {}).get("body") if flow.get("response") else None
            if isinstance(resp_body, dict):
                if resp_body.get("kind") == "sse":
                    entry["event_types"].update(resp_body.get("counts", {}).keys())
                elif resp_body.get("kind") == "json" and isinstance(resp_body.get("json"), dict):
                    entry["response_body_keys"].update(resp_body["json"].keys())

            ws = flow.get("websocket")
            if ws:
                entry["event_types"].update(ws.get("server_counts", {}).keys())
                for msg in ws.get("client_messages", []):
                    if not isinstance(msg, dict):
                        continue
                    entry["client_message_types"].add(str(msg.get("type")))
                    entry["request_body_keys"].update(msg.keys())
                    entry["input_item_kinds"].update(input_item_kinds(msg))
                    for item in msg.get("input", []):
                        if isinstance(item, dict) and item.get("type") == "additional_tools":
                            for ns in item.get("tools", []) or []:
                                entry["tool_types"].add(f"namespace:{ns.get('name')}")
    return {k: {kk: sorted(vv) for kk, vv in v.items()} for k, v in sorted(endpoints.items())}


def cmd_build(args) -> int:
    paths = [p for pattern in args.inputs for p in glob.glob(pattern)]
    if not paths:
        print("error: 没有匹配到输入文件", file=sys.stderr)
        return 1
    snapshot = {
        "cli_version": args.cli_version or detect_cli_version(),
        "sources": sorted(os.path.basename(p) for p in paths),
        "endpoints": collect(paths),
    }
    os.makedirs(os.path.dirname(os.path.abspath(args.output)), exist_ok=True)
    with open(args.output, "w", encoding="utf-8") as f:
        json.dump(snapshot, f, ensure_ascii=False, indent=2, sort_keys=True)
        f.write("\n")
    print(f"快照已写入 {args.output}（CLI {snapshot['cli_version']}，{len(snapshot['endpoints'])} 个端点，"
          f"来自 {len(paths)} 份抓包）")
    return 0


def detect_cli_version() -> str:
    sandbox = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
    try:
        pkg = json.load(open(os.path.join(sandbox, "node_modules", "@openai", "codex", "package.json")))
        return pkg.get("version", "unknown")
    except Exception:
        return "unknown"


def diff_lists(old, new):
    added = [x for x in new if x not in old]
    removed = [x for x in old if x not in new]
    return added, removed


def cmd_diff(args) -> int:
    old = json.load(open(args.old, encoding="utf-8"))
    new = json.load(open(args.new, encoding="utf-8"))
    print(f"对比 {old.get('cli_version')} → {new.get('cli_version')}\n")
    old_eps, new_eps = old["endpoints"], new["endpoints"]
    changed = False

    for ep in sorted(set(new_eps) - set(old_eps)):
        changed = True
        print(f"[新增端点] {ep}")
    for ep in sorted(set(old_eps) - set(new_eps)):
        changed = True
        print(f"[移除端点] {ep}")

    for ep in sorted(set(old_eps) & set(new_eps)):
        lines = []
        for field in ("request_headers", "response_headers", "request_body_keys", "input_item_kinds",
                      "tool_types", "client_message_types", "event_types", "response_body_keys", "statuses"):
            added, removed = diff_lists(old_eps[ep].get(field, []), new_eps[ep].get(field, []))
            if added:
                lines.append(f"    + {field}: {added}")
            if removed:
                lines.append(f"    - {field}: {removed}")
        if lines:
            changed = True
            print(f"[变更] {ep}")
            print("\n".join(lines))

    if not changed:
        print("无差异：wire 契约未变化，adapter 无需调整。")
    else:
        print("\n以上每一项都可能对应 adapter 中的一处逻辑，请逐条确认。")
    return 0


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    sub = parser.add_subparsers(dest="cmd", required=True)

    build = sub.add_parser("build", help="从抓包摘要生成 wire 契约快照")
    build.add_argument("inputs", nargs="+", help="flows/*.jsonl")
    build.add_argument("-o", "--output", required=True, help="输出快照路径，如 wire/0.152.1.json")
    build.add_argument("--cli-version", help="覆盖自动探测的 CLI 版本")
    build.set_defaults(func=cmd_build)

    diff = sub.add_parser("diff", help="对比两份快照，列出契约变化")
    diff.add_argument("old")
    diff.add_argument("new")
    diff.set_defaults(func=cmd_diff)

    args = parser.parse_args()
    return args.func(args)


if __name__ == "__main__":
    raise SystemExit(main())
