package breakerstore

// 入口 RequestAdmission Lua（§5.3.17、§2.12、§2.14）。route-user RPM/RPD + concurrency 对整次客户请求
// 只计算一次；TPM 由 ReserveRequestTokens 幂等预占一次。所有操作先校验完整性 epoch marker 与两个全局
// route-rate 与 concurrency control 的 active/pending/revision，再进入 all-or-nothing 写阶段；`0=不限` 仍写稳定窗口桶。
// request token 的生命周期参数只读 circuit-breaker 最后 committed active payload；该 control 的 pending
// 不阻断入口准入，且 request token 不冻结/校验 circuit-breaker revision。
//
// 稳定窗口桶（RPM 分钟 / RPD 日 / TPM 分钟）由 Go 依据 bucket 号构造 key 传入（单 Redis 部署，§D19）；
// 并发 active set 与租约有效期用 Redis TIME 判定。

// luaAcquireRequestAdmission
// KEYS: [1]=route_rate_control [2]=global_conc_control [3]=circuit_breaker_control [4]=marker
//
//	[5]=request_token [6]=rpm_bucket [7]=rpd_bucket [8]=conc_zset [9]=infrastructure_fault_latch
//	[10]=Redis instance reconciliation proof
//
// ARGV: request_admission_id, fingerprint, route_id, user_id, expected_epoch, expected_epoch_revision,
//
//	expected_route_rate_rev, expected_conc_rev,
//	trusted_rpm_override, trusted_rpd_override, trusted_tpm_override, trusted_concurrency_override
//
// 返回 {'allowed', lease_until_ms, renew_interval_ms} | {'limited', dim} | {'store_unavailable'?}(基础设施由 Go 处理)
//
//	{'runtime_state_lost'|'stale_integrity_epoch'} | {'runtime_sync_required'|'runtime_sync_pending'|'stale_setting_revision', which}
//	{'conflict'} | {'idempotent', lease_until_ms, renew_interval_ms}
const luaAcquireRequestAdmission = luaRedisInstanceHelpers + luaAuthoritativeControlHelpers + `
local function now_ms()
  local t = redis.call('TIME')
  return tonumber(t[1]) * 1000 + math.floor(tonumber(t[2]) / 1000)
end
local route_rate_ctl = KEYS[1]
local conc_ctl = KEYS[2]
local breaker_ctl = KEYS[3]
local marker = KEYS[4]
local token_key = KEYS[5]
local rpm_key = KEYS[6]
local rpd_key = KEYS[7]
local conc_key = KEYS[8]
local fault_latch = KEYS[9]
local instance_proof = KEYS[10]

local rid = ARGV[1]
local fingerprint = ARGV[2]
local route_id = ARGV[3]
local user_id = ARGV[4]
local expected_epoch = ARGV[5]
local expected_epoch_rev = ARGV[6]
local expected_route_rate_rev = tonumber(ARGV[7])
local expected_conc_rev = tonumber(ARGV[8])
local rpm_override = ARGV[9]
local rpd_override = ARGV[10]
local tpm_override = ARGV[11]
local concurrency_override = ARGV[12]

if redis.call('EXISTS', fault_latch) == 1 then return {'store_unavailable'} end
local instance_matches = redis_instance_proof_matches(instance_proof)
if instance_matches == nil then return redis.error_reply('invalid Redis instance reconciliation proof') end
if not instance_matches then return {'redis_instance_changed'} end
local now = now_ms()

-- 完整性 epoch 硬门禁。
if redis_key_type(marker) ~= 'hash' then return {'runtime_state_lost'} end
if redis.call('HGET', marker, 'state') ~= 'ready' then return {'runtime_state_lost'} end
if redis.call('HGET', marker, 'epoch') ~= expected_epoch then return {'stale_integrity_epoch'} end
if redis.call('HGET', marker, 'revision') ~= expected_epoch_rev then return {'stale_integrity_epoch'} end

-- 幂等恢复不重新取得资源；control 在响应丢失后进入 pending 也可取回原 token。
local token_type = redis_key_type(token_key)
if token_type == 'hash' then
  if redis.call('HGET', token_key, 'admission_fingerprint') ~= fingerprint then return {'conflict'} end
  if redis.call('HGET', token_key, 'status') ~= 'active' then return {'conflict'} end
  if redis.call('HGET', token_key, 'route_id') ~= route_id or redis.call('HGET', token_key, 'user_id') ~= user_id or
      redis.call('HGET', token_key, 'runtime_integrity_epoch') ~= expected_epoch or
      redis.call('HGET', token_key, 'runtime_integrity_revision') ~= expected_epoch_rev or
      redis.call('HGET', token_key, 'route_rate_limits_revision') ~= ARGV[7] or
      redis.call('HGET', token_key, 'global_concurrency_revision') ~= ARGV[8] or
      redis.call('HGET', token_key, 'rpm_override') ~= rpm_override or
      redis.call('HGET', token_key, 'rpd_override') ~= rpd_override or
      redis.call('HGET', token_key, 'tpm_override') ~= tpm_override or
      redis.call('HGET', token_key, 'concurrency_override') ~= concurrency_override then
    return {'conflict'}
  end
  local lease_until = tonumber(redis.call('HGET', token_key, 'lease_until_ms'))
  local renew_ms = tonumber(redis.call('HGET', token_key, 'renew_ms'))
  if lease_until == nil or renew_ms == nil or renew_ms <= 0 then
    return {'runtime_sync_required', 'request_token'}
  end
  return {'idempotent', lease_until, renew_ms}
end
if token_type ~= 'none' then return {'runtime_sync_required', 'request_token'} end

-- 新 token 的所有执行值都来自 committed active controls；调用方 effective/TTL 不参与。
local route_rate, route_rate_state = read_new_admission_control(route_rate_ctl, expected_route_rate_rev, parse_rate_limit_defaults_payload)
if route_rate == nil then return {route_rate_state, 'route_rate'} end
local concurrency, concurrency_state = read_new_admission_control(conc_ctl, expected_conc_rev, parse_global_concurrency_payload)
if concurrency == nil then return {concurrency_state, 'global_concurrency'} end
local breaker = read_committed_control(breaker_ctl, parse_circuit_breaker_payload)
if breaker == nil then return {'runtime_sync_required', 'circuit_breaker'} end

local eff_rpm = resolve_request_limit_override(rpm_override, route_rate.rpm)
local eff_rpd = resolve_request_limit_override(rpd_override, route_rate.rpd)
local eff_tpm = resolve_request_limit_override(tpm_override, route_rate.tpm)
local eff_conc = resolve_request_limit_override(concurrency_override, concurrency.key_limit)
if eff_rpm == nil or eff_rpd == nil or eff_tpm == nil or eff_conc == nil then
  return {'runtime_sync_required', 'request_overrides'}
end
local lease_ttl_ms = breaker.attempt_permit_ttl_ms
local renew_ms = breaker.attempt_permit_renew_interval_ms
local terminal_ttl_ms = breaker.attempt_permit_terminal_ttl_ms
local bucket_ttl_ms = lease_ttl_ms + terminal_ttl_ms + 120000

-- Validate every resource key/value before the unified write stage. Redis Lua errors do not roll back.
local rpm_used = read_nonnegative_counter(rpm_key)
local rpd_used = read_nonnegative_counter(rpd_key)
local conc_used = active_zset_count(conc_key, now)
if rpm_used == nil or rpd_used == nil or conc_used == nil then
  return {'runtime_sync_required', 'request_resources'}
end
if rpm_used >= MAX_EXACT_INTEGER or rpd_used >= MAX_EXACT_INTEGER then
  return {'runtime_sync_required', 'request_resources'}
end
if eff_rpm > 0 and rpm_used + 1 > eff_rpm then return {'limited', 'rpm'} end
if eff_rpd > 0 and rpd_used + 1 > eff_rpd then return {'limited', 'rpd'} end
if eff_conc > 0 and conc_used >= eff_conc then return {'limited', 'concurrency'} end

-- Unified write stage: limits are already satisfied and all key types are known.
redis.call('INCR', rpm_key)
redis.call('PEXPIRE', rpm_key, bucket_ttl_ms)
redis.call('INCR', rpd_key)
redis.call('PEXPIRE', rpd_key, bucket_ttl_ms)
local lease_until = now + lease_ttl_ms
redis.call('ZREMRANGEBYSCORE', conc_key, '-inf', now)
redis.call('ZADD', conc_key, lease_until, rid)
redis.call('PEXPIRE', conc_key, lease_until - now + terminal_ttl_ms)

redis.call('HSET', token_key,
  'status', 'active',
  'route_id', route_id, 'user_id', user_id,
  'admission_fingerprint', fingerprint,
  'runtime_integrity_epoch', expected_epoch, 'runtime_integrity_revision', expected_epoch_rev,
  'route_rate_limits_revision', expected_route_rate_rev, 'global_concurrency_revision', expected_conc_rev,
  'rpm_override', rpm_override, 'rpd_override', rpd_override,
  'tpm_override', tpm_override, 'concurrency_override', concurrency_override,
  'eff_rpm', eff_rpm, 'eff_rpd', eff_rpd, 'eff_tpm', eff_tpm, 'eff_concurrency', eff_conc,
  'rpm_bucket', rpm_key, 'rpd_bucket', rpd_key, 'conc_key', conc_key,
  'reserve_state', 'none',
  'lease_ttl_ms', lease_ttl_ms, 'renew_ms', renew_ms, 'terminal_ttl_ms', terminal_ttl_ms,
  'bucket_ttl_ms', bucket_ttl_ms,
  'acquired_at_ms', now, 'lease_until_ms', lease_until)
redis.call('PEXPIRE', token_key, lease_until - now + terminal_ttl_ms)
return {'allowed', lease_until, renew_ms}
`

