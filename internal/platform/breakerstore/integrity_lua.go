package breakerstore

// 完整性 epoch marker Lua（§5.1、§5.3.19、§5.5）。marker 与 epoch operation key 必须在
// 同一脚本中分类和更新；其它 operation、更新 epoch、畸形 marker 均只返回 conflict。

// luaStateIntegrityRead 返回 marker 的完整可分类视图。KEYS[1]=marker。
const luaStateIntegrityRead = `
local marker = KEYS[1]
if redis.call('EXISTS', marker) == 0 then
  return {0, '', '', 0, '', '', '', '', '', 0, '', 0, '', ''}
end
return {1,
  redis.call('HGET', marker, 'state') or '',
  redis.call('HGET', marker, 'epoch') or '',
  tonumber(redis.call('HGET', marker, 'revision')) or 0,
  redis.call('HGET', marker, 'marker_hash') or '',
  redis.call('HGET', marker, 'operation_token') or '',
  redis.call('HGET', marker, 'transition_hash') or '',
  redis.call('HGET', marker, 'expected_marker_hash') or '',
  redis.call('HGET', marker, 'old_epoch') or '',
  tonumber(redis.call('HGET', marker, 'old_revision')) or 0,
  redis.call('HGET', marker, 'new_epoch') or '',
  tonumber(redis.call('HGET', marker, 'new_revision')) or 0,
  redis.call('HGET', marker, 'last_operation_token') or '',
  redis.call('HGET', marker, 'last_transition_hash') or ''}
`

// luaEpochPrepare 是 Prepare/Recover 共用的五分支真值表。
// KEYS: marker, operation key。
// ARGV: token,old_epoch,old_revision,new_epoch,new_revision,transition_hash,expected_marker_hash,new_ready_hash。
// 返回 prepared|new_ready_observed|conflict。
const luaEpochPrepare = `
local function now_ms()
  local t = redis.call('TIME')
  return tonumber(t[1]) * 1000 + math.floor(tonumber(t[2]) / 1000)
end

local marker = KEYS[1]
local op = KEYS[2]
local token = ARGV[1]
local old_epoch = ARGV[2]
local old_revision = ARGV[3]
local new_epoch = ARGV[4]
local new_revision = ARGV[5]
local transition_hash = ARGV[6]
local expected_hash = ARGV[7]
local new_ready_hash = ARGV[8]
local now = now_ms()

local function op_compatible(allowed_state)
  if redis.call('EXISTS', op) == 0 then return true end
  if redis.call('HGET', op, 'token') ~= token
      or redis.call('HGET', op, 'transition_hash') ~= transition_hash
      or redis.call('HGET', op, 'new_epoch') ~= new_epoch
      or redis.call('HGET', op, 'new_revision') ~= new_revision then
    return false
  end
  return redis.call('HGET', op, 'state') == allowed_state
end

local function write_pending()
  redis.call('HSET', marker,
    'state', 'pending',
    'operation_token', token,
    'transition_hash', transition_hash,
    'expected_marker_hash', expected_hash,
    'old_epoch', old_epoch,
    'old_revision', old_revision,
    'new_epoch', new_epoch,
    'new_revision', new_revision,
    'pending_at_ms', now)
  redis.call('HDEL', marker, 'marker_hash', 'last_operation_token', 'last_transition_hash')
  if redis.call('HGET', marker, 'epoch') == false then
    redis.call('HSET', marker, 'epoch', new_epoch, 'revision', new_revision, 'initialized_at_ms', now)
  end
  redis.call('HSET', op,
    'token', token,
    'transition_hash', transition_hash,
    'expected_marker_hash', expected_hash,
    'old_epoch', old_epoch,
    'old_revision', old_revision,
    'new_epoch', new_epoch,
    'new_revision', new_revision,
    'state', 'pending',
    'updated_at_ms', now)
  return {'prepared'}
end

-- 1) marker absent：只有 durable expected=absent 可以建立 pending。
if redis.call('EXISTS', marker) == 0 then
  if expected_hash ~= 'absent' or not op_compatible('pending') then
    return {'conflict'}
  end
  return write_pending()
end

local state = redis.call('HGET', marker, 'state') or ''
local cur_epoch = redis.call('HGET', marker, 'epoch') or ''
local cur_revision = redis.call('HGET', marker, 'revision') or ''

-- 2) durable old ready：必须同时匹配 old epoch/revision 与精确 canonical hash。
if state == 'ready' and cur_epoch == old_epoch and cur_revision == old_revision
    and expected_hash ~= 'absent'
    and redis.call('HGET', marker, 'marker_hash') == expected_hash then
  if not op_compatible('pending') then return {'conflict'} end
  return write_pending()
end

-- 3) 同 operation pending：幂等，op key 丢失时按 marker 重建。
if state == 'pending'
    and redis.call('HGET', marker, 'operation_token') == token
    and redis.call('HGET', marker, 'transition_hash') == transition_hash
    and redis.call('HGET', marker, 'expected_marker_hash') == expected_hash
    and redis.call('HGET', marker, 'old_epoch') == old_epoch
    and redis.call('HGET', marker, 'old_revision') == old_revision
    and redis.call('HGET', marker, 'new_epoch') == new_epoch
    and redis.call('HGET', marker, 'new_revision') == new_revision then
  if redis.call('EXISTS', op) == 1 and not op_compatible('pending') then return {'conflict'} end
  redis.call('HSET', op,
    'token', token, 'transition_hash', transition_hash, 'expected_marker_hash', expected_hash,
    'old_epoch', old_epoch, 'old_revision', old_revision,
    'new_epoch', new_epoch, 'new_revision', new_revision,
    'state', 'pending', 'updated_at_ms', now)
  return {'prepared'}
end

-- 4) 同 operation new ready：只报告观测，是否可终结 PostgreSQL 由 application 根据 db_committed 决定。
if state == 'ready' and cur_epoch == new_epoch and cur_revision == new_revision
    and redis.call('HGET', marker, 'marker_hash') == new_ready_hash
    and redis.call('HGET', marker, 'last_operation_token') == token
    and redis.call('HGET', marker, 'last_transition_hash') == transition_hash then
  if redis.call('EXISTS', op) == 1 and not op_compatible('committed') then return {'conflict'} end
  redis.call('HSET', op,
    'token', token, 'transition_hash', transition_hash, 'expected_marker_hash', expected_hash,
    'old_epoch', old_epoch, 'old_revision', old_revision,
    'new_epoch', new_epoch, 'new_revision', new_revision,
    'state', 'committed', 'updated_at_ms', now)
  return {'new_ready_observed'}
end

-- 5) 其它 marker/op：零覆盖 conflict。
return {'conflict'}
`

