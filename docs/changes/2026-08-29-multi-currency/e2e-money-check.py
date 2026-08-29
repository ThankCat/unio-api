#!/usr/bin/env python3
"""多货币 e2e 金额矩阵验证（对真实 admin-server + 开发库交叉验证）。

用法：
    ADMIN_TOKEN=$(curl -s -X POST localhost:8521/v1/login -H 'Content-Type: application/json' \
        -d '{"username":"...","password":"..."}' | jq -r .token) \
    python3 e2e-money-check.py

口径（两条河 + 三边界）：
- 成本快照按 provider 币种记账（CNY 行必须钉 fx_rate 且 total_cost_amount_usd = amount / fx）；
- 客户账本恒 USD；provider 账本恒原币；
- 汇总/毛利一律 total_cost_amount_usd；展示层折算与守卫共用最新汇率。
每项检查：API 值 vs DB 直算值，容差 1e-9（金额均为 NUMERIC 精确串）。
"""

import json
import os
import subprocess
import sys
import urllib.request
from decimal import Decimal

BASE = os.environ.get("ADMIN_BASE", "http://localhost:8521/v1")
TOKEN = os.environ.get("ADMIN_TOKEN") or open("/tmp/admin-token.txt").read().strip()
PSQL = [
    "docker", "compose", "--env-file", "deploy/env/.env.dev", "-f", "deploy/compose.dev.yml",
    "exec", "-T", "postgres", "psql", "-U", os.environ.get("POSTGRES_USER", "unio"),
    "-d", os.environ.get("POSTGRES_DB", "unio"), "-t", "-A", "-c",
]

results: list[tuple[str, bool, str]] = []


def api(path: str):
    req = urllib.request.Request(BASE + path, headers={"Authorization": f"Bearer {TOKEN}"})
    with urllib.request.urlopen(req) as resp:
        return json.load(resp)


def db(sql: str) -> list[list[str]]:
    out = subprocess.run(PSQL + [sql], capture_output=True, text=True, check=True).stdout.strip()
    return [line.split("|") for line in out.splitlines() if line]


def dec(v) -> Decimal | None:
    if v is None or v == "":
        return None
    return Decimal(str(v))


def check(name: str, ok: bool, detail: str = ""):
    results.append((name, ok, detail))
    print(f"{'PASS' if ok else 'FAIL'}  {name}" + (f"  [{detail}]" if detail and not ok else ""))


def approx(a: Decimal | None, b: Decimal | None, tol: str = "1e-9") -> bool:
    if a is None or b is None:
        return a == b
    return abs(a - b) <= Decimal(tol)


# ---------- 1. 请求列表：币种/汇率/USD 折算自洽 ----------
rows = api("/requests?limit=50")["data"]
settled = [r for r in rows if r.get("total_cost_amount") is not None]
check("requests.list 至少有已结算行", len(settled) > 0, f"settled={len(settled)}")
for r in settled:
    rid = r["id"]
    ccy = r.get("cost_currency")
    amt, usd, fx = dec(r.get("total_cost_amount")), dec(r.get("total_cost_usd")), dec(r.get("cost_fx_rate"))
    if ccy == "USD":
        check(f"req#{rid} USD 行 usd==amount", approx(amt, usd), f"amt={amt} usd={usd}")
        check(f"req#{rid} USD 行无汇率", fx is None, f"fx={fx}")
    else:
        check(f"req#{rid} {ccy} 行钉了汇率", fx is not None and usd is not None, f"fx={fx} usd={usd}")
        if fx and usd is not None and amt is not None:
            # 结算侧 scale 10 单次舍入 → 容差放到 5e-10
            check(f"req#{rid} usd≈amount/fx", approx(usd, amt / fx, "5e-10"), f"{usd} vs {amt / fx}")
    # 分项和 = 总额（原币）
    parts = [dec(r.get(k)) or Decimal(0) for k in (
        "uncached_input_cost_amount", "cache_read_input_cost_amount",
        "cache_creation_5m_input_cost_amount", "cache_creation_1h_input_cost_amount",
        "cache_creation_30m_input_cost_amount", "output_cost_amount", "reasoning_output_cost_amount")]
    if amt is not None:
        check(f"req#{rid} 分项和==总额", approx(sum(parts), amt), f"{sum(parts)} vs {amt}")
    # 与 DB 快照一致
    dbrow = db(f"SELECT currency, total_cost_amount, total_cost_amount_usd, fx_rate FROM cost_snapshots WHERE request_record_id={rid}")
    if dbrow:
        dccy, damt, dusd, dfx = dbrow[0][0], dec(dbrow[0][1]), dec(dbrow[0][2]), dec(dbrow[0][3] or None)
        check(f"req#{rid} API==DB", ccy == dccy and approx(amt, damt) and approx(usd, dusd) and approx(fx, dfx),
              f"api=({ccy},{amt},{usd},{fx}) db=({dccy},{damt},{dusd},{dfx})")

