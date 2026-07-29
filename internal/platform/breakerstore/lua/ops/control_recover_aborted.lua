local control = KEYS[1]
local op = KEYS[2]
local token = ARGV[1]
local current_rev = tonumber(ARGV[2])
local next_rev = tonumber(ARGV[3])
local pending_hash = ARGV[4]
local current_hash = ARGV[5]
local current_payload = ARGV[6]
local op_ttl_ms = tonumber(ARGV[7])

if next_rev ~= current_rev + 1 then return { 'invalid' } end

if redis.call('EXISTS', op) == 1 then
  local otoken = redis.call('HGET', op, 'token')
  local ohash = redis.call('HGET', op, 'payload_hash')
  local ostate = redis.call('HGET', op, 'state')
  if otoken ~= token or ohash ~= pending_hash then return { 'conflict' } end
  if ostate == 'committed' then return { 'committed_conflict' } end
end

if redis.call('EXISTS', control) == 0 then
  redis.call(
    'HSET',
    control,
    'active_revision',
    current_rev,
    'active_payload_hash',
    current_hash,
    'active_payload',
    current_payload,
    'last_terminal',
    'aborted'
  )
else
  local active_rev = tonumber(redis.call('HGET', control, 'active_revision')) or 0
  local active_hash = redis.call('HGET', control, 'active_payload_hash') or ''
  if active_rev ~= current_rev or active_hash ~= current_hash then return { 'stale', active_rev } end
end

local pending_rev = tonumber(redis.call('HGET', control, 'pending_revision')) or 0
if pending_rev ~= 0 then
  if
    pending_rev ~= next_rev
    or redis.call('HGET', control, 'pending_op_token') ~= token
    or redis.call('HGET', control, 'pending_payload_hash') ~= pending_hash
  then
    return { 'conflict_pending' }
  end
  redis.call('HDEL', control, 'pending_revision', 'pending_payload_hash', 'pending_payload', 'pending_op_token')
end

redis.call('HSET', control, 'last_terminal', 'aborted')
redis.call('HSET', op, 'token', token, 'payload_hash', pending_hash, 'next_revision', next_rev, 'state', 'aborted')
if op_ttl_ms > 0 then redis.call('PEXPIRE', op, op_ttl_ms) end
return { 'aborted', current_rev }