// luaReserveRequestTokens 一次性幂等 TPM 预占（§2.14.11）。首次 reserved|limited 结果固化在 token；
// 同估算重试返回首次结果，异估算 conflict。KEYS[1]=marker KEYS[2]=request_token
// KEYS[3]=tpm_bucket KEYS[4]=infrastructure_fault_latch KEYS[5]=Redis instance reconciliation proof。
// ARGV: estimated_tokens, route_id, user_id, expected_epoch, expected_epoch_revision。
// TPM limit/bucket TTL 只从 request token 冻结值读取；marker/token/expected 三方 epoch 不一致时零写入。
// 返回 {'reserved'|'limited'|'conflict'|'unknown_request_admission'|'runtime_state_lost'|'stale_integrity_epoch'}。
const luaReserveRequestTokens = luaRedisInstanceHelpers + `
local function key_type(key)
  local reply = redis.call('TYPE', key)
  if type(reply) == 'table' then return reply['ok'] end
  return reply
end
local marker = KEYS[1]
local token_key = KEYS[2]
local tpm_key = KEYS[3]
if redis.call('EXISTS', KEYS[4]) == 1 then return {'store_unavailable'} end
local instance_matches = redis_instance_proof_matches(KEYS[5])
if instance_matches == nil then return redis.error_reply('invalid Redis instance reconciliation proof') end
if not instance_matches then return {'redis_instance_changed'} end
local estimate = tonumber(ARGV[1])
local route_id = ARGV[2]
local user_id = ARGV[3]
local expected_epoch = ARGV[4]
local expected_epoch_rev = ARGV[5]

if key_type(marker) ~= 'hash' then return {'runtime_state_lost'} end
if redis.call('HGET', marker, 'state') ~= 'ready' then return {'runtime_state_lost'} end
if redis.call('HGET', marker, 'epoch') ~= expected_epoch or
    redis.call('HGET', marker, 'revision') ~= expected_epoch_rev then
  return {'stale_integrity_epoch'}
end

local token_type = key_type(token_key)
if token_type ~= 'hash' then return {'unknown_request_admission'} end
if redis.call('HGET', token_key, 'runtime_integrity_epoch') ~= expected_epoch or
    redis.call('HGET', token_key, 'runtime_integrity_revision') ~= expected_epoch_rev then
  return {'stale_integrity_epoch'}
end
if redis.call('HGET', token_key, 'status') ~= 'active' then return {'unknown_request_admission'} end
if redis.call('HGET', token_key, 'route_id') ~= route_id or redis.call('HGET', token_key, 'user_id') ~= user_id then
  return {'conflict'}
end

local state = redis.call('HGET', token_key, 'reserve_state')
if state == 'reserved' or state == 'limited' then
  local prev = tonumber(redis.call('HGET', token_key, 'reserve_estimated_input_tokens')) or -1
  if prev ~= estimate then return {'conflict'} end
  return {state}
end

local eff_tpm_raw = redis.call('HGET', token_key, 'eff_tpm')
local bucket_ttl_raw = redis.call('HGET', token_key, 'bucket_ttl_ms')
if eff_tpm_raw == false or string.match(eff_tpm_raw, '^%d+$') == nil or
    bucket_ttl_raw == false or string.match(bucket_ttl_raw, '^%d+$') == nil then
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
  used = tonumber(raw)
end
if eff_tpm > 0 and used + estimate > eff_tpm then
  redis.call('HSET', token_key, 'reserve_state', 'limited', 'reserve_estimated_input_tokens', estimate, 'reserve_result', 'limited')
  return {'limited'}
end
redis.call('INCRBY', tpm_key, estimate)
redis.call('PEXPIRE', tpm_key, bucket_ttl_ms)
redis.call('HSET', token_key, 'reserve_state', 'reserved', 'reserve_estimated_input_tokens', estimate,
  'reserve_result', 'reserved', 'reserved_tpm_bucket', tpm_key, 'reserved_tpm_amount', estimate)
return {'reserved'}
`

