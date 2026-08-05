#!/usr/bin/env python3
"""初始化本地 / 测试数据库拓扑（纯 stdlib，无第三方依赖）。

全部由本脚本 + JSON 配置完成，不再依赖 scripts/*.sql 种子：
  - capability_keys → migrations/000006（migrate 时写入）
  - test_user（开发用户 + 充值）→ 配置 test_user
  - 服务商 / 渠道 / 模型 / 模型能力 / 线路 / 绑定 / 倍率 → 配置其余字段
  - 模型基准售价（DEC-026 model_prices）← 配置 models 价格字段
    （cache_read 默认 = input × 0.1；5.6 等可配 cache_write_30m / long_context_*）

配置：
  scripts/test/test_db/config.local.json         # 真实 credential，gitignore，不入库
  scripts/test/test_db/config.local.example.json # 打码示例，入库

用法：
  python3 scripts/test/init_test_db.py export
  python3 scripts/test/init_test_db.py export --with-secrets
  python3 scripts/test/init_test_db.py init --confirm-offline

init 会直接写 PostgreSQL，只用于 Gateway、Admin、Worker 已停止的空库 / 测试库初始化。
运行中系统的配置变更必须走 Admin API，不能用本脚本代替热更新。

连接（二选一）：
  - 默认：docker exec -i $POSTGRES_CONTAINER psql -U unio -d unio
  - 或设 DATABASE_URL=postgres://... 时用本机 psql "$DATABASE_URL"
"""

from __future__ import annotations

import argparse
import json
import os
import subprocess
import sys
from pathlib import Path
from typing import Any

ROOT = Path(__file__).resolve().parents[2]  # unio-gateway/
TEST_DB_DIR = Path(__file__).resolve().parent / "test_db"
DEFAULT_CONFIG = TEST_DB_DIR / "config.local.json"
EXAMPLE_CONFIG = TEST_DB_DIR / "config.local.example.json"
DEFAULT_CONTAINER = os.environ.get("POSTGRES_CONTAINER", "unio-postgres")
ALLOWED_PRIORITIES = tuple(range(0, 101, 10))


# ---------------------------------------------------------------------------
# SQL helpers
# ---------------------------------------------------------------------------

def sql_quote(value: Any) -> str:
    """把 Python 值编成 SQL 字面量（仅用于种子脚本，非通用安全层）。"""
    if value is None:
        return "NULL"
    if isinstance(value, bool):
        return "TRUE" if value else "FALSE"
    if isinstance(value, (int, float)):
        return str(value)
    text = str(value)
    return "'" + text.replace("'", "''") + "'"


def sql_numeric(value: Any) -> str:
    if value is None or str(value) == "":
        return "NULL"
    return f"{sql_quote(str(value))}::numeric"


def sql_bigint(value: Any) -> str:
    if value is None or str(value) == "":
        return "NULL"
    return str(int(value))


def run_psql(sql: str, *, quiet: bool = False) -> None:
    database_url = os.environ.get("DATABASE_URL", "").strip()
    if database_url:
        cmd = ["psql", database_url, "-v", "ON_ERROR_STOP=1", "-f", "-"]
    else:
        cmd = [
            "docker",
            "exec",
            "-i",
            DEFAULT_CONTAINER,
            "psql",
            "-U",
            "unio",
            "-d",
            "unio",
            "-v",
            "ON_ERROR_STOP=1",
            "-f",
            "-",
        ]
    if not quiet:
        print(f">> psql ({'DATABASE_URL' if database_url else DEFAULT_CONTAINER})")
    proc = subprocess.run(cmd, input=sql, text=True, capture_output=True)
    if proc.returncode != 0:
        sys.stderr.write(proc.stdout or "")
        sys.stderr.write(proc.stderr or "")
        raise SystemExit(f"psql failed (exit {proc.returncode})")
    if proc.stdout and not quiet:
        # 只回显有意义的 NOTICE / 结果行，避免刷屏
        for line in proc.stdout.splitlines():
            if line.strip():
                print(line)


# ---------------------------------------------------------------------------
# Config → SQL
# ---------------------------------------------------------------------------

DEFAULT_TEST_USER = {
    "email": "dev@unio.local",
    "display_name": "Dev User",
    "password_hash": "seed-placeholder-not-a-real-hash",
    "credit_usd": "100",
}


def config_error(path: str, message: str) -> None:
    raise SystemExit(f"{path}: {message}")


