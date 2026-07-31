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

local permit_key = KEYS[2]
local origin_key = KEYS[3]
local channel_key = KEYS[4]
local conc_key = KEYS[5]
local route_channel_rpd_key = KEYS[6]
local breaker_ctl = KEYS[7]
local evidence_channels_key = KEYS[8]
local evidence_models_key = KEYS[9]

local permit_id = ARGV[1]
local ep_outcome = ARGV[18]
local ch_outcome = ARGV[19]
local tpm_actual = ARGV[20] -- '' 表示无权威 usage；此时至少保留输入估算
local origin_evidence = ARGV[21]
local request_write_state = ARGV[22]
local response_headers_received = ARGV[23]
local first_token_eligible = ARGV[24]
local interaction_evidence = request_write_state == 'completed'
  or request_write_state == 'uncertain'
  or response_headers_received == 'true'
  or first_token_eligible == 'true'

local now = now_ms()

local lifecycle_guard = validate_attempt_permit_lifecycle()
if lifecycle_guard ~= nil then
  if lifecycle_guard == 'conflict' then lifecycle_guard = 'terminal_conflict' end
  return { lifecycle_guard, lifecycle_guard }
end
if redis.call('HGET', permit_key, 'status') ~= 'active' then
  return {
    redis.call('HGET', permit_key, 'origin_disposition') or 'terminal_conflict',
    redis.call('HGET', permit_key, 'channel_disposition') or 'terminal_conflict',
  }
end

-- Channel TPM 已不再是准入门槛（§1.2/§8），无需结算算术校验；仍在 permit 上记录终态口径。
if tpm_actual ~= '' then
  if string.match(tpm_actual, '^%d+$') == nil then return { 'runtime_sync_required', 'runtime_sync_required' } end
  local actual = tonumber(tpm_actual)
  if actual == nil or actual > MAX_EXACT_INTEGER or actual ~= math.floor(actual) then
    return { 'runtime_sync_required', 'runtime_sync_required' }
  end
end

-- 归因桶只记录已经进入真实上游交互的 attempt。它不参与 Channel 全局 RPD 限制，
-- 但必须和 permit 冻结的 route/channel/day key 一起原子写入，避免 fallback 漏记。
if route_channel_rpd_key ~= '' and interaction_evidence then
  local attribution_type = attempt_key_type(route_channel_rpd_key)
  if attribution_type ~= 'none' and attribution_type ~= 'string' then
    return { 'runtime_sync_required', 'runtime_sync_required' }
  end
  redis.call('INCR', route_channel_rpd_key)
  if redis.call('PTTL', route_channel_rpd_key) < 0 then
    redis.call('PEXPIRE', route_channel_rpd_key, 86400000 + 420000)
  end
end

local breaker = read_committed_control(breaker_ctl, parse_circuit_breaker_payload)
local breaker_config_valid = breaker ~= nil
local breaker_enabled = 0
local window_ms = 1
local min_requests = 2
local failure_ratio = 1
local consecutive_failures_target = 1
local consecutive_window_ms = 1
local half_open_successes_target = 2
local open_durations = { 1 }
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
if conc_key ~= '' then redis.call('ZREM', conc_key, permit_id) end

-- Channel TPM 不再占用限额，只在 permit 上冻结终态口径供审计（settled=有可靠 actual；retained=仅输入估算）。
if redis.call('HGET', permit_key, 'admission_enforced') == '1' then
  if tpm_actual == '' then
    redis.call('HSET', permit_key, 'tpm_state', 'retained')
  else
    redis.call('HSET', permit_key, 'tpm_actual_total', tonumber(tpm_actual) or 0, 'tpm_state', 'settled')
  end
end

redis.call(
  'HSET',
  permit_key,
  'request_write_state',
  request_write_state,
  'response_headers_received',
  response_headers_received,
  'first_token_eligible',
  first_token_eligible
)