// luaRenewRequestAdmission 延长 active token 与 route-user concurrency lease；不重复增加窗口资源。
// KEYS[1]=marker KEYS[2]=request_token KEYS[3]=conc_zset。
// ARGV: request_admission_id, route_id, user_id, expected_epoch, expected_epoch_revision。
// marker/token/expected 三方 epoch 不一致时零写入。
const luaRenewRequestAdmission = `
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

if key_type(marker) ~= 'hash' then return {'runtime_state_lost'} end
if redis.call('HGET', marker, 'state') ~= 'ready' then return {'runtime_state_lost'} end
if redis.call('HGET', marker, 'epoch') ~= expected_epoch or
    redis.call('HGET', marker, 'revision') ~= expected_epoch_rev then
  return {'stale_integrity_epoch'}
end

if key_type(token_key) ~= 'hash' then return {'unknown_request_admission'} end
if redis.call('HGET', token_key, 'runtime_integrity_epoch') ~= expected_epoch or
    redis.call('HGET', token_key, 'runtime_integrity_revision') ~= expected_epoch_rev then
  return {'stale_integrity_epoch'}
end
if redis.call('HGET', token_key, 'route_id') ~= route_id or
    redis.call('HGET', token_key, 'user_id') ~= user_id or
    redis.call('HGET', token_key, 'conc_key') ~= conc_key then
  return {'conflict'}
end
if redis.call('HGET', token_key, 'status') ~= 'active' then return {'terminal'} end

local lease_raw = redis.call('HGET', token_key, 'lease_until_ms')
local ttl_raw = redis.call('HGET', token_key, 'lease_ttl_ms')
local terminal_ttl_raw = redis.call('HGET', token_key, 'terminal_ttl_ms')
if type(lease_raw) ~= 'string' or string.match(lease_raw, '^%d+$') == nil or
    type(ttl_raw) ~= 'string' or string.match(ttl_raw, '^%d+$') == nil or
    type(terminal_ttl_raw) ~= 'string' or string.match(terminal_ttl_raw, '^%d+$') == nil then
  return {'runtime_sync_required'}
end
local lease_until = tonumber(lease_raw)
local ttl = tonumber(ttl_raw)
local terminal_ttl = tonumber(terminal_ttl_raw)
if lease_until == nil or ttl == nil or terminal_ttl == nil or ttl <= 0 or terminal_ttl < ttl then
  return {'runtime_sync_required'}
end
if now >= lease_until then return {'expired'} end
if key_type(conc_key) ~= 'zset' then return {'runtime_sync_required'} end
if redis.call('ZSCORE', conc_key, rid) == false then return {'expired'} end

local new_lease = now + ttl
redis.call('HSET', token_key, 'lease_until_ms', new_lease)
redis.call('PEXPIRE', token_key, new_lease - now + terminal_ttl)
redis.call('ZADD', conc_key, new_lease, rid)
redis.call('PEXPIRE', conc_key, new_lease - now + terminal_ttl)
return {'renewed', new_lease}
`

