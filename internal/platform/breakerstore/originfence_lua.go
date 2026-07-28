package breakerstore

// Provider revision fences use a Redis-side operation record in addition to the Provider control.
// The operation record makes prepare/commit/abort first-terminal-wins even after a response is lost.
// Non-terminal records intentionally have no TTL; terminal records are retained for the caller supplied
// bounded retention period. Every script validates all keys before entering its write phase.
const luaOriginFenceHelpers = `
local function key_type(key)
  local t = redis.call('TYPE', key)
  if type(t) == 'table' then return t.ok end
  return t
end

local function now_ms()
  local t = redis.call('TIME')
  return tonumber(t[1]) * 1000 + math.floor(tonumber(t[2]) / 1000)
end

local function valid_status(value)
  return value == 'enabled' or value == 'disabled' or value == 'archived'
end

local function read_op(op, token, payload_hash, kind, provider_id, target_count)
  local typ = key_type(op)
  if typ == 'none' then return 'none' end
  if typ ~= 'hash' then return 'invalid' end
  if redis.call('HGET', op, 'token') ~= token or
     redis.call('HGET', op, 'payload_hash') ~= payload_hash or
     redis.call('HGET', op, 'kind') ~= kind then
    return 'conflict'
  end
  if provider_id ~= '' and redis.call('HGET', op, 'provider_id') ~= provider_id then
    return 'conflict'
  end
  if target_count ~= '' and redis.call('HGET', op, 'target_count') ~= target_count then
    return 'conflict'
  end
  local state = redis.call('HGET', op, 'state')
  if state ~= 'prepared' and state ~= 'committed' and state ~= 'aborted' then return 'invalid' end
  return state
end

local function write_prepared_op(op, token, payload_hash, kind, provider_id, target_count)
  redis.call('HSET', op,
    'token', token, 'payload_hash', payload_hash, 'kind', kind,
    'provider_id', provider_id, 'target_count', target_count, 'state', 'prepared')
  redis.call('PERSIST', op)
end

local function write_terminal_op(op, token, payload_hash, kind, provider_id, target_count, state, ttl_ms)
  redis.call('HSET', op,
    'token', token, 'payload_hash', payload_hash, 'kind', kind,
    'provider_id', provider_id, 'target_count', target_count, 'state', state)
  redis.call('PEXPIRE', op, ttl_ms)
end

local function reset_origin(ep, now)
  local gen = (tonumber(redis.call('HGET', ep, 'state_generation')) or 0) + 1
  redis.call('HSET', ep,
    'state', 'closed', 'state_generation', gen, 'window_started_at_ms', now,
    'eligible_successes', '0', 'eligible_failures', '0',
    'consecutive_eligible_failures', '0', 'open_level', '0',
    'half_open_successes', '0', 'last_transition_at_ms', now)
  redis.call('HDEL', ep,
    'half_open_permit_id', 'half_open_lease_until_ms', 'open_until_ms',
    'last_failure_at_ms', 'last_failure_category')
end

local function restore_origin(ep, base_rev, status_rev, effective_status, now)
  redis.call('HSET', ep,
    'control_present', '1', 'effective_status', effective_status,
    'origin_revision', base_rev, 'status_revision', status_rev,
    'origin_fence_generation', '1', 'status_fence_generation', '1',
    'origin_revision_state', 'active', 'status_revision_state', 'active',
    'state', 'closed', 'state_generation', '1', 'window_started_at_ms', now,
    'eligible_successes', '0', 'eligible_failures', '0',
    'consecutive_eligible_failures', '0', 'open_level', '0',
    'half_open_successes', '0', 'last_transition_at_ms', now)
end
`

// KEYS[1]=provider. ARGV: origin_revision, status_revision, effective_status.
const luaInitProviderControl = luaOriginFenceHelpers + `
local ep = KEYS[1]
if key_type(ep) ~= 'none' and key_type(ep) ~= 'hash' then return redis.error_reply('invalid provider key type') end
if redis.call('HGET', ep, 'control_present') == '1' then return {'exists'} end
if tonumber(ARGV[1]) == nil or tonumber(ARGV[1]) < 1 or
   tonumber(ARGV[2]) == nil or tonumber(ARGV[2]) < 1 or not valid_status(ARGV[3]) then
  return redis.error_reply('invalid provider control')
end
restore_origin(ep, ARGV[1], ARGV[2], ARGV[3], now_ms())
return {'created'}
`

