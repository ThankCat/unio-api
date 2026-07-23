package breakerstore

// Redis Lua 脚本（§5.3）。所有脚本使用 Redis TIME，不信任调用方时钟；先完成全部校验再进入统一写阶段，
// 全有或全无；first-terminal-wins。脚本内不读取 PostgreSQL——版本/代际由调用方从强一致 PG 读取后传入，
// 脚本只做相等校验与原子状态迁移。
//
// 约定 ARGV/KEYS 见各 Go 包装方法。返回值统一为 Redis 数组，首元素为字符串状态码。

// luaGateAndAcquire 实现 AcquireAttempt 的 breaker 门禁 + half-open 租约 + 并发租约 + permit 创建。
//
// KEYS[1]=endpoint state, KEYS[2]=channel state, KEYS[3]=channel concurrency zset, KEYS[4]=permit hash
// ARGV: 见 acquireArgv 组装顺序。
//
// 返回：
//
//	{"permit", endpoint_gen, channel_gen, endpoint_probe(0/1), channel_probe(0/1), lease_until_ms, acquired_at_ms}
//	{"denied", reason}
//	{"conflict"}  -- 同 permit_id 不同指纹
//	{"idempotent", ...permit fields}  -- 同 permit_id 同指纹重试
const luaGateAndAcquire = luaRedisInstanceHelpers + luaAuthoritativeControlHelpers + `
local function now_ms()
  local t = redis.call('TIME')
  return tonumber(t[1]) * 1000 + math.floor(tonumber(t[2]) / 1000)
end

local endpoint_key = KEYS[1]
local channel_key = KEYS[2]
local conc_key = KEYS[3]
local permit_key = KEYS[4]
local cooldown_key = KEYS[5]
local permission_key = KEYS[6]
local ch_admission_ctl = KEYS[7]
local ch_rpm_key = KEYS[8]
local ch_rpd_key = KEYS[9]
local ch_tpm_key = KEYS[10]
local channel_rate_ctl = KEYS[11]
local global_conc_ctl = KEYS[12]
local breaker_ctl = KEYS[13]
local integrity_marker = KEYS[14]
local request_admission_key = KEYS[15]
local fault_latch = KEYS[16]
local instance_proof = KEYS[17]

local permit_id = ARGV[1]
local fingerprint = ARGV[2]
local request_admission_id = ARGV[3]
local endpoint_id = ARGV[4]
local channel_id = ARGV[5]
local endpoint_base_url_rev = ARGV[6]
local endpoint_status_rev = ARGV[7]
local channel_config_rev = ARGV[8]
local model_id = ARGV[9]
local upstream_operation = ARGV[10]
local request_mode = ARGV[11]
local expected_ch_admission_rev = tonumber(ARGV[12])
local expected_channel_rate_rev = tonumber(ARGV[13])
local expected_global_conc_rev = tonumber(ARGV[14])
local expected_breaker_rev = tonumber(ARGV[15])
local estimate = tonumber(ARGV[16])
local expected_integrity_epoch = ARGV[17]
local expected_integrity_revision = ARGV[18]
-- Endpoint control 围栏校验开关（enforce=1 时要求 control 存在、effective_status=enabled、无 pending、revision 匹配，§5.3.2）。
local enforce_endpoint_control = tonumber(ARGV[19])

if redis.call('EXISTS', fault_latch) == 1 then
  return {'denied', 'breaker_store_unavailable'}
end
local instance_matches = redis_instance_proof_matches(instance_proof)
if instance_matches == nil then return redis.error_reply('invalid Redis instance reconciliation proof') end
if not instance_matches then return {'denied', 'redis_instance_changed'} end
local now = now_ms()

-- PostgreSQL snapshot、Redis marker 与 active request token 必须属于同一完整性 epoch。
-- request token 还必须已用本次相同 estimate 完成 Reserve，才能取得候选级资源。
if redis_key_type(integrity_marker) ~= 'hash' or redis.call('HGET', integrity_marker, 'state') ~= 'ready' then
  return {'denied', 'runtime_state_lost'}
end
if redis.call('HGET', integrity_marker, 'epoch') ~= expected_integrity_epoch or
    redis.call('HGET', integrity_marker, 'revision') ~= expected_integrity_revision then
  return {'denied', 'stale_integrity_epoch'}
end
local request_admission_type = redis_key_type(request_admission_key)
if request_admission_type == 'none' then return {'denied', 'unknown_request_admission'} end
if request_admission_type ~= 'hash' then return {'denied', 'runtime_sync_required'} end
if redis.call('HGET', request_admission_key, 'status') ~= 'active' then
  return {'denied', 'unknown_request_admission'}
end
if redis.call('HGET', request_admission_key, 'runtime_integrity_epoch') ~= expected_integrity_epoch or
    redis.call('HGET', request_admission_key, 'runtime_integrity_revision') ~= expected_integrity_revision then
  return {'denied', 'stale_integrity_epoch'}
end
if redis.call('HGET', request_admission_key, 'reserve_state') ~= 'reserved' or
    tonumber(redis.call('HGET', request_admission_key, 'reserve_estimated_input_tokens')) ~= estimate then
  return {'denied', 'unknown_request_admission'}
end

-- 幂等：已存在 permit。
if redis.call('EXISTS', permit_key) == 1 then
  local existing_fp = redis.call('HGET', permit_key, 'fingerprint')
  if existing_fp ~= fingerprint then
    return {'conflict'}
  end
  local status = redis.call('HGET', permit_key, 'status')
  if status ~= 'active' then
    return {'conflict'}
  end
  return {'idempotent',
    redis.call('HGET', permit_key, 'endpoint_state_generation'),
    redis.call('HGET', permit_key, 'channel_state_generation'),
    redis.call('HGET', permit_key, 'endpoint_half_open_probe'),
    redis.call('HGET', permit_key, 'channel_half_open_probe'),
    redis.call('HGET', permit_key, 'lease_until_ms'),
    redis.call('HGET', permit_key, 'acquired_at_ms'),
    redis.call('HGET', permit_key, 'permit_ttl_ms'),
    redis.call('HGET', permit_key, 'renew_ms'),
    redis.call('HGET', permit_key, 'terminal_ttl_ms')}
end

-- New permits require all four candidate controls to be active, revision-current, and strictly decodable.
local channel_rate, channel_rate_state = read_new_admission_control(
  channel_rate_ctl, expected_channel_rate_rev, parse_rate_limit_defaults_payload)
if channel_rate == nil then return {'denied', channel_rate_state} end
local global_concurrency, global_concurrency_state = read_new_admission_control(
  global_conc_ctl, expected_global_conc_rev, parse_global_concurrency_payload)
if global_concurrency == nil then return {'denied', global_concurrency_state} end
local channel_limits, channel_limits_state = read_new_admission_control(
  ch_admission_ctl, expected_ch_admission_rev, parse_channel_admission_payload)
if channel_limits == nil then
  if channel_limits_state == 'stale_setting_revision' then
    return {'denied', 'stale_config_revision'}
  end
  return {'denied', channel_limits_state}
end
local breaker, breaker_state = read_new_admission_control(
  breaker_ctl, expected_breaker_rev, parse_circuit_breaker_payload)
if breaker == nil then return {'denied', breaker_state} end

local eff_ch_rpm = resolve_channel_limit(channel_limits.rpm, channel_rate.rpm)
local eff_ch_rpd = resolve_channel_limit(channel_limits.rpd, channel_rate.rpd)
local eff_ch_tpm = resolve_channel_limit(channel_limits.tpm, channel_rate.tpm)
local eff_ch_conc = resolve_channel_limit(channel_limits.concurrency, global_concurrency.channel_limit)
local breaker_enabled = 0
if breaker.enabled then breaker_enabled = 1 end
local half_open_successes = breaker.half_open_successes
local permit_ttl_ms = breaker.attempt_permit_ttl_ms
local renew_ms = breaker.attempt_permit_renew_interval_ms
local terminal_ttl_ms = breaker.attempt_permit_terminal_ttl_ms
local bucket_ttl_ms = permit_ttl_ms + terminal_ttl_ms + 120000

-- gate 返回 allow, probe(0/1), reason；probe=1 表示本次占用了该作用域的 half-open 租约。
local function gate(state_key, rotate_before_gate)
  if breaker_enabled == 0 then
    return true, 0, ''
  end
  if rotate_before_gate == 1 then
    return true, 0, ''
  end
  local st = redis.call('HGET', state_key, 'state')
  if st == false or st == nil then
    return true, 0, ''
  end
  if st == 'open' then
    local open_until = tonumber(redis.call('HGET', state_key, 'open_until_ms')) or 0
    if now < open_until then
      return false, 0, 'open'
    end
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

-- 429 冷却优先于 breaker：冷却未到期直接 rate_limited（不增加任何 breaker eligible 计数，§2.4.1）。
if cooldown_key ~= '' and redis.call('EXISTS', cooldown_key) == 1 then
  local until_ms = tonumber(redis.call('HGET', cooldown_key, 'until_ms')) or 0
  if now < until_ms then
    return {'denied', 'rate_limited'}
  end
end

-- (channel_id, model_id) 403 权限暂停：仅当暂停记录的三类 revision 与本次候选完全一致且未复检通过时硬拒绝（§2.4.2）。
-- 不把整个 Channel 的 credential_valid 翻 false；配置真变化/新绑定使旧 permission stale，不再命中。
if permission_key ~= '' and redis.call('EXISTS', permission_key) == 1 then
  local p_state = redis.call('HGET', permission_key, 'recheck_state')
  if p_state ~= 'cleared' then
    local p_cfg = redis.call('HGET', permission_key, 'channel_config_revision')
    local p_burl = redis.call('HGET', permission_key, 'endpoint_base_url_revision')
    local p_sts = redis.call('HGET', permission_key, 'endpoint_status_revision')
    if p_cfg == channel_config_rev and p_burl == endpoint_base_url_rev and p_sts == endpoint_status_rev then
      return {'denied', 'model_permission_paused'}
    end
  end
end

-- Endpoint control 围栏校验（§5.3.2）：control 缺失/ pending / effective_status 非 enabled / revision 落后均拒绝。
if enforce_endpoint_control == 1 then
  if redis.call('HGET', endpoint_key, 'control_present') ~= '1' then return {'denied', 'runtime_sync_required'} end
  if redis.call('HGET', endpoint_key, 'base_url_revision_state') == 'pending' then return {'denied', 'runtime_sync_required'} end
  if redis.call('HGET', endpoint_key, 'status_revision_state') == 'pending' then return {'denied', 'runtime_sync_required'} end
  local cur_srev = redis.call('HGET', endpoint_key, 'status_revision')
  local cur_burl = redis.call('HGET', endpoint_key, 'base_url_revision')
  if cur_srev ~= endpoint_status_rev then return {'denied', 'stale_status_revision'} end
  if cur_burl ~= endpoint_base_url_rev then return {'denied', 'stale_revision'} end
  if redis.call('HGET', endpoint_key, 'effective_status') ~= 'enabled' then return {'denied', 'stale_status_revision'} end
end

-- Channel state 绑定 PostgreSQL 候选的 Endpoint 身份与三类 revision。只计算是否需要 rotate，
-- 在所有业务门槛通过前不修改状态；候选落后或同 config revision 却换 Endpoint 时直接拒绝。
local channel_exists = redis.call('EXISTS', channel_key)
local channel_rotate = 0
if channel_exists == 1 then
  local stored_cfg_raw = redis.call('HGET', channel_key, 'channel_config_revision')
  local stored_ep_raw = redis.call('HGET', channel_key, 'provider_endpoint_id')
  local stored_burl_raw = redis.call('HGET', channel_key, 'base_url_revision')
  local stored_status_raw = redis.call('HGET', channel_key, 'status_revision')
  local stored_state = redis.call('HGET', channel_key, 'state')
  if stored_cfg_raw == false or stored_ep_raw == false or stored_burl_raw == false or stored_status_raw == false or stored_state == false then
    channel_rotate = 1
  else
    local stored_cfg = tonumber(stored_cfg_raw)
    local candidate_cfg = tonumber(channel_config_rev)
    if stored_cfg == nil then return redis.error_reply('malformed channel_config_revision') end
    if stored_cfg > candidate_cfg then return {'denied', 'stale_config_revision'} end
    if stored_cfg < candidate_cfg then
      channel_rotate = 1
    else
      local stored_ep = tonumber(stored_ep_raw)
      local stored_burl = tonumber(stored_burl_raw)
      local stored_status = tonumber(stored_status_raw)
      local candidate_ep = tonumber(endpoint_id)
      local candidate_burl = tonumber(endpoint_base_url_rev)
      local candidate_status = tonumber(endpoint_status_rev)
      if stored_ep == nil or stored_burl == nil or stored_status == nil then
        return redis.error_reply('malformed channel endpoint binding')
      end
      if stored_ep ~= candidate_ep then return {'denied', 'stale_config_revision'} end
      if stored_burl > candidate_burl then return {'denied', 'stale_revision'} end
      if stored_status > candidate_status then return {'denied', 'stale_status_revision'} end
      if stored_burl < candidate_burl or stored_status < candidate_status then channel_rotate = 1 end
    end
  end
else
  channel_rotate = 1
end

local ep_allow, ep_probe, ep_reason = gate(endpoint_key, 0)
if not ep_allow then return {'denied', ep_reason} end
local ch_allow, ch_probe, ch_reason = gate(channel_key, channel_rotate)
if not ch_allow then return {'denied', ch_reason} end

-- Validate all stable resource keys and evaluate limits without mutation. This prevents Redis script
-- errors or a later limit denial from leaving zero-valued keys/TTLs or partially acquired resources.
local conc_used = active_zset_count(conc_key, now)
local rpm_used = read_nonnegative_counter(ch_rpm_key)
local rpd_used = read_nonnegative_counter(ch_rpd_key)
local tpm_used = read_nonnegative_counter(ch_tpm_key)
if conc_used == nil or rpm_used == nil or rpd_used == nil or tpm_used == nil then
  return {'denied', 'runtime_sync_required'}
end
if rpm_used >= MAX_EXACT_INTEGER or rpd_used >= MAX_EXACT_INTEGER or
    tpm_used > MAX_EXACT_INTEGER - estimate then
  return {'denied', 'runtime_sync_required'}
end
if eff_ch_conc > 0 and conc_used >= eff_ch_conc then return {'denied', 'concurrency_limited'} end
if eff_ch_rpm > 0 and rpm_used + 1 > eff_ch_rpm then return {'denied', 'rate_limited'} end
if eff_ch_rpd > 0 and rpd_used + 1 > eff_ch_rpd then return {'denied', 'rate_limited'} end
if eff_ch_tpm > 0 and tpm_used + estimate > eff_ch_tpm then return {'denied', 'rate_limited'} end

-- 统一写阶段：全部条件通过，创建 permit、占 half-open/并发租约。
local lease_until = now + permit_ttl_ms

redis.call('INCR', ch_rpm_key)
redis.call('PEXPIRE', ch_rpm_key, bucket_ttl_ms)
redis.call('INCR', ch_rpd_key)
redis.call('PEXPIRE', ch_rpd_key, bucket_ttl_ms)
if estimate > 0 then
  redis.call('INCRBY', ch_tpm_key, estimate)
  redis.call('PEXPIRE', ch_tpm_key, bucket_ttl_ms)
end
redis.call('ZREMRANGEBYSCORE', conc_key, '-inf', now)

local function ensure_endpoint_state(state_key)
  if redis.call('EXISTS', state_key) == 0 then
    redis.call('HSET', state_key,
      'state', 'closed', 'state_generation', '1', 'window_started_at_ms', now,
      'eligible_successes', '0', 'eligible_failures', '0', 'consecutive_eligible_failures', '0',
      'open_level', '0', 'half_open_successes', '0', 'last_transition_at_ms', now)
  end
end
ensure_endpoint_state(endpoint_key)

if channel_rotate == 1 then
  local gen = 1
  if channel_exists == 1 then
    gen = (tonumber(redis.call('HGET', channel_key, 'state_generation')) or 0) + 1
  end
  redis.call('HSET', channel_key,
    'provider_endpoint_id', endpoint_id,
    'base_url_revision', endpoint_base_url_rev,
    'status_revision', endpoint_status_rev,
    'channel_config_revision', channel_config_rev,
    'state', 'closed', 'state_generation', gen, 'window_started_at_ms', now,
    'eligible_successes', '0', 'eligible_failures', '0', 'consecutive_eligible_failures', '0',
    'open_level', '0', 'half_open_successes', '0', 'last_transition_at_ms', now)
  redis.call('HDEL', channel_key,
    'half_open_permit_id', 'half_open_lease_until_ms', 'open_until_ms',
    'last_failure_at_ms', 'last_failure_category', 'ttft_ewma_ms', 'ttft_samples')
end

local function take_probe(state_key, probe)
  if probe == 1 then
    -- 进入/保持 half-open，占探测租约，推进 generation（取得下一次探测资格）。
    local gen = tonumber(redis.call('HGET', state_key, 'state_generation')) or 1
    local cur = redis.call('HGET', state_key, 'state')
    if cur ~= 'half_open' then
      gen = gen + 1
      redis.call('HSET', state_key, 'state', 'half_open', 'state_generation', gen,
        'half_open_successes', '0', 'last_transition_at_ms', now)
    end
    redis.call('HSET', state_key, 'half_open_permit_id', permit_id, 'half_open_lease_until_ms', lease_until)
    return gen
  end
  return tonumber(redis.call('HGET', state_key, 'state_generation')) or 1
end

local ep_gen = take_probe(endpoint_key, ep_probe)
local ch_gen = take_probe(channel_key, ch_probe)

if conc_key ~= '' then
  redis.call('ZADD', conc_key, lease_until, permit_id)
  redis.call('PEXPIRE', conc_key, lease_until - now + terminal_ttl_ms)
end

redis.call('HSET', permit_key,
  'status', 'active',
	'permit_id', permit_id,
  'fingerprint', fingerprint,
  'request_admission_id', request_admission_id,
  'runtime_integrity_epoch', expected_integrity_epoch,
  'runtime_integrity_revision', expected_integrity_revision,
  'endpoint_id', endpoint_id,
  'channel_id', channel_id,
  'endpoint_base_url_revision', endpoint_base_url_rev,
  'endpoint_status_revision', endpoint_status_rev,
  'endpoint_control_enforced', enforce_endpoint_control,
  'endpoint_base_url_fence_generation', redis.call('HGET', endpoint_key, 'base_url_fence_generation') or '0',
  'endpoint_status_fence_generation', redis.call('HGET', endpoint_key, 'status_fence_generation') or '0',
  'channel_config_revision', channel_config_rev,
  'model_id', model_id,
  'upstream_operation', upstream_operation,
  'request_mode', request_mode,
  'endpoint_state_generation', ep_gen,
  'channel_state_generation', ch_gen,
  'endpoint_half_open_probe', ep_probe,
  'channel_half_open_probe', ch_probe,
  'concurrency_channel_id', channel_id,
  'admission_enforced', '1',
  'channel_admission_revision', expected_ch_admission_rev,
  'channel_rate_limits_revision', expected_channel_rate_rev,
  'global_concurrency_revision', expected_global_conc_rev,
  'circuit_breaker_revision', expected_breaker_rev,
  'ch_rpm_bucket', ch_rpm_key,
  'ch_rpd_bucket', ch_rpd_key,
  'ch_tpm_bucket', ch_tpm_key,
  'tpm_estimate', estimate,
  'permit_ttl_ms', permit_ttl_ms,
  'renew_ms', renew_ms,
  'terminal_ttl_ms', terminal_ttl_ms,
  'acquired_at_ms', now,
  'lease_until_ms', lease_until)
redis.call('PEXPIRE', permit_key, lease_until - now + terminal_ttl_ms)

return {'permit', ep_gen, ch_gen, ep_probe, ch_probe, lease_until, now,
  permit_ttl_ms, renew_ms, terminal_ttl_ms}
`

