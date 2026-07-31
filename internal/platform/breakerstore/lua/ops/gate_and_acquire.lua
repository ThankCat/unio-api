local function now_ms()
  local t = redis.call('TIME')
  return tonumber(t[1]) * 1000 + math.floor(tonumber(t[2]) / 1000)
end

local origin_key = KEYS[1]
local channel_key = KEYS[2]
local conc_key = KEYS[3]
local permit_key = KEYS[4]
local cooldown_key = KEYS[5]
local permission_key = KEYS[6]
local channel_capacity_ctl = KEYS[7]
local global_conc_ctl = KEYS[8]
local breaker_ctl = KEYS[9]
local integrity_marker = KEYS[10]
local request_admission_key = KEYS[11]
local fault_latch = KEYS[12]
local instance_proof = KEYS[13]
local route_channel_rpd_key = KEYS[14]

local permit_id = ARGV[1]
local fingerprint = ARGV[2]
local request_admission_id = ARGV[3]
local provider_id = ARGV[4]
local channel_id = ARGV[5]
local origin_revision = ARGV[6]
local status_revision = ARGV[7]
local channel_config_rev = ARGV[8]
local model_id = ARGV[9]
local upstream_endpoint = ARGV[10]
local request_mode = ARGV[11]
local expected_channel_capacity_rev = tonumber(ARGV[12])
local expected_global_conc_rev = tonumber(ARGV[13])
local expected_breaker_rev = tonumber(ARGV[14])
local input_estimate = tonumber(ARGV[15]) or 0
local expected_integrity_epoch = ARGV[16]
local expected_integrity_revision = ARGV[17]
-- Provider control 围栏校验开关（enforce=1 时要求 control 存在、effective_status=enabled、无 pending、revision 匹配，§5.3.2）。
local enforce_origin_control = tonumber(ARGV[18])

if redis.call('EXISTS', fault_latch) == 1 then return { 'denied', 'breaker_store_unavailable' } end
local instance_matches = redis_instance_proof_matches(instance_proof)
if instance_matches == nil then return redis.error_reply('invalid Redis instance reconciliation proof') end
if not instance_matches then return { 'denied', 'redis_instance_changed' } end
local now = now_ms()

-- PostgreSQL snapshot、Redis marker 与 active request token 必须属于同一完整性 epoch。
-- request token 还必须已用本次相同 estimate 完成 Reserve，才能取得候选级资源。
if redis_key_type(integrity_marker) ~= 'hash' or redis.call('HGET', integrity_marker, 'state') ~= 'ready' then
  return { 'denied', 'runtime_state_lost' }
end
if
  redis.call('HGET', integrity_marker, 'epoch') ~= expected_integrity_epoch
  or redis.call('HGET', integrity_marker, 'revision') ~= expected_integrity_revision
then
  return { 'denied', 'stale_integrity_epoch' }
end
local request_admission_type = redis_key_type(request_admission_key)
if request_admission_type == 'none' then return { 'denied', 'unknown_request_admission' } end
if request_admission_type ~= 'hash' then return { 'denied', 'runtime_sync_required' } end
if redis.call('HGET', request_admission_key, 'status') ~= 'active' then
  return { 'denied', 'unknown_request_admission' }
end
if
  redis.call('HGET', request_admission_key, 'runtime_integrity_epoch') ~= expected_integrity_epoch
  or redis.call('HGET', request_admission_key, 'runtime_integrity_revision') ~= expected_integrity_revision
then
  return { 'denied', 'stale_integrity_epoch' }
end
if
  redis.call('HGET', request_admission_key, 'tpm_state') ~= 'held'
  or (tonumber(redis.call('HGET', request_admission_key, 'tpm_input_estimate')) or 0) < input_estimate
then
  return { 'denied', 'unknown_request_admission' }
end

-- 幂等：已存在 permit。
if redis.call('EXISTS', permit_key) == 1 then
  local existing_fp = redis.call('HGET', permit_key, 'fingerprint')
  if existing_fp ~= fingerprint then return { 'conflict' } end
  local status = redis.call('HGET', permit_key, 'status')
  if status ~= 'active' then return { 'conflict' } end
  return {
    'idempotent',
    redis.call('HGET', permit_key, 'provider_state_generation'),
    redis.call('HGET', permit_key, 'channel_state_generation'),
    redis.call('HGET', permit_key, 'provider_half_open_probe'),
    redis.call('HGET', permit_key, 'channel_half_open_probe'),
    redis.call('HGET', permit_key, 'lease_until_ms'),
    redis.call('HGET', permit_key, 'acquired_at_ms'),
    redis.call('HGET', permit_key, 'permit_ttl_ms'),
    redis.call('HGET', permit_key, 'renew_ms'),
    redis.call('HGET', permit_key, 'terminal_ttl_ms'),
    redis.call('HGET', permit_key, 'route_channel_rpd_bucket'),
  }