-- Origin fence 在 permit 服务端记录中冻结。prepare 即使最终 abort，也已永久推进 fence generation；
-- 因此旧 permit 的真实结果只能收口资源，不得写 Origin 或任一子 Channel 的当前 breaker/TTFT。
local origin_fence_disposition = nil
if redis.call('HGET', permit_key, 'origin_control_enforced') == '1' then
  local stored_base_fence = redis.call('HGET', permit_key, 'origin_fence_generation')
  local stored_status_fence = redis.call('HGET', permit_key, 'status_fence_generation')
  local current_base_fence = redis.call('HGET', origin_key, 'origin_fence_generation')
  local current_status_fence = redis.call('HGET', origin_key, 'status_fence_generation')
  if
    stored_base_fence == false
    or stored_status_fence == false
    or current_base_fence == false
    or current_status_fence == false
  then
    origin_fence_disposition = 'runtime_sync_required'
  elseif
    redis.call('HGET', origin_key, 'status_revision_state') ~= 'active'
    or current_status_fence ~= stored_status_fence
    or redis.call('HGET', origin_key, 'status_revision') ~= redis.call('HGET', permit_key, 'status_revision')
  then
    origin_fence_disposition = 'stale_status_revision'
  elseif
    redis.call('HGET', origin_key, 'origin_revision_state') ~= 'active'
    or current_base_fence ~= stored_base_fence
    or redis.call('HGET', origin_key, 'origin_revision') ~= redis.call('HGET', permit_key, 'origin_revision')
  then
    origin_fence_disposition = 'stale_revision'
  end
end

local channel_fence_disposition = nil
if redis.call('EXISTS', channel_key) == 0 then
  channel_fence_disposition = 'stale_generation'
elseif
  redis.call('HGET', channel_key, 'channel_config_revision')
  ~= redis.call('HGET', permit_key, 'channel_config_revision')
then
  channel_fence_disposition = 'stale_config_revision'
elseif redis.call('HGET', channel_key, 'provider_id') ~= redis.call('HGET', permit_key, 'provider_id') then
  channel_fence_disposition = 'stale_config_revision'
elseif redis.call('HGET', channel_key, 'status_revision') ~= redis.call('HGET', permit_key, 'status_revision') then
  channel_fence_disposition = 'stale_status_revision'
elseif redis.call('HGET', channel_key, 'origin_revision') ~= redis.call('HGET', permit_key, 'origin_revision') then
  channel_fence_disposition = 'stale_revision'
end

-- 条件 Origin 故障必须在同一次 Finish 中原子收集多 Gateway 共享证据。集合使用固定窗口，
-- 且最多保存配置阈值数量的整数 ID，避免随 Channel/model 数量无界增长。
local evidence_disposition = nil
if origin_evidence ~= '' then
  if origin_fence_disposition ~= nil then
    evidence_disposition = origin_fence_disposition
  elseif not breaker_config_valid then
    evidence_disposition = 'runtime_sync_required'
  elseif breaker_enabled == 0 then
    evidence_disposition = 'not_applicable'
  elseif redis.call('EXISTS', origin_key) == 0 then
    evidence_disposition = 'stale_generation'
  elseif
    (tonumber(redis.call('HGET', origin_key, 'state_generation')) or 0)
    ~= (tonumber(redis.call('HGET', permit_key, 'provider_state_generation')) or -1)
  then
    evidence_disposition = 'stale_generation'
  else
    local channel_key_type = attempt_key_type(evidence_channels_key)
    local model_key_type = attempt_key_type(evidence_models_key)
    if
      (channel_key_type ~= 'none' and channel_key_type ~= 'set')
      or (model_key_type ~= 'none' and model_key_type ~= 'set')
    then
      evidence_disposition = 'runtime_sync_required'
    else
      local channel_limit = breaker.provider_ambiguous_distinct_channels
      local model_limit = breaker.provider_ambiguous_distinct_models
      local channel_id = redis.call('HGET', permit_key, 'channel_id')
      local model_id = redis.call('HGET', permit_key, 'model_id')

      if
        redis.call('SISMEMBER', evidence_channels_key, channel_id) == 0
        and redis.call('SCARD', evidence_channels_key) < channel_limit
      then
        redis.call('SADD', evidence_channels_key, channel_id)
      end
      if
        redis.call('SISMEMBER', evidence_models_key, model_id) == 0
        and redis.call('SCARD', evidence_models_key) < model_limit
      then
        redis.call('SADD', evidence_models_key, model_id)
      end
      if redis.call('PTTL', evidence_channels_key) < 0 then redis.call('PEXPIRE', evidence_channels_key, window_ms) end
      if redis.call('PTTL', evidence_models_key) < 0 then redis.call('PEXPIRE', evidence_models_key, window_ms) end

      if
        redis.call('SCARD', evidence_channels_key) >= channel_limit
        and redis.call('SCARD', evidence_models_key) >= model_limit
      then
        ep_outcome = 'eligible_failure'
      end
    end
  end
end

