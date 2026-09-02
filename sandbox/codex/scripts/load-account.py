#!/usr/bin/env python3
"""把 sub2api 导出文件里的某个 OpenAI 订阅账号凭据注入沙箱的隔离 CODEX_HOME。

写出的 home/auth.json 被 .gitignore 排除，不会入库。凭据只落在沙箱本地。

用法：
    python3 scripts/load-account.py <sub2api-accounts.json> [账号序号，默认 0]
"""
import base64
import datetime
import json
import os
import sys


def decode_jwt_exp(token: str):
    try:
        payload_b64 = token.split(".")[1]
        payload_b64 += "=" * (-len(payload_b64) % 4)
        payload = json.loads(base64.urlsafe_b64decode(payload_b64))
        return payload.get("exp")
    except Exception:
        return None


def main() -> int:
    if len(sys.argv) < 2:
        print(__doc__)
        return 2

    src = sys.argv[1]
    idx = int(sys.argv[2]) if len(sys.argv) > 2 else 0

    with open(src) as f:
        data = json.load(f)

    accounts = data.get("accounts", [])
    if not accounts:
        print("error: 文件中没有 accounts", file=sys.stderr)
        return 1
    if idx >= len(accounts):
        print(f"error: 账号序号 {idx} 越界（共 {len(accounts)} 个）", file=sys.stderr)
        return 1

    account = accounts[idx]
    cred = account.get("credentials", {})
    if cred.get("type") != "oauth" or account.get("platform") != "openai":
        print(f"error: 序号 {idx} 不是 openai oauth 账号（platform={account.get('platform')}, "
              f"type={cred.get('type')}）", file=sys.stderr)
        return 1

    access_token = cred.get("access_token")
    if not access_token:
        print("error: 该账号缺少 access_token", file=sys.stderr)
        return 1

    auth = {
        "OPENAI_API_KEY": None,
        "tokens": {
            "id_token": cred.get("id_token"),
            "access_token": access_token,
            "refresh_token": cred.get("refresh_token"),
            "account_id": cred.get("chatgpt_account_id"),
        },
        "last_refresh": datetime.datetime.utcnow().strftime("%Y-%m-%dT%H:%M:%S.000Z"),
    }

    home = os.path.join(os.path.dirname(os.path.dirname(os.path.abspath(__file__))), "home")
    os.makedirs(home, exist_ok=True)
    out = os.path.join(home, "auth.json")
    with open(out, "w") as f:
        json.dump(auth, f)
    os.chmod(out, 0o600)

    exp = decode_jwt_exp(access_token)
    now = int(datetime.datetime.utcnow().timestamp())
    if exp:
        remaining = (exp - now) / 3600
        status = "有效" if exp > now else "已过期，需 refresh"
        exp_str = datetime.datetime.utcfromtimestamp(exp).strftime("%Y-%m-%d %H:%M:%SZ")
        token_note = f"access_token 过期 {exp_str}（{status}，距今 {remaining:.1f} 小时）"
    else:
        token_note = "access_token 非标准 JWT，过期时间未知"

    print(f"已注入账号 [{idx}]  plan={cred.get('plan_type')}  ->  {out}")
    print(token_note)
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
