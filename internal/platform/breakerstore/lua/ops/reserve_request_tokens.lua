local function key_type(key)
  local reply = redis.call('TYPE', key)
  if type(reply) == 'table' then return reply['ok'] end
  return reply
end
local marker = KEYS[1]
local token_key = KEYS[2]
local tpm_key = KEYS[3]
if redis.call('EXISTS', KEYS[4]) == 1 then return { 'store_unavailable' } end
local instance_matches = redis_instance_proof_matches(KEYS[5])
if instance_matches == nil then return redis.error_reply('invalid Redis instance reconciliation proof') end
if not instance_matches then return { 'redis_instance_changed' } end
local input_estimate = tonumber(ARGV[1]) or 0
local route_id = ARGV[2]
local user_id = ARGV[3]
local expected_epoch = ARGV[4]
local expected_epoch_rev = ARGV[5]
local max_exact_integer = 9007199254740991

if key_type(marker) ~= 'hash' then return { 'runtime_state_lost' } end
if redis.call('HGET', marker, 'state') ~= 'ready' then return { 'runtime_state_lost' } end
if
  redis.call('HGET', marker, 'epoch') ~= expected_epoch
  or redis.call('HGET', marker, 'revision') ~= expected_epoch_rev
then
  return { 'stale_integrity_epoch' }
end

local token_type = key_type(token_key)
if token_type ~= 'hash' then return { 'unknown_request_admission' } end
if
  redis.call('HGET', token_key, 'runtime_integrity_epoch') ~= expected_epoch
  or redis.call('HGET', token_key, 'runtime_integrity_revision') ~= expected_epoch_rev
then
  return { 'stale_integrity_epoch' }
end
if redis.call('HGET', token_key, 'status') ~= 'active' then return { 'unknown_request_admission' } end
if redis.call('HGET', token_key, 'route_id') ~= route_id or redis.call('HGET', token_key, 'user_id') ~= user_id then
  return { 'conflict' }
end

local state = redis.call('HGET', token_key, 'tpm_state')
if state == 'held' or state == 'limited' then
  local previous = tonumber(redis.call('HGET', token_key, 'tpm_input_estimate')) or -1
  if previous ~= input_estimate then return { 'conflict' } end
  if state == 'held' then return { 'reserved' } end
  return { 'limited' }
end

local eff_tpm_raw = redis.call('HGET', token_key, 'eff_tpm')
local bucket_ttl_raw = redis.call('HGET', token_key, 'bucket_ttl_ms')
if
  eff_tpm_raw == false
  or string.match(eff_tpm_raw, '^%d+$') == nil
  or bucket_ttl_raw == false
  or string.match(bucket_ttl_raw, '^%d+$') == nil
then
  return redis.error_reply('malformed request admission resource values')
end
local eff_tpm = tonumber(eff_tpm_raw)
local bucket_ttl_ms = tonumber(bucket_ttl_raw)
local tpm_type = key_type(tpm_key)
if tpm_type ~= 'none' and tpm_type ~= 'string' then return redis.error_reply('malformed request TPM bucket type') end
local used = 0
if tpm_type == 'string' then
  local raw = redis.call('GET', tpm_key)
  if raw == false or string.match(raw, '^%d+$') == nil then return redis.error_reply('malformed request TPM bucket') end
  local parsed_used = tonumber(raw)
  if parsed_used == nil then return redis.error_reply('malformed request TPM bucket') end
  used = parsed_used
end
if used > max_exact_integer - input_estimate then
  return redis.error_reply('request TPM bucket exceeds Lua exact integer range')
end
if eff_tpm > 0 and used + input_estimate > eff_tpm then
  redis.call(
    'HSET',
    token_key,
    'tpm_state',
    'limited',
    'tpm_input_estimate',
    input_estimate,
    'tpm_terminal_reason',
    'limited'
  )
  return { 'limited' }
end
redis.call('INCRBY', tpm_key, input_estimate)
redis.call('PEXPIRE', tpm_key, bucket_ttl_ms)
redis.call('HSET', token_key, 'tpm_state', 'held', 'tpm_input_estimate', input_estimate, 'tpm_bucket', tpm_key)
return { 'reserved' }