// luaAttemptPermitLifecycleGuard 在 Renew/Finish/Abort 的任何写入前校验调用方 expected epoch、
// Redis marker 与服务端 permit hash。三方 epoch 或 permit 身份不一致时只返回稳定结果，不修改 key。
//
// Common KEYS: marker, permit, endpoint state, channel state, channel concurrency zset.
// Common ARGV: permit_id, epoch, epoch_revision, request_admission_id, endpoint_id, channel_id,
// base_url_revision, status_revision, channel_config_revision, model_id, operation, request_mode,
// endpoint_generation, channel_generation, endpoint_probe, channel_probe.
const luaAttemptPermitLifecycleGuard = `
local function attempt_key_type(key)
  local reply = redis.call('TYPE', key)
  if type(reply) == 'table' then return reply['ok'] end
  return reply
end

local function validate_attempt_permit_lifecycle()
  local marker_key = KEYS[1]
  local permit_key = KEYS[2]

  if attempt_key_type(marker_key) ~= 'hash' then return 'runtime_state_lost' end
  local marker_state = redis.call('HGET', marker_key, 'state')
  local marker_epoch = redis.call('HGET', marker_key, 'epoch')
  local marker_revision = redis.call('HGET', marker_key, 'revision')
  if marker_state ~= 'ready' or marker_epoch == false or marker_revision == false then
    return 'runtime_state_lost'
  end
  if marker_epoch ~= ARGV[2] or marker_revision ~= ARGV[3] then
    return 'stale_integrity_epoch'
  end

  local permit_type = attempt_key_type(permit_key)
  if permit_type == 'none' then return 'unknown_permit' end
  if permit_type ~= 'hash' then return 'runtime_sync_required' end

  local permit_epoch = redis.call('HGET', permit_key, 'runtime_integrity_epoch')
  local permit_revision = redis.call('HGET', permit_key, 'runtime_integrity_revision')
  if permit_epoch == false or permit_revision == false then return 'runtime_sync_required' end
  if permit_epoch ~= ARGV[2] or permit_revision ~= ARGV[3] then
    return 'stale_integrity_epoch'
  end

  local identities = {
    {'permit_id', 1},
    {'request_admission_id', 4},
    {'endpoint_id', 5},
    {'channel_id', 6},
    {'endpoint_base_url_revision', 7},
    {'endpoint_status_revision', 8},
    {'channel_config_revision', 9},
    {'model_id', 10},
    {'upstream_operation', 11},
    {'request_mode', 12},
    {'endpoint_state_generation', 13},
    {'channel_state_generation', 14},
    {'endpoint_half_open_probe', 15},
    {'channel_half_open_probe', 16},
    {'concurrency_channel_id', 6}
  }
  for _, identity in ipairs(identities) do
    local stored = redis.call('HGET', permit_key, identity[1])
    if stored == false then return 'runtime_sync_required' end
    if stored ~= ARGV[identity[2]] then return 'conflict' end
  end

  local status = redis.call('HGET', permit_key, 'status')
  if status ~= 'active' and status ~= 'finished' and status ~= 'aborted' then
    return 'runtime_sync_required'
  end

  local endpoint_control_enforced = redis.call('HGET', permit_key, 'endpoint_control_enforced')
  local endpoint_base_fence = redis.call('HGET', permit_key, 'endpoint_base_url_fence_generation')
  local endpoint_status_fence = redis.call('HGET', permit_key, 'endpoint_status_fence_generation')
  if (endpoint_control_enforced ~= '0' and endpoint_control_enforced ~= '1') or
      type(endpoint_base_fence) ~= 'string' or string.match(endpoint_base_fence, '^%d+$') == nil or
      type(endpoint_status_fence) ~= 'string' or string.match(endpoint_status_fence, '^%d+$') == nil then
    return 'runtime_sync_required'
  end

  local endpoint_type = attempt_key_type(KEYS[3])
  local channel_type = attempt_key_type(KEYS[4])
  local concurrency_type = attempt_key_type(KEYS[5])
  if (endpoint_type ~= 'none' and endpoint_type ~= 'hash') or
      (channel_type ~= 'none' and channel_type ~= 'hash') or
      (concurrency_type ~= 'none' and concurrency_type ~= 'zset') then
    return 'runtime_sync_required'
  end

if redis.call('HGET', permit_key, 'admission_enforced') ~= '1' then
    return 'runtime_sync_required'
  end
  local bucket_fields = {'ch_rpm_bucket', 'ch_rpd_bucket', 'ch_tpm_bucket'}
  for _, field in ipairs(bucket_fields) do
    local bucket_key = redis.call('HGET', permit_key, field)
    if type(bucket_key) ~= 'string' or bucket_key == '' then return 'runtime_sync_required' end
    local bucket_type = attempt_key_type(bucket_key)
    if bucket_type ~= 'none' and bucket_type ~= 'string' then return 'runtime_sync_required' end
  end
  local estimate = redis.call('HGET', permit_key, 'tpm_estimate')
  if type(estimate) ~= 'string' or string.match(estimate, '^%d+$') == nil then
    return 'runtime_sync_required'
  end
  return nil
end
`

