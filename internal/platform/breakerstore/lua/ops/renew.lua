local function now_ms()
  local t = redis.call('TIME')
  return tonumber(t[1]) * 1000 + math.floor(tonumber(t[2]) / 1000)
end
local permit_key = KEYS[2]
local origin_key = KEYS[3]
local channel_key = KEYS[4]
local conc_key = KEYS[5]
local permit_id = ARGV[1]
local now = now_ms()

local lifecycle_guard = validate_attempt_permit_lifecycle()
if lifecycle_guard ~= nil then return { lifecycle_guard } end
if redis.call('HGET', permit_key, 'status') ~= 'active' then return { 'terminal_conflict' } end
local lease_until = tonumber(redis.call('HGET', permit_key, 'lease_until_ms')) or 0
if now >= lease_until then return { 'expired' } end

local ttl = tonumber(redis.call('HGET', permit_key, 'permit_ttl_ms')) or 30000
local terminal_ttl = tonumber(redis.call('HGET', permit_key, 'terminal_ttl_ms')) or 300000
local new_lease = now + ttl
redis.call('HSET', permit_key, 'lease_until_ms', new_lease)
redis.call('PEXPIRE', permit_key, new_lease - now + terminal_ttl)

if conc_key ~= '' then
  if redis.call('ZSCORE', conc_key, permit_id) ~= false then
    redis.call('ZADD', conc_key, new_lease, permit_id)
    redis.call('PEXPIRE', conc_key, new_lease - now + terminal_ttl)
  end
end

local origin_fence_current = true
if redis.call('HGET', permit_key, 'origin_control_enforced') == '1' then
  origin_fence_current = redis.call('HGET', origin_key, 'origin_revision_state') == 'active'
    and redis.call('HGET', origin_key, 'status_revision_state') == 'active'
    and redis.call('HGET', origin_key, 'origin_fence_generation') == redis.call(
      'HGET',
      permit_key,
      'origin_fence_generation'
    )
    and redis.call('HGET', origin_key, 'status_fence_generation') == redis.call(
      'HGET',
      permit_key,
      'status_fence_generation'
    )
    and redis.call('HGET', origin_key, 'origin_revision') == redis.call('HGET', permit_key, 'origin_revision')
    and redis.call('HGET', origin_key, 'status_revision') == redis.call('HGET', permit_key, 'status_revision')
end
local channel_fence_current = origin_fence_current
  and redis.call('HGET', channel_key, 'channel_config_revision') == redis.call(
    'HGET',
    permit_key,
    'channel_config_revision'
  )
  and redis.call('HGET', channel_key, 'provider_id') == redis.call('HGET', permit_key, 'provider_id')
  and redis.call('HGET', channel_key, 'origin_revision') == redis.call('HGET', permit_key, 'origin_revision')
  and redis.call('HGET', channel_key, 'status_revision') == redis.call('HGET', permit_key, 'status_revision')

local function renew_probe(state_key, gen_field, probe_field, fence_current)
  if fence_current and redis.call('HGET', permit_key, probe_field) == '1' then
    local permit_gen = tonumber(redis.call('HGET', permit_key, gen_field)) or 0
    local cur_gen = tonumber(redis.call('HGET', state_key, 'state_generation')) or -1
    if cur_gen == permit_gen and redis.call('HGET', state_key, 'half_open_permit_id') == permit_id then
      redis.call('HSET', state_key, 'half_open_lease_until_ms', new_lease)
    end
  end
end
renew_probe(origin_key, 'provider_state_generation', 'provider_half_open_probe', origin_fence_current)
renew_probe(channel_key, 'channel_state_generation', 'channel_half_open_probe', channel_fence_current)

return { 'renewed', new_lease }