// luaEpochCommit 仅把同 token/hash 的 pending 激活为 new ready。
// KEYS: marker, operation key。
// ARGV: token,transition_hash,new_epoch,new_revision,new_ready_hash,op_ttl_ms。
const luaEpochCommit = `
local function now_ms()
  local t = redis.call('TIME')
  return tonumber(t[1]) * 1000 + math.floor(tonumber(t[2]) / 1000)
end
local marker = KEYS[1]
local op = KEYS[2]
local token = ARGV[1]
local transition_hash = ARGV[2]
local new_epoch = ARGV[3]
local new_revision = ARGV[4]
local new_ready_hash = ARGV[5]
local op_ttl_ms = tonumber(ARGV[6]) or 0
local now = now_ms()

if redis.call('EXISTS', marker) == 0 then return {'conflict'} end
local state = redis.call('HGET', marker, 'state') or ''

if state == 'ready'
    and redis.call('HGET', marker, 'epoch') == new_epoch
    and redis.call('HGET', marker, 'revision') == new_revision
    and redis.call('HGET', marker, 'marker_hash') == new_ready_hash
    and redis.call('HGET', marker, 'last_operation_token') == token
    and redis.call('HGET', marker, 'last_transition_hash') == transition_hash then
  return {'committed'}
end

if state ~= 'pending'
    or redis.call('HGET', marker, 'operation_token') ~= token
    or redis.call('HGET', marker, 'transition_hash') ~= transition_hash
    or redis.call('HGET', marker, 'new_epoch') ~= new_epoch
    or redis.call('HGET', marker, 'new_revision') ~= new_revision then
  return {'conflict'}
end

if redis.call('EXISTS', op) == 1 then
  if redis.call('HGET', op, 'token') ~= token
      or redis.call('HGET', op, 'transition_hash') ~= transition_hash
      or redis.call('HGET', op, 'state') ~= 'pending' then
    return {'conflict'}
  end
end

redis.call('HSET', marker,
  'state', 'ready',
  'epoch', new_epoch,
  'revision', new_revision,
  'marker_hash', new_ready_hash,
  'activated_at_ms', now,
  'last_operation_token', token,
  'last_transition_hash', transition_hash)
redis.call('HDEL', marker,
  'operation_token', 'transition_hash', 'expected_marker_hash',
  'old_epoch', 'old_revision', 'new_epoch', 'new_revision', 'pending_at_ms')
redis.call('HSET', op,
  'token', token, 'transition_hash', transition_hash,
  'new_epoch', new_epoch, 'new_revision', new_revision,
  'state', 'committed', 'updated_at_ms', now)
if op_ttl_ms > 0 then redis.call('PEXPIRE', op, op_ttl_ms) end
return {'committed'}
`

