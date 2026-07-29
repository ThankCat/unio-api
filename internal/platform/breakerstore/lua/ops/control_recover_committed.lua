local control = KEYS[1]
local op = KEYS[2]
local token = ARGV[1]
local current_rev = tonumber(ARGV[2])
local next_rev = tonumber(ARGV[3])
local payload_hash = ARGV[4]
local payload = ARGV[5]
local op_ttl_ms = tonumber(ARGV[6])

if next_rev ~= current_rev + 1 then return { 'invalid' } end

if redis.call('EXISTS', op) == 1 then
  local otoken = redis.call('HGET', op, 'token')
  local ohash = redis.call('HGET', op, 'payload_hash')
  local ostate = redis.call('HGET', op, 'state')
  if otoken ~= token or ohash ~= payload_hash then return { 'conflict' } end
  if ostate == 'aborted' then return { 'aborted_conflict' } end
end

local exists = redis.call('EXISTS', control)
local active_rev = tonumber(redis.call('HGET', control, 'active_revision')) or 0
local active_hash = redis.call('HGET', control, 'active_payload_hash') or ''
local pending_rev = tonumber(redis.call('HGET', control, 'pending_revision')) or 0

if exists == 1 and active_rev ~= current_rev and active_rev ~= next_rev then return { 'stale', active_rev } end
if active_rev == next_rev and active_hash ~= payload_hash then return { 'conflict' } end
if pending_rev ~= 0 then
  if
    pending_rev ~= next_rev
    or redis.call('HGET', control, 'pending_op_token') ~= token
    or redis.call('HGET', control, 'pending_payload_hash') ~= payload_hash
  then
    return { 'conflict_pending' }
  end
end

redis.call(
  'HSET',
  control,
  'active_revision',
  next_rev,
  'active_payload_hash',
  payload_hash,
  'active_payload',
  payload,
  'last_terminal',
  'committed'
)
redis.call('HDEL', control, 'pending_revision', 'pending_payload_hash', 'pending_payload', 'pending_op_token')
redis.call('HSET', op, 'token', token, 'payload_hash', payload_hash, 'next_revision', next_rev, 'state', 'committed')
if op_ttl_ms > 0 then redis.call('PEXPIRE', op, op_ttl_ms) end
return { 'committed', next_rev }