// Recovery-only absent control restore. Existing controls are never overwritten.
const luaRestoreMissingProviderControl = luaOriginFenceHelpers + `
local ep = KEYS[1]
if key_type(ep) ~= 'none' and key_type(ep) ~= 'hash' then return redis.error_reply('invalid provider key type') end
if redis.call('HGET', ep, 'control_present') == '1' then return {'exists'} end
if tonumber(ARGV[1]) == nil or tonumber(ARGV[1]) < 1 or
   tonumber(ARGV[2]) == nil or tonumber(ARGV[2]) < 1 or not valid_status(ARGV[3]) then
  return redis.error_reply('invalid provider control')
end
restore_origin(ep, ARGV[1], ARGV[2], ARGV[3], now_ms())
return {'installed'}
`

// Startup-only authoritative reconciliation. It replaces only Provider routing control fields,
// clears stale pending fences, and preserves the breaker window/state stored in the same hash.
const luaReconcileProviderControl = luaOriginFenceHelpers + `
local ep = KEYS[1]
local typ = key_type(ep)
if tonumber(ARGV[1]) == nil or tonumber(ARGV[1]) < 1 or
   tonumber(ARGV[2]) == nil or tonumber(ARGV[2]) < 1 or not valid_status(ARGV[3]) then
  return redis.error_reply('invalid provider control')
end
local unchanged = typ == 'hash'
    and redis.call('HGET', ep, 'control_present') == '1'
    and redis.call('HGET', ep, 'origin_revision') == ARGV[1]
    and redis.call('HGET', ep, 'status_revision') == ARGV[2]
    and redis.call('HGET', ep, 'effective_status') == ARGV[3]
    and redis.call('HGET', ep, 'origin_revision_state') == 'active'
    and redis.call('HGET', ep, 'status_revision_state') == 'active'
    and redis.call('HEXISTS', ep, 'pending_origin_revision') == 0
    and redis.call('HEXISTS', ep, 'origin_fence_token') == 0
    and redis.call('HEXISTS', ep, 'origin_payload_hash') == 0
    and redis.call('HEXISTS', ep, 'pending_status_revision') == 0
    and redis.call('HEXISTS', ep, 'pending_effective_status') == 0
    and redis.call('HEXISTS', ep, 'status_fence_token') == 0
    and redis.call('HEXISTS', ep, 'status_payload_hash') == 0
if unchanged then return {'unchanged'} end
if typ ~= 'hash' then
  redis.call('DEL', ep)
  restore_origin(ep, ARGV[1], ARGV[2], ARGV[3], now_ms())
else
  redis.call('HSET', ep,
    'control_present', '1',
    'effective_status', ARGV[3],
    'origin_revision', ARGV[1],
    'status_revision', ARGV[2],
    'origin_revision_state', 'active',
    'status_revision_state', 'active',
    'origin_fence_generation', (tonumber(redis.call('HGET', ep, 'origin_fence_generation')) or 0) + 1,
    'status_fence_generation', (tonumber(redis.call('HGET', ep, 'status_fence_generation')) or 0) + 1)
  redis.call('HDEL', ep,
    'pending_origin_revision', 'origin_fence_token', 'origin_payload_hash',
    'pending_status_revision', 'pending_effective_status', 'status_fence_token', 'status_payload_hash')
end
return {'reconciled'}
`

// Singular status prepare. KEYS: provider, op. ARGV: current, next, next_effective, token, hash.
const luaPrepareOriginStatus = luaOriginFenceHelpers + `
local ep, op = KEYS[1], KEYS[2]
local current, next_rev, next_eff, token, payload_hash = ARGV[1], ARGV[2], ARGV[3], ARGV[4], ARGV[5]
local op_state = read_op(op, token, payload_hash, 'status', '', '1')
if op_state == 'committed' or op_state == 'aborted' then return {op_state} end
if op_state == 'conflict' or op_state == 'invalid' then return {'conflict'} end
if key_type(ep) ~= 'hash' or redis.call('HGET', ep, 'control_present') ~= '1' then return {'absent'} end
if tonumber(current) == nil or tonumber(next_rev) ~= tonumber(current) + 1 or not valid_status(next_eff) then return {'invalid'} end
if op_state == 'prepared' then
  if redis.call('HGET', ep, 'status_revision_state') == 'pending' and
     redis.call('HGET', ep, 'status_fence_token') == token and
     redis.call('HGET', ep, 'status_payload_hash') == payload_hash and
     redis.call('HGET', ep, 'pending_status_revision') == next_rev and
     redis.call('HGET', ep, 'pending_effective_status') == next_eff then return {'prepared'} end
  return {'conflict'}
end
if redis.call('HGET', ep, 'origin_revision_state') ~= 'active' or
   redis.call('HGET', ep, 'status_revision_state') ~= 'active' then return {'conflict'} end
if redis.call('HGET', ep, 'status_revision') ~= current then return {'stale'} end
local gen = (tonumber(redis.call('HGET', ep, 'status_fence_generation')) or 0) + 1
redis.call('HSET', ep,
  'status_revision_state', 'pending', 'pending_status_revision', next_rev,
  'pending_effective_status', next_eff, 'status_fence_token', token,
  'status_payload_hash', payload_hash, 'status_fence_generation', gen)
write_prepared_op(op, token, payload_hash, 'status', '', '1')
return {'prepared'}
`