// luaFinishRequestAdmission 唯一终态：释放 route-user concurrency，保留已接收请求的 RPM/RPD，
// 按可空权威 usage 对账/释放 TPM（§2.14.12）。first-terminal-wins。预占桶从 token 记录读取。
// KEYS[1]=marker KEYS[2]=request_token KEYS[3]=conc_zset
// ARGV: request_admission_id, route_id, user_id, authoritative_tpm(空表示无权威 usage → 释放预占),
// expected_epoch, expected_epoch_revision
// 返回 {'finished'} | {'unknown_request_admission'} | {'terminal'}（重复终态返回首次）。
const luaFinishRequestAdmission = `
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
local authoritative = ARGV[4]
local expected_epoch = ARGV[5]
local expected_epoch_rev = ARGV[6]
local now = now_ms()

if key_type(marker) ~= 'hash' then return {'runtime_state_lost'} end
if redis.call('HGET', marker, 'state') ~= 'ready' then return {'runtime_state_lost'} end
if redis.call('HGET', marker, 'epoch') ~= expected_epoch or
    redis.call('HGET', marker, 'revision') ~= expected_epoch_rev then
  return {'stale_integrity_epoch'}
end

if key_type(token_key) ~= 'hash' then return {'unknown_request_admission'} end
if redis.call('HGET', token_key, 'runtime_integrity_epoch') ~= expected_epoch or
    redis.call('HGET', token_key, 'runtime_integrity_revision') ~= expected_epoch_rev then
  return {'stale_integrity_epoch'}
end
if redis.call('HGET', token_key, 'route_id') ~= route_id or
    redis.call('HGET', token_key, 'user_id') ~= user_id or
    redis.call('HGET', token_key, 'conc_key') ~= conc_key then
  return {'conflict'}
end
if redis.call('HGET', token_key, 'status') ~= 'active' then return {'terminal'} end

local terminal_ttl_raw = redis.call('HGET', token_key, 'terminal_ttl_ms')
if type(terminal_ttl_raw) ~= 'string' or string.match(terminal_ttl_raw, '^%d+$') == nil then
  return {'runtime_sync_required'}
end
local terminal_ttl = tonumber(terminal_ttl_raw)
if terminal_ttl == nil or terminal_ttl <= 0 then return {'runtime_sync_required'} end

-- 释放 route-user 并发（RPM/RPD 作为已接收请求保留，不回退）。
if key_type(conc_key) == 'zset' then redis.call('ZREM', conc_key, rid) end

-- TPM 对账：有权威 usage 则按 (actual - estimate) 调整仍存在的原始桶；无权威 usage 则释放预占。
local reserve_state = redis.call('HGET', token_key, 'reserve_state')
if reserve_state == 'reserved' then
  local tpm_bucket = redis.call('HGET', token_key, 'reserved_tpm_bucket')
  local reserved = tonumber(redis.call('HGET', token_key, 'reserved_tpm_amount')) or 0
  if tpm_bucket ~= false and tpm_bucket ~= '' then
    if authoritative == '' then
      if redis.call('EXISTS', tpm_bucket) == 1 and reserved > 0 then
        redis.call('DECRBY', tpm_bucket, reserved)
      end
    else
      local actual = tonumber(authoritative) or 0
      local delta = actual - reserved
      if redis.call('EXISTS', tpm_bucket) == 1 and delta ~= 0 then
        redis.call('INCRBY', tpm_bucket, delta)
      end
    end
  end
end

redis.call('HSET', token_key, 'status', 'finished', 'terminal_at_ms', now, 'terminal_result', 'finished')
redis.call('PEXPIRE', token_key, terminal_ttl)
return {'finished'}
`