-- apply_scope 对某作用域应用 outcome，返回 disposition。
local function apply_scope(state_key, outcome, permit_gen_field, permit_probe_field, is_channel)
  local permit_gen = tonumber(redis.call('HGET', permit_key, permit_gen_field)) or 0
  local probe = redis.call('HGET', permit_key, permit_probe_field)

  if not breaker_config_valid then return 'runtime_sync_required' end
  if breaker_enabled == 0 then return 'not_applicable' end
  if redis.call('EXISTS', state_key) == 0 then return 'stale_generation' end
  local cur_gen = tonumber(redis.call('HGET', state_key, 'state_generation')) or 0
  if cur_gen ~= permit_gen then return 'stale_generation' end

  -- half-open 探测收口：核对 lease 归属与有效期。
  if probe == '1' then
    local holder = redis.call('HGET', state_key, 'half_open_permit_id')
    local lease_until = tonumber(redis.call('HGET', state_key, 'half_open_lease_until_ms')) or 0
    if holder == permit_id and now < lease_until then
      if outcome == 'eligible_success' then
        local hos = (tonumber(redis.call('HGET', state_key, 'half_open_successes')) or 0) + 1
        if hos >= half_open_successes_target then
          local gen = (tonumber(redis.call('HGET', state_key, 'state_generation')) or 1) + 1
          redis.call(
            'HSET',
            state_key,
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
          redis.call('HDEL', state_key, 'half_open_permit_id', 'half_open_lease_until_ms')
        else
          redis.call('HSET', state_key, 'half_open_successes', hos)
          redis.call('HDEL', state_key, 'half_open_permit_id', 'half_open_lease_until_ms')
        end
      elseif outcome == 'eligible_failure' then
        local level = tonumber(redis.call('HGET', state_key, 'open_level')) or 0
        local open_until, next_level = next_open_until(open_durations, level, now)
        local gen = (tonumber(redis.call('HGET', state_key, 'state_generation')) or 1) + 1
        redis.call(
          'HSET',
          state_key,
          'state',
          'open',
          'state_generation',
          gen,
          'open_until_ms',
          open_until,
          'open_level',
          next_level,
          'half_open_successes',
          '0',
          'last_transition_at_ms',
          now,
          'last_failure_at_ms',
          now
        )
        redis.call('HDEL', state_key, 'half_open_permit_id', 'half_open_lease_until_ms')
      else
        -- ignored：中性释放 lease，不计成功/失败。
        redis.call('HDEL', state_key, 'half_open_permit_id', 'half_open_lease_until_ms')
      end
      -- TTFT 不再写入 breaker 状态：评分样本由独立的 30 分钟分钟桶承担（§12），与熔断解耦。
      return 'applied'
    else
      -- 探测租约已被新一轮夺走或过期：本结果中性 no-op。
      return 'stale_generation'
    end
  end

  -- closed 普通窗口计数（半开由上面分支处理）。
  local cur_state = redis.call('HGET', state_key, 'state')
  if cur_state == 'open' then return 'stale_generation' end

  -- 窗口过期则重置计数。
  local ws = tonumber(redis.call('HGET', state_key, 'window_started_at_ms')) or now
  if now - ws >= window_ms then
    redis.call('HSET', state_key, 'window_started_at_ms', now, 'eligible_successes', '0', 'eligible_failures', '0')
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
      redis.call(
        'HSET',
        state_key,
        'state',
        'open',
        'state_generation',
        gen,
        'open_until_ms',
        open_until,
        'open_level',
        next_level,
        'half_open_successes',
        '0',
        'last_transition_at_ms',
        now
      )
    end
  else
    -- ignored：既不增加失败也不清连续失败。
    return 'not_applicable'
  end

  -- TTFT 不再写入 breaker 状态：评分样本由独立的 30 分钟分钟桶承担（§12），与熔断解耦。
  return 'applied'
end

local ep_disp = origin_fence_disposition
if ep_disp == nil then ep_disp = evidence_disposition end
if ep_disp == nil then
  ep_disp = apply_scope(origin_key, ep_outcome, 'provider_state_generation', 'provider_half_open_probe', 0)
end
local ch_disp = origin_fence_disposition
if ch_disp == nil then ch_disp = channel_fence_disposition end
if ch_disp == nil then
  ch_disp = apply_scope(channel_key, ch_outcome, 'channel_state_generation', 'channel_half_open_probe', 1)
end

-- 写 permit 终态（first-terminal-wins tombstone）。
local terminal_ttl = tonumber(redis.call('HGET', permit_key, 'terminal_ttl_ms')) or 300000
redis.call(
  'HSET',
  permit_key,
  'status',
  'finished',
  'terminal_at_ms',
  now,
  'origin_disposition',
  ep_disp,
  'channel_disposition',
  ch_disp
)
redis.call('PEXPIRE', permit_key, terminal_ttl)

return { ep_disp, ch_disp }