// luaRuntimeReadiness 原子核对共享 infrastructure-fault latch、ready marker 和五个关键
// control。返回 active payload/hash 供 Go 侧继续校验 SHA-256，因 Redis Lua 不提供 SHA-256。
// KEYS[1..6]=marker + five critical controls, KEYS[7]=persistent fault latch,
// KEYS[8]=last successful Redis instance reconciliation proof。
const luaRuntimeReadiness = luaRedisInstanceHelpers + `
if redis.call('EXISTS', KEYS[7]) == 1 then return {'breaker_store_unavailable'} end
local instance_matches = redis_instance_proof_matches(KEYS[8])
if instance_matches == nil then return redis.error_reply('invalid Redis instance reconciliation proof') end
if not instance_matches then return {'redis_instance_changed'} end
local marker = KEYS[1]
if redis.call('EXISTS', marker) == 0 then return {'marker_absent'} end
if redis.call('HGET', marker, 'state') ~= 'ready' then return {'marker_not_ready'} end
if redis.call('HGET', marker, 'epoch') ~= ARGV[1]
    or redis.call('HGET', marker, 'revision') ~= ARGV[2]
    or redis.call('HGET', marker, 'marker_hash') ~= ARGV[8] then
  return {'marker_mismatch'}
end

local payloads = {}
for index = 2, 6 do
  local control = KEYS[index]
  local expected_revision = ARGV[index + 1]
  if redis.call('EXISTS', control) == 0 then return {'control_absent', index - 1} end
  local pending = tonumber(redis.call('HGET', control, 'pending_revision')) or 0
  if pending ~= 0 then return {'control_pending', index - 1} end
  if redis.call('HGET', control, 'active_revision') ~= expected_revision then
    return {'control_revision_mismatch', index - 1}
  end
  local payload = redis.call('HGET', control, 'active_payload') or ''
  local payload_hash = redis.call('HGET', control, 'active_payload_hash') or ''
  if payload == '' or payload_hash == '' then return {'control_invalid', index - 1} end
  table.insert(payloads, payload)
  table.insert(payloads, payload_hash)
end
return {'ready', unpack(payloads)}
`