# ---------- 2. 请求详情：cost_snapshot 与列表一致 ----------
sample = settled[:3]
for r in sample:
    d = api(f"/requests/{r['request_id']}")["data"]
    cs = d.get("cost_snapshot") or {}
    check(f"detail#{r['id']} currency/fx/usd 透出",
          cs.get("currency") == r.get("cost_currency")
          and approx(dec(cs.get("fx_rate")), dec(r.get("cost_fx_rate")))
          and approx(dec(cs.get("total_cost_amount_usd")), dec(r.get("total_cost_usd"))),
          f"cs=({cs.get('currency')},{cs.get('fx_rate')},{cs.get('total_cost_amount_usd')})")
    # 用户扣费 = USD 账本净额
    dbn = db(f"""SELECT COALESCE(SUM(CASE WHEN entry_type IN ('debit','adjustment_debit') THEN amount
                 WHEN entry_type IN ('credit','refund','adjustment_credit') THEN -amount ELSE 0 END),0)
                 FROM ledger_entries WHERE request_record_id={r['id']} AND currency='USD'""")
    check(f"detail#{r['id']} user_charge==USD账本净额", approx(dec(r.get("user_charge_usd")), dec(dbn[0][0])),
          f"api={r.get('user_charge_usd')} db={dbn[0][0]}")

# ---------- 3. 最新汇率：API == DB ----------
latest_rows = api("/exchange-rates/latest?quote=CNY")["data"]
latest = latest_rows[0] if latest_rows else {}
dbfx = db("SELECT rate, rate_date FROM exchange_rates WHERE base_currency='USD' AND quote_currency='CNY' ORDER BY rate_date DESC, fetched_at DESC LIMIT 1")
fx_now = dec(dbfx[0][0]) if dbfx else None
check("exchange-rates.latest == DB", approx(dec(latest.get("rate")), fx_now), f"api={latest.get('rate')} db={fx_now}")

# ---------- 4. providers ops：原币余额 + USD 折算 ----------
provs = api("/providers/ops")["data"]
for p in provs:
    pid, ccy = p["id"], p.get("currency")
    bal, bal_usd = dec(p.get("balance")), dec(p.get("balance_usd"))
    dbb = db(f"SELECT COALESCE(SUM(CASE WHEN entry_type LIKE '%credit' THEN amount ELSE -amount END),0) FROM provider_ledger_entries WHERE provider_id={pid} AND currency='{ccy}'")
    dbal = dec(dbb[0][0])
    check(f"provider#{pid}({ccy}) balance==原币账本", approx(bal, dbal), f"api={bal} db={dbal}")
    if ccy != "USD" and fx_now and bal is not None:
        check(f"provider#{pid} balance_usd≈balance/最新汇率",
              bal_usd is not None and approx(bal_usd, bal / fx_now, "1e-6"), f"api={bal_usd} calc={bal / fx_now if bal is not None else None}")

# ---------- 5. 设价面板（models/{id}/ops/channels）：原币成本 + USD 折算 ----------
models = api("/models/ops?limit=100")["data"]
checked_ch = 0
for m in models:
    if checked_ch >= 6:
        break
    chs = api(f"/models/{m['id']}/ops/channels")["data"]
    for ch in chs:
        ccy = ch.get("cost_currency")
        ic, icu = dec(ch.get("input_cost")), dec(ch.get("input_cost_usd"))
        if ic is None:
            continue
        checked_ch += 1
        if ccy == "USD":
            check(f"model#{m['id']}/ch#{ch['channel_id']} USD 成本 usd==orig", approx(ic, icu), f"{ic} vs {icu}")
        elif fx_now:
            check(f"model#{m['id']}/ch#{ch['channel_id']} {ccy} input_cost_usd≈orig/fx",
                  icu is not None and approx(icu, ic / fx_now, "1e-6"), f"api={icu} calc={ic / fx_now}")
