package breakerstore

// Redis restarts and Sentinel failovers produce a new run_id. The last fully reconciled run_id is
// stored in a shared proof key; every new-admission gate checks the current identity atomically with
// its normal Redis reads and writes.
const luaRedisInstanceHelpers = `
local function redis_server_identity()
  local info = redis.call('INFO', 'server')
  if type(info) ~= 'string' then return nil, nil end
  local run_id = string.match(info, '[\r\n]run_id:([^\r\n]+)')
  local version = string.match(info, '[\r\n]redis_version:([^\r\n]+)')
  if run_id == nil or string.len(run_id) ~= 40 or string.match(run_id, '^[0-9a-f]+$') == nil or
      version == nil or version == '' then
    return nil, nil
  end
  return run_id, version
end

local function redis_instance_proof_matches(proof_key)
  local run_id, version = redis_server_identity()
  if run_id == nil then return nil, nil, nil end
  local proof_type = redis.call('TYPE', proof_key)
  if type(proof_type) == 'table' then proof_type = proof_type['ok'] end
  if proof_type == 'none' then return false, run_id, version end
  if proof_type ~= 'string' then return nil, run_id, version end
  return redis.call('GET', proof_key) == run_id, run_id, version
end
`

const luaRedisServerIdentity = luaRedisInstanceHelpers + `
local run_id, version = redis_server_identity()
if run_id == nil then return redis.error_reply('invalid Redis INFO server identity') end
return {run_id, version}
`

// luaBeginRuntimeReconciliation captures current Redis identity and the exact shared latch token in
// one linearizable read. A restart before clear changes run_id and invalidates this generation.
const luaBeginRuntimeReconciliation = luaRedisInstanceHelpers + `
local run_id = redis_server_identity()
if run_id == nil then return redis.error_reply('invalid Redis INFO server identity') end
local fault_type = redis.call('TYPE', KEYS[1])
if type(fault_type) == 'table' then fault_type = fault_type['ok'] end
if fault_type ~= 'none' and fault_type ~= 'string' then
  return redis.error_reply('WRONGTYPE runtime infrastructure fault latch must be a string')
end
local fault_token = ''
if fault_type == 'string' then fault_token = redis.call('GET', KEYS[1]) or '' end
return {run_id, fault_token}
`