def validate_optional_nonnegative_int(value: Any, path: str) -> None:
    if value is None:
        return
    if isinstance(value, bool) or not isinstance(value, int):
        config_error(path, "必须是非负整数或 null")
    if value < 0:
        config_error(path, "必须 >= 0")


def validate_optional_positive_int(value: Any, path: str) -> None:
    if value is None:
        return
    if isinstance(value, bool) or not isinstance(value, int):
        config_error(path, "必须是正整数或 null")
    if value <= 0:
        config_error(path, "必须 > 0")


def validate_topology_config(cfg: dict[str, Any]) -> None:
    for index, channel in enumerate(cfg.get("channels") or []):
        path = f"channels[{index}]"
        for retired_key in ("timeout_ms", "rpm_limit", "rpd_limit", "tpm_limit"):
            if retired_key in channel:
                config_error(
                    f"{path}.{retired_key}",
                    "Channel 旧字段已删除，请使用 response_timeout_ms / first_token_timeout_ms / concurrency_limit",
                )
        priority = channel.get("priority", 0)
        if isinstance(priority, bool) or not isinstance(priority, int):
            config_error(f"{path}.priority", "必须是整数")
        if priority not in ALLOWED_PRIORITIES:
            allowed = ",".join(str(value) for value in ALLOWED_PRIORITIES)
            config_error(f"{path}.priority", f"只允许 {allowed}")

        validate_optional_nonnegative_int(
            channel.get("concurrency_limit"), f"{path}.concurrency_limit"
        )
        validate_optional_positive_int(
            channel.get("response_timeout_ms"), f"{path}.response_timeout_ms"
        )
        validate_optional_positive_int(
            channel.get("first_token_timeout_ms"), f"{path}.first_token_timeout_ms"
        )

        sticky_configured = (
            "sticky_enabled" in channel or "sticky_ttl_ms" in channel
        )
        if not sticky_configured:
            continue
        if "sticky_enabled" not in channel:
            config_error(
                f"{path}.sticky_enabled",
                "配置 sticky_ttl_ms 时必须同时配置 sticky_enabled",
            )
        sticky_enabled = channel["sticky_enabled"]
        sticky_ttl_ms = channel.get("sticky_ttl_ms")
        if sticky_enabled is not None and not isinstance(sticky_enabled, bool):
            config_error(f"{path}.sticky_enabled", "必须是 true、false 或 null")
        if sticky_enabled is True:
            if (
                isinstance(sticky_ttl_ms, bool)
                or not isinstance(sticky_ttl_ms, int)
                or sticky_ttl_ms <= 0
            ):
                config_error(
                    f"{path}.sticky_ttl_ms",
                    "sticky_enabled=true 时必须是正整数",
                )
        elif sticky_ttl_ms is not None:
            config_error(
                f"{path}.sticky_ttl_ms",
                "继承或关闭 Sticky 时必须为 null",
            )

    for index, route in enumerate(cfg.get("routes") or []):
        path = f"routes[{index}]"
        validate_optional_nonnegative_int(
            route.get("concurrency_limit"), f"{path}.concurrency_limit"
        )
        if route.get("sticky_enabled") is not None:
            config_error(
                f"{path}.sticky_enabled",
                "Route Sticky 已废弃，请在各 Channel 配置 Sticky",
            )
        if "tpm_limit" in route:
            config_error(
                f"{path}.tpm_limit",
                "Unio 不限制 TPM，该字段已删除；token 吞吐只做观测，不参与准入",
            )


