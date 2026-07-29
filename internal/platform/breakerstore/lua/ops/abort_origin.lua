local ep, op = KEYS[1], KEYS[#KEYS]
local token, payload_hash, ttl_ms = ARGV[1], ARGV[2], tonumber(ARGV[3])
local op_state = read_op(op, token, payload_hash, 'origin', '', '1')
if op_state == 'aborted' then return { 'aborted' } end
if op_state == 'committed' or op_state == 'conflict' or op_state == 'invalid' then return { 'conflict' } end
if ttl_ms == nil or ttl_ms < 1 or key_type(ep) ~= 'hash' then return { 'conflict' } end
if
  redis.call('HGET', ep, 'status_revision_state') ~= 'active'
  or redis.call('HGET', ep, 'origin_revision_state') ~= 'pending'
  or redis.call('HGET', ep, 'origin_fence_token') ~= token
  or redis.call('HGET', ep, 'origin_payload_hash') ~= payload_hash
then
  return { 'conflict' }
end
redis.call('HSET', ep, 'origin_revision_state', 'active')
redis.call('HDEL', ep, 'pending_origin_revision', 'origin_fence_token', 'origin_payload_hash')
reset_origin(ep, now_ms())
for i = 2, #KEYS - 1 do
  redis.call('DEL', KEYS[i])
end
write_terminal_op(op, token, payload_hash, 'origin', '', '1', 'aborted', ttl_ms)
return { 'aborted' }