end

-- 新 permit 要求三个候选级 control 均 active、revision 一致且严格可解码。
-- Channel RPM/RPD/TPM 已不再是准入门槛（§1.2/§8）：并发是唯一的渠道级硬门槛，
-- 因此不再读取 channel-rate control，也不再占用/结算这三个计数。
local global_concurrency, global_concurrency_state =
  read_new_admission_control(global_conc_ctl, expected_global_conc_rev, parse_global_concurrency_payload)
if global_concurrency == nil then return { 'denied', global_concurrency_state } end
-- parse_channel_capacity_payload comes from helpers/authoritative_control.lua at assemble time.
---@diagnostic disable-next-line: undefined-global
local channel_capacity, channel_capacity_state = read_new_admission_control(channel_capacity_ctl, expected_channel_capacity_rev, parse_channel_capacity_payload)
if channel_capacity == nil then
  if channel_capacity_state == 'stale_setting_revision' then return { 'denied', 'stale_config_revision' } end
  return { 'denied', channel_capacity_state }
end
local breaker, breaker_state =
  read_new_admission_control(breaker_ctl, expected_breaker_rev, parse_circuit_breaker_payload)
if breaker == nil then return { 'denied', breaker_state } end

local eff_ch_conc = resolve_channel_limit(channel_capacity.concurrency, global_concurrency.channel_limit)
local breaker_enabled = 0
if breaker.enabled then breaker_enabled = 1 end
local permit_ttl_ms = breaker.attempt_permit_ttl_ms
local renew_ms = breaker.attempt_permit_renew_interval_ms
local terminal_ttl_ms = breaker.attempt_permit_terminal_ttl_ms

-- gate 返回 allow, probe(0/1), reason；probe=1 表示本次占用了该作用域的 half-open 租约。
local function gate(state_key, rotate_before_gate)
  if breaker_enabled == 0 then return true, 0, '' end
  if rotate_before_gate == 1 then return true, 0, '' end
  local st = redis.call('HGET', state_key, 'state')
  if st == false or st == nil then return true, 0, '' end
  if st == 'open' then
    local open_until = tonumber(redis.call('HGET', state_key, 'open_until_ms')) or 0
    if now < open_until then return false, 0, 'open' end
    -- 冷却到期：进入 half-open，占探测（需在写阶段设置租约）。
    return true, 1, ''
  elseif st == 'half_open' then
    local lease_until = tonumber(redis.call('HGET', state_key, 'half_open_lease_until_ms')) or 0
    local holder = redis.call('HGET', state_key, 'half_open_permit_id')
    if holder ~= false and holder ~= nil and holder ~= '' and now < lease_until then
      return false, 0, 'half_open_busy'
    end
    return true, 1, ''
  end
  return true, 0, ''
end

-- 429 冷却优先于 breaker：冷却未到期直接 cooldown（不增加任何 breaker eligible 计数，§2.4.1）。
-- cooldown 与 concurrency_full 严格区分：只有并发满才允许进入全池短等（§9.3）。
-- 同时返回剩余毫秒，供全池均冷却时给出准确 Retry-After（§9.5）。
if cooldown_key ~= '' and redis.call('EXISTS', cooldown_key) == 1 then
  local until_ms = tonumber(redis.call('HGET', cooldown_key, 'until_ms')) or 0
  if now < until_ms then return { 'denied', 'cooldown', until_ms - now } end
end

-- (channel_id, model_id) 403 权限暂停：仅当暂停记录的三类 revision 与本次候选完全一致且未复检通过时硬拒绝（§2.4.2）。
-- 不把整个 Channel 的 credential_valid 翻 false；配置真变化/新绑定使旧 permission stale，不再命中。
if permission_key ~= '' and redis.call('EXISTS', permission_key) == 1 then
  local p_state = redis.call('HGET', permission_key, 'recheck_state')
  if p_state ~= 'cleared' then
    local p_cfg = redis.call('HGET', permission_key, 'channel_config_revision')
    local p_burl = redis.call('HGET', permission_key, 'origin_revision')
    local p_sts = redis.call('HGET', permission_key, 'status_revision')
    if p_cfg == channel_config_rev and p_burl == origin_revision and p_sts == status_revision then
      return { 'denied', 'model_permission_paused' }
    end
  end
end