def append_test_user_sql(parts: list[str], cfg: dict[str, Any]) -> None:
    """开发测试用户 + 幂等充值（原 seed-test-user.sql）。cfg.test_user=null 则跳过。"""
    raw = cfg.get("test_user", DEFAULT_TEST_USER)
    if raw is None:
        return
    tu = {**DEFAULT_TEST_USER, **raw}
    email = tu["email"]
    credit = str(tu.get("credit_usd", "100"))
    idem = f"seed:{email}:credit:{credit}:USD"
    parts.append(
        f"""
-- test_user：开发用户（幂等）+ 充值 {credit} USD（固定幂等键，重跑不加第二次）
INSERT INTO users (email, password_hash, display_name)
SELECT {sql_quote(email)}, {sql_quote(tu['password_hash'])}, {sql_quote(tu['display_name'])}
WHERE NOT EXISTS (
    SELECT 1 FROM users WHERE lower(email) = lower({sql_quote(email)})
);

INSERT INTO user_balances (user_id, currency, balance)
SELECT u.id, 'USD', 0
FROM users u
WHERE lower(u.email) = lower({sql_quote(email)})
ON CONFLICT (user_id, currency) DO NOTHING;

WITH target AS (
    SELECT ub.user_id, ub.balance AS balance_before
    FROM user_balances ub
    JOIN users u ON u.id = ub.user_id
    WHERE lower(u.email) = lower({sql_quote(email)})
      AND ub.currency = 'USD'
      AND NOT EXISTS (
          SELECT 1 FROM ledger_entries le
          WHERE le.idempotency_key = {sql_quote(idem)}
      )
    FOR UPDATE OF ub
),
updated AS (
    UPDATE user_balances ub
    SET balance = ub.balance + {sql_quote(credit)}::numeric,
        updated_at = now()
    FROM target t
    WHERE ub.user_id = t.user_id AND ub.currency = 'USD'
    RETURNING ub.user_id, t.balance_before, ub.balance AS balance_after
)
INSERT INTO ledger_entries (
    user_id, request_record_id, entry_type, amount, currency,
    balance_before, balance_after, idempotency_key, reason
)
SELECT
    user_id, NULL, 'credit', {sql_quote(credit)}::numeric, 'USD',
    balance_before, balance_after, {sql_quote(idem)},
    {sql_quote(f'seed: local dev top-up {credit} USD')}
FROM updated;
""".strip()
    )


