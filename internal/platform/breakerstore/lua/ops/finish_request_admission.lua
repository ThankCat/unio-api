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
local actual_total = ARGV[4]
local terminal_reason = ARGV[5]
local expected_epoch = ARGV[6]
local expected_epoch_rev = ARGV[7]
local now = now_ms()
local max_exact_integer = 9007199254740991

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

local tpm_state = redis.call('HGET', token_key, 'tpm_state')
local tpm_bucket = false
local input_estimate = 0
local tpm_bucket_value = 0
if tpm_state == 'held' then
  tpm_bucket = redis.call('HGET', token_key, 'tpm_bucket')
  local input_raw = redis.call('HGET', token_key, 'tpm_input_estimate')
  if
    type(tpm_bucket) ~= 'string'
    or tpm_bucket == ''
    or type(input_raw) ~= 'string'
    or string.match(input_raw, '^%d+$') == nil
  then
    return { 'runtime_sync_required' }
  end
  local parsed_input = tonumber(input_raw)
  if parsed_input == nil or parsed_input > max_exact_integer then return { 'runtime_sync_required' } end
  input_estimate = parsed_input
  local bucket_type = key_type(tpm_bucket)
  if bucket_type ~= 'none' and bucket_type ~= 'string' then return { 'runtime_sync_required' } end
  if bucket_type == 'string' then
    local bucket_raw = redis.call('GET', tpm_bucket)
    local bucket_value = tonumber(bucket_raw)
    if
      bucket_raw == false
      or string.match(bucket_raw, '^%d+$') == nil
      or bucket_value == nil
      or bucket_value > max_exact_integer
      or bucket_value ~= math.floor(bucket_value)
    then
      return { 'runtime_sync_required' }
    end
    tpm_bucket_value = bucket_value
  end
elseif tpm_state ~= 'none' and tpm_state ~= 'limited' then
  return { 'runtime_sync_required' }
end
if actual_total ~= '' then
  if string.match(actual_total, '^%d+$') == nil then return { 'runtime_sync_required' } end
  local actual_value = tonumber(actual_total)
  if actual_value == nil or actual_value > max_exact_integer or actual_value ~= math.floor(actual_value) then
    return { 'runtime_sync_required' }
  end
  local delta = actual_value - input_estimate
  if
    tpm_state == 'held'
    and redis.call('EXISTS', tpm_bucket) == 1
    and delta > 0
    and tpm_bucket_value > max_exact_integer - delta
  then
    return { 'runtime_sync_required' }
  end
end

-- 释放 route-user 并发（RPM/RPD 作为已接收请求保留，不回退）。
if key_type(conc_key) == 'zset' then redis.call('ZREM', conc_key, rid) end

local function adjust_bucket(bucket_key, delta)
  if bucket_key == false or bucket_key == '' or redis.call('EXISTS', bucket_key) == 0 or delta == 0 then return end
  if delta > 0 then
    redis.call('INCRBY', bucket_key, delta)
    return
  end
  local used = tonumber(redis.call('GET', bucket_key)) or 0
  local next_value = used + delta
  if next_value < 0 then next_value = 0 end
  redis.call('SET', bucket_key, next_value, 'KEEPTTL')
end

if tpm_state == 'held' then
  if actual_total ~= '' then
    local actual = tonumber(actual_total) or 0
    adjust_bucket(tpm_bucket, actual - input_estimate)
    redis.call('HSET', token_key, 'tpm_actual_total', actual, 'tpm_state', 'settled')
  elseif terminal_reason == 'not_reached' then
    adjust_bucket(tpm_bucket, -input_estimate)
    redis.call('HSET', token_key, 'tpm_state', 'released')
  elseif terminal_reason == 'reached_without_usage' or terminal_reason == 'uncertain' then
    redis.call('HSET', token_key, 'tpm_state', 'retained')
  end
end

redis.call(
  'HSET',
  token_key,
  'status',
  'finished',
  'terminal_at_ms',
  now,
  'terminal_result',
  'finished',
  'tpm_terminal_reason',
  terminal_reason
)
redis.call('PEXPIRE', token_key, terminal_ttl)
return { 'finished' }