-- Provider control 围栏校验（§5.3.2）：control 缺失/ pending / effective_status 非 enabled / revision 落后均拒绝。
if enforce_origin_control == 1 then
  if redis.call('HGET', origin_key, 'control_present') ~= '1' then return { 'denied', 'runtime_sync_required' } end
  if redis.call('HGET', origin_key, 'origin_revision_state') == 'pending' then
    return { 'denied', 'runtime_sync_required' }
  end
  if redis.call('HGET', origin_key, 'status_revision_state') == 'pending' then
    return { 'denied', 'runtime_sync_required' }
  end
  local cur_srev = redis.call('HGET', origin_key, 'status_revision')
  local cur_burl = redis.call('HGET', origin_key, 'origin_revision')
  if cur_srev ~= status_revision then return { 'denied', 'stale_status_revision' } end
  if cur_burl ~= origin_revision then return { 'denied', 'stale_revision' } end
  if redis.call('HGET', origin_key, 'effective_status') ~= 'enabled' then
    return { 'denied', 'stale_status_revision' }
  end
end

-- Channel state 绑定 PostgreSQL 候选的 Origin 身份与三类 revision。只计算是否需要 rotate，
-- 在所有业务门槛通过前不修改状态；候选落后或同 config revision 却换 Origin 时直接拒绝。
local channel_exists = redis.call('EXISTS', channel_key)
local channel_rotate = 0
if channel_exists == 1 then
  local stored_cfg_raw = redis.call('HGET', channel_key, 'channel_config_revision')
  local stored_ep_raw = redis.call('HGET', channel_key, 'provider_id')
  local stored_burl_raw = redis.call('HGET', channel_key, 'origin_revision')
  local stored_status_raw = redis.call('HGET', channel_key, 'status_revision')
  local stored_state = redis.call('HGET', channel_key, 'state')
  if
    stored_cfg_raw == false
    or stored_ep_raw == false
    or stored_burl_raw == false
    or stored_status_raw == false
    or stored_state == false
  then
    channel_rotate = 1
  else
    local stored_cfg = tonumber(stored_cfg_raw)
    local candidate_cfg = tonumber(channel_config_rev)
    if stored_cfg == nil then return redis.error_reply('malformed channel_config_revision') end
    if stored_cfg > candidate_cfg then return { 'denied', 'stale_config_revision' } end
    if stored_cfg < candidate_cfg then
      channel_rotate = 1
    else
      local stored_ep = tonumber(stored_ep_raw)
      local stored_burl = tonumber(stored_burl_raw)
      local stored_status = tonumber(stored_status_raw)
      local candidate_ep = tonumber(provider_id)
      local candidate_burl = tonumber(origin_revision)
      local candidate_status = tonumber(status_revision)
      if stored_ep == nil or stored_burl == nil or stored_status == nil then
        return redis.error_reply('malformed channel origin binding')
      end
      if stored_ep ~= candidate_ep then return { 'denied', 'stale_config_revision' } end
      if stored_burl > candidate_burl then return { 'denied', 'stale_revision' } end
      if stored_status > candidate_status then return { 'denied', 'stale_status_revision' } end
      if stored_burl < candidate_burl or stored_status < candidate_status then channel_rotate = 1 end
    end
  end
else
  channel_rotate = 1
end

local ep_allow, ep_probe, ep_reason = gate(origin_key, 0)
if not ep_allow then return { 'denied', ep_reason } end
local ch_allow, ch_probe, ch_reason = gate(channel_key, channel_rotate)
if not ch_allow then return { 'denied', ch_reason } end

-- 先只读校验并发这一唯一渠道级硬门槛，再进入统一写阶段，避免部分占用。
-- 并发满返回 concurrency_full（可进入全池短等）；429 冷却返回 cooldown（不进入等待，§9.3）。
local conc_used = active_zset_count(conc_key, now)
if conc_used == nil then return { 'denied', 'runtime_sync_required' } end
if eff_ch_conc > 0 and conc_used >= eff_ch_conc then return { 'denied', 'concurrency_full' } end

-- 统一写阶段：全部条件通过，创建 permit、占 half-open/并发租约。
local lease_until = now + permit_ttl_ms

redis.call('ZREMRANGEBYSCORE', conc_key, '-inf', now)

local function ensure_origin_state(state_key)
  if redis.call('EXISTS', state_key) == 0 then
    redis.call(
      'HSET',
      state_key,
      'state',
      'closed',
      'state_generation',
      '1',
      'window_started_at_ms',
      now,
      'eligible_successes',
      '0',
      'eligible_failures',
      '0',
      'consecutive_eligible_failures',
      '0',
      'open_level',
      '0',
      'half_open_successes',
      '0',
      'last_transition_at_ms',
      now
    )
  end