// luaRuntimeFaultClearProof ignores the latch as an admission gate but returns its exact token while
// validating the marker and five critical controls. Go validates each payload SHA-256 before commit.
// KEYS: fault latch, marker, route-rate, channel-rate, global-concurrency, circuit-breaker, routing-balance.
// ARGV[9] is the Redis run_id captured immediately before the reconciliation pass.
const luaRuntimeFaultClearProof = luaRedisInstanceHelpers + `
local current_run_id = redis_server_identity()
if current_run_id == nil then return redis.error_reply('invalid Redis INFO server identity') end
if current_run_id ~= ARGV[9] then return {'redis_instance_changed'} end
local fault_type = redis.call('TYPE', KEYS[1])
if type(fault_type) == 'table' then fault_type = fault_type['ok'] end
if fault_type ~= 'none' and fault_type ~= 'string' then
  return redis.error_reply('WRONGTYPE runtime infrastructure fault latch must be a string')
end
local fault_token = ''
if fault_type == 'string' then fault_token = redis.call('GET', KEYS[1]) or '' end

local marker = KEYS[2]
if redis.call('EXISTS', marker) == 0 then return {'marker_absent'} end
if redis.call('HGET', marker, 'state') ~= 'ready' then return {'marker_not_ready'} end
if redis.call('HGET', marker, 'epoch') ~= ARGV[1]
    or redis.call('HGET', marker, 'revision') ~= ARGV[2]
    or redis.call('HGET', marker, 'marker_hash') ~= ARGV[8] then
  return {'marker_mismatch'}
end

local payloads = {}
for index = 3, 7 do
  local control = KEYS[index]
  local expected_revision = ARGV[index]
  if redis.call('EXISTS', control) == 0 then return {'control_absent', index - 2} end
  local pending = tonumber(redis.call('HGET', control, 'pending_revision')) or 0
  if pending ~= 0 then return {'control_pending', index - 2} end
  if redis.call('HGET', control, 'active_revision') ~= expected_revision then
    return {'control_revision_mismatch', index - 2}
  end
  local payload = redis.call('HGET', control, 'active_payload') or ''
  local payload_hash = redis.call('HGET', control, 'active_payload_hash') or ''
  if payload == '' or payload_hash == '' then return {'control_invalid', index - 2} end
  table.insert(payloads, payload)
  table.insert(payloads, payload_hash)
end
return {'ready', fault_token, unpack(payloads)}
`

