std = "lua51"

globals = {
  "redis",
  "cjson",
  "KEYS",
  "ARGV",
}

-- Manifest helpers expose a shared local API; an assembled script may use only
-- part of that API. Keep other unused locals visible to luacheck.
ignore = {
  "211/active_zset_count",
  "211/now_ms",
  "211/parse_channel_admission_payload",
  "211/parse_global_concurrency_payload",
  "211/parse_rate_limit_defaults_payload",
  "211/parse_routing_balance_payload",
  "211/read_committed_control",
  "211/read_new_admission_control",
  "211/read_nonnegative_counter",
  "211/read_op",
  "211/redis_instance_proof_matches",
  "211/reset_origin",
  "211/resolve_channel_limit",
  "211/resolve_request_limit_override",
  "211/restore_origin",
  "211/valid_status",
  "211/write_prepared_op",
  "211/write_terminal_op",
}