// luaFinish 实现 Finish：first-terminal-wins；校验 permit；释放并发/half-open；按每作用域 outcome 推进
// 双触发状态机；stream permit 且有有效 FirstToken 时更新 TTFT EWMA。
//
// KEYS[1..5] 与 guard 相同，KEYS[6]=gateway.circuit_breaker runtime control,
// KEYS[7]=gateway.routing_balance runtime control，KEYS[8..9]=本次类别的 Endpoint distinct
// Channel/model 证据集合。ARGV[1..16] 与 guard 相同，ARGV[17]=endpoint outcome,
// [18]=channel outcome, [19]=first_token_ms(空串表示无样本), [20]=actual TPM(空串表示无权威 usage),
// [21]=endpoint evidence category(空串表示无条件证据)。breaker 与 TTFT alpha 只读 Redis committed active control。
// 返回 {endpoint_disposition, channel_disposition}。
const luaFinish = luaAuthoritativeControlHelpers + luaAttemptPermitLifecycleGuard + `
local function now_ms()
  local t = redis.call('TIME')
  return tonumber(t[1]) * 1000 + math.floor(tonumber(t[2]) / 1000)
end
local function next_open_until(open_durations, level, now)
  local idx = level + 1
  if idx > #open_durations then idx = #open_durations end
  if idx < 1 then idx = 1 end
  return now + open_durations[idx], math.min(level + 1, #open_durations - 1)
end

local marker_key = KEYS[1]
local permit_key = KEYS[2]
local endpoint_key = KEYS[3]
local channel_key = KEYS[4]
local conc_key = KEYS[5]
local breaker_ctl = KEYS[6]
local routing_balance_ctl = KEYS[7]
local evidence_channels_key = KEYS[8]
local evidence_models_key = KEYS[9]

local permit_id = ARGV[1]
local ep_outcome = ARGV[17]
local ch_outcome = ARGV[18]
local first_token_ms = ARGV[19]
local tpm_actual = ARGV[20] -- '' 表示无权威 usage
local endpoint_evidence = ARGV[21]

local now = now_ms()

local lifecycle_guard = validate_attempt_permit_lifecycle()
if lifecycle_guard ~= nil then
  if lifecycle_guard == 'conflict' then lifecycle_guard = 'terminal_conflict' end
  return {lifecycle_guard, lifecycle_guard}
end
if redis.call('HGET', permit_key, 'status') ~= 'active' then
  return {redis.call('HGET', permit_key, 'endpoint_disposition') or 'terminal_conflict',
          redis.call('HGET', permit_key, 'channel_disposition') or 'terminal_conflict'}
end

local breaker = read_committed_control(breaker_ctl, parse_circuit_breaker_payload)
local breaker_config_valid = breaker ~= nil
local routing_balance = nil
if first_token_ms ~= '' then
  routing_balance = read_committed_control(routing_balance_ctl, parse_routing_balance_payload)
end
local routing_balance_valid = first_token_ms == '' or routing_balance ~= nil
local ttft_alpha = 0
if routing_balance ~= nil then ttft_alpha = routing_balance.ttft_ewma_alpha end
local breaker_enabled = 0
local window_ms = 1
local min_requests = 2
local failure_ratio = 1
local consecutive_failures_target = 1
local consecutive_window_ms = 1
local half_open_successes_target = 2
local open_durations = {1}
if breaker_config_valid then
  if breaker.enabled then breaker_enabled = 1 end
  window_ms = breaker.window_ms
  min_requests = breaker.min_requests
  failure_ratio = breaker.failure_ratio
  consecutive_failures_target = breaker.consecutive_failures
  consecutive_window_ms = breaker.consecutive_window_ms
  half_open_successes_target = breaker.half_open_successes
  open_durations = breaker.open_durations_ms
end

-- 资源收口：释放并发租约（first-terminal-wins，始终执行）。
if conc_key ~= '' then
  redis.call('ZREM', conc_key, permit_id)
end

-- Channel RPM/RPD 作为真实上游调用保留（不回退）；Channel TPM 按权威 usage 对账、无权威则释放预占（§2.12.8）。
if redis.call('HGET', permit_key, 'admission_enforced') == '1' then
  local tpm_bucket = redis.call('HGET', permit_key, 'ch_tpm_bucket')
  local estimate = tonumber(redis.call('HGET', permit_key, 'tpm_estimate')) or 0
  if tpm_bucket ~= false and tpm_bucket ~= '' and estimate > 0 then
    if tpm_actual == '' then
      if redis.call('EXISTS', tpm_bucket) == 1 then redis.call('DECRBY', tpm_bucket, estimate) end
    else
      local actual = tonumber(tpm_actual) or 0
      local delta = actual - estimate
      if redis.call('EXISTS', tpm_bucket) == 1 and delta ~= 0 then redis.call('INCRBY', tpm_bucket, delta) end
    end
  end
end

-- Endpoint fence 在 permit 服务端记录中冻结。prepare 即使最终 abort，也已永久推进 fence generation；
-- 因此旧 permit 的真实结果只能收口资源，不得写 Endpoint 或任一子 Channel 的当前 breaker/TTFT。
local endpoint_fence_disposition = nil
if redis.call('HGET', permit_key, 'endpoint_control_enforced') == '1' then
  local stored_base_fence = redis.call('HGET', permit_key, 'endpoint_base_url_fence_generation')
  local stored_status_fence = redis.call('HGET', permit_key, 'endpoint_status_fence_generation')
  local current_base_fence = redis.call('HGET', endpoint_key, 'base_url_fence_generation')
  local current_status_fence = redis.call('HGET', endpoint_key, 'status_fence_generation')
  if stored_base_fence == false or stored_status_fence == false or
      current_base_fence == false or current_status_fence == false then
    endpoint_fence_disposition = 'runtime_sync_required'
  elseif redis.call('HGET', endpoint_key, 'status_revision_state') ~= 'active' or
      current_status_fence ~= stored_status_fence or
      redis.call('HGET', endpoint_key, 'status_revision') ~= redis.call('HGET', permit_key, 'endpoint_status_revision') then
    endpoint_fence_disposition = 'stale_status_revision'
  elseif redis.call('HGET', endpoint_key, 'base_url_revision_state') ~= 'active' or
      current_base_fence ~= stored_base_fence or
      redis.call('HGET', endpoint_key, 'base_url_revision') ~= redis.call('HGET', permit_key, 'endpoint_base_url_revision') then
    endpoint_fence_disposition = 'stale_revision'
  end
end

local channel_fence_disposition = nil
if redis.call('EXISTS', channel_key) == 0 then
  channel_fence_disposition = 'stale_generation'
elseif redis.call('HGET', channel_key, 'channel_config_revision') ~= redis.call('HGET', permit_key, 'channel_config_revision') then
  channel_fence_disposition = 'stale_config_revision'
elseif redis.call('HGET', channel_key, 'provider_endpoint_id') ~= redis.call('HGET', permit_key, 'endpoint_id') then
  channel_fence_disposition = 'stale_config_revision'
elseif redis.call('HGET', channel_key, 'status_revision') ~= redis.call('HGET', permit_key, 'endpoint_status_revision') then
  channel_fence_disposition = 'stale_status_revision'
elseif redis.call('HGET', channel_key, 'base_url_revision') ~= redis.call('HGET', permit_key, 'endpoint_base_url_revision') then
  channel_fence_disposition = 'stale_revision'
end

-- 条件 Endpoint 故障必须在同一次 Finish 中原子收集多 Gateway 共享证据。集合使用固定窗口，
-- 且最多保存配置阈值数量的整数 ID，避免随 Channel/model 数量无界增长。
local evidence_disposition = nil
if endpoint_evidence ~= '' then
  if endpoint_fence_disposition ~= nil then
    evidence_disposition = endpoint_fence_disposition
  elseif not breaker_config_valid then
    evidence_disposition = 'runtime_sync_required'
  elseif breaker_enabled == 0 then
    evidence_disposition = 'not_applicable'
  elseif redis.call('EXISTS', endpoint_key) == 0 then
    evidence_disposition = 'stale_generation'
  elseif (tonumber(redis.call('HGET', endpoint_key, 'state_generation')) or 0) ~=
      (tonumber(redis.call('HGET', permit_key, 'endpoint_state_generation')) or -1) then
    evidence_disposition = 'stale_generation'
  else
    local channel_key_type = attempt_key_type(evidence_channels_key)
    local model_key_type = attempt_key_type(evidence_models_key)
    if (channel_key_type ~= 'none' and channel_key_type ~= 'set') or
        (model_key_type ~= 'none' and model_key_type ~= 'set') then
      evidence_disposition = 'runtime_sync_required'
    else
      local channel_limit = breaker.endpoint_ambiguous_distinct_channels
      local model_limit = breaker.endpoint_ambiguous_distinct_models
      local channel_id = redis.call('HGET', permit_key, 'channel_id')
      local model_id = redis.call('HGET', permit_key, 'model_id')

      if redis.call('SISMEMBER', evidence_channels_key, channel_id) == 0 and
          redis.call('SCARD', evidence_channels_key) < channel_limit then
        redis.call('SADD', evidence_channels_key, channel_id)
      end
      if redis.call('SISMEMBER', evidence_models_key, model_id) == 0 and
          redis.call('SCARD', evidence_models_key) < model_limit then
        redis.call('SADD', evidence_models_key, model_id)
      end
      if redis.call('PTTL', evidence_channels_key) < 0 then
        redis.call('PEXPIRE', evidence_channels_key, window_ms)
      end
      if redis.call('PTTL', evidence_models_key) < 0 then
        redis.call('PEXPIRE', evidence_models_key, window_ms)
      end

      if redis.call('SCARD', evidence_channels_key) >= channel_limit and
          redis.call('SCARD', evidence_models_key) >= model_limit then
        ep_outcome = 'eligible_failure'
      end
    end
  end
end

-- apply_scope 对某作用域应用 outcome，返回 disposition。
local function apply_scope(state_key, outcome, permit_gen_field, permit_probe_field, is_channel)
  local permit_gen = tonumber(redis.call('HGET', permit_key, permit_gen_field)) or 0
  local probe = redis.call('HGET', permit_key, permit_probe_field)

  if not breaker_config_valid then
    return 'runtime_sync_required'
  end
  if is_channel == 1 and not routing_balance_valid then
    return 'runtime_sync_required'
  end
  if breaker_enabled == 0 then return 'not_applicable' end
  if redis.call('EXISTS', state_key) == 0 then
    return 'stale_generation'
  end
  local cur_gen = tonumber(redis.call('HGET', state_key, 'state_generation')) or 0
  if cur_gen ~= permit_gen then
    return 'stale_generation'
  end

  -- half-open 探测收口：核对 lease 归属与有效期。
  if probe == '1' then
    local holder = redis.call('HGET', state_key, 'half_open_permit_id')
    local lease_until = tonumber(redis.call('HGET', state_key, 'half_open_lease_until_ms')) or 0
    if holder == permit_id and now < lease_until then
      if outcome == 'eligible_success' then
        local hos = (tonumber(redis.call('HGET', state_key, 'half_open_successes')) or 0) + 1
        if hos >= half_open_successes_target then
          local gen = (tonumber(redis.call('HGET', state_key, 'state_generation')) or 1) + 1
          redis.call('HSET', state_key, 'state', 'closed', 'state_generation', gen,
            'window_started_at_ms', now, 'eligible_successes', '0', 'eligible_failures', '0',
            'consecutive_eligible_failures', '0', 'open_level', '0', 'half_open_successes', '0',
            'last_transition_at_ms', now)
          redis.call('HDEL', state_key, 'half_open_permit_id', 'half_open_lease_until_ms')
        else
          redis.call('HSET', state_key, 'half_open_successes', hos)
          redis.call('HDEL', state_key, 'half_open_permit_id', 'half_open_lease_until_ms')
        end
      elseif outcome == 'eligible_failure' then
        local level = tonumber(redis.call('HGET', state_key, 'open_level')) or 0
        local open_until, next_level = next_open_until(open_durations, level, now)
        local gen = (tonumber(redis.call('HGET', state_key, 'state_generation')) or 1) + 1
        redis.call('HSET', state_key, 'state', 'open', 'state_generation', gen,
          'open_until_ms', open_until, 'open_level', next_level, 'half_open_successes', '0',
          'last_transition_at_ms', now, 'last_failure_at_ms', now)
        redis.call('HDEL', state_key, 'half_open_permit_id', 'half_open_lease_until_ms')
      else
        -- ignored：中性释放 lease，不计成功/失败。
        redis.call('HDEL', state_key, 'half_open_permit_id', 'half_open_lease_until_ms')
      end
      -- TTFT 仅 channel 更新。
      if is_channel == 1 and first_token_ms ~= '' then
        local sample = tonumber(first_token_ms)
        local samples = tonumber(redis.call('HGET', state_key, 'ttft_samples')) or 0
        if samples == 0 then
          redis.call('HSET', state_key, 'ttft_ewma_ms', sample, 'ttft_samples', 1)
        else
          local old = tonumber(redis.call('HGET', state_key, 'ttft_ewma_ms')) or sample
          local ewma = ttft_alpha * sample + (1 - ttft_alpha) * old
          redis.call('HSET', state_key, 'ttft_ewma_ms', ewma, 'ttft_samples', samples + 1)
        end
      end
      return 'applied'
    else
      -- 探测租约已被新一轮夺走或过期：本结果中性 no-op。
      return 'stale_generation'
    end
  end

  -- closed 普通窗口计数（半开由上面分支处理）。
  local cur_state = redis.call('HGET', state_key, 'state')
  if cur_state == 'open' then
    return 'stale_generation'
  end

  -- 窗口过期则重置计数。
  local ws = tonumber(redis.call('HGET', state_key, 'window_started_at_ms')) or now
  if now - ws >= window_ms then
    redis.call('HSET', state_key, 'window_started_at_ms', now,
      'eligible_successes', '0', 'eligible_failures', '0')
  end

  if outcome == 'eligible_success' then
    redis.call('HINCRBY', state_key, 'eligible_successes', 1)
    redis.call('HSET', state_key, 'consecutive_eligible_failures', '0')
  elseif outcome == 'eligible_failure' then
    redis.call('HINCRBY', state_key, 'eligible_failures', 1)
    local last_fail = tonumber(redis.call('HGET', state_key, 'last_failure_at_ms')) or 0
    local cef = tonumber(redis.call('HGET', state_key, 'consecutive_eligible_failures')) or 0
    if last_fail > 0 and (now - last_fail) <= consecutive_window_ms then
      cef = cef + 1
    else
      cef = 1
    end
    redis.call('HSET', state_key, 'consecutive_eligible_failures', cef, 'last_failure_at_ms', now)

    local fire = false
    -- 快速触发：连续 N 次可归因失败（窗口内）。
    if cef >= consecutive_failures_target then fire = true end
    -- 比例触发：窗口内样本足够且失败率达标。
    local succ = tonumber(redis.call('HGET', state_key, 'eligible_successes')) or 0
    local fail = tonumber(redis.call('HGET', state_key, 'eligible_failures')) or 0
    local total = succ + fail
    if total >= min_requests and (fail / total) >= failure_ratio then fire = true end

    if fire then
      local level = tonumber(redis.call('HGET', state_key, 'open_level')) or 0
      local open_until, next_level = next_open_until(open_durations, level, now)
      local gen = (tonumber(redis.call('HGET', state_key, 'state_generation')) or 1) + 1
      redis.call('HSET', state_key, 'state', 'open', 'state_generation', gen,
        'open_until_ms', open_until, 'open_level', next_level, 'half_open_successes', '0',
        'last_transition_at_ms', now)
    end
  else
    -- ignored：既不增加失败也不清连续失败。
    return 'not_applicable'
  end

  -- TTFT 仅 channel。
  if is_channel == 1 and first_token_ms ~= '' then
    local sample = tonumber(first_token_ms)
    local samples = tonumber(redis.call('HGET', state_key, 'ttft_samples')) or 0
    if samples == 0 then
      redis.call('HSET', state_key, 'ttft_ewma_ms', sample, 'ttft_samples', 1)
    else
      local old = tonumber(redis.call('HGET', state_key, 'ttft_ewma_ms')) or sample
      local ewma = ttft_alpha * sample + (1 - ttft_alpha) * old
      redis.call('HSET', state_key, 'ttft_ewma_ms', ewma, 'ttft_samples', samples + 1)
    end
  end
  return 'applied'
end

local ep_disp = endpoint_fence_disposition
if ep_disp == nil then ep_disp = evidence_disposition end
if ep_disp == nil then
  ep_disp = apply_scope(endpoint_key, ep_outcome, 'endpoint_state_generation', 'endpoint_half_open_probe', 0)
end
local ch_disp = endpoint_fence_disposition
if ch_disp == nil then ch_disp = channel_fence_disposition end
if ch_disp == nil then
  ch_disp = apply_scope(channel_key, ch_outcome, 'channel_state_generation', 'channel_half_open_probe', 1)
end

-- 写 permit 终态（first-terminal-wins tombstone）。
local terminal_ttl = tonumber(redis.call('HGET', permit_key, 'terminal_ttl_ms')) or 300000
redis.call('HSET', permit_key, 'status', 'finished', 'terminal_at_ms', now,
  'endpoint_disposition', ep_disp, 'channel_disposition', ch_disp)
redis.call('PEXPIRE', permit_key, terminal_ttl)

return {ep_disp, ch_disp}
`