// luaRuntimeFaultClearCommit compare-and-deletes only the latch generation proven above. It repeats
// marker/revision/pending checks, compares the exact critical payload/hash pairs, and validates all
// PostgreSQL-derived Origin fences and Channel admission controls in the same atomic execution.
// KEYS[8] is the shared Redis instance proof written only by this successful full-reconciliation
// commit. ARGV[9] is the captured run_id and ARGV[10] is the exact fault token.
const luaRuntimeFaultClearCommit = luaRedisInstanceHelpers + `
local origin_count = tonumber(ARGV[21])
local channel_count = tonumber(ARGV[22])
if origin_count == nil or channel_count == nil or origin_count < 0 or channel_count < 0 or
    origin_count ~= math.floor(origin_count) or channel_count ~= math.floor(channel_count) or
    #KEYS ~= 8 + origin_count + channel_count or
    #ARGV ~= 22 + origin_count * 3 + channel_count * 3 then
  return redis.error_reply('invalid runtime fault clear proof shape')
end
local current_run_id = redis_server_identity()
if current_run_id == nil then return redis.error_reply('invalid Redis INFO server identity') end
if current_run_id ~= ARGV[9] then return {'redis_instance_changed'} end
local fault_type = redis.call('TYPE', KEYS[1])
if type(fault_type) == 'table' then fault_type = fault_type['ok'] end
if fault_type ~= 'none' and fault_type ~= 'string' then
  return redis.error_reply('WRONGTYPE runtime infrastructure fault latch must be a string')
end
local current_fault_token = ''
if fault_type == 'string' then current_fault_token = redis.call('GET', KEYS[1]) or '' end
if current_fault_token ~= ARGV[10] then return {'fault_changed'} end

local marker = KEYS[2]
if redis.call('EXISTS', marker) == 0 then return {'marker_absent'} end
if redis.call('HGET', marker, 'state') ~= 'ready' then return {'marker_not_ready'} end
if redis.call('HGET', marker, 'epoch') ~= ARGV[1]
    or redis.call('HGET', marker, 'revision') ~= ARGV[2]
    or redis.call('HGET', marker, 'marker_hash') ~= ARGV[8] then
  return {'marker_mismatch'}
end

for index = 3, 7 do
  local control = KEYS[index]
  local expected_revision = ARGV[index]
  local proof_index = 11 + (index - 3) * 2
  if redis.call('EXISTS', control) == 0 then return {'control_absent', index - 2} end
  local pending = tonumber(redis.call('HGET', control, 'pending_revision')) or 0
  if pending ~= 0 then return {'control_pending', index - 2} end
  if redis.call('HGET', control, 'active_revision') ~= expected_revision then
    return {'control_revision_mismatch', index - 2}
  end
  if redis.call('HGET', control, 'active_payload') ~= ARGV[proof_index]
      or redis.call('HGET', control, 'active_payload_hash') ~= ARGV[proof_index + 1] then
    return {'control_payload_changed', index - 2}
  end
end

for index = 1, origin_count do
  local key_index = 8 + index
	local arg_index = 23 + (index - 1) * 3
  local origin = KEYS[key_index]
  local origin_type = redis.call('TYPE', origin)
  if type(origin_type) == 'table' then origin_type = origin_type['ok'] end
  if origin_type ~= 'hash' then
    return redis.error_reply('WRONGTYPE runtime origin control must be a hash')
  end
  if redis.call('HGET', origin, 'control_present') ~= '1' or
      redis.call('HGET', origin, 'base_url_revision_state') ~= 'active' or
      redis.call('HGET', origin, 'status_revision_state') ~= 'active' or
      redis.call('HGET', origin, 'base_url_revision') ~= ARGV[arg_index] or
      redis.call('HGET', origin, 'status_revision') ~= ARGV[arg_index + 1] or
      redis.call('HGET', origin, 'effective_status') ~= ARGV[arg_index + 2] or
      redis.call('HEXISTS', origin, 'pending_base_url_revision') == 1 or
      redis.call('HEXISTS', origin, 'base_url_fence_token') == 1 or
      redis.call('HEXISTS', origin, 'base_url_payload_hash') == 1 or
      redis.call('HEXISTS', origin, 'pending_status_revision') == 1 or
      redis.call('HEXISTS', origin, 'pending_effective_status') == 1 or
      redis.call('HEXISTS', origin, 'status_fence_token') == 1 or
      redis.call('HEXISTS', origin, 'status_payload_hash') == 1 then
    return {'origin_control_changed', index}
  end
end

local channel_key_offset = 8 + origin_count
local channel_arg_offset = 23 + origin_count * 3
for index = 1, channel_count do
  local control = KEYS[channel_key_offset + index]
  local arg_index = channel_arg_offset + (index - 1) * 3
  local control_type = redis.call('TYPE', control)
  if type(control_type) == 'table' then control_type = control_type['ok'] end
  if control_type ~= 'hash' then
    return redis.error_reply('WRONGTYPE runtime channel admission control must be a hash')
  end
  if redis.call('HGET', control, 'active_revision') ~= ARGV[arg_index] or
      redis.call('HGET', control, 'active_payload') ~= ARGV[arg_index + 1] or
      redis.call('HGET', control, 'active_payload_hash') ~= ARGV[arg_index + 2] or
      redis.call('HEXISTS', control, 'pending_revision') == 1 or
      redis.call('HEXISTS', control, 'pending_payload_hash') == 1 or
      redis.call('HEXISTS', control, 'pending_payload') == 1 or
      redis.call('HEXISTS', control, 'pending_op_token') == 1 then
    return {'channel_control_changed', index}
  end
end

redis.call('SET', KEYS[8], current_run_id)
if current_fault_token == '' then return {'already_clear'} end
return {'verified'}
`

// luaRuntimeFaultLatchDelete runs only after the Go-local fault generation has also been cleared.
// Keeping the shared latch through the proof commit prevents another Gateway from admitting work in
// the interval between the Redis proof and the local generation CAS.
const luaRuntimeFaultLatchDelete = luaRedisInstanceHelpers + `
local current_run_id = redis_server_identity()
if current_run_id == nil then return redis.error_reply('invalid Redis INFO server identity') end
if current_run_id ~= ARGV[2] then return {'redis_instance_changed'} end

local proof_type = redis.call('TYPE', KEYS[2])
if type(proof_type) == 'table' then proof_type = proof_type['ok'] end
if proof_type ~= 'string' or redis.call('GET', KEYS[2]) ~= current_run_id then
  return {'proof_changed'}
end

local fault_type = redis.call('TYPE', KEYS[1])
if type(fault_type) == 'table' then fault_type = fault_type['ok'] end
if fault_type ~= 'none' and fault_type ~= 'string' then
  return redis.error_reply('WRONGTYPE runtime infrastructure fault latch must be a string')
end
if fault_type == 'none' then return {'already_clear'} end
if redis.call('GET', KEYS[1]) ~= ARGV[1] then return {'fault_changed'} end
redis.call('DEL', KEYS[1])
return {'cleared'}
`