// Singular status commit. KEYS: provider, six evidence keys, op. ARGV: token, hash, terminal_ttl_ms.
const luaCommitOriginStatus = luaOriginFenceHelpers + `
local ep, op = KEYS[1], KEYS[#KEYS]
local token, payload_hash, ttl_ms = ARGV[1], ARGV[2], tonumber(ARGV[3])
local op_state = read_op(op, token, payload_hash, 'status', '', '1')
if op_state == 'committed' then return {'committed'} end
if op_state == 'aborted' or op_state == 'conflict' or op_state == 'invalid' then return {'conflict'} end
if ttl_ms == nil or ttl_ms < 1 or key_type(ep) ~= 'hash' then return {'conflict'} end
if redis.call('HGET', ep, 'origin_revision_state') ~= 'active' or
   redis.call('HGET', ep, 'status_revision_state') ~= 'pending' or
   redis.call('HGET', ep, 'status_fence_token') ~= token or
   redis.call('HGET', ep, 'status_payload_hash') ~= payload_hash then return {'conflict'} end
local next_rev = redis.call('HGET', ep, 'pending_status_revision')
local next_eff = redis.call('HGET', ep, 'pending_effective_status')
if tonumber(next_rev) == nil or not valid_status(next_eff) then return {'conflict'} end
redis.call('HSET', ep, 'status_revision', next_rev, 'effective_status', next_eff, 'status_revision_state', 'active')
redis.call('HDEL', ep, 'pending_status_revision', 'pending_effective_status', 'status_fence_token', 'status_payload_hash')
reset_origin(ep, now_ms())
for i = 2, #KEYS - 1 do redis.call('DEL', KEYS[i]) end
write_terminal_op(op, token, payload_hash, 'status', '', '1', 'committed', ttl_ms)
return {'committed', next_rev}
`

// Singular status abort. KEYS: provider, six evidence keys, op. ARGV: token, hash, terminal_ttl_ms.
const luaAbortOriginStatus = luaOriginFenceHelpers + `
local ep, op = KEYS[1], KEYS[#KEYS]
local token, payload_hash, ttl_ms = ARGV[1], ARGV[2], tonumber(ARGV[3])
local op_state = read_op(op, token, payload_hash, 'status', '', '1')
if op_state == 'aborted' then return {'aborted'} end
if op_state == 'committed' or op_state == 'conflict' or op_state == 'invalid' then return {'conflict'} end
if ttl_ms == nil or ttl_ms < 1 or key_type(ep) ~= 'hash' then return {'conflict'} end
if redis.call('HGET', ep, 'origin_revision_state') ~= 'active' or
   redis.call('HGET', ep, 'status_revision_state') ~= 'pending' or
   redis.call('HGET', ep, 'status_fence_token') ~= token or
   redis.call('HGET', ep, 'status_payload_hash') ~= payload_hash then return {'conflict'} end
redis.call('HSET', ep, 'status_revision_state', 'active')
redis.call('HDEL', ep, 'pending_status_revision', 'pending_effective_status', 'status_fence_token', 'status_payload_hash')
reset_origin(ep, now_ms())
for i = 2, #KEYS - 1 do redis.call('DEL', KEYS[i]) end
write_terminal_op(op, token, payload_hash, 'status', '', '1', 'aborted', ttl_ms)
return {'aborted'}
`

