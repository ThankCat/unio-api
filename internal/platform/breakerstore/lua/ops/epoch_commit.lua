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

if redis.call('EXISTS', marker) == 0 then return { 'conflict' } end
local state = redis.call('HGET', marker, 'state') or ''

if
  state == 'ready'
  and redis.call('HGET', marker, 'epoch') == new_epoch
  and redis.call('HGET', marker, 'revision') == new_revision
  and redis.call('HGET', marker, 'marker_hash') == new_ready_hash
  and redis.call('HGET', marker, 'last_operation_token') == token
  and redis.call('HGET', marker, 'last_transition_hash') == transition_hash
then
  return { 'committed' }
end

if
  state ~= 'pending'
  or redis.call('HGET', marker, 'operation_token') ~= token
  or redis.call('HGET', marker, 'transition_hash') ~= transition_hash
  or redis.call('HGET', marker, 'new_epoch') ~= new_epoch
  or redis.call('HGET', marker, 'new_revision') ~= new_revision
then
  return { 'conflict' }
end

if redis.call('EXISTS', op) == 1 then
  if
    redis.call('HGET', op, 'token') ~= token
    or redis.call('HGET', op, 'transition_hash') ~= transition_hash
    or redis.call('HGET', op, 'state') ~= 'pending'
  then
    return { 'conflict' }
  end
end

redis.call(
  'HSET',
  marker,
  'state',
  'ready',
  'epoch',
  new_epoch,
  'revision',
  new_revision,
  'marker_hash',
  new_ready_hash,
  'activated_at_ms',
  now,
  'last_operation_token',
  token,
  'last_transition_hash',
  transition_hash
)
redis.call(
  'HDEL',
  marker,
  'operation_token',
  'transition_hash',
  'expected_marker_hash',
  'old_epoch',
  'old_revision',
  'new_epoch',
  'new_revision',
  'pending_at_ms'
)
redis.call(
  'HSET',
  op,
  'token',
  token,
  'transition_hash',
  transition_hash,
  'new_epoch',
  new_epoch,
  'new_revision',
  new_revision,
  'state',
  'committed',
  'updated_at_ms',
  now
)
if op_ttl_ms > 0 then redis.call('PEXPIRE', op, op_ttl_ms) end
return { 'committed' }
