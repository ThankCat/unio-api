package breakerstore

// Origin revision fences use a Redis-side operation record in addition to the Origin control.
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
    'base_url_revision', base_rev, 'status_revision', status_rev,
    'base_url_fence_generation', '1', 'status_fence_generation', '1',
    'base_url_revision_state', 'active', 'status_revision_state', 'active',
    'state', 'closed', 'state_generation', '1', 'window_started_at_ms', now,
    'eligible_successes', '0', 'eligible_failures', '0',
    'consecutive_eligible_failures', '0', 'open_level', '0',
    'half_open_successes', '0', 'last_transition_at_ms', now)
end
`

// KEYS[1]=origin. ARGV: base_url_revision, status_revision, effective_status.
const luaInitOriginControl = luaOriginFenceHelpers + `
local ep = KEYS[1]
if key_type(ep) ~= 'none' and key_type(ep) ~= 'hash' then return redis.error_reply('invalid origin key type') end
if redis.call('HGET', ep, 'control_present') == '1' then return {'exists'} end
if tonumber(ARGV[1]) == nil or tonumber(ARGV[1]) < 1 or
   tonumber(ARGV[2]) == nil or tonumber(ARGV[2]) < 1 or not valid_status(ARGV[3]) then
  return redis.error_reply('invalid origin control')
end
restore_origin(ep, ARGV[1], ARGV[2], ARGV[3], now_ms())
return {'created'}
`

// Recovery-only absent control restore. Existing controls are never overwritten.
const luaRestoreMissingOriginControl = luaOriginFenceHelpers + `
local ep = KEYS[1]
if key_type(ep) ~= 'none' and key_type(ep) ~= 'hash' then return redis.error_reply('invalid origin key type') end
if redis.call('HGET', ep, 'control_present') == '1' then return {'exists'} end
if tonumber(ARGV[1]) == nil or tonumber(ARGV[1]) < 1 or
   tonumber(ARGV[2]) == nil or tonumber(ARGV[2]) < 1 or not valid_status(ARGV[3]) then
  return redis.error_reply('invalid origin control')
