local function now_ms()
  local t = redis.call('TIME')
  return tonumber(t[1]) * 1000 + math.floor(tonumber(t[2]) / 1000)
end
local function key_type(key)
  local reply = redis.call('TYPE', key)
  if type(reply) == 'table' then return reply['ok'] end
  return reply
end
local marker = KEYS[1]
local token_key = KEYS[2]
local conc_key = KEYS[3]
local rid = ARGV[1]
local route_id = ARGV[2]
local user_id = ARGV[3]
local expected_epoch = ARGV[4]
local expected_epoch_rev = ARGV[5]
local now = now_ms()

if key_type(marker) ~= 'hash' then return { 'runtime_state_lost' } end
if redis.call('HGET', marker, 'state') ~= 'ready' then return { 'runtime_state_lost' } end
if
  redis.call('HGET', marker, 'epoch') ~= expected_epoch
  or redis.call('HGET', marker, 'revision') ~= expected_epoch_rev
then
  return { 'stale_integrity_epoch' }
end

if key_type(token_key) ~= 'hash' then return { 'unknown_request_admission' } end
if
  redis.call('HGET', token_key, 'runtime_integrity_epoch') ~= expected_epoch
  or redis.call('HGET', token_key, 'runtime_integrity_revision') ~= expected_epoch_rev
then
  return { 'stale_integrity_epoch' }
end
if
  redis.call('HGET', token_key, 'route_id') ~= route_id
  or redis.call('HGET', token_key, 'user_id') ~= user_id
  or redis.call('HGET', token_key, 'conc_key') ~= conc_key
then
  return { 'conflict' }
end
if redis.call('HGET', token_key, 'status') ~= 'active' then return { 'terminal' } end

local lease_raw = redis.call('HGET', token_key, 'lease_until_ms')
local ttl_raw = redis.call('HGET', token_key, 'lease_ttl_ms')
local terminal_ttl_raw = redis.call('HGET', token_key, 'terminal_ttl_ms')
if
  type(lease_raw) ~= 'string'
  or string.match(lease_raw, '^%d+$') == nil
  or type(ttl_raw) ~= 'string'
  or string.match(ttl_raw, '^%d+$') == nil
  or type(terminal_ttl_raw) ~= 'string'
  or string.match(terminal_ttl_raw, '^%d+$') == nil
then
  return { 'runtime_sync_required' }
end
local lease_until = tonumber(lease_raw)
local ttl = tonumber(ttl_raw)
local terminal_ttl = tonumber(terminal_ttl_raw)
if lease_until == nil or ttl == nil or terminal_ttl == nil or ttl <= 0 or terminal_ttl < ttl then
  return { 'runtime_sync_required' }
end
if now >= lease_until then return { 'expired' } end
if key_type(conc_key) ~= 'zset' then return { 'runtime_sync_required' } end
if redis.call('ZSCORE', conc_key, rid) == false then return { 'expired' } end

local new_lease = now + ttl
redis.call('HSET', token_key, 'lease_until_ms', new_lease)
redis.call('PEXPIRE', token_key, new_lease - now + terminal_ttl)
redis.call('ZADD', conc_key, new_lease, rid)
redis.call('PEXPIRE', conc_key, new_lease - now + terminal_ttl)
return { 'renewed', new_lease }
