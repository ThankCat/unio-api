package breakerstore

// luaAuthoritativeControlHelpers is prepended to admission/acquire/finish scripts. Runtime-control
// payloads are execution authority, so Lua must decode the committed payload itself instead of
// trusting caller-supplied effective values. Every parser is exact: missing/unknown fields, wrong
// JSON types, fractional/negative limits, and invalid breaker invariants are rejected.
const luaAuthoritativeControlHelpers = `
local MAX_EXACT_INTEGER = 9007199254740991

local function redis_key_type(key)
  local reply = redis.call('TYPE', key)
  if type(reply) == 'table' then return reply['ok'] end
  return reply
end

local function exact_object(payload, allowed)
  if type(payload) ~= 'string' or payload == '' then return nil end
  local ok, value = pcall(cjson.decode, payload)
  if not ok or type(value) ~= 'table' then return nil end
  for key, _ in pairs(value) do
    if type(key) ~= 'string' or allowed[key] ~= true then return nil end
  end
  for key, _ in pairs(allowed) do
    if value[key] == nil then return nil end
  end
  return value
end

local function nonnegative_integer(value)
  if type(value) ~= 'number' or value < 0 or value > MAX_EXACT_INTEGER or value ~= math.floor(value) then
    return nil
  end
  return value
end

local function positive_integer(value)
  local parsed = nonnegative_integer(value)
  if parsed == nil or parsed == 0 then return nil end
  return parsed
end

local function positive_revision(raw)
  if type(raw) ~= 'string' or string.match(raw, '^%d+$') == nil then return nil end
  local value = tonumber(raw)
  if value == nil or value < 1 or value > MAX_EXACT_INTEGER or value ~= math.floor(value) then return nil end
  return value
end

local function optional_pending_revision(raw)
  if raw == false or raw == nil then return 0 end
  if type(raw) ~= 'string' or string.match(raw, '^%d+$') == nil then return nil end
  local value = tonumber(raw)
  if value == nil or value < 0 or value > MAX_EXACT_INTEGER or value ~= math.floor(value) then return nil end
  return value
end

local function valid_payload_hash(value)
  return type(value) == 'string' and #value == 64 and string.match(value, '[^0-9a-f]') == nil
end

local function parse_rate_limit_defaults_payload(payload)
  local value = exact_object(payload, {rpm=true, tpm=true, rpd=true})
  if value == nil then return nil end
  value.rpm = nonnegative_integer(value.rpm)
  value.tpm = nonnegative_integer(value.tpm)
  value.rpd = nonnegative_integer(value.rpd)
  if value.rpm == nil or value.tpm == nil or value.rpd == nil then return nil end
  return value
end

local function parse_global_concurrency_payload(payload)
  local value = exact_object(payload, {key_limit=true, channel_limit=true})
  if value == nil then return nil end
  value.key_limit = nonnegative_integer(value.key_limit)
  value.channel_limit = nonnegative_integer(value.channel_limit)
  if value.key_limit == nil or value.channel_limit == nil then return nil end
  return value
end

local function nullable_nonnegative_integer(value)
  if value == cjson.null then return cjson.null end
  return nonnegative_integer(value)
end

local function parse_channel_admission_payload(payload)
  local value = exact_object(payload, {rpm=true, rpd=true, tpm=true, concurrency=true})
  if value == nil then return nil end
  value.rpm = nullable_nonnegative_integer(value.rpm)
  value.rpd = nullable_nonnegative_integer(value.rpd)
  value.tpm = nullable_nonnegative_integer(value.tpm)
  value.concurrency = nullable_nonnegative_integer(value.concurrency)
  if value.rpm == nil or value.rpd == nil or value.tpm == nil or value.concurrency == nil then return nil end
  return value
end

local function positive_nondecreasing_integer_array(value)
  if type(value) ~= 'table' then return nil end
  local count = 0
  for key, item in pairs(value) do
    if type(key) ~= 'number' or key < 1 or key ~= math.floor(key) then return nil end
    if positive_integer(item) == nil then return nil end
    count = count + 1
  end
  if count == 0 then return nil end
  local previous = 0
  for index = 1, count do
    local item = value[index]
    if positive_integer(item) == nil or item < previous then return nil end
    previous = item
  end
  return value
end

local function parse_circuit_breaker_payload(payload)
  local value = exact_object(payload, {
    enabled=true,
    window_ms=true,
    min_requests=true,
    failure_ratio=true,
    consecutive_failures=true,
    consecutive_window_ms=true,
    half_open_successes=true,
    attempt_permit_ttl_ms=true,
    attempt_permit_renew_interval_ms=true,
    attempt_permit_terminal_ttl_ms=true,
    origin_base_url_revision_operation_ttl_ms=true,
    origin_status_revision_operation_ttl_ms=true,
    origin_status_batch_max=true,
    open_durations_ms=true,
    origin_ambiguous_distinct_channels=true,
    origin_ambiguous_distinct_models=true
  })
  if value == nil or type(value.enabled) ~= 'boolean' then return nil end

  value.window_ms = positive_integer(value.window_ms)
  value.min_requests = positive_integer(value.min_requests)
  if value.min_requests == nil or value.min_requests < 2 then return nil end
  if type(value.failure_ratio) ~= 'number' or value.failure_ratio <= 0 or value.failure_ratio > 1 then return nil end
  value.consecutive_failures = positive_integer(value.consecutive_failures)
  value.consecutive_window_ms = positive_integer(value.consecutive_window_ms)
  value.half_open_successes = positive_integer(value.half_open_successes)
  if value.half_open_successes == nil or value.half_open_successes < 2 then return nil end
  value.attempt_permit_ttl_ms = positive_integer(value.attempt_permit_ttl_ms)
  value.attempt_permit_renew_interval_ms = positive_integer(value.attempt_permit_renew_interval_ms)
  value.attempt_permit_terminal_ttl_ms = positive_integer(value.attempt_permit_terminal_ttl_ms)
  value.origin_base_url_revision_operation_ttl_ms = positive_integer(value.origin_base_url_revision_operation_ttl_ms)
  value.origin_status_revision_operation_ttl_ms = positive_integer(value.origin_status_revision_operation_ttl_ms)
  value.origin_status_batch_max = positive_integer(value.origin_status_batch_max)
  value.origin_ambiguous_distinct_channels = positive_integer(value.origin_ambiguous_distinct_channels)
  value.origin_ambiguous_distinct_models = positive_integer(value.origin_ambiguous_distinct_models)
  value.open_durations_ms = positive_nondecreasing_integer_array(value.open_durations_ms)

  if value.window_ms == nil or value.consecutive_failures == nil or value.consecutive_window_ms == nil or
      value.attempt_permit_ttl_ms == nil or value.attempt_permit_renew_interval_ms == nil or
      value.attempt_permit_terminal_ttl_ms == nil or value.origin_base_url_revision_operation_ttl_ms == nil or
      value.origin_status_revision_operation_ttl_ms == nil or value.origin_status_batch_max == nil or
      value.open_durations_ms == nil or value.origin_ambiguous_distinct_channels == nil or
      value.origin_ambiguous_distinct_models == nil then
    return nil
  end
  if value.attempt_permit_renew_interval_ms * 3 > value.attempt_permit_ttl_ms then return nil end
  if value.attempt_permit_terminal_ttl_ms < value.attempt_permit_ttl_ms then return nil end
  if value.attempt_permit_ttl_ms > MAX_EXACT_INTEGER - value.attempt_permit_terminal_ttl_ms - 120000 then return nil end
  if value.origin_status_batch_max > 1024 then return nil end
  if value.origin_ambiguous_distinct_channels < 2 or value.origin_ambiguous_distinct_models < 2 then return nil end
  return value
end

local function parse_routing_balance_payload(payload)
  local value = exact_object(payload, {
    ttft_target_ms=true,
    ttft_weight=true,
    cost_weight=true,
    minimum_routing_factor=true,
    ttft_ewma_alpha=true
  })
  if value == nil then
    value = exact_object(payload, {
      ttft_target_ms=true,
      ttft_weight=true,
      minimum_routing_factor=true,
      ttft_ewma_alpha=true
    })
    if value == nil then return nil end
    -- Legacy four-field payloads remain revision-stable and cost-neutral during upgrade.
    value.cost_weight = 0
  end
  value.ttft_target_ms = positive_integer(value.ttft_target_ms)
  if value.ttft_target_ms == nil then return nil end
  if type(value.ttft_weight) ~= 'number' or value.ttft_weight ~= value.ttft_weight or
      value.ttft_weight < 0 or value.ttft_weight > 1 then return nil end
  if type(value.cost_weight) ~= 'number' or value.cost_weight ~= value.cost_weight or
      value.cost_weight < 0 or value.cost_weight > 1 then return nil end
  if type(value.minimum_routing_factor) ~= 'number' or
      value.minimum_routing_factor ~= value.minimum_routing_factor or
      value.minimum_routing_factor <= 0 or value.minimum_routing_factor > 1 then return nil end
  if type(value.ttft_ewma_alpha) ~= 'number' or
      value.ttft_ewma_alpha ~= value.ttft_ewma_alpha or
      value.ttft_ewma_alpha <= 0 or value.ttft_ewma_alpha > 1 then return nil end
  return value
end

-- read_new_admission_control validates a control used by a new Acquire. Pending blocks only new
-- acquisitions; idempotent recovery is handled by each script before calling this helper.
local function read_new_admission_control(control, expected_revision, parser)
  if redis_key_type(control) ~= 'hash' then return nil, 'runtime_sync_required' end
  local pending = optional_pending_revision(redis.call('HGET', control, 'pending_revision'))
  if pending == nil then return nil, 'runtime_sync_required' end
  if pending ~= 0 then return nil, 'runtime_sync_pending' end
  local active = positive_revision(redis.call('HGET', control, 'active_revision'))
  if active == nil then return nil, 'runtime_sync_required' end
  if active ~= expected_revision then return nil, 'stale_setting_revision' end
  local payload_hash = redis.call('HGET', control, 'active_payload_hash')
  if not valid_payload_hash(payload_hash) then return nil, 'runtime_sync_required' end
  local payload = redis.call('HGET', control, 'active_payload')
  if payload == false then return nil, 'runtime_sync_required' end
  local parsed = parser(payload)
  if parsed == nil then return nil, 'runtime_sync_required' end
  return parsed, 'active'
end

-- Finish must always release permit-owned resources. It uses the latest committed active breaker
-- payload even while a newer revision is pending; absent/malformed active data yields a neutral
-- runtime_sync_required breaker disposition after resources are released.
local function read_committed_control(control, parser)
  if redis_key_type(control) ~= 'hash' then return nil end
  if positive_revision(redis.call('HGET', control, 'active_revision')) == nil then return nil end
  if not valid_payload_hash(redis.call('HGET', control, 'active_payload_hash')) then return nil end
  local payload = redis.call('HGET', control, 'active_payload')
  if payload == false then return nil end
  return parser(payload)
end

local function resolve_channel_limit(value, inherited)
  if value == cjson.null then return inherited end
  return value
end

-- Trusted authentication snapshots use the same nullable override semantics as persisted limits.
-- "inherit" is the only inheritance sentinel; numeric values must be exact non-negative integers.
local function resolve_request_limit_override(raw, inherited)
  if raw == 'inherit' then return inherited end
  if type(raw) ~= 'string' or string.match(raw, '^%d+$') == nil then return nil end
  local value = tonumber(raw)
  if value == nil or value < 0 or value > MAX_EXACT_INTEGER or value ~= math.floor(value) then return nil end
  return value
end

local function read_nonnegative_counter(key)
  local kind = redis_key_type(key)
  if kind == 'none' then return 0 end
  if kind ~= 'string' then return nil end
  local raw = redis.call('GET', key)
  if raw == false or string.match(raw, '^%d+$') == nil then return nil end
  local value = tonumber(raw)
  if value == nil or value < 0 or value > MAX_EXACT_INTEGER or value ~= math.floor(value) then return nil end
  return value
end

local function active_zset_count(key, now)
  local kind = redis_key_type(key)
  if kind == 'none' then return 0 end
  if kind ~= 'zset' then return nil end
  return tonumber(redis.call('ZCOUNT', key, '(' .. now, '+inf'))
end
`
