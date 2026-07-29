local count = tonumber(ARGV[1])
if count == nil or count < 1 or count ~= math.floor(count) or #KEYS ~= 7 + count * 6 or #ARGV ~= 8 + count * 9 then
  return redis.error_reply('invalid snapshot batch shape')
end

local marker = KEYS[1]
local channel_rate_ctl = KEYS[2]
local global_conc_ctl = KEYS[3]
local breaker_ctl = KEYS[4]
local balance_ctl = KEYS[5]
local expected_epoch = ARGV[3]
local expected_epoch_revision = ARGV[4]

if redis.call('EXISTS', KEYS[#KEYS - 1]) == 1 then return { 'error', 'breaker_store_unavailable' } end
local instance_matches = redis_instance_proof_matches(KEYS[#KEYS])
if instance_matches == nil then return redis.error_reply('invalid Redis instance reconciliation proof') end
if not instance_matches then return { 'error', 'redis_instance_changed' } end

if redis_key_type(marker) ~= 'hash' or redis.call('HGET', marker, 'state') ~= 'ready' then
  return { 'error', 'runtime_state_lost' }
end
if
  redis.call('HGET', marker, 'epoch') ~= expected_epoch
  or redis.call('HGET', marker, 'revision') ~= expected_epoch_revision
then
  return { 'error', 'stale_integrity_epoch' }
end

local function require_control(key, expected, parser, stale_reason)
  local value, state = read_new_admission_control(key, expected, parser)
  if value ~= nil then return value, nil end
  if state == 'stale_setting_revision' then return nil, stale_reason end
  return nil, state
end

local channel_rate, reason =
  require_control(channel_rate_ctl, tonumber(ARGV[5]), parse_rate_limit_defaults_payload, 'stale_admission_revision')
if channel_rate == nil then return { 'error', reason } end
local global_conc
global_conc, reason =
  require_control(global_conc_ctl, tonumber(ARGV[6]), parse_global_concurrency_payload, 'stale_admission_revision')
if global_conc == nil then return { 'error', reason } end
local breaker
breaker, reason =
  require_control(breaker_ctl, tonumber(ARGV[7]), parse_circuit_breaker_payload, 'stale_setting_revision')
if breaker == nil then return { 'error', reason } end
local balance
balance, reason =
  require_control(balance_ctl, tonumber(ARGV[8]), parse_routing_balance_payload, 'stale_setting_revision')
if balance == nil then return { 'error', reason } end

local t = redis.call('TIME')
local now = tonumber(t[1]) * 1000 + math.floor(tonumber(t[2]) / 1000)
local minute_bucket = math.floor(now / 60000)
local day_bucket = math.floor(now / 86400000)

local function read_snapshot(state_key)
  if redis.call('EXISTS', state_key) == 0 then return { 'absent', now } end
  local fields = redis.call('HGETALL', state_key)
  local state = redis.call('HGET', state_key, 'state') or 'closed'
  local window_started_at_ms = tonumber(redis.call('HGET', state_key, 'window_started_at_ms'))
  if state == 'closed' and window_started_at_ms ~= nil and now - window_started_at_ms >= breaker.window_ms then
    -- SnapshotMany is read-only: make an expired eligible window neutral in the returned copy only.
    -- TTFT fields remain untouched because their EWMA lifetime is independent from the breaker window.
    for index = 1, #fields, 2 do
      if fields[index] == 'eligible_successes' or fields[index] == 'eligible_failures' then fields[index + 1] = '0' end
    end
  end
  local open_until = tonumber(redis.call('HGET', state_key, 'open_until_ms')) or 0
  local remaining = 0
  if open_until > now then remaining = open_until - now end
  return { 'present', now, remaining, fields }
end

local rows = {}
for candidate = 1, count do
  local key_offset = 5 + (candidate - 1) * 6
  local arg_offset = 8 + (candidate - 1) * 9
  local origin_key = KEYS[key_offset + 1]
  local channel_key = KEYS[key_offset + 2]
  local concurrency_key = KEYS[key_offset + 3]
  local cooldown_key = KEYS[key_offset + 4]
  local permission_key = KEYS[key_offset + 5]
  local channel_ctl = KEYS[key_offset + 6]
  local expected_origin_revision = ARGV[arg_offset + 3]
  local expected_status_revision = ARGV[arg_offset + 4]
  local expected_config_revision = ARGV[arg_offset + 5]
  local expected_channel_revision = tonumber(ARGV[arg_offset + 6])
  local rpm_key = ARGV[arg_offset + 7] .. minute_bucket
  local rpd_key = ARGV[arg_offset + 8] .. day_bucket
  local tpm_key = ARGV[arg_offset + 9] .. minute_bucket

  local origin_type = redis_key_type(origin_key)
  local channel_type = redis_key_type(channel_key)
  if (origin_type ~= 'none' and origin_type ~= 'hash') or (channel_type ~= 'none' and channel_type ~= 'hash') then
    return redis.error_reply('WRONGTYPE snapshot state key must be a hash')
  end

  local channel_limits
  channel_limits, reason =
    require_control(channel_ctl, expected_channel_revision, parse_channel_admission_payload, 'stale_admission_revision')
  if channel_limits == nil then return { 'error', reason } end
  local effective_concurrency = resolve_channel_limit(channel_limits.concurrency, global_conc.channel_limit)
  local effective_rpm = resolve_channel_limit(channel_limits.rpm, channel_rate.rpm)
  local effective_rpd = resolve_channel_limit(channel_limits.rpd, channel_rate.rpd)
  local effective_tpm = resolve_channel_limit(channel_limits.tpm, channel_rate.tpm)

  local concurrency_used = active_zset_count(concurrency_key, now)
  local rpm_used = read_nonnegative_counter(rpm_key)
  local rpd_used = read_nonnegative_counter(rpd_key)
  local tpm_used = read_nonnegative_counter(tpm_key)
  if concurrency_used == nil or rpm_used == nil or rpd_used == nil or tpm_used == nil then
    return { 'error', 'runtime_sync_required' }
  end

  local cooldown_remaining = 0
  local cooldown_type = redis_key_type(cooldown_key)
  if cooldown_type ~= 'none' and cooldown_type ~= 'hash' then return { 'error', 'runtime_sync_required' } end
  if cooldown_type == 'hash' then
    local until_ms = tonumber(redis.call('HGET', cooldown_key, 'until_ms'))
    if until_ms == nil then return { 'error', 'runtime_sync_required' } end
    if until_ms > now then cooldown_remaining = until_ms - now end
  end

  local permission_paused = 0
  local permission_state = 'absent'
  local permission_type = redis_key_type(permission_key)
  if permission_type ~= 'none' and permission_type ~= 'hash' then return { 'error', 'runtime_sync_required' } end
  if permission_type == 'hash' then
    permission_state = redis.call('HGET', permission_key, 'recheck_state') or ''
    if permission_state == '' then return { 'error', 'runtime_sync_required' } end
    if
      permission_state ~= 'cleared'
      and redis.call('HGET', permission_key, 'channel_config_revision') == expected_config_revision
      and redis.call('HGET', permission_key, 'origin_revision') == expected_origin_revision
      and redis.call('HGET', permission_key, 'status_revision') == expected_status_revision
    then
      permission_paused = 1
    end
  end

  rows[#rows + 1] = {
    cooldown_remaining,
    permission_paused,
    permission_state,
    concurrency_used,
    effective_concurrency,
    rpm_used,
    effective_rpm,
    rpd_used,
    effective_rpd,
    tpm_used,
    effective_tpm,
    read_snapshot(origin_key),
    read_snapshot(channel_key),
    redis.call('HGET', channel_ctl, 'active_payload'),
    redis.call('HGET', channel_ctl, 'active_payload_hash'),
  }
end

local breaker_enabled = 0
if breaker.enabled then breaker_enabled = 1 end
local control_proofs = {
  {
    redis.call('HGET', channel_rate_ctl, 'active_payload'),
    redis.call('HGET', channel_rate_ctl, 'active_payload_hash'),
  },
  { redis.call('HGET', global_conc_ctl, 'active_payload'), redis.call('HGET', global_conc_ctl, 'active_payload_hash') },
  { redis.call('HGET', breaker_ctl, 'active_payload'), redis.call('HGET', breaker_ctl, 'active_payload_hash') },
  { redis.call('HGET', balance_ctl, 'active_payload'), redis.call('HGET', balance_ctl, 'active_payload_hash') },
}
return {
  'ok',
  now,
  tonumber(ARGV[8]),
  balance.ttft_target_ms,
  tostring(balance.ttft_weight),
  balance.economic_weight_pct,
  balance.health_weight_pct,
  balance.capacity_weight_pct,
  balance.priority_weight_pct,
  breaker_enabled,
  rows,
  control_proofs,
  tostring(balance.cost_weight),
  tostring(balance.minimum_routing_factor),
}
