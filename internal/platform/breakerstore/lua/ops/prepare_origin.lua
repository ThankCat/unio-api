local ep, op = KEYS[1], KEYS[2]
local current, next_rev, token, payload_hash = ARGV[1], ARGV[2], ARGV[3], ARGV[4]
local op_state = read_op(op, token, payload_hash, 'origin', '', '1')
if op_state == 'committed' or op_state == 'aborted' then return { op_state } end
if op_state == 'conflict' or op_state == 'invalid' then return { 'conflict' } end
if key_type(ep) ~= 'hash' or redis.call('HGET', ep, 'control_present') ~= '1' then return { 'absent' } end
if tonumber(current) == nil or tonumber(next_rev) ~= tonumber(current) + 1 then return { 'invalid' } end
if op_state == 'prepared' then
  if
    redis.call('HGET', ep, 'origin_revision_state') == 'pending'
    and redis.call('HGET', ep, 'origin_fence_token') == token
    and redis.call('HGET', ep, 'origin_payload_hash') == payload_hash
    and redis.call('HGET', ep, 'pending_origin_revision') == next_rev
  then
    return { 'prepared' }
  end
  return { 'conflict' }
end
if
  redis.call('HGET', ep, 'origin_revision_state') ~= 'active'
  or redis.call('HGET', ep, 'status_revision_state') ~= 'active'
then
  return { 'conflict' }
end
if redis.call('HGET', ep, 'origin_revision') ~= current then return { 'stale' } end
local gen = (tonumber(redis.call('HGET', ep, 'origin_fence_generation')) or 0) + 1
redis.call(
  'HSET',
  ep,
  'origin_revision_state',
  'pending',
  'pending_origin_revision',
  next_rev,
  'origin_fence_token',
  token,
  'origin_payload_hash',
  payload_hash,
  'origin_fence_generation',
  gen
)
write_prepared_op(op, token, payload_hash, 'origin', '', '1')
return { 'prepared' }
