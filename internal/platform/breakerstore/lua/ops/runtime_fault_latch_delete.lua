local current_run_id = redis_server_identity()
if current_run_id == nil then return redis.error_reply('invalid Redis INFO server identity') end
if current_run_id ~= ARGV[2] then return { 'redis_instance_changed' } end

local proof_type = redis.call('TYPE', KEYS[2])
if type(proof_type) == 'table' then proof_type = proof_type['ok'] end
if proof_type ~= 'string' or redis.call('GET', KEYS[2]) ~= current_run_id then return { 'proof_changed' } end

local fault_type = redis.call('TYPE', KEYS[1])
if type(fault_type) == 'table' then fault_type = fault_type['ok'] end
if fault_type ~= 'none' and fault_type ~= 'string' then
  return redis.error_reply('WRONGTYPE runtime infrastructure fault latch must be a string')
end
if fault_type == 'none' then return { 'already_clear' } end
if redis.call('GET', KEYS[1]) ~= ARGV[1] then return { 'fault_changed' } end
redis.call('DEL', KEYS[1])
return { 'cleared' }
