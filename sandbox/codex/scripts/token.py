#!/usr/bin/env python3
"""沙箱账号令牌工具：查看有效期、必要时用 refresh_token 刷新 access_token。

    python3 scripts/token.py status                 # 打印 access_token 过期时间，过期返回 1
    python3 scripts/token.py refresh                # 仅在已过期/即将过期（<1h）时刷新
    python3 scripts/token.py refresh --force        # 强制刷新

刷新流程与 Codex CLI / Sub2API 一致：POST https://auth.openai.com/oauth/token，
grant_type=refresh_token + refresh_token + client_id + scope。只在响应带回新 refresh_token 时才覆盖旧值。

注意：若同一账号还被别处（如另一套 sub2api）持有，刷新可能轮换 refresh_token 并使对方的副本失效；
因此默认只在必要时刷新。
"""
import base64
import datetime
import json
import os
import sys
import urllib.error
import urllib.parse
import urllib.request

TOKEN_URL = "https://auth.openai.com/oauth/token"
REFRESH_SCOPES = "openid profile email"
SANDBOX_DIR = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
CODEX_HOME = os.environ.get("CODEX_SANDBOX_HOME") or os.path.join(SANDBOX_DIR, "home")
AUTH_PATH = os.path.join(CODEX_HOME, "auth.json")


def jwt_claims(token: str) -> dict:
    try:
        payload = token.split(".")[1]
        payload += "=" * (-len(payload) % 4)
        return json.loads(base64.urlsafe_b64decode(payload))
    except Exception:
        return {}


def cli_version() -> str:
    try:
        pkg = json.load(open(os.path.join(SANDBOX_DIR, "node_modules", "@openai", "codex", "package.json")))
        return pkg.get("version", "0.0.0")
    except Exception:
        return "0.0.0"


def load_auth() -> dict:
    with open(AUTH_PATH) as f:
        return json.load(f)


def save_auth(auth: dict) -> None:
    tmp = AUTH_PATH + ".tmp"
    with open(tmp, "w") as f:
        json.dump(auth, f)
    os.chmod(tmp, 0o600)
    os.replace(tmp, AUTH_PATH)


def expiry(auth: dict):
    exp = jwt_claims(auth.get("tokens", {}).get("access_token", "")).get("exp")
    return int(exp) if exp else None


def describe(auth: dict) -> str:
    exp = expiry(auth)
    if not exp:
        return "access_token 非 JWT 或无 exp"
    now = int(datetime.datetime.utcnow().timestamp())
    when = datetime.datetime.utcfromtimestamp(exp).strftime("%Y-%m-%d %H:%M:%SZ")
    hours = (exp - now) / 3600
    state = "有效" if exp > now else "已过期"
    return f"access_token 过期 {when}（{state}，距今 {hours:.1f} 小时）"


def refresh(auth: dict) -> dict:
    tokens = auth.get("tokens", {})
    refresh_token = tokens.get("refresh_token")
    if not refresh_token:
        raise SystemExit("error: auth.json 中没有 refresh_token，无法刷新")
    client_id = jwt_claims(tokens.get("access_token", "")).get("client_id") or tokens.get("client_id")
    if not client_id:
        raise SystemExit("error: 无法确定 OAuth client_id（access_token 中无 client_id 声明）")

    ver = cli_version()
    form = urllib.parse.urlencode({
        "grant_type": "refresh_token",
        "refresh_token": refresh_token,
        "client_id": client_id,
        "scope": REFRESH_SCOPES,
    }).encode()
    req = urllib.request.Request(TOKEN_URL, data=form, method="POST")
    req.add_header("Content-Type", "application/x-www-form-urlencoded")
    req.add_header("User-Agent", f"codex_cli_rs/{ver} (Mac OS 15.2.0; arm64) sandbox")
    req.add_header("originator", "codex_cli_rs")
    try:
        with urllib.request.urlopen(req, timeout=30) as resp:
            data = json.loads(resp.read().decode("utf-8"))
    except urllib.error.HTTPError as e:
        body = e.read().decode("utf-8", "replace")[:500]
        raise SystemExit(f"error: 刷新失败 HTTP {e.code}: {body}")

    if not data.get("access_token"):
        raise SystemExit(f"error: 刷新响应缺少 access_token: {json.dumps(data)[:300]}")
    tokens["access_token"] = data["access_token"]
    if data.get("id_token"):
        tokens["id_token"] = data["id_token"]
    if data.get("refresh_token"):  # 只在带回新值时覆盖，避免用空值冲掉有效凭据
        tokens["refresh_token"] = data["refresh_token"]
    auth["tokens"] = tokens
    auth["last_refresh"] = datetime.datetime.utcnow().strftime("%Y-%m-%dT%H:%M:%S.000Z")
    return auth


def main() -> int:
    if len(sys.argv) < 2 or sys.argv[1] not in ("status", "refresh"):
        print(__doc__)
        return 2
    if not os.path.exists(AUTH_PATH):
        print(f"error: 未找到 {AUTH_PATH}，先运行 scripts/load-account.py", file=sys.stderr)
        return 1
    auth = load_auth()
    cmd = sys.argv[1]
    if cmd == "status":
        print(describe(auth))
        exp = expiry(auth)
        return 0 if exp and exp > int(datetime.datetime.utcnow().timestamp()) else 1

    force = "--force" in sys.argv
    exp = expiry(auth)
    now = int(datetime.datetime.utcnow().timestamp())
    if not force and exp and exp - now > 3600:
        print(f"无需刷新：{describe(auth)}")
        return 0
    print("正在刷新 access_token …")
    auth = refresh(auth)
    save_auth(auth)
    print(f"已刷新并写回 {AUTH_PATH}：{describe(auth)}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