// luaAbort 实现 Abort：未进入真实 transport 的路径；first-terminal-wins；释放并发/half-open 租约，
// 不计成功/失败/EWMA/退避。KEYS/ARGV 使用 lifecycle guard 的 common shape。
const luaAbort = luaAttemptPermitLifecycleGuard + `
local function now_ms()
  local t = redis.call('TIME')
  return tonumber(t[1]) * 1000 + math.floor(tonumber(t[2]) / 1000)
end
local marker_key = KEYS[1]
local permit_key = KEYS[2]
local endpoint_key = KEYS[3]
local channel_key = KEYS[4]
local conc_key = KEYS[5]
local permit_id = ARGV[1]
local now = now_ms()

local lifecycle_guard = validate_attempt_permit_lifecycle()
if lifecycle_guard ~= nil then return {lifecycle_guard} end
if redis.call('HGET', permit_key, 'status') ~= 'active' then
  return {'terminal_conflict'}
end

if conc_key ~= '' then
  redis.call('ZREM', conc_key, permit_id)
end

-- pre-transport 精确归还 Channel RPM/RPD/TPM 预占（仅原始桶仍存在时）。
if redis.call('HGET', permit_key, 'admission_enforced') == '1' then
  local rpm_bucket = redis.call('HGET', permit_key, 'ch_rpm_bucket')
  local rpd_bucket = redis.call('HGET', permit_key, 'ch_rpd_bucket')
  local tpm_bucket = redis.call('HGET', permit_key, 'ch_tpm_bucket')
  local estimate = tonumber(redis.call('HGET', permit_key, 'tpm_estimate')) or 0
  if rpm_bucket ~= false and rpm_bucket ~= '' and redis.call('EXISTS', rpm_bucket) == 1 then redis.call('DECR', rpm_bucket) end
  if rpd_bucket ~= false and rpd_bucket ~= '' and redis.call('EXISTS', rpd_bucket) == 1 then redis.call('DECR', rpd_bucket) end
  if tpm_bucket ~= false and tpm_bucket ~= '' and estimate > 0 and redis.call('EXISTS', tpm_bucket) == 1 then redis.call('DECRBY', tpm_bucket, estimate) end
end

-- 释放本 permit 仍持有的 half-open 租约（不释放后来 permit 的租约）。
local function release_probe(state_key, probe_field)
  if redis.call('HGET', permit_key, probe_field) == '1' then
    if redis.call('HGET', state_key, 'half_open_permit_id') == permit_id then
      redis.call('HDEL', state_key, 'half_open_permit_id', 'half_open_lease_until_ms')
    end
  end
end
release_probe(endpoint_key, 'endpoint_half_open_probe')
release_probe(channel_key, 'channel_half_open_probe')

local terminal_ttl = tonumber(redis.call('HGET', permit_key, 'terminal_ttl_ms')) or 300000
redis.call('HSET', permit_key, 'status', 'aborted', 'terminal_at_ms', now)
redis.call('PEXPIRE', permit_key, terminal_ttl)
return {'aborted'}
`

