local current_run_id = redis_server_identity()
if current_run_id == nil then return redis.error_reply('invalid Redis INFO server identity') end
if current_run_id ~= ARGV[8] then return { 'redis_instance_changed' } end
local fault_type = redis.call('TYPE', KEYS[1])
if type(fault_type) == 'table' then fault_type = fault_type['ok'] end
if fault_type ~= 'none' and fault_type ~= 'string' then
  return redis.error_reply('WRONGTYPE runtime infrastructure fault latch must be a string')
end
local fault_token = ''
if fault_type == 'string' then fault_token = redis.call('GET', KEYS[1]) or '' end

local marker = KEYS[2]
if redis.call('EXISTS', marker) == 0 then return { 'marker_absent' } end
if redis.call('HGET', marker, 'state') ~= 'ready' then return { 'marker_not_ready' } end
if
  redis.call('HGET', marker, 'epoch') ~= ARGV[1]
  or redis.call('HGET', marker, 'revision') ~= ARGV[2]
  or redis.call('HGET', marker, 'marker_hash') ~= ARGV[7]
then
  return { 'marker_mismatch' }
end

local payloads = {}
for index = 3, 6 do
  local control = KEYS[index]
  local expected_revision = ARGV[index]
  if redis.call('EXISTS', control) == 0 then return { 'control_absent', index - 2 } end
  local pending = tonumber(redis.call('HGET', control, 'pending_revision')) or 0
  if pending ~= 0 then return { 'control_pending', index - 2 } end
  if redis.call('HGET', control, 'active_revision') ~= expected_revision then
    return { 'control_revision_mismatch', index - 2 }
  end
  local payload = redis.call('HGET', control, 'active_payload') or ''
  local payload_hash = redis.call('HGET', control, 'active_payload_hash') or ''
  if payload == '' or payload_hash == '' then return { 'control_invalid', index - 2 } end
  table.insert(payloads, payload)
  table.insert(payloads, payload_hash)
end
return { 'ready', fault_token, unpack(payloads) }