// Singular origin scripts mirror status scripts while requiring status to stay active.
const luaPrepareOrigin = luaOriginFenceHelpers + `
local ep, op = KEYS[1], KEYS[2]
local current, next_rev, token, payload_hash = ARGV[1], ARGV[2], ARGV[3], ARGV[4]
local op_state = read_op(op, token, payload_hash, 'origin', '', '1')
if op_state == 'committed' or op_state == 'aborted' then return {op_state} end
if op_state == 'conflict' or op_state == 'invalid' then return {'conflict'} end
if key_type(ep) ~= 'hash' or redis.call('HGET', ep, 'control_present') ~= '1' then return {'absent'} end
if tonumber(current) == nil or tonumber(next_rev) ~= tonumber(current) + 1 then return {'invalid'} end
if op_state == 'prepared' then
  if redis.call('HGET', ep, 'origin_revision_state') == 'pending' and
     redis.call('HGET', ep, 'origin_fence_token') == token and
     redis.call('HGET', ep, 'origin_payload_hash') == payload_hash and
     redis.call('HGET', ep, 'pending_origin_revision') == next_rev then return {'prepared'} end
  return {'conflict'}
end
if redis.call('HGET', ep, 'origin_revision_state') ~= 'active' or
   redis.call('HGET', ep, 'status_revision_state') ~= 'active' then return {'conflict'} end
if redis.call('HGET', ep, 'origin_revision') ~= current then return {'stale'} end
local gen = (tonumber(redis.call('HGET', ep, 'origin_fence_generation')) or 0) + 1
redis.call('HSET', ep,
  'origin_revision_state', 'pending', 'pending_origin_revision', next_rev,
  'origin_fence_token', token, 'origin_payload_hash', payload_hash,
  'origin_fence_generation', gen)
write_prepared_op(op, token, payload_hash, 'origin', '', '1')
return {'prepared'}
`

const luaCommitOrigin = luaOriginFenceHelpers + `
local ep, op = KEYS[1], KEYS[#KEYS]
local token, payload_hash, ttl_ms = ARGV[1], ARGV[2], tonumber(ARGV[3])
local op_state = read_op(op, token, payload_hash, 'origin', '', '1')
if op_state == 'committed' then return {'committed'} end
if op_state == 'aborted' or op_state == 'conflict' or op_state == 'invalid' then return {'conflict'} end
if ttl_ms == nil or ttl_ms < 1 or key_type(ep) ~= 'hash' then return {'conflict'} end
if redis.call('HGET', ep, 'status_revision_state') ~= 'active' or
   redis.call('HGET', ep, 'origin_revision_state') ~= 'pending' or
   redis.call('HGET', ep, 'origin_fence_token') ~= token or
   redis.call('HGET', ep, 'origin_payload_hash') ~= payload_hash then return {'conflict'} end
local next_rev = redis.call('HGET', ep, 'pending_origin_revision')
if tonumber(next_rev) == nil then return {'conflict'} end
redis.call('HSET', ep, 'origin_revision', next_rev, 'origin_revision_state', 'active')
redis.call('HDEL', ep, 'pending_origin_revision', 'origin_fence_token', 'origin_payload_hash')
reset_origin(ep, now_ms())
for i = 2, #KEYS - 1 do redis.call('DEL', KEYS[i]) end
write_terminal_op(op, token, payload_hash, 'origin', '', '1', 'committed', ttl_ms)
return {'committed', next_rev}
`

const luaAbortOrigin = luaOriginFenceHelpers + `
local ep, op = KEYS[1], KEYS[#KEYS]
local token, payload_hash, ttl_ms = ARGV[1], ARGV[2], tonumber(ARGV[3])
local op_state = read_op(op, token, payload_hash, 'origin', '', '1')
if op_state == 'aborted' then return {'aborted'} end
if op_state == 'committed' or op_state == 'conflict' or op_state == 'invalid' then return {'conflict'} end
if ttl_ms == nil or ttl_ms < 1 or key_type(ep) ~= 'hash' then return {'conflict'} end
if redis.call('HGET', ep, 'status_revision_state') ~= 'active' or
   redis.call('HGET', ep, 'origin_revision_state') ~= 'pending' or
   redis.call('HGET', ep, 'origin_fence_token') ~= token or
   redis.call('HGET', ep, 'origin_payload_hash') ~= payload_hash then return {'conflict'} end
redis.call('HSET', ep, 'origin_revision_state', 'active')
redis.call('HDEL', ep, 'pending_origin_revision', 'origin_fence_token', 'origin_payload_hash')
reset_origin(ep, now_ms())
for i = 2, #KEYS - 1 do redis.call('DEL', KEYS[i]) end
write_terminal_op(op, token, payload_hash, 'origin', '', '1', 'aborted', ttl_ms)
return {'aborted'}
`

// Combined BaseURL + status prepare changes both pending fences in one write phase.
// KEYS: origin, op. ARGV: current_base, next_base, current_status, next_status,
// next_effective, token, payload_hash.