// luaRenew 延长仍 active 的 permit 与并发租约，并对 generation 仍匹配的作用域延长 half-open 租约。
// KEYS/ARGV 使用 lifecycle guard 的 common shape。返回 {status, lease_until_ms}。
const luaRenew = luaAttemptPermitLifecycleGuard + `
local function now_ms()
  local t = redis.call('TIME')
  return tonumber(t[1]) * 1000 + math.floor(tonumber(t[2]) / 1000)
end
local marker_key = KEYS[1]
local permit_key = KEYS[2]
local endpoint_key = KEYS[3]
local channel_key = KEYS[4]
local conc_key = KEYS[5]
local permit_id = ARGV[1]
local now = now_ms()

local lifecycle_guard = validate_attempt_permit_lifecycle()
if lifecycle_guard ~= nil then return {lifecycle_guard} end
if redis.call('HGET', permit_key, 'status') ~= 'active' then return {'terminal_conflict'} end
local lease_until = tonumber(redis.call('HGET', permit_key, 'lease_until_ms')) or 0
if now >= lease_until then return {'expired'} end

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

local endpoint_fence_current = true
if redis.call('HGET', permit_key, 'endpoint_control_enforced') == '1' then
  endpoint_fence_current =
    redis.call('HGET', endpoint_key, 'base_url_revision_state') == 'active' and
    redis.call('HGET', endpoint_key, 'status_revision_state') == 'active' and
    redis.call('HGET', endpoint_key, 'base_url_fence_generation') == redis.call('HGET', permit_key, 'endpoint_base_url_fence_generation') and
    redis.call('HGET', endpoint_key, 'status_fence_generation') == redis.call('HGET', permit_key, 'endpoint_status_fence_generation') and
    redis.call('HGET', endpoint_key, 'base_url_revision') == redis.call('HGET', permit_key, 'endpoint_base_url_revision') and
    redis.call('HGET', endpoint_key, 'status_revision') == redis.call('HGET', permit_key, 'endpoint_status_revision')
end
local channel_fence_current = endpoint_fence_current and
  redis.call('HGET', channel_key, 'channel_config_revision') == redis.call('HGET', permit_key, 'channel_config_revision') and
  redis.call('HGET', channel_key, 'provider_endpoint_id') == redis.call('HGET', permit_key, 'endpoint_id') and
  redis.call('HGET', channel_key, 'base_url_revision') == redis.call('HGET', permit_key, 'endpoint_base_url_revision') and
  redis.call('HGET', channel_key, 'status_revision') == redis.call('HGET', permit_key, 'endpoint_status_revision')

local function renew_probe(state_key, gen_field, probe_field, fence_current)
  if fence_current and redis.call('HGET', permit_key, probe_field) == '1' then
    local permit_gen = tonumber(redis.call('HGET', permit_key, gen_field)) or 0
    local cur_gen = tonumber(redis.call('HGET', state_key, 'state_generation')) or -1
    if cur_gen == permit_gen and redis.call('HGET', state_key, 'half_open_permit_id') == permit_id then
      redis.call('HSET', state_key, 'half_open_lease_until_ms', new_lease)
    end
  end
end
renew_probe(endpoint_key, 'endpoint_state_generation', 'endpoint_half_open_probe', endpoint_fence_current)
renew_probe(channel_key, 'channel_state_generation', 'channel_half_open_probe', channel_fence_current)

return {'renewed', new_lease}
`

