---@diagnostic disable: unused-function, unused-local

local function key_type(key)
  local t = redis.call('TYPE', key)
  if type(t) == 'table' then return t.ok end
  return t
end

local function now_ms()
  local t = redis.call('TIME')
  return tonumber(t[1]) * 1000 + math.floor(tonumber(t[2]) / 1000)
end

local function valid_status(value) return value == 'enabled' or value == 'disabled' or value == 'archived' end

local function read_op(op, token, payload_hash, kind, provider_id, target_count)
  local typ = key_type(op)
  if typ == 'none' then return 'none' end
  if typ ~= 'hash' then return 'invalid' end
  if
    redis.call('HGET', op, 'token') ~= token
    or redis.call('HGET', op, 'payload_hash') ~= payload_hash
    or redis.call('HGET', op, 'kind') ~= kind
  then
    return 'conflict'
  end
  if provider_id ~= '' and redis.call('HGET', op, 'provider_id') ~= provider_id then return 'conflict' end
  if target_count ~= '' and redis.call('HGET', op, 'target_count') ~= target_count then return 'conflict' end
  local state = redis.call('HGET', op, 'state')
  if state ~= 'prepared' and state ~= 'committed' and state ~= 'aborted' then return 'invalid' end
  return state
end

local function write_prepared_op(op, token, payload_hash, kind, provider_id, target_count)
  redis.call(
    'HSET',
    op,
    'token',
    token,
    'payload_hash',
    payload_hash,
    'kind',
    kind,
    'provider_id',
    provider_id,
    'target_count',
    target_count,
    'state',
    'prepared'
  )
  redis.call('PERSIST', op)
end

local function write_terminal_op(op, token, payload_hash, kind, provider_id, target_count, state, ttl_ms)
  redis.call(
    'HSET',
    op,
    'token',
    token,
    'payload_hash',
    payload_hash,
    'kind',
    kind,
    'provider_id',
    provider_id,
    'target_count',
    target_count,
    'state',
    state
  )
  redis.call('PEXPIRE', op, ttl_ms)
end

local function reset_origin(ep, now)
  local gen = (tonumber(redis.call('HGET', ep, 'state_generation')) or 0) + 1
  redis.call(
    'HSET',
    ep,
    'state',
    'closed',
    'state_generation',
    gen,
    'window_started_at_ms',
    now,
    'eligible_successes',
    '0',
    'eligible_failures',
    '0',
    'consecutive_eligible_failures',
    '0',
    'open_level',
    '0',
    'half_open_successes',
    '0',
    'last_transition_at_ms',
    now
  )
  redis.call(
    'HDEL',
    ep,
    'half_open_permit_id',
    'half_open_lease_until_ms',
    'open_until_ms',
    'last_failure_at_ms',
    'last_failure_category'
  )
end

local function restore_origin(ep, base_rev, status_rev, effective_status, now)
  redis.call(
    'HSET',
    ep,
    'control_present',
    '1',
    'effective_status',
    effective_status,
    'origin_revision',
    base_rev,
    'status_revision',
    status_rev,
    'origin_fence_generation',
    '1',
    'status_fence_generation',
    '1',
    'origin_revision_state',
    'active',
    'status_revision_state',
    'active',
    'state',
    'closed',
    'state_generation',
    '1',
    'window_started_at_ms',
    now,
    'eligible_successes',
    '0',
    'eligible_failures',
    '0',
    'consecutive_eligible_failures',
    '0',
    'open_level',
    '0',
    'half_open_successes',
    '0',
    'last_transition_at_ms',
    now
  )
end