check("设价面板检查了成本行", checked_ch > 0, f"checked={checked_ch}")

# ---------- 6. 驾驶舱成本（按 provider 行精确对照 DB 的 USD 折算和） ----------
# 用显式 from/to 消除默认窗口差异；口径逐字对齐 DashboardBreakdownProvider 的 money_agg：
# r.created_at 过滤 + r.final_provider_id 归因 + SUM(cs.total_cost_amount_usd)。
BD_FROM, BD_TO = "2026-08-01T00:00:00Z", "2026-08-30T00:00:00Z"
bd = api(f"/dashboard/breakdown?dimension=provider&from={BD_FROM}&to={BD_TO}")["data"]["rows"]
check("dashboard.breakdown 有行", len(bd) > 0)
for row in bd:
    ref = row.get("ref_id")
    dbc = db(
        f"SELECT COALESCE(SUM(cs.total_cost_amount_usd),0) FROM request_records r "
        f"JOIN cost_snapshots cs ON cs.request_record_id = r.id "
        f"WHERE r.final_provider_id = {ref} "
        f"AND r.created_at >= '{BD_FROM}' AND r.created_at < '{BD_TO}'"
    )
    check(f"breakdown provider#{ref} cost_usd==DB(USD 折算)",
          approx(dec(row.get("cost_usd")), dec(dbc[0][0]), "1e-9"),
          f"api={row.get('cost_usd')} db={dbc[0][0]}")

# ---------- 7. provider 账本新分录币种 == provider 币种 ----------
# 历史分录（D2 修订与数据修正之前）保留错标时代的 'USD' 标签——账本不可变、§12.C.1 拍板不回填；
# 只验证全部服务运行新代码后（2026-08-29 17:30 +08 之后）的新分录必须按 provider 币种记账。
# （17:21 有两条 probe_debit USD 分录：旧二进制进程在 probe.go 改动落地前执行的探测，已实测新代码产出 CNY。）
mism = db(
    "SELECT COUNT(*) FROM provider_ledger_entries ple JOIN providers p ON p.id=ple.provider_id "
    "WHERE ple.currency <> p.currency AND ple.created_at >= '2026-08-29T17:30:00+08:00'"
)
check("provider 账本新分录币种==provider 币种", mism[0][0] == "0", f"mismatch={mism[0][0]}")

# ---------- 8. 客户账本恒 USD ----------
nonusd = db("SELECT COUNT(*) FROM ledger_entries WHERE currency <> 'USD'")
check("客户账本恒 USD", nonusd[0][0] == "0", f"non-usd={nonusd[0][0]}")

# ---------- 9. 守卫违规视图为空 ----------
viol = db("SELECT COUNT(*) FROM margin_violations_current")
check("margin_violations_current 零违规", viol[0][0] == "0", f"violations={viol[0][0]}")

# ---------- 10. CNY 快照完整性（CHECK 四象限之外的运行时复核） ----------
badfx = db("SELECT COUNT(*) FROM cost_snapshots WHERE currency <> 'USD' AND (fx_rate IS NULL OR total_cost_amount_usd IS NULL)")
check("非 USD 快照必有 fx+usd", badfx[0][0] == "0", f"bad={badfx[0][0]}")
badusd = db("SELECT COUNT(*) FROM cost_snapshots WHERE currency <> 'USD' AND ABS(total_cost_amount_usd - total_cost_amount / fx_rate) > 1e-9")
check("非 USD 快照 usd==amount/fx（全量）", badusd[0][0] == "0", f"bad={badusd[0][0]}")

fails = [r for r in results if not r[1]]
print(f"\n===== {len(results) - len(fails)}/{len(results)} PASS =====")
if fails:
    sys.exit(1)