// luaReset 原子递增作用域 state_generation 并恢复 closed/no-sample（不删 key），旧 permit 随后 no-op。
// KEYS[1]=state key；Endpoint reset 时 KEYS[2..] 是三类 bounded evidence sets。返回 {new_generation}。
const luaReset = `
local function now_ms()
  local t = redis.call('TIME')
  return tonumber(t[1]) * 1000 + math.floor(tonumber(t[2]) / 1000)
end
local state_key = KEYS[1]
local now = now_ms()
local gen = (tonumber(redis.call('HGET', state_key, 'state_generation')) or 0) + 1
redis.call('HSET', state_key,
  'state', 'closed', 'state_generation', gen, 'window_started_at_ms', now,
  'eligible_successes', '0', 'eligible_failures', '0', 'consecutive_eligible_failures', '0',
  'open_level', '0', 'half_open_successes', '0', 'last_transition_at_ms', now)
redis.call('HDEL', state_key, 'half_open_permit_id', 'half_open_lease_until_ms', 'open_until_ms')
redis.call('HDEL', state_key, 'last_failure_at_ms', 'last_failure_category', 'ttft_ewma_ms', 'ttft_samples')
for i = 2, #KEYS do redis.call('DEL', KEYS[i]) end
return {gen}
`

// luaSnapshot 只读返回作用域当前运行态（用 Redis TIME 计算 open 剩余），不推进状态机。
// KEYS[1]=state key。返回 flat 数组 [field,value,...] 外加 now_ms 与 open_remaining_ms。
const luaSnapshot = `
local function now_ms()
  local t = redis.call('TIME')
  return tonumber(t[1]) * 1000 + math.floor(tonumber(t[2]) / 1000)
end
local state_key = KEYS[1]
local now = now_ms()
if redis.call('EXISTS', state_key) == 0 then
  return {'absent', now}
end
local h = redis.call('HGETALL', state_key)
local open_until = tonumber(redis.call('HGET', state_key, 'open_until_ms')) or 0
local remaining = 0
if open_until > now then remaining = open_until - now end
return {'present', now, remaining, h}
`

