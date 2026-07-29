local run_id = redis_server_identity()
if run_id == nil then return redis.error_reply('invalid Redis INFO server identity') end
local fault_type = redis.call('TYPE', KEYS[1])
if type(fault_type) == 'table' then fault_type = fault_type['ok'] end
if fault_type ~= 'none' and fault_type ~= 'string' then
  return redis.error_reply('WRONGTYPE runtime infrastructure fault latch must be a string')
end
local fault_token = ''
if fault_type == 'string' then fault_token = redis.call('GET', KEYS[1]) or '' end
return { run_id, fault_token }