end
ensure_origin_state(origin_key)

if channel_rotate == 1 then
  local gen = 1
  if channel_exists == 1 then gen = (tonumber(redis.call('HGET', channel_key, 'state_generation')) or 0) + 1 end
  redis.call(
    'HSET',
    channel_key,
    'provider_id',
    provider_id,
    'origin_revision',
    origin_revision,
    'status_revision',
    status_revision,
    'channel_config_revision',
    channel_config_rev,
    'state',
    'closed',
    'state_generation',
    gen,
    'window_started_at_ms',
    now,
    'eligible_successes',
    '0',
    'eligible_failures',
    '0',
    'consecutive_eligible_failures',
    '0',
    'open_level',
    '0',
    'half_open_successes',
    '0',
    'last_transition_at_ms',
    now
  )
  redis.call(
    'HDEL',
    channel_key,
    'half_open_permit_id',
    'half_open_lease_until_ms',
    'open_until_ms',
    'last_failure_at_ms',
    'last_failure_category'
  )
end

local function take_probe(state_key, probe)
  if probe == 1 then
    -- 进入/保持 half-open，占探测租约，推进 generation（取得下一次探测资格）。
    local gen = tonumber(redis.call('HGET', state_key, 'state_generation')) or 1
    local cur = redis.call('HGET', state_key, 'state')
    if cur ~= 'half_open' then
      gen = gen + 1
      redis.call(
        'HSET',
        state_key,
        'state',
        'half_open',
        'state_generation',
        gen,
        'half_open_successes',
        '0',
        'last_transition_at_ms',
        now
      )
    end
    redis.call('HSET', state_key, 'half_open_permit_id', permit_id, 'half_open_lease_until_ms', lease_until)
    return gen
  end
  return tonumber(redis.call('HGET', state_key, 'state_generation')) or 1
end

local ep_gen = take_probe(origin_key, ep_probe)
local ch_gen = take_probe(channel_key, ch_probe)

if conc_key ~= '' then
  redis.call('ZADD', conc_key, lease_until, permit_id)
  redis.call('PEXPIRE', conc_key, lease_until - now + terminal_ttl_ms)
end

redis.call(
  'HSET',
  permit_key,
  'status',
  'active',
  'permit_id',
  permit_id,
  'fingerprint',
  fingerprint,
  'request_admission_id',
  request_admission_id,
  'runtime_integrity_epoch',
  expected_integrity_epoch,
  'runtime_integrity_revision',
  expected_integrity_revision,
  'provider_id',
  provider_id,
  'channel_id',
  channel_id,
  'route_id',
  redis.call('HGET', request_admission_key, 'route_id') or '0',
  'origin_revision',
  origin_revision,
  'status_revision',
  status_revision,
  'origin_control_enforced',
  enforce_origin_control,
  'origin_fence_generation',
  redis.call('HGET', origin_key, 'origin_fence_generation') or '0',
  'status_fence_generation',
  redis.call('HGET', origin_key, 'status_fence_generation') or '0',
  'channel_config_revision',
  channel_config_rev,
  'model_id',
  model_id,
  'upstream_endpoint',
  upstream_endpoint,
  'request_mode',
  request_mode,
  'provider_state_generation',
  ep_gen,
  'channel_state_generation',
  ch_gen,
  'provider_half_open_probe',
  ep_probe,
  'channel_half_open_probe',
  ch_probe,
  'concurrency_channel_id',
  channel_id,
  'admission_enforced',
  '1',
  'channel_capacity_revision',
  expected_channel_capacity_rev,
  'global_concurrency_revision',
  expected_global_conc_rev,
  'circuit_breaker_revision',
  expected_breaker_rev,
  'tpm_input_estimate',
  input_estimate,
  'tpm_state',
  'held',
  'request_write_state',
  'not_started',
  'response_headers_received',
  'false',
  'first_token_eligible',
  'false',
  'route_channel_rpd_bucket',
  route_channel_rpd_key,
  'permit_ttl_ms',
  permit_ttl_ms,
  'renew_ms',
  renew_ms,
  'terminal_ttl_ms',
  terminal_ttl_ms,
  'acquired_at_ms',
  now,
  'lease_until_ms',
  lease_until
)
redis.call('PEXPIRE', permit_key, lease_until - now + terminal_ttl_ms)

return {
  'permit',
  ep_gen,
  ch_gen,
  ep_probe,
  ch_probe,
  lease_until,
  now,
  permit_ttl_ms,
  renew_ms,
  terminal_ttl_ms,
  route_channel_rpd_key,
}
