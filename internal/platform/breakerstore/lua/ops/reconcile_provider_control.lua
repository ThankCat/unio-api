local ep = KEYS[1]
local typ = key_type(ep)
if
  tonumber(ARGV[1]) == nil
  or tonumber(ARGV[1]) < 1
  or tonumber(ARGV[2]) == nil
  or tonumber(ARGV[2]) < 1
  or not valid_status(ARGV[3])
then
  return redis.error_reply('invalid provider control')
end
local unchanged = typ == 'hash'
  and redis.call('HGET', ep, 'control_present') == '1'
  and redis.call('HGET', ep, 'origin_revision') == ARGV[1]
  and redis.call('HGET', ep, 'status_revision') == ARGV[2]
  and redis.call('HGET', ep, 'effective_status') == ARGV[3]
  and redis.call('HGET', ep, 'origin_revision_state') == 'active'
  and redis.call('HGET', ep, 'status_revision_state') == 'active'
  and redis.call('HEXISTS', ep, 'pending_origin_revision') == 0
  and redis.call('HEXISTS', ep, 'origin_fence_token') == 0
  and redis.call('HEXISTS', ep, 'origin_payload_hash') == 0
  and redis.call('HEXISTS', ep, 'pending_status_revision') == 0
  and redis.call('HEXISTS', ep, 'pending_effective_status') == 0
  and redis.call('HEXISTS', ep, 'status_fence_token') == 0
  and redis.call('HEXISTS', ep, 'status_payload_hash') == 0
if unchanged then return { 'unchanged' } end
if typ ~= 'hash' then
  redis.call('DEL', ep)
  restore_origin(ep, ARGV[1], ARGV[2], ARGV[3], now_ms())
else
  redis.call(
    'HSET',
    ep,
    'control_present',
    '1',
    'effective_status',
    ARGV[3],
    'origin_revision',
    ARGV[1],
    'status_revision',
    ARGV[2],
    'origin_revision_state',
    'active',
    'status_revision_state',
    'active',
    'origin_fence_generation',
    (tonumber(redis.call('HGET', ep, 'origin_fence_generation')) or 0) + 1,
    'status_fence_generation',
    (tonumber(redis.call('HGET', ep, 'status_fence_generation')) or 0) + 1
  )
  redis.call(
    'HDEL',
    ep,
    'pending_origin_revision',
    'origin_fence_token',
    'origin_payload_hash',
    'pending_status_revision',
    'pending_effective_status',
    'status_fence_token',
    'status_payload_hash'
  )
end
return { 'reconciled' }