def build_topology_sql(cfg: dict[str, Any]) -> str:
    validate_topology_config(cfg)
    parts: list[str] = ["BEGIN;", ""]

    append_test_user_sql(parts, cfg)

    # providers
    for p in cfg.get("providers") or []:
        slug = p["slug"]
        parts.append(
            f"""
INSERT INTO providers (slug, name, origin, status)
VALUES ({sql_quote(slug)}, {sql_quote(p.get('name', slug))}, {sql_quote(p['origin'])}, {sql_quote(p.get('status', 'enabled'))})
ON CONFLICT (slug) DO UPDATE SET
    name = EXCLUDED.name,
    origin = EXCLUDED.origin,
    status = EXCLUDED.status,
    updated_at = now();
""".strip()
        )

    # models
    for m in cfg.get("models") or []:
        parts.append(
            f"""
INSERT INTO models (
    model_id, display_name, owned_by, status,
    context_window_tokens, max_output_tokens,
    input_price_usd_per_million_tokens, output_price_usd_per_million_tokens,
    release_date, source
) VALUES (
    {sql_quote(m['model_id'])},
    {sql_quote(m.get('display_name', m['model_id']))},
    {sql_quote(m.get('owned_by', 'openai'))},
    {sql_quote(m.get('status', 'enabled'))},
    {sql_quote(m.get('context_window_tokens'))},
    {sql_quote(m.get('max_output_tokens'))},
    {sql_quote(m.get('input_price_usd_per_million_tokens'))},
    {sql_quote(m.get('output_price_usd_per_million_tokens'))},
    {sql_quote(m.get('release_date'))}::date,
    {sql_quote(m.get('source', 'manual'))}
)
ON CONFLICT (model_id) DO UPDATE SET
    display_name = EXCLUDED.display_name,
    owned_by = EXCLUDED.owned_by,
    status = EXCLUDED.status,
    context_window_tokens = EXCLUDED.context_window_tokens,
    max_output_tokens = EXCLUDED.max_output_tokens,
    input_price_usd_per_million_tokens = EXCLUDED.input_price_usd_per_million_tokens,
    output_price_usd_per_million_tokens = EXCLUDED.output_price_usd_per_million_tokens,
    release_date = EXCLUDED.release_date,
    source = EXCLUDED.source,
    updated_at = now();
""".strip()
        )
        # DEC-026 基准售价：admin / 结算读 model_prices。
        # cache_read 默认 = input × 0.1；cache_write_30m / long_context 按配置（5.6 等）。
        in_price = m.get("input_price_usd_per_million_tokens")
        out_price = m.get("output_price_usd_per_million_tokens")
        if in_price is not None and str(in_price) != "" and out_price is not None and str(out_price) != "":
            model_key = m["model_id"]
            cache_read = m.get("cache_read_price_usd_per_million_tokens")
            if cache_read is None or str(cache_read) == "":
                cache_read_sql = f"({sql_quote(str(in_price))}::numeric * 0.1)"
            else:
                cache_read_sql = sql_numeric(cache_read)
            cache_write_5m_sql = sql_numeric(m.get("cache_write_5m_price_usd_per_million_tokens"))
            cache_write_1h_sql = sql_numeric(m.get("cache_write_1h_price_usd_per_million_tokens"))
            cache_write_30m_sql = sql_numeric(m.get("cache_write_30m_price_usd_per_million_tokens"))
            reasoning_sql = sql_numeric(m.get("reasoning_output_price_usd_per_million_tokens"))
            lc_enabled = bool(m.get("long_context_enabled", False))
            lc_threshold_sql = sql_bigint(m.get("long_context_threshold"))
            lc_in_mult_sql = sql_numeric(m.get("long_context_input_multiplier"))
            lc_out_mult_sql = sql_numeric(m.get("long_context_output_multiplier"))
            if lc_enabled and (
                lc_threshold_sql == "NULL"
                or lc_in_mult_sql == "NULL"
                or lc_out_mult_sql == "NULL"
            ):
                raise SystemExit(
                    f"model {model_key!r}: long_context_enabled=true 时必须提供 "
                    "long_context_threshold / long_context_input_multiplier / "
                    "long_context_output_multiplier"
                )
            parts.append(
                f"""
UPDATE model_prices mp
SET
    uncached_input_price = {sql_numeric(in_price)},
    cache_read_input_price = {cache_read_sql},
    cache_write_5m_input_price = {cache_write_5m_sql},
    cache_write_1h_input_price = {cache_write_1h_sql},
    cache_write_30m_input_price = {cache_write_30m_sql},
    output_price = {sql_numeric(out_price)},
    reasoning_output_price = {reasoning_sql},
    long_context_enabled = {sql_quote(lc_enabled)},
    long_context_threshold = {lc_threshold_sql},
    long_context_input_multiplier = {lc_in_mult_sql},
    long_context_output_multiplier = {lc_out_mult_sql},
    currency = 'USD',
    pricing_unit = 'per_1m_tokens',
    status = 'enabled',
    updated_at = now()
FROM models m
WHERE mp.model_id = m.id
  AND m.model_id = {sql_quote(model_key)}
  AND mp.status = 'enabled'
  AND (mp.effective_to IS NULL OR mp.effective_to > now());

INSERT INTO model_prices (
    model_id, currency, pricing_unit,
    uncached_input_price, cache_read_input_price,
    cache_write_5m_input_price, cache_write_1h_input_price, cache_write_30m_input_price,
    output_price, reasoning_output_price,
    long_context_enabled, long_context_threshold,
    long_context_input_multiplier, long_context_output_multiplier,
    status, effective_from
)
SELECT
    m.id, 'USD', 'per_1m_tokens',
    {sql_numeric(in_price)},
    {cache_read_sql},
    {cache_write_5m_sql},
    {cache_write_1h_sql},
    {cache_write_30m_sql},
    {sql_numeric(out_price)},
    {reasoning_sql},
    {sql_quote(lc_enabled)},
    {lc_threshold_sql},
    {lc_in_mult_sql},
    {lc_out_mult_sql},
    'enabled', now()
FROM models m
WHERE m.model_id = {sql_quote(model_key)}
  AND NOT EXISTS (
      SELECT 1 FROM model_prices mp
      WHERE mp.model_id = m.id
        AND mp.status = 'enabled'
        AND (mp.effective_to IS NULL OR mp.effective_to > now())
  );
""".strip()
            )

    # channels（按 provider.slug + channel.name 幂等）
    for c in cfg.get("channels") or []:
        key = c.get("key") or c["name"]
        provider = c["provider"]
        cred = c.get("credential")
        if not cred or "REPLACE_ME" in str(cred) or "…" in str(cred):
            raise SystemExit(
                f"channel {key!r} credential 未填写真实值；请编辑 config.local.json"
            )
        sticky_configured = "sticky_enabled" in c or "sticky_ttl_ms" in c
        sticky_enabled_sql = sql_quote(c.get("sticky_enabled"))
        sticky_ttl_sql = sql_quote(c.get("sticky_ttl_ms"))
        sticky_enabled_update = (
            "EXCLUDED.sticky_enabled"
            if sticky_configured
            else "channels.sticky_enabled"
        )
        sticky_ttl_update = (
            "EXCLUDED.sticky_ttl_ms"
            if sticky_configured
            else "channels.sticky_ttl_ms"
        )
        parts.append(
            f"""
INSERT INTO channels (
    provider_id, name, protocol, adapter_key, credential, status, priority,
	response_timeout_ms, first_token_timeout_ms, concurrency_limit,
    upstream_bills_on_disconnect, sticky_enabled, sticky_ttl_ms
)
SELECT
    p.id,
    {sql_quote(c['name'])},
    {sql_quote(c.get('protocol', 'openai'))},
    {sql_quote(c.get('adapter_key', 'openai'))},
    {sql_quote(cred)},
    {sql_quote(c.get('status', 'enabled'))},
    {sql_quote(int(c.get('priority', 0)))},
	{sql_quote(c.get('response_timeout_ms'))},
	{sql_quote(c.get('first_token_timeout_ms'))},
    {sql_quote(c.get('concurrency_limit'))},
    {sql_quote(bool(c.get('upstream_bills_on_disconnect', False)))},
    {sticky_enabled_sql},
    {sticky_ttl_sql}
FROM providers p
WHERE p.slug = {sql_quote(provider)}
ON CONFLICT (provider_id, name) DO UPDATE SET
    protocol = EXCLUDED.protocol,
    adapter_key = EXCLUDED.adapter_key,
    credential = EXCLUDED.credential,
    status = EXCLUDED.status,
    priority = EXCLUDED.priority,
	response_timeout_ms = EXCLUDED.response_timeout_ms,
	first_token_timeout_ms = EXCLUDED.first_token_timeout_ms,
    concurrency_limit = EXCLUDED.concurrency_limit,
    upstream_bills_on_disconnect = EXCLUDED.upstream_bills_on_disconnect,
    sticky_enabled = {sticky_enabled_update},
    sticky_ttl_ms = {sticky_ttl_update},
    config_revision = channels.config_revision + CASE WHEN ROW(
        channels.protocol, channels.adapter_key, channels.credential,
		channels.status, channels.priority,
		channels.response_timeout_ms, channels.first_token_timeout_ms,
        channels.sticky_enabled, channels.sticky_ttl_ms
    ) IS DISTINCT FROM ROW(
        EXCLUDED.protocol, EXCLUDED.adapter_key, EXCLUDED.credential,
		EXCLUDED.status, EXCLUDED.priority,
		EXCLUDED.response_timeout_ms, EXCLUDED.first_token_timeout_ms,
        {sticky_enabled_update}, {sticky_ttl_update}
    ) THEN 1 ELSE 0 END,
	capacity_revision = channels.capacity_revision + CASE WHEN
		channels.concurrency_limit IS DISTINCT FROM EXCLUDED.concurrency_limit
	THEN 1 ELSE 0 END,
    updated_at = now();
""".strip()
        )

        # 渠道默认价格倍率（model_id NULL）；无当前启用窗口则插入
        mult = c.get("cost_multiplier")
        if mult is not None and str(mult) != "":
            parts.append(
                f"""
INSERT INTO channel_cost_multipliers (channel_id, model_id, multiplier, status, effective_from)
SELECT ch.id, NULL, {sql_quote(str(mult))}::numeric, 'enabled', now()
FROM channels ch
JOIN providers p ON p.id = ch.provider_id
WHERE p.slug = {sql_quote(provider)} AND ch.name = {sql_quote(c['name'])}
  AND NOT EXISTS (
      SELECT 1 FROM channel_cost_multipliers ccm
      WHERE ccm.channel_id = ch.id
        AND ccm.model_id IS NULL
        AND ccm.status = 'enabled'
        AND (ccm.effective_to IS NULL OR ccm.effective_to > now())
  );
""".strip()
            )

        factor = c.get("recharge_factor")
        if factor is not None and str(factor) != "":
            parts.append(
                f"""
INSERT INTO channel_recharge_factors (channel_id, factor, status, effective_from)
SELECT ch.id, {sql_quote(str(factor))}::numeric, 'enabled', now()
FROM channels ch
JOIN providers p ON p.id = ch.provider_id
WHERE p.slug = {sql_quote(provider)} AND ch.name = {sql_quote(c['name'])}
  AND NOT EXISTS (
      SELECT 1 FROM channel_recharge_factors crf
      WHERE crf.channel_id = ch.id
        AND crf.status = 'enabled'
        AND (crf.effective_to IS NULL OR crf.effective_to > now())
  );
""".strip()
            )

    # channel_models
    for cm in cfg.get("channel_models") or []:
        parts.append(
            f"""
INSERT INTO channel_models (channel_id, model_id, upstream_model, status)
SELECT ch.id, m.id, {sql_quote(cm.get('upstream_model', cm['model']))}, {sql_quote(cm.get('status', 'enabled'))}
FROM channels ch
JOIN models m ON m.model_id = {sql_quote(cm['model'])}
WHERE ch.name = {sql_quote(cm['channel'])}
ON CONFLICT (channel_id, model_id) DO UPDATE SET
    upstream_model = EXCLUDED.upstream_model,
    status = EXCLUDED.status,
    updated_at = now();
""".strip()
        )

    # routes + route_channels
    for r in cfg.get("routes") or []:
        concurrency_configured = "concurrency_limit" in r
        concurrency_update = (
            "EXCLUDED.concurrency_limit"
            if concurrency_configured
            else "routes.concurrency_limit"
        )
        parts.append(
            f"""
INSERT INTO routes (
    name, mode, status, description, price_ratio,
    rpm_limit, rpd_limit, concurrency_limit
) VALUES (
    {sql_quote(r['name'])},
    {sql_quote(r.get('mode', 'balanced'))},
    {sql_quote(r.get('status', 'enabled'))},
    {sql_quote(r.get('description') or None)},
    {sql_quote(str(r.get('price_ratio', '1.0')))}::numeric,
    {sql_quote(r.get('rpm_limit'))},
    {sql_quote(r.get('rpd_limit'))},
    {sql_quote(r.get('concurrency_limit'))}
)
ON CONFLICT (name) DO UPDATE SET
    mode = EXCLUDED.mode,
    status = EXCLUDED.status,
    description = EXCLUDED.description,
    price_ratio = EXCLUDED.price_ratio,
    rpm_limit = EXCLUDED.rpm_limit,
    rpd_limit = EXCLUDED.rpd_limit,
    concurrency_limit = {concurrency_update},
    updated_at = now();
""".strip()
        )
        channel_names = r.get("channels") or []
        if channel_names:
            values = ", ".join(sql_quote(n) for n in channel_names)
            parts.append(
                f"""
INSERT INTO route_channels (route_id, channel_id)
SELECT rt.id, ch.id
FROM routes rt
JOIN channels ch ON ch.name IN ({values})
WHERE rt.name = {sql_quote(r['name'])}
ON CONFLICT (route_id, channel_id) DO NOTHING;

DELETE FROM route_channels rc
USING routes rt
WHERE rc.route_id = rt.id
  AND rt.name = {sql_quote(r['name'])}
  AND rc.channel_id NOT IN (
      SELECT ch.id FROM channels ch WHERE ch.name IN ({values})
  );
""".strip()
            )

    # model_capabilities（按 model_id + capability_key 幂等）
    for cap in cfg.get("model_capabilities") or []:
        limits = cap.get("limits")
        if limits is None:
            limits_sql = "NULL"
        else:
            limits_sql = sql_quote(json.dumps(limits, ensure_ascii=False)) + "::jsonb"
        parts.append(
            f"""
INSERT INTO model_capabilities (model_id, capability_key, support_level, limits, updated_by, updated_at)
SELECT m.id, {sql_quote(cap['capability_key'])}, {sql_quote(cap.get('support_level', 'full'))},
       {limits_sql}, 'init_test_db', now()
FROM models m
WHERE m.model_id = {sql_quote(cap['model'])}
ON CONFLICT (model_id, capability_key) DO UPDATE SET
    support_level = EXCLUDED.support_level,
    limits = EXCLUDED.limits,
    updated_by = EXCLUDED.updated_by,
    updated_at = now();
""".strip()
        )

    parts.append("")
    parts.append("COMMIT;")
    return "\n\n".join(parts) + "\n"