// luaSnapshotMany 是 routing 的只读线性化点。它一次校验完整性 marker、四项候选 control、
// 每个 Endpoint/Channel control 与 stable resource，并返回评分所需的原始事实；任一 runtime-sync
// 或数据形状错误都拒绝整批，绝不返回可被部分使用的结果。
//
// KEYS: marker, channel-rate, global-concurrency, circuit-breaker, routing-balance，随后每候选：
// endpoint, channel, channel-concurrency-zset, 429-cooldown, model-permission, channel-admission-control；
// 最后两个 key 是 infrastructure-fault latch 与 Redis instance reconciliation proof。
// ARGV: count, model_id, epoch, epoch_revision, 四项 expected revision；随后每候选：
// endpoint_id, channel_id, base_url_revision, status_revision, config_revision,
// channel_admission_revision, channel_rpm_bucket_prefix, channel_rpd_bucket_prefix,
// channel_tpm_bucket_prefix。
const luaSnapshotMany = luaRedisInstanceHelpers + luaAuthoritativeControlHelpers + `
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

if redis.call('EXISTS', KEYS[#KEYS - 1]) == 1 then
  return {'error', 'breaker_store_unavailable'}
end
local instance_matches = redis_instance_proof_matches(KEYS[#KEYS])
if instance_matches == nil then return redis.error_reply('invalid Redis instance reconciliation proof') end
if not instance_matches then return {'error', 'redis_instance_changed'} end

if redis_key_type(marker) ~= 'hash' or redis.call('HGET', marker, 'state') ~= 'ready' then
  return {'error', 'runtime_state_lost'}
end
if redis.call('HGET', marker, 'epoch') ~= expected_epoch or
    redis.call('HGET', marker, 'revision') ~= expected_epoch_revision then
  return {'error', 'stale_integrity_epoch'}
end

local function require_control(key, expected, parser, stale_reason)
  local value, state = read_new_admission_control(key, expected, parser)
  if value ~= nil then return value, nil end
  if state == 'stale_setting_revision' then return nil, stale_reason end
  return nil, state
end

local channel_rate, reason = require_control(channel_rate_ctl, tonumber(ARGV[5]), parse_rate_limit_defaults_payload, 'stale_admission_revision')
if channel_rate == nil then return {'error', reason} end
local global_conc
global_conc, reason = require_control(global_conc_ctl, tonumber(ARGV[6]), parse_global_concurrency_payload, 'stale_admission_revision')
if global_conc == nil then return {'error', reason} end
local breaker
breaker, reason = require_control(breaker_ctl, tonumber(ARGV[7]), parse_circuit_breaker_payload, 'stale_setting_revision')
if breaker == nil then return {'error', reason} end
local balance
balance, reason = require_control(balance_ctl, tonumber(ARGV[8]), parse_routing_balance_payload, 'stale_setting_revision')
if balance == nil then return {'error', reason} end

local t = redis.call('TIME')
local now = tonumber(t[1]) * 1000 + math.floor(tonumber(t[2]) / 1000)
local minute_bucket = math.floor(now / 60000)
local day_bucket = math.floor(now / 86400000)

local function read_snapshot(state_key)
  if redis.call('EXISTS', state_key) == 0 then return {'absent', now} end
  local fields = redis.call('HGETALL', state_key)
  local state = redis.call('HGET', state_key, 'state') or 'closed'
  local window_started_at_ms = tonumber(redis.call('HGET', state_key, 'window_started_at_ms'))
  if state == 'closed' and window_started_at_ms ~= nil and now - window_started_at_ms >= breaker.window_ms then
    -- SnapshotMany is read-only: make an expired eligible window neutral in the returned copy only.
    -- TTFT fields remain untouched because their EWMA lifetime is independent from the breaker window.
    for index = 1, #fields, 2 do
      if fields[index] == 'eligible_successes' or fields[index] == 'eligible_failures' then
        fields[index + 1] = '0'
      end
    end
  end
  local open_until = tonumber(redis.call('HGET', state_key, 'open_until_ms')) or 0
  local remaining = 0
  if open_until > now then remaining = open_until - now end
  return {'present', now, remaining, fields}
end

local rows = {}
for candidate = 1, count do
  local key_offset = 5 + (candidate - 1) * 6
  local arg_offset = 8 + (candidate - 1) * 9
  local endpoint_key = KEYS[key_offset + 1]
  local channel_key = KEYS[key_offset + 2]
  local concurrency_key = KEYS[key_offset + 3]
  local cooldown_key = KEYS[key_offset + 4]
  local permission_key = KEYS[key_offset + 5]
  local channel_ctl = KEYS[key_offset + 6]
  local expected_base_url_revision = ARGV[arg_offset + 3]
  local expected_status_revision = ARGV[arg_offset + 4]
  local expected_config_revision = ARGV[arg_offset + 5]
  local expected_channel_revision = tonumber(ARGV[arg_offset + 6])
  local rpm_key = ARGV[arg_offset + 7] .. minute_bucket
  local rpd_key = ARGV[arg_offset + 8] .. day_bucket
  local tpm_key = ARGV[arg_offset + 9] .. minute_bucket

  local endpoint_type = redis_key_type(endpoint_key)
  local channel_type = redis_key_type(channel_key)
  if (endpoint_type ~= 'none' and endpoint_type ~= 'hash') or
      (channel_type ~= 'none' and channel_type ~= 'hash') then
    return redis.error_reply('WRONGTYPE snapshot state key must be a hash')
  end

  local channel_limits
  channel_limits, reason = require_control(channel_ctl, expected_channel_revision, parse_channel_admission_payload, 'stale_admission_revision')
  if channel_limits == nil then return {'error', reason} end
  local effective_concurrency = resolve_channel_limit(channel_limits.concurrency, global_conc.channel_limit)
  local effective_rpm = resolve_channel_limit(channel_limits.rpm, channel_rate.rpm)
  local effective_rpd = resolve_channel_limit(channel_limits.rpd, channel_rate.rpd)
  local effective_tpm = resolve_channel_limit(channel_limits.tpm, channel_rate.tpm)

  local concurrency_used = active_zset_count(concurrency_key, now)
  local rpm_used = read_nonnegative_counter(rpm_key)
  local rpd_used = read_nonnegative_counter(rpd_key)
  local tpm_used = read_nonnegative_counter(tpm_key)
  if concurrency_used == nil or rpm_used == nil or rpd_used == nil or tpm_used == nil then
    return {'error', 'runtime_sync_required'}
  end

  local cooldown_remaining = 0
  local cooldown_type = redis_key_type(cooldown_key)
  if cooldown_type ~= 'none' and cooldown_type ~= 'hash' then return {'error', 'runtime_sync_required'} end
  if cooldown_type == 'hash' then
    local until_ms = tonumber(redis.call('HGET', cooldown_key, 'until_ms'))
    if until_ms == nil then return {'error', 'runtime_sync_required'} end
    if until_ms > now then cooldown_remaining = until_ms - now end
  end

  local permission_paused = 0
  local permission_state = 'absent'
  local permission_type = redis_key_type(permission_key)
  if permission_type ~= 'none' and permission_type ~= 'hash' then return {'error', 'runtime_sync_required'} end
  if permission_type == 'hash' then
    permission_state = redis.call('HGET', permission_key, 'recheck_state') or ''
    if permission_state == '' then return {'error', 'runtime_sync_required'} end
    if permission_state ~= 'cleared' and
        redis.call('HGET', permission_key, 'channel_config_revision') == expected_config_revision and
        redis.call('HGET', permission_key, 'endpoint_base_url_revision') == expected_base_url_revision and
        redis.call('HGET', permission_key, 'endpoint_status_revision') == expected_status_revision then
      permission_paused = 1
    end
  end

  rows[#rows + 1] = {
    cooldown_remaining, permission_paused, permission_state,
    concurrency_used, effective_concurrency,
    rpm_used, effective_rpm, rpd_used, effective_rpd, tpm_used, effective_tpm,
    read_snapshot(endpoint_key), read_snapshot(channel_key),
    redis.call('HGET', channel_ctl, 'active_payload'), redis.call('HGET', channel_ctl, 'active_payload_hash')
  }
end

local breaker_enabled = 0
if breaker.enabled then breaker_enabled = 1 end
local control_proofs = {
  {redis.call('HGET', channel_rate_ctl, 'active_payload'), redis.call('HGET', channel_rate_ctl, 'active_payload_hash')},
  {redis.call('HGET', global_conc_ctl, 'active_payload'), redis.call('HGET', global_conc_ctl, 'active_payload_hash')},
  {redis.call('HGET', breaker_ctl, 'active_payload'), redis.call('HGET', breaker_ctl, 'active_payload_hash')},
  {redis.call('HGET', balance_ctl, 'active_payload'), redis.call('HGET', balance_ctl, 'active_payload_hash')}
}
return {'ok', now, tonumber(ARGV[8]), balance.ttft_target_ms, tostring(balance.ttft_weight),
  tostring(balance.minimum_routing_factor), tostring(balance.cost_weight), breaker_enabled, rows, control_proofs}
`

