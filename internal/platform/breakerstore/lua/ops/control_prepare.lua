local control = KEYS[1]
local op = KEYS[2]
local token = ARGV[1]
local current_rev = tonumber(ARGV[2])
local next_rev = tonumber(ARGV[3])
local payload_hash = ARGV[4]
local payload = ARGV[5]

-- 幂等/冲突：op 已存在。
if redis.call('EXISTS', op) == 1 then
  local otoken = redis.call('HGET', op, 'token')
  local ohash = redis.call('HGET', op, 'payload_hash')
  if otoken == token and ohash == payload_hash then return { redis.call('HGET', op, 'state') } end
  return { 'conflict' }
end

if next_rev ~= current_rev + 1 then return { 'invalid' } end

local active_rev = tonumber(redis.call('HGET', control, 'active_revision')) or 0
if active_rev ~= current_rev then return { 'stale', active_rev } end
local pending_rev = tonumber(redis.call('HGET', control, 'pending_revision')) or 0
if pending_rev ~= 0 then return { 'conflict_pending' } end

redis.call(
  'HSET',
  control,
  'pending_revision',
  next_rev,
  'pending_payload_hash',
  payload_hash,
  'pending_payload',
  payload,
  'pending_op_token',
  token
)
redis.call('HSET', op, 'token', token, 'payload_hash', payload_hash, 'next_revision', next_rev, 'state', 'prepared')
return { 'prepared' }