# ---------------------------------------------------------------------------
# export
# ---------------------------------------------------------------------------

EXPORT_SQL = r"""
SELECT json_build_object(
  'providers', (
      SELECT coalesce(json_agg(json_build_object(
          'slug', slug, 'name', name, 'origin', origin, 'status', status
      ) ORDER BY id), '[]'::json)
      FROM providers WHERE archived_at IS NULL
  ),
  'channels', (
      SELECT coalesce(json_agg(json_build_object(
          'key', c.name,
          'provider', p.slug,
          'name', c.name,
          'protocol', c.protocol,
          'adapter_key', c.adapter_key,
          'credential', c.credential,
          'status', c.status,
          'priority', c.priority,
		  'response_timeout_ms', c.response_timeout_ms,
		  'first_token_timeout_ms', c.first_token_timeout_ms,
          'concurrency_limit', c.concurrency_limit,
          'sticky_enabled', c.sticky_enabled,
          'sticky_ttl_ms', c.sticky_ttl_ms,
          'upstream_bills_on_disconnect', c.upstream_bills_on_disconnect,
          'cost_multiplier', (
              SELECT ccm.multiplier::text
              FROM channel_cost_multipliers ccm
              WHERE ccm.channel_id = c.id AND ccm.model_id IS NULL AND ccm.status = 'enabled'
                AND (ccm.effective_to IS NULL OR ccm.effective_to > now())
              ORDER BY ccm.effective_from DESC, ccm.id DESC
              LIMIT 1
          ),
          'recharge_factor', (
              SELECT crf.factor::text
              FROM channel_recharge_factors crf
              WHERE crf.channel_id = c.id AND crf.status = 'enabled'
                AND (crf.effective_to IS NULL OR crf.effective_to > now())
              ORDER BY crf.effective_from DESC, crf.id DESC
              LIMIT 1
          )
      ) ORDER BY c.id), '[]'::json)
      FROM channels c
      JOIN providers p ON p.id = c.provider_id
      WHERE c.archived_at IS NULL
  ),
  'models', (
      SELECT coalesce(json_agg(json_build_object(
          'model_id', m.model_id,
          'display_name', m.display_name,
          'owned_by', m.owned_by,
          'status', m.status,
          'context_window_tokens', m.context_window_tokens,
          'max_output_tokens', m.max_output_tokens,
          'input_price_usd_per_million_tokens', coalesce(mp.uncached_input_price::text, m.input_price_usd_per_million_tokens::text),
          'output_price_usd_per_million_tokens', coalesce(mp.output_price::text, m.output_price_usd_per_million_tokens::text),
          'cache_read_price_usd_per_million_tokens', mp.cache_read_input_price::text,
          'cache_write_5m_price_usd_per_million_tokens', mp.cache_write_5m_input_price::text,
          'cache_write_1h_price_usd_per_million_tokens', mp.cache_write_1h_input_price::text,
          'cache_write_30m_price_usd_per_million_tokens', mp.cache_write_30m_input_price::text,
          'reasoning_output_price_usd_per_million_tokens', mp.reasoning_output_price::text,
          'long_context_enabled', coalesce(mp.long_context_enabled, false),
          'long_context_threshold', mp.long_context_threshold,
          'long_context_input_multiplier', mp.long_context_input_multiplier::text,
          'long_context_output_multiplier', mp.long_context_output_multiplier::text,
          'release_date', m.release_date::text,
          'source', m.source
      ) ORDER BY m.id), '[]'::json)
      FROM models m
      LEFT JOIN LATERAL (
          SELECT *
          FROM model_prices mp0
          WHERE mp0.model_id = m.id AND mp0.status = 'enabled'
            AND (mp0.effective_to IS NULL OR mp0.effective_to > now())
          ORDER BY mp0.effective_from DESC, mp0.id DESC
          LIMIT 1
      ) mp ON TRUE
  ),
  'channel_models', (
      SELECT coalesce(json_agg(json_build_object(
          'channel', c.name,
          'model', m.model_id,
          'upstream_model', cm.upstream_model,
          'status', cm.status
      ) ORDER BY c.id, m.id), '[]'::json)
      FROM channel_models cm
      JOIN channels c ON c.id = cm.channel_id
      JOIN models m ON m.id = cm.model_id
  ),
  'routes', (
      SELECT coalesce(json_agg(json_build_object(
          'name', r.name,
          'mode', r.mode,
          'status', r.status,
          'description', coalesce(r.description, ''),
          'price_ratio', r.price_ratio::text,
          'rpm_limit', r.rpm_limit,
          'rpd_limit', r.rpd_limit,
          'concurrency_limit', r.concurrency_limit,
          'channels', (
              SELECT coalesce(json_agg(c2.name ORDER BY c2.id), '[]'::json)
              FROM route_channels rc
              JOIN channels c2 ON c2.id = rc.channel_id
              WHERE rc.route_id = r.id
          )
      ) ORDER BY r.id), '[]'::json)
      FROM routes r
      WHERE r.archived_at IS NULL
  ),
  'model_capabilities', (
      SELECT coalesce(json_agg(json_build_object(
          'model', m.model_id,
          'capability_key', mc.capability_key,
          'support_level', mc.support_level,
          'limits', mc.limits
      ) ORDER BY m.model_id, mc.capability_key), '[]'::json)
      FROM model_capabilities mc
      JOIN models m ON m.id = mc.model_id
  )
);
"""


