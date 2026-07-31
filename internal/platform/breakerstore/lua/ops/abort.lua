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

-- Channel 并发是唯一需要归还的渠道级资源；RPM/RPD/TPM 已不再占用（§1.2/§8），无需精确归还。
if conc_key ~= '' then redis.call('ZREM', conc_key, permit_id) end

-- 释放本 permit 仍持有的 half-open 租约（不释放后来 permit 的租约）。
local function release_probe(state_key, probe_field)
  if redis.call('HGET', permit_key, probe_field) == '1' then
    if redis.call('HGET', state_key, 'half_open_permit_id') == permit_id then
      redis.call('HDEL', state_key, 'half_open_permit_id', 'half_open_lease_until_ms')
    end
  end
end
release_probe(origin_key, 'provider_half_open_probe')
release_probe(channel_key, 'channel_half_open_probe')

local terminal_ttl = tonumber(redis.call('HGET', permit_key, 'terminal_ttl_ms')) or 300000
redis.call(
  'HSET',
  permit_key,
  'status',
  'aborted',
  'terminal_at_ms',
  now,
  'tpm_state',
  'released',
  'request_write_state',
  'not_started',
  'response_headers_received',
  'false',
  'first_token_eligible',
  'false'
)
redis.call('PEXPIRE', permit_key, terminal_ttl)
return { 'aborted' }