// luaSetCooldown 登记/延长 Channel 429 冷却（所有 Gateway 共享）。取现有与新 until 的较大值（不缩短），
// 物理 TTL 覆盖到 until + 少量余量。KEYS[1]=cooldown key。ARGV[1]=duration_ms, ARGV[2]=source_retry_after_ms。
// 返回 {until_ms}。
const luaSetCooldown = `
local function now_ms()
  local t = redis.call('TIME')
  return tonumber(t[1]) * 1000 + math.floor(tonumber(t[2]) / 1000)
end
local cooldown_key = KEYS[1]
local duration_ms = tonumber(ARGV[1])
local source_retry_after_ms = tonumber(ARGV[2])
local now = now_ms()
local until_ms = now + duration_ms
local existing = tonumber(redis.call('HGET', cooldown_key, 'until_ms')) or 0
if existing > until_ms then until_ms = existing end
redis.call('HSET', cooldown_key, 'until_ms', until_ms, 'source_retry_after_ms', source_retry_after_ms)
redis.call('PEXPIRE', cooldown_key, (until_ms - now) + 5000)
return {until_ms}
`

// luaCooldownRemaining 只读返回 Channel 429 冷却剩余毫秒（0 表示无冷却或已到期）。KEYS[1]=cooldown key。
const luaCooldownRemaining = `
local function now_ms()
  local t = redis.call('TIME')
  return tonumber(t[1]) * 1000 + math.floor(tonumber(t[2]) / 1000)
end
local cooldown_key = KEYS[1]
if redis.call('EXISTS', cooldown_key) == 0 then return {0} end
local until_ms = tonumber(redis.call('HGET', cooldown_key, 'until_ms')) or 0
local now = now_ms()
if until_ms <= now then return {0} end
return {until_ms - now}
`

// luaPausePermission 登记/更新 (channel_id, model_id) 403 权限暂停，固化观察到的三类 revision，
// 并把该唯一绑定立即排入复检队列（§2.4.2）。同 revision 已在检查或退避时不缩短既有租约/退避；
// 任一迟到的旧 revision 都只能 no-op，不能覆盖较新的暂停或复检状态。
// KEYS: permission key, recheck queue。ARGV: config_rev, base_url_rev, status_rev, channel_id, model_id。
const luaPausePermission = `
local function now_ms()
  local t = redis.call('TIME')
  return tonumber(t[1]) * 1000 + math.floor(tonumber(t[2]) / 1000)
end
local key = KEYS[1]
local queue_key = KEYS[2]
local now = now_ms()
local exists = redis.call('EXISTS', key) == 1
local same_identity = exists and
  redis.call('HGET', key, 'channel_id') == ARGV[4] and
  redis.call('HGET', key, 'model_id') == ARGV[5]
local current_config_revision = tonumber(redis.call('HGET', key, 'channel_config_revision'))
local current_base_url_revision = tonumber(redis.call('HGET', key, 'endpoint_base_url_revision'))
local current_status_revision = tonumber(redis.call('HGET', key, 'endpoint_status_revision'))
local incoming_config_revision = tonumber(ARGV[1])
local incoming_base_url_revision = tonumber(ARGV[2])
local incoming_status_revision = tonumber(ARGV[3])

if same_identity and current_config_revision and current_base_url_revision and current_status_revision and
    (incoming_config_revision < current_config_revision or
     incoming_base_url_revision < current_base_url_revision or
     incoming_status_revision < current_status_revision) then
  return {'stale', redis.call('HGET', key, 'recheck_state') or ''}
end

local same_revision = same_identity and
  redis.call('HGET', key, 'channel_config_revision') == ARGV[1] and
  redis.call('HGET', key, 'endpoint_base_url_revision') == ARGV[2] and
  redis.call('HGET', key, 'endpoint_status_revision') == ARGV[3]

if same_revision then
  local state = redis.call('HGET', key, 'recheck_state') or ''
  if state ~= 'cleared' and state ~= 'stale' then
    -- ZADD NX keeps an existing claim lease or retry backoff. It only repairs a missing queue member.
    redis.call('ZADD', queue_key, 'NX', now, key)
    return {'paused', state}
  end
end

redis.call('HSET', key,
  'channel_config_revision', ARGV[1],
  'endpoint_base_url_revision', ARGV[2],
  'endpoint_status_revision', ARGV[3],
  'channel_id', ARGV[4],
  'model_id', ARGV[5],
  'paused_at_ms', now,
  'recheck_state', 'queued',
  'last_rechecked_at_ms', '0',
  'recheck_attempts', '0')
redis.call('HDEL', key, 'claim_token', 'claimed_by', 'claim_until_ms')
redis.call('ZADD', queue_key, now, key)
return {'paused', 'queued'}
`

// luaClearPermission 仅在暂停记录的三类 revision 仍与调用方 expected 一致时 CAS 清除暂停（复检通过，§2.4.4）。
// KEYS: permission key, recheck queue。ARGV: expected config_rev, base_url_rev, status_rev。
// 返回 {'cleared'} 或 {'stale'} 或 {'absent'}。
const luaClearPermission = `
local function now_ms()
  local t = redis.call('TIME')
  return tonumber(t[1]) * 1000 + math.floor(tonumber(t[2]) / 1000)
end
local key = KEYS[1]
local queue_key = KEYS[2]
if redis.call('EXISTS', key) == 0 then
  redis.call('ZREM', queue_key, key)
  return {'absent'}
end
local p_cfg = redis.call('HGET', key, 'channel_config_revision')
local p_burl = redis.call('HGET', key, 'endpoint_base_url_revision')
local p_sts = redis.call('HGET', key, 'endpoint_status_revision')
if p_cfg == ARGV[1] and p_burl == ARGV[2] and p_sts == ARGV[3] then
  redis.call('HSET', key, 'recheck_state', 'cleared', 'last_rechecked_at_ms', now_ms())
  redis.call('HDEL', key, 'claim_token', 'claimed_by', 'claim_until_ms')
  redis.call('ZREM', queue_key, key)
  return {'cleared'}
end
return {'stale'}
`