def redact_credential(cred: str) -> str:
    if not cred:
        return "REPLACE_ME"
    prefix = cred[:7] if len(cred) >= 7 else cred
    return f"{prefix}…REPLACE_ME"


def cmd_export(args: argparse.Namespace) -> None:
    database_url = os.environ.get("DATABASE_URL", "").strip()
    if database_url:
        cmd = ["psql", database_url, "-t", "-A", "-c", EXPORT_SQL]
    else:
        cmd = [
            "docker",
            "exec",
            "-i",
            DEFAULT_CONTAINER,
            "psql",
            "-U",
            "unio",
            "-d",
            "unio",
            "-t",
            "-A",
            "-c",
            EXPORT_SQL,
        ]
    proc = subprocess.run(cmd, capture_output=True, text=True)
    if proc.returncode != 0:
        sys.stderr.write(proc.stderr or proc.stdout or "")
        raise SystemExit("export query failed")
    raw = proc.stdout.strip()
    if not raw:
        raise SystemExit("export returned empty result")
    data = json.loads(raw)

    base = {
        "test_user": dict(DEFAULT_TEST_USER),
        "providers": data["providers"],
        "channels": data["channels"],
        "models": data["models"],
        "model_capabilities": data.get("model_capabilities") or [],
        "channel_models": data["channel_models"],
        "routes": data["routes"],
    }

    TEST_DB_DIR.mkdir(parents=True, exist_ok=True)

    example = json.loads(json.dumps(base))
    example["_comment"] = (
        "复制为 config.local.json 并填入真实 credential。"
        "config.local.json 已 gitignore，勿提交。"
        "init 只能在 Gateway、Admin、Worker 停止时执行。"
    )
    for ch in example["channels"]:
        ch["credential"] = redact_credential(ch.get("credential") or "")
    EXAMPLE_CONFIG.write_text(
        json.dumps(example, ensure_ascii=False, indent=2) + "\n", encoding="utf-8"
    )
    print(f"wrote {EXAMPLE_CONFIG.relative_to(ROOT)}")

    if args.with_secrets:
        DEFAULT_CONFIG.write_text(
            json.dumps(base, ensure_ascii=False, indent=2) + "\n", encoding="utf-8"
        )
        print(f"wrote {DEFAULT_CONFIG.relative_to(ROOT)} (gitignored)")
    else:
        print("tip: 加 --with-secrets 同时写出 config.local.json（含真实 credential）")


