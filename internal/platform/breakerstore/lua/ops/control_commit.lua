local control = KEYS[1]
local op = KEYS[2]
local token = ARGV[1]
local payload_hash = ARGV[2]
local op_ttl_ms = tonumber(ARGV[3])

if redis.call('EXISTS', op) == 0 then return { 'unknown_op' } end
local state = redis.call('HGET', op, 'state')
if state == 'committed' then return { 'committed', tonumber(redis.call('HGET', control, 'active_revision')) or 0 } end
if state == 'aborted' then return { 'aborted_conflict' } end
if redis.call('HGET', op, 'token') ~= token or redis.call('HGET', op, 'payload_hash') ~= payload_hash then
  return { 'conflict' }
end
if
  redis.call('HGET', control, 'pending_op_token') ~= token
  or redis.call('HGET', control, 'pending_payload_hash') ~= payload_hash
then
  return { 'conflict' }
end

local next_rev = tonumber(redis.call('HGET', control, 'pending_revision'))
redis.call(
  'HSET',
  control,
  'active_revision',
  next_rev,
  'active_payload_hash',
  redis.call('HGET', control, 'pending_payload_hash'),
  'active_payload',
  redis.call('HGET', control, 'pending_payload'),
  'last_terminal',
  'committed'
)
redis.call('HDEL', control, 'pending_revision', 'pending_payload_hash', 'pending_payload', 'pending_op_token')
redis.call('HSET', op, 'state', 'committed')
if op_ttl_ms > 0 then redis.call('PEXPIRE', op, op_ttl_ms) end
return { 'committed', next_rev }