end
restore_origin(ep, ARGV[1], ARGV[2], ARGV[3], now_ms())
return {'installed'}
`

// Singular status prepare. KEYS: origin, op. ARGV: current, next, next_effective, token, hash.
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
if redis.call('HGET', ep, 'base_url_revision_state') ~= 'active' or
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

// Singular status commit. KEYS: origin, six evidence keys, op. ARGV: token, hash, terminal_ttl_ms.
const luaCommitOriginStatus = luaOriginFenceHelpers + `
local ep, op = KEYS[1], KEYS[#KEYS]
local token, payload_hash, ttl_ms = ARGV[1], ARGV[2], tonumber(ARGV[3])
local op_state = read_op(op, token, payload_hash, 'status', '', '1')
if op_state == 'committed' then return {'committed'} end
if op_state == 'aborted' or op_state == 'conflict' or op_state == 'invalid' then return {'conflict'} end
if ttl_ms == nil or ttl_ms < 1 or key_type(ep) ~= 'hash' then return {'conflict'} end
if redis.call('HGET', ep, 'base_url_revision_state') ~= 'active' or
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

// Singular status abort. KEYS: origin, six evidence keys, op. ARGV: token, hash, terminal_ttl_ms.
const luaAbortOriginStatus = luaOriginFenceHelpers + `
local ep, op = KEYS[1], KEYS[#KEYS]
local token, payload_hash, ttl_ms = ARGV[1], ARGV[2], tonumber(ARGV[3])
local op_state = read_op(op, token, payload_hash, 'status', '', '1')
if op_state == 'aborted' then return {'aborted'} end
if op_state == 'committed' or op_state == 'conflict' or op_state == 'invalid' then return {'conflict'} end
if ttl_ms == nil or ttl_ms < 1 or key_type(ep) ~= 'hash' then return {'conflict'} end
if redis.call('HGET', ep, 'base_url_revision_state') ~= 'active' or
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

// Singular BaseURL scripts mirror status scripts while requiring status to stay active.
const luaPrepareOriginBaseURL = luaOriginFenceHelpers + `
local ep, op = KEYS[1], KEYS[2]
local current, next_rev, token, payload_hash = ARGV[1], ARGV[2], ARGV[3], ARGV[4]
local op_state = read_op(op, token, payload_hash, 'base_url', '', '1')
if op_state == 'committed' or op_state == 'aborted' then return {op_state} end
if op_state == 'conflict' or op_state == 'invalid' then return {'conflict'} end
if key_type(ep) ~= 'hash' or redis.call('HGET', ep, 'control_present') ~= '1' then return {'absent'} end
if tonumber(current) == nil or tonumber(next_rev) ~= tonumber(current) + 1 then return {'invalid'} end
if op_state == 'prepared' then
  if redis.call('HGET', ep, 'base_url_revision_state') == 'pending' and
     redis.call('HGET', ep, 'base_url_fence_token') == token and
     redis.call('HGET', ep, 'base_url_payload_hash') == payload_hash and
     redis.call('HGET', ep, 'pending_base_url_revision') == next_rev then return {'prepared'} end
  return {'conflict'}
end
if redis.call('HGET', ep, 'base_url_revision_state') ~= 'active' or
   redis.call('HGET', ep, 'status_revision_state') ~= 'active' then return {'conflict'} end
if redis.call('HGET', ep, 'base_url_revision') ~= current then return {'stale'} end
local gen = (tonumber(redis.call('HGET', ep, 'base_url_fence_generation')) or 0) + 1
redis.call('HSET', ep,
  'base_url_revision_state', 'pending', 'pending_base_url_revision', next_rev,
  'base_url_fence_token', token, 'base_url_payload_hash', payload_hash,
  'base_url_fence_generation', gen)
write_prepared_op(op, token, payload_hash, 'base_url', '', '1')
return {'prepared'}
`

const luaCommitOriginBaseURL = luaOriginFenceHelpers + `
local ep, op = KEYS[1], KEYS[#KEYS]
local token, payload_hash, ttl_ms = ARGV[1], ARGV[2], tonumber(ARGV[3])
local op_state = read_op(op, token, payload_hash, 'base_url', '', '1')
if op_state == 'committed' then return {'committed'} end
if op_state == 'aborted' or op_state == 'conflict' or op_state == 'invalid' then return {'conflict'} end
if ttl_ms == nil or ttl_ms < 1 or key_type(ep) ~= 'hash' then return {'conflict'} end
if redis.call('HGET', ep, 'status_revision_state') ~= 'active' or
   redis.call('HGET', ep, 'base_url_revision_state') ~= 'pending' or
   redis.call('HGET', ep, 'base_url_fence_token') ~= token or
   redis.call('HGET', ep, 'base_url_payload_hash') ~= payload_hash then return {'conflict'} end
local next_rev = redis.call('HGET', ep, 'pending_base_url_revision')
if tonumber(next_rev) == nil then return {'conflict'} end
redis.call('HSET', ep, 'base_url_revision', next_rev, 'base_url_revision_state', 'active')
redis.call('HDEL', ep, 'pending_base_url_revision', 'base_url_fence_token', 'base_url_payload_hash')
reset_origin(ep, now_ms())
for i = 2, #KEYS - 1 do redis.call('DEL', KEYS[i]) end
write_terminal_op(op, token, payload_hash, 'base_url', '', '1', 'committed', ttl_ms)
return {'committed', next_rev}
`

const luaAbortOriginBaseURL = luaOriginFenceHelpers + `
local ep, op = KEYS[1], KEYS[#KEYS]
local token, payload_hash, ttl_ms = ARGV[1], ARGV[2], tonumber(ARGV[3])
local op_state = read_op(op, token, payload_hash, 'base_url', '', '1')
if op_state == 'aborted' then return {'aborted'} end
if op_state == 'committed' or op_state == 'conflict' or op_state == 'invalid' then return {'conflict'} end
if ttl_ms == nil or ttl_ms < 1 or key_type(ep) ~= 'hash' then return {'conflict'} end
if redis.call('HGET', ep, 'status_revision_state') ~= 'active' or
   redis.call('HGET', ep, 'base_url_revision_state') ~= 'pending' or
   redis.call('HGET', ep, 'base_url_fence_token') ~= token or
   redis.call('HGET', ep, 'base_url_payload_hash') ~= payload_hash then return {'conflict'} end
redis.call('HSET', ep, 'base_url_revision_state', 'active')
redis.call('HDEL', ep, 'pending_base_url_revision', 'base_url_fence_token', 'base_url_payload_hash')
reset_origin(ep, now_ms())
for i = 2, #KEYS - 1 do redis.call('DEL', KEYS[i]) end
write_terminal_op(op, token, payload_hash, 'base_url', '', '1', 'aborted', ttl_ms)
return {'aborted'}
`

// Combined BaseURL + status prepare changes both pending fences in one write phase.
// KEYS: origin, op. ARGV: current_base, next_base, current_status, next_status,
// next_effective, token, payload_hash.
const luaPrepareOriginRoutingChange = luaOriginFenceHelpers + `
local ep, op = KEYS[1], KEYS[2]
local cb, nb, cs, ns, ne, token, payload_hash = ARGV[1], ARGV[2], ARGV[3], ARGV[4], ARGV[5], ARGV[6], ARGV[7]
local op_state = read_op(op, token, payload_hash, 'base_url_status', '', '1')
if op_state == 'committed' or op_state == 'aborted' then return {op_state} end
if op_state == 'conflict' or op_state == 'invalid' then return {'conflict'} end
if key_type(ep) ~= 'hash' or redis.call('HGET', ep, 'control_present') ~= '1' then return {'absent'} end
if tonumber(nb) ~= tonumber(cb) + 1 or tonumber(ns) ~= tonumber(cs) + 1 or not valid_status(ne) then return {'invalid'} end
if op_state == 'prepared' then
  if redis.call('HGET', ep, 'base_url_revision_state') == 'pending' and
     redis.call('HGET', ep, 'status_revision_state') == 'pending' and
     redis.call('HGET', ep, 'base_url_fence_token') == token and
     redis.call('HGET', ep, 'status_fence_token') == token and
     redis.call('HGET', ep, 'base_url_payload_hash') == payload_hash and
     redis.call('HGET', ep, 'status_payload_hash') == payload_hash and
     redis.call('HGET', ep, 'pending_base_url_revision') == nb and
     redis.call('HGET', ep, 'pending_status_revision') == ns and
     redis.call('HGET', ep, 'pending_effective_status') == ne then return {'prepared'} end
  return {'conflict'}
end
if redis.call('HGET', ep, 'base_url_revision_state') ~= 'active' or
   redis.call('HGET', ep, 'status_revision_state') ~= 'active' or
   redis.call('HGET', ep, 'base_url_revision') ~= cb or
   redis.call('HGET', ep, 'status_revision') ~= cs then return {'stale'} end
local bgen = (tonumber(redis.call('HGET', ep, 'base_url_fence_generation')) or 0) + 1
local sgen = (tonumber(redis.call('HGET', ep, 'status_fence_generation')) or 0) + 1
redis.call('HSET', ep,
  'base_url_revision_state', 'pending', 'pending_base_url_revision', nb,
  'base_url_fence_token', token, 'base_url_payload_hash', payload_hash,
  'base_url_fence_generation', bgen,
  'status_revision_state', 'pending', 'pending_status_revision', ns,
  'pending_effective_status', ne, 'status_fence_token', token,
  'status_payload_hash', payload_hash, 'status_fence_generation', sgen)
write_prepared_op(op, token, payload_hash, 'base_url_status', '', '1')
return {'prepared'}
`

const luaCommitOriginRoutingChange = luaOriginFenceHelpers + `
local ep, op = KEYS[1], KEYS[#KEYS]
local token, payload_hash, ttl_ms = ARGV[1], ARGV[2], tonumber(ARGV[3])
local op_state = read_op(op, token, payload_hash, 'base_url_status', '', '1')
if op_state == 'committed' then return {'committed'} end
if op_state == 'aborted' or op_state == 'conflict' or op_state == 'invalid' then return {'conflict'} end
if ttl_ms == nil or ttl_ms < 1 or key_type(ep) ~= 'hash' then return {'conflict'} end
if redis.call('HGET', ep, 'base_url_revision_state') ~= 'pending' or
   redis.call('HGET', ep, 'status_revision_state') ~= 'pending' or
   redis.call('HGET', ep, 'base_url_fence_token') ~= token or
   redis.call('HGET', ep, 'status_fence_token') ~= token or
   redis.call('HGET', ep, 'base_url_payload_hash') ~= payload_hash or
   redis.call('HGET', ep, 'status_payload_hash') ~= payload_hash then return {'conflict'} end
local nb = redis.call('HGET', ep, 'pending_base_url_revision')
local ns = redis.call('HGET', ep, 'pending_status_revision')
local ne = redis.call('HGET', ep, 'pending_effective_status')
if tonumber(nb) == nil or tonumber(ns) == nil or not valid_status(ne) then return {'conflict'} end
redis.call('HSET', ep,
  'base_url_revision', nb, 'base_url_revision_state', 'active',
  'status_revision', ns, 'effective_status', ne, 'status_revision_state', 'active')
redis.call('HDEL', ep,
  'pending_base_url_revision', 'base_url_fence_token', 'base_url_payload_hash',
  'pending_status_revision', 'pending_effective_status', 'status_fence_token', 'status_payload_hash')
reset_origin(ep, now_ms())
for i = 2, #KEYS - 1 do redis.call('DEL', KEYS[i]) end
write_terminal_op(op, token, payload_hash, 'base_url_status', '', '1', 'committed', ttl_ms)
return {'committed', nb, ns}
`

const luaAbortOriginRoutingChange = luaOriginFenceHelpers + `
local ep, op = KEYS[1], KEYS[#KEYS]
local token, payload_hash, ttl_ms = ARGV[1], ARGV[2], tonumber(ARGV[3])
local op_state = read_op(op, token, payload_hash, 'base_url_status', '', '1')
if op_state == 'aborted' then return {'aborted'} end
if op_state == 'committed' or op_state == 'conflict' or op_state == 'invalid' then return {'conflict'} end
if ttl_ms == nil or ttl_ms < 1 or key_type(ep) ~= 'hash' then return {'conflict'} end
if redis.call('HGET', ep, 'base_url_revision_state') ~= 'pending' or
   redis.call('HGET', ep, 'status_revision_state') ~= 'pending' or
   redis.call('HGET', ep, 'base_url_fence_token') ~= token or
   redis.call('HGET', ep, 'status_fence_token') ~= token or
   redis.call('HGET', ep, 'base_url_payload_hash') ~= payload_hash or
   redis.call('HGET', ep, 'status_payload_hash') ~= payload_hash then return {'conflict'} end
redis.call('HSET', ep, 'base_url_revision_state', 'active', 'status_revision_state', 'active')
redis.call('HDEL', ep,
  'pending_base_url_revision', 'base_url_fence_token', 'base_url_payload_hash',
  'pending_status_revision', 'pending_effective_status', 'status_fence_token', 'status_payload_hash')
reset_origin(ep, now_ms())
for i = 2, #KEYS - 1 do redis.call('DEL', KEYS[i]) end
write_terminal_op(op, token, payload_hash, 'base_url_status', '', '1', 'aborted', ttl_ms)
return {'aborted'}
`

// Provider status batch prepare. KEYS[1..n]=ordered origins, KEYS[n+1]=op.
// ARGV: n, max, provider_id, token, hash, then repeated origin_id,current,next,next_effective.
const luaPrepareOriginStatusBatch = luaOriginFenceHelpers + `
local n, max_n = tonumber(ARGV[1]), tonumber(ARGV[2])
local provider_id, token, payload_hash = ARGV[3], ARGV[4], ARGV[5]
if n == nil or max_n == nil or n < 1 or max_n < 1 or max_n > 1024 or n > max_n or #KEYS ~= n + 1 then return {'too_large'} end
local op = KEYS[n + 1]
local op_state = read_op(op, token, payload_hash, 'provider_status_batch', provider_id, tostring(n))
if op_state == 'committed' or op_state == 'aborted' then return {op_state} end
if op_state == 'conflict' or op_state == 'invalid' then return {'conflict'} end
local previous_id = 0
for i = 1, n do
  local offset = 5 + (i - 1) * 4
  local origin_id, current, next_rev, next_eff = tonumber(ARGV[offset + 1]), ARGV[offset + 2], ARGV[offset + 3], ARGV[offset + 4]
  local ep = KEYS[i]
  if origin_id == nil or origin_id <= previous_id or tonumber(next_rev) ~= tonumber(current) + 1 or not valid_status(next_eff) then return {'invalid'} end
  previous_id = origin_id
  if key_type(ep) ~= 'hash' or redis.call('HGET', ep, 'control_present') ~= '1' then return {'absent'} end
  if op_state == 'prepared' then
    if redis.call('HGET', ep, 'status_revision_state') ~= 'pending' or
       redis.call('HGET', ep, 'status_fence_token') ~= token or
       redis.call('HGET', ep, 'status_payload_hash') ~= payload_hash or
       redis.call('HGET', ep, 'pending_status_revision') ~= next_rev or
       redis.call('HGET', ep, 'pending_effective_status') ~= next_eff then return {'conflict'} end
  else
    if redis.call('HGET', ep, 'base_url_revision_state') ~= 'active' or
       redis.call('HGET', ep, 'status_revision_state') ~= 'active' then return {'conflict'} end
    if redis.call('HGET', ep, 'status_revision') ~= current then return {'stale'} end
  end
end
if op_state == 'prepared' then return {'prepared'} end
for i = 1, n do
  local offset = 5 + (i - 1) * 4
  local next_rev, next_eff = ARGV[offset + 3], ARGV[offset + 4]
  local ep = KEYS[i]
  local gen = (tonumber(redis.call('HGET', ep, 'status_fence_generation')) or 0) + 1
  redis.call('HSET', ep,
    'status_revision_state', 'pending', 'pending_status_revision', next_rev,
    'pending_effective_status', next_eff, 'status_fence_token', token,
    'status_payload_hash', payload_hash, 'status_fence_generation', gen)
end
write_prepared_op(op, token, payload_hash, 'provider_status_batch', provider_id, tostring(n))
return {'prepared'}
`

// Batch commit/abort KEYS: n origins, 6*n evidence keys, op.
// ARGV: n, provider_id, token, hash, terminal_ttl_ms.
const luaCommitOriginStatusBatch = luaOriginFenceHelpers + `
local n, provider_id, token, payload_hash, ttl_ms = tonumber(ARGV[1]), ARGV[2], ARGV[3], ARGV[4], tonumber(ARGV[5])
if n == nil or n < 1 or #KEYS ~= n * 7 + 1 or ttl_ms == nil or ttl_ms < 1 then return {'invalid'} end
local op = KEYS[#KEYS]
local op_state = read_op(op, token, payload_hash, 'provider_status_batch', provider_id, tostring(n))
if op_state == 'committed' then return {'committed'} end
if op_state == 'aborted' or op_state == 'conflict' or op_state == 'invalid' then return {'conflict'} end
for i = 1, n do
  local ep = KEYS[i]
  if key_type(ep) ~= 'hash' or redis.call('HGET', ep, 'base_url_revision_state') ~= 'active' or
     redis.call('HGET', ep, 'status_revision_state') ~= 'pending' or
     redis.call('HGET', ep, 'status_fence_token') ~= token or
     redis.call('HGET', ep, 'status_payload_hash') ~= payload_hash or
     tonumber(redis.call('HGET', ep, 'pending_status_revision')) == nil or
     not valid_status(redis.call('HGET', ep, 'pending_effective_status')) then return {'conflict'} end
end
local now = now_ms()
for i = 1, n do
  local ep = KEYS[i]
  local next_rev = redis.call('HGET', ep, 'pending_status_revision')
  local next_eff = redis.call('HGET', ep, 'pending_effective_status')
  redis.call('HSET', ep, 'status_revision', next_rev, 'effective_status', next_eff, 'status_revision_state', 'active')
  redis.call('HDEL', ep, 'pending_status_revision', 'pending_effective_status', 'status_fence_token', 'status_payload_hash')
  reset_origin(ep, now)
end
for i = n + 1, #KEYS - 1 do redis.call('DEL', KEYS[i]) end
write_terminal_op(op, token, payload_hash, 'provider_status_batch', provider_id, tostring(n), 'committed', ttl_ms)
return {'committed'}
`

const luaAbortOriginStatusBatch = luaOriginFenceHelpers + `
local n, provider_id, token, payload_hash, ttl_ms = tonumber(ARGV[1]), ARGV[2], ARGV[3], ARGV[4], tonumber(ARGV[5])
if n == nil or n < 1 or #KEYS ~= n * 7 + 1 or ttl_ms == nil or ttl_ms < 1 then return {'invalid'} end
local op = KEYS[#KEYS]
local op_state = read_op(op, token, payload_hash, 'provider_status_batch', provider_id, tostring(n))
if op_state == 'aborted' then return {'aborted'} end
if op_state == 'committed' or op_state == 'conflict' or op_state == 'invalid' then return {'conflict'} end
for i = 1, n do
  local ep = KEYS[i]
  if key_type(ep) ~= 'hash' or redis.call('HGET', ep, 'base_url_revision_state') ~= 'active' or
     redis.call('HGET', ep, 'status_revision_state') ~= 'pending' or
     redis.call('HGET', ep, 'status_fence_token') ~= token or
     redis.call('HGET', ep, 'status_payload_hash') ~= payload_hash then return {'conflict'} end
end
local now = now_ms()
for i = 1, n do
  local ep = KEYS[i]
  redis.call('HSET', ep, 'status_revision_state', 'active')
  redis.call('HDEL', ep, 'pending_status_revision', 'pending_effective_status', 'status_fence_token', 'status_payload_hash')
  reset_origin(ep, now)
end
for i = n + 1, #KEYS - 1 do redis.call('DEL', KEYS[i]) end
write_terminal_op(op, token, payload_hash, 'provider_status_batch', provider_id, tostring(n), 'aborted', ttl_ms)
return {'aborted'}
`

// Recovery reconciles a durable PostgreSQL operation against current business facts. It can restore
// absent controls at arbitrary revisions, finish matching pending fences, or recognize an already active
// terminal result without advancing a generation twice.
// KEYS: n origins, 6*n evidence keys, op.
// ARGV: mode(committed|aborted), kind, n, provider_id, token, payload_hash, terminal_ttl_ms,
// then repeated: origin_id,current_base,next_base,current_status,next_status,current_eff,next_eff,
// fact_base,fact_status,fact_eff.
const luaRecoverOriginRouting = luaOriginFenceHelpers + `
local mode, kind, n = ARGV[1], ARGV[2], tonumber(ARGV[3])
local provider_id, token, payload_hash, ttl_ms = ARGV[4], ARGV[5], ARGV[6], tonumber(ARGV[7])
if (mode ~= 'committed' and mode ~= 'aborted') or
   (kind ~= 'base_url' and kind ~= 'status' and kind ~= 'base_url_status' and kind ~= 'provider_status_batch') or
   n == nil or n < 1 or n > 1024 or #KEYS ~= n * 7 + 1 or ttl_ms == nil or ttl_ms < 1 then return {'invalid'} end
if kind ~= 'provider_status_batch' and n ~= 1 then return {'invalid'} end
local op = KEYS[#KEYS]
local op_state = read_op(op, token, payload_hash, kind, provider_id, tostring(n))
if op_state == 'conflict' or op_state == 'invalid' then return {'conflict'} end
if (mode == 'committed' and op_state == 'aborted') or (mode == 'aborted' and op_state == 'committed') then return {'conflict'} end
local actions = {}
local previous_id = 0
for i = 1, n do
  local offset = 7 + (i - 1) * 10
  local origin_id = tonumber(ARGV[offset + 1])
  local cb, nb, cs, ns = ARGV[offset + 2], ARGV[offset + 3], ARGV[offset + 4], ARGV[offset + 5]
  local ce, ne = ARGV[offset + 6], ARGV[offset + 7]
  local fb, fs, fe = ARGV[offset + 8], ARGV[offset + 9], ARGV[offset + 10]
  local ep = KEYS[i]
  if origin_id == nil or origin_id <= previous_id or tonumber(fb) == nil or tonumber(fb) < 1 or
     tonumber(fs) == nil or tonumber(fs) < 1 or not valid_status(fe) then return {'invalid'} end
  previous_id = origin_id
  local typ = key_type(ep)
  if typ == 'none' then
    actions[i] = 'restore'
  elseif typ ~= 'hash' or redis.call('HGET', ep, 'control_present') ~= '1' then
    return {'conflict'}
  else
    local bstate = redis.call('HGET', ep, 'base_url_revision_state')
    local sstate = redis.call('HGET', ep, 'status_revision_state')
    local active_b = redis.call('HGET', ep, 'base_url_revision')
    local active_s = redis.call('HGET', ep, 'status_revision')
    local active_e = redis.call('HGET', ep, 'effective_status')
    local pending_b = bstate == 'pending'
    local pending_s = sstate == 'pending'
    if kind == 'base_url' then
      if sstate ~= 'active' or active_s ~= fs or active_e ~= fe then return {'conflict'} end
      if pending_b then
        if redis.call('HGET', ep, 'base_url_fence_token') ~= token or
           redis.call('HGET', ep, 'base_url_payload_hash') ~= payload_hash or
           redis.call('HGET', ep, 'pending_base_url_revision') ~= nb or active_b ~= cb then return {'conflict'} end
        actions[i] = 'pending'
      elseif bstate == 'active' and active_b == fb then actions[i] = 'active' else return {'conflict'} end
    elseif kind == 'status' or kind == 'provider_status_batch' then
      if bstate ~= 'active' or active_b ~= fb then return {'conflict'} end
      if pending_s then
        if redis.call('HGET', ep, 'status_fence_token') ~= token or
           redis.call('HGET', ep, 'status_payload_hash') ~= payload_hash or
           redis.call('HGET', ep, 'pending_status_revision') ~= ns or
           redis.call('HGET', ep, 'pending_effective_status') ~= ne or active_s ~= cs or active_e ~= ce then return {'conflict'} end
        actions[i] = 'pending'
      elseif sstate == 'active' and active_s == fs and active_e == fe then actions[i] = 'active' else return {'conflict'} end
    else
      if pending_b ~= pending_s then return {'conflict'} end
      if pending_b then
        if redis.call('HGET', ep, 'base_url_fence_token') ~= token or
           redis.call('HGET', ep, 'status_fence_token') ~= token or
           redis.call('HGET', ep, 'base_url_payload_hash') ~= payload_hash or
           redis.call('HGET', ep, 'status_payload_hash') ~= payload_hash or
           redis.call('HGET', ep, 'pending_base_url_revision') ~= nb or
           redis.call('HGET', ep, 'pending_status_revision') ~= ns or
           redis.call('HGET', ep, 'pending_effective_status') ~= ne or
           active_b ~= cb or active_s ~= cs or active_e ~= ce then return {'conflict'} end
        actions[i] = 'pending'
      elseif bstate == 'active' and sstate == 'active' and active_b == fb and active_s == fs and active_e == fe then
        actions[i] = 'active'
      else return {'conflict'} end
    end
    if actions[i] == 'pending' then
      if mode == 'committed' and (fb ~= nb or fs ~= ns or fe ~= ne) then return {'conflict'} end
      if mode == 'aborted' and (fb ~= cb or fs ~= cs or fe ~= ce) then return {'conflict'} end
    end
  end
end

local now = now_ms()
for i = 1, n do
  local offset = 7 + (i - 1) * 10
  local fb, fs, fe = ARGV[offset + 8], ARGV[offset + 9], ARGV[offset + 10]
  local ep = KEYS[i]
  if actions[i] == 'restore' then
    restore_origin(ep, fb, fs, fe, now)
  elseif actions[i] == 'pending' then
    if kind == 'base_url' then
      redis.call('HSET', ep, 'base_url_revision', fb, 'base_url_revision_state', 'active')
      redis.call('HDEL', ep, 'pending_base_url_revision', 'base_url_fence_token', 'base_url_payload_hash')
    elseif kind == 'status' or kind == 'provider_status_batch' then
      redis.call('HSET', ep, 'status_revision', fs, 'effective_status', fe, 'status_revision_state', 'active')
      redis.call('HDEL', ep, 'pending_status_revision', 'pending_effective_status', 'status_fence_token', 'status_payload_hash')
    else
      redis.call('HSET', ep,
        'base_url_revision', fb, 'base_url_revision_state', 'active',
        'status_revision', fs, 'effective_status', fe, 'status_revision_state', 'active')
      redis.call('HDEL', ep,
        'pending_base_url_revision', 'base_url_fence_token', 'base_url_payload_hash',
        'pending_status_revision', 'pending_effective_status', 'status_fence_token', 'status_payload_hash')
    end
    reset_origin(ep, now)
  end
  if actions[i] ~= 'active' then
    local evidence_start = n + (i - 1) * 6 + 1
    for k = evidence_start, evidence_start + 5 do redis.call('DEL', KEYS[k]) end
  end
end
write_terminal_op(op, token, payload_hash, kind, provider_id, tostring(n), mode, ttl_ms)
return {mode}
`
