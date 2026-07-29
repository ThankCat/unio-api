local origin_count = tonumber(ARGV[21])
local channel_count = tonumber(ARGV[22])
if
  origin_count == nil
  or channel_count == nil
  or origin_count < 0
  or channel_count < 0
  or origin_count ~= math.floor(origin_count)
  or channel_count ~= math.floor(channel_count)
  or #KEYS ~= 8 + origin_count + channel_count
  or #ARGV ~= 22 + origin_count * 3 + channel_count * 3
then
  return redis.error_reply('invalid runtime fault clear proof shape')
end
local current_run_id = redis_server_identity()
if current_run_id == nil then return redis.error_reply('invalid Redis INFO server identity') end
if current_run_id ~= ARGV[9] then return { 'redis_instance_changed' } end
local fault_type = redis.call('TYPE', KEYS[1])
if type(fault_type) == 'table' then fault_type = fault_type['ok'] end
if fault_type ~= 'none' and fault_type ~= 'string' then
  return redis.error_reply('WRONGTYPE runtime infrastructure fault latch must be a string')
end
local current_fault_token = ''
if fault_type == 'string' then current_fault_token = redis.call('GET', KEYS[1]) or '' end
if current_fault_token ~= ARGV[10] then return { 'fault_changed' } end

local marker = KEYS[2]
if redis.call('EXISTS', marker) == 0 then return { 'marker_absent' } end
if redis.call('HGET', marker, 'state') ~= 'ready' then return { 'marker_not_ready' } end
if
  redis.call('HGET', marker, 'epoch') ~= ARGV[1]
  or redis.call('HGET', marker, 'revision') ~= ARGV[2]
  or redis.call('HGET', marker, 'marker_hash') ~= ARGV[8]
then
  return { 'marker_mismatch' }
end

for index = 3, 7 do
  local control = KEYS[index]
  local expected_revision = ARGV[index]
  local proof_index = 11 + (index - 3) * 2
  if redis.call('EXISTS', control) == 0 then return { 'control_absent', index - 2 } end
  local pending = tonumber(redis.call('HGET', control, 'pending_revision')) or 0
  if pending ~= 0 then return { 'control_pending', index - 2 } end
  if redis.call('HGET', control, 'active_revision') ~= expected_revision then
    return { 'control_revision_mismatch', index - 2 }
  end
  if
    redis.call('HGET', control, 'active_payload') ~= ARGV[proof_index]
    or redis.call('HGET', control, 'active_payload_hash') ~= ARGV[proof_index + 1]
  then
    return { 'control_payload_changed', index - 2 }
  end
end

for index = 1, origin_count do
  local key_index = 8 + index
  local arg_index = 23 + (index - 1) * 3
  local origin = KEYS[key_index]
  local origin_type = redis.call('TYPE', origin)
  if type(origin_type) == 'table' then origin_type = origin_type['ok'] end
  if origin_type ~= 'hash' then return redis.error_reply('WRONGTYPE runtime provider control must be a hash') end
  if
    redis.call('HGET', origin, 'control_present') ~= '1'
    or redis.call('HGET', origin, 'origin_revision_state') ~= 'active'
    or redis.call('HGET', origin, 'status_revision_state') ~= 'active'
    or redis.call('HGET', origin, 'origin_revision') ~= ARGV[arg_index]
    or redis.call('HGET', origin, 'status_revision') ~= ARGV[arg_index + 1]
    or redis.call('HGET', origin, 'effective_status') ~= ARGV[arg_index + 2]
    or redis.call('HEXISTS', origin, 'pending_origin_revision') == 1
    or redis.call('HEXISTS', origin, 'origin_fence_token') == 1
    or redis.call('HEXISTS', origin, 'origin_payload_hash') == 1
    or redis.call('HEXISTS', origin, 'pending_status_revision') == 1
    or redis.call('HEXISTS', origin, 'pending_effective_status') == 1
    or redis.call('HEXISTS', origin, 'status_fence_token') == 1
    or redis.call('HEXISTS', origin, 'status_payload_hash') == 1
  then
    return { 'origin_control_changed', index }
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
  if
    redis.call('HGET', control, 'active_revision') ~= ARGV[arg_index]
    or redis.call('HGET', control, 'active_payload') ~= ARGV[arg_index + 1]
    or redis.call('HGET', control, 'active_payload_hash') ~= ARGV[arg_index + 2]
    or redis.call('HEXISTS', control, 'pending_revision') == 1
    or redis.call('HEXISTS', control, 'pending_payload_hash') == 1
    or redis.call('HEXISTS', control, 'pending_payload') == 1
    or redis.call('HEXISTS', control, 'pending_op_token') == 1
  then
    return { 'channel_control_changed', index }
  end
end

redis.call('SET', KEYS[8], current_run_id)
if current_fault_token == '' then return { 'already_clear' } end
return { 'verified' }
