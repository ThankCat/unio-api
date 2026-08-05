-- finish_request_admission.lua 是 request token 的唯一终态：释放 route-user 并发并关闭 token。
--
-- RPM/RPD 作为「已经接收过的请求」保留，不回退。这里没有任何 token 对账：
-- TPM 不再是准入维度，观测由独立的 obs:tpm 分钟桶承担（§8）。
--
-- KEYS[1] = 完整性 marker
-- KEYS[2] = request token hash
-- KEYS[3] = route-user 并发 zset
-- ARGV[1] = request admission id
-- ARGV[2] = route_id
-- ARGV[3] = user_id
-- ARGV[4] = expected integrity epoch
-- ARGV[5] = expected integrity revision
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

local terminal_ttl_raw = redis.call('HGET', token_key, 'terminal_ttl_ms')
if type(terminal_ttl_raw) ~= 'string' or string.match(terminal_ttl_raw, '^%d+$') == nil then
  return { 'runtime_sync_required' }
end
local terminal_ttl = tonumber(terminal_ttl_raw)
if terminal_ttl == nil or terminal_ttl <= 0 then return { 'runtime_sync_required' } end

-- 释放 route-user 并发（RPM/RPD 作为已接收请求保留，不回退）。
if key_type(conc_key) == 'zset' then redis.call('ZREM', conc_key, rid) end

redis.call(
  'HSET',
  token_key,
  'status',
  'finished',
  'terminal_at_ms',
  now,
  'terminal_result',
  'finished'
)
redis.call('PEXPIRE', token_key, terminal_ttl)
return { 'finished' }
