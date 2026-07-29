---@diagnostic disable: unused-function, unused-local

local function redis_server_identity()
  local info = redis.call('INFO', 'server')
  if type(info) ~= 'string' then return nil, nil end
  local run_id = string.match(info, '[\r\n]run_id:([^\r\n]+)')
  local version = string.match(info, '[\r\n]redis_version:([^\r\n]+)')
  if
    run_id == nil
    or string.len(run_id) ~= 40
    or string.match(run_id, '^[0-9a-f]+$') == nil
    or version == nil
    or version == ''
  then
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