def cmd_init(args: argparse.Namespace) -> None:
    if not args.confirm_offline:
        raise SystemExit(
            "init 会直接写 PostgreSQL。请先停止 Gateway、Admin、Worker，"
            "确认后追加 --confirm-offline；在线变更请使用 Admin API。"
        )
    config_path = Path(args.config)
    if not config_path.is_file():
        raise SystemExit(
            f"缺少配置 {config_path}。\n"
            f"请先：cp {EXAMPLE_CONFIG} {DEFAULT_CONFIG} 并填入 credential，\n"
            f"或：python3 scripts/test/init_test_db.py export --with-secrets"
        )
    cfg = json.loads(config_path.read_text(encoding="utf-8"))
    # 兼容旧配置里残留的 sql_seeds（已内建，忽略）
    if cfg.get("sql_seeds"):
        print("note: sql_seeds 已废弃（逻辑已并入 init_test_db.py），忽略", cfg["sql_seeds"])

    caps = len(cfg.get("model_capabilities") or [])
    tu = cfg.get("test_user", DEFAULT_TEST_USER)
    tu_label = "skip" if tu is None else tu.get("email", DEFAULT_TEST_USER["email"])
    print(f">> init {config_path} (test_user={tu_label}, model_capabilities={caps})")
    run_psql(build_topology_sql(cfg), quiet=True)
    print("done.")


def main() -> None:
    parser = argparse.ArgumentParser(description="初始化测试数据库拓扑")
    sub = parser.add_subparsers(dest="cmd", required=True)

    p_export = sub.add_parser("export", help="从当前库导出配置 JSON")
    p_export.add_argument(
        "--with-secrets",
        action="store_true",
        help="同时写出 config.local.json（含真实 credential，gitignore）",
    )
    p_export.set_defaults(func=cmd_export)

    p_init = sub.add_parser("init", help="按配置一键初始化")
    p_init.add_argument(
        "--config",
        default=str(DEFAULT_CONFIG),
        help=f"配置路径（默认 {DEFAULT_CONFIG}）",
    )
    p_init.add_argument(
        "--confirm-offline",
        action="store_true",
        help="确认 Gateway、Admin、Worker 已停止；初始化会直接写 PostgreSQL",
    )
    p_init.set_defaults(func=cmd_init)

    args = parser.parse_args()
    args.func(args)


if __name__ == "__main__":
    main()
