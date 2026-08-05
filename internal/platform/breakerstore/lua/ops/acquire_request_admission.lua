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
local concurrency_override = ARGV[11]

if redis.call('EXISTS', fault_latch) == 1 then return { 'store_unavailable' } end
local instance_matches = redis_instance_proof_matches(instance_proof)
if instance_matches == nil then return redis.error_reply('invalid Redis instance reconciliation proof') end
if not instance_matches then return { 'redis_instance_changed' } end
local now = now_ms()

-- 完整性 epoch 硬门禁。
if redis_key_type(marker) ~= 'hash' then return { 'runtime_state_lost' } end
if redis.call('HGET', marker, 'state') ~= 'ready' then return { 'runtime_state_lost' } end
if redis.call('HGET', marker, 'epoch') ~= expected_epoch then return { 'stale_integrity_epoch' } end
if redis.call('HGET', marker, 'revision') ~= expected_epoch_rev then return { 'stale_integrity_epoch' } end

-- 幂等恢复不重新取得资源；control 在响应丢失后进入 pending 也可取回原 token。
local token_type = redis_key_type(token_key)
if token_type == 'hash' then
  if redis.call('HGET', token_key, 'admission_fingerprint') ~= fingerprint then return { 'conflict' } end
  if redis.call('HGET', token_key, 'status') ~= 'active' then return { 'conflict' } end
  if
    redis.call('HGET', token_key, 'route_id') ~= route_id
    or redis.call('HGET', token_key, 'user_id') ~= user_id
    or redis.call('HGET', token_key, 'runtime_integrity_epoch') ~= expected_epoch
    or redis.call('HGET', token_key, 'runtime_integrity_revision') ~= expected_epoch_rev
    or redis.call('HGET', token_key, 'route_rate_limits_revision') ~= ARGV[7]
    or redis.call('HGET', token_key, 'global_concurrency_revision') ~= ARGV[8]
    or redis.call('HGET', token_key, 'rpm_override') ~= rpm_override
    or redis.call('HGET', token_key, 'rpd_override') ~= rpd_override
    or redis.call('HGET', token_key, 'concurrency_override') ~= concurrency_override
  then
    return { 'conflict' }
  end
  local lease_until = tonumber(redis.call('HGET', token_key, 'lease_until_ms'))
  local renew_ms = tonumber(redis.call('HGET', token_key, 'renew_ms'))
  if lease_until == nil or renew_ms == nil or renew_ms <= 0 then return { 'runtime_sync_required', 'request_token' } end
  return { 'idempotent', lease_until, renew_ms }
end
if token_type ~= 'none' then return { 'runtime_sync_required', 'request_token' } end

-- 新 token 的所有执行值都来自 committed active controls；调用方 effective/TTL 不参与。
local route_rate, route_rate_state =
  read_new_admission_control(route_rate_ctl, expected_route_rate_rev, parse_rate_limit_defaults_payload)
if route_rate == nil then return { route_rate_state, 'route_rate' } end
local concurrency, concurrency_state =
  read_new_admission_control(conc_ctl, expected_conc_rev, parse_global_concurrency_payload)
if concurrency == nil then return { concurrency_state, 'global_concurrency' } end
local breaker = read_committed_control(breaker_ctl, parse_circuit_breaker_payload)
if breaker == nil then return { 'runtime_sync_required', 'circuit_breaker' } end

local eff_rpm = resolve_request_limit_override(rpm_override, route_rate.rpm)
local eff_rpd = resolve_request_limit_override(rpd_override, route_rate.rpd)
local eff_conc = resolve_request_limit_override(concurrency_override, concurrency.key_limit)
if eff_rpm == nil or eff_rpd == nil or eff_conc == nil then
  return { 'runtime_sync_required', 'request_overrides' }
end
local lease_ttl_ms = breaker.attempt_permit_ttl_ms
local renew_ms = breaker.attempt_permit_renew_interval_ms
local terminal_ttl_ms = breaker.attempt_permit_terminal_ttl_ms
-- 分钟窗口桶（RPM，按分钟号分桶）：TTL 只需覆盖分钟窗口 + permit 生命周期余量即可安全计数。
local bucket_ttl_ms = lease_ttl_ms + terminal_ttl_ms + 120000
-- 日窗口桶（RPD，按 UTC 日号分桶）：TTL 必须覆盖整个日窗口，否则静默过期会把当日计数清零、限额失效。
-- 与 Go 侧 dayBucket = now.Unix()/86400（86400s=一日）一致，额外叠加分钟桶余量兜底时钟偏移。
local rpd_bucket_ttl_ms = 86400000 + bucket_ttl_ms

-- Validate every resource key/value before the unified write stage. Redis Lua errors do not roll back.
local rpm_used = read_nonnegative_counter(rpm_key)
local rpd_used = read_nonnegative_counter(rpd_key)
local conc_used = active_zset_count(conc_key, now)
if rpm_used == nil or rpd_used == nil or conc_used == nil then
  return { 'runtime_sync_required', 'request_resources' }
end
if rpm_used >= MAX_EXACT_INTEGER or rpd_used >= MAX_EXACT_INTEGER then
  return { 'runtime_sync_required', 'request_resources' }
end
if eff_rpm > 0 and rpm_used + 1 > eff_rpm then return { 'limited', 'rpm' } end
if eff_rpd > 0 and rpd_used + 1 > eff_rpd then return { 'limited', 'rpd' } end
if eff_conc > 0 and conc_used >= eff_conc then return { 'limited', 'concurrency' } end

-- Unified write stage: limits are already satisfied and all key types are known.
redis.call('INCR', rpm_key)
redis.call('PEXPIRE', rpm_key, bucket_ttl_ms)
redis.call('INCR', rpd_key)
redis.call('PEXPIRE', rpd_key, rpd_bucket_ttl_ms)
local lease_until = now + lease_ttl_ms
redis.call('ZREMRANGEBYSCORE', conc_key, '-inf', now)
redis.call('ZADD', conc_key, lease_until, rid)
redis.call('PEXPIRE', conc_key, lease_until - now + terminal_ttl_ms)

redis.call(
  'HSET',
  token_key,
  'status',
  'active',
  'route_id',
  route_id,
  'user_id',
  user_id,
  'admission_fingerprint',
  fingerprint,
  'runtime_integrity_epoch',
  expected_epoch,
  'runtime_integrity_revision',
  expected_epoch_rev,
  'route_rate_limits_revision',
  expected_route_rate_rev,
  'global_concurrency_revision',
  expected_conc_rev,
  'rpm_override',
  rpm_override,
  'rpd_override',
  rpd_override,
  'concurrency_override',
  concurrency_override,
  'eff_rpm',
  eff_rpm,
  'eff_rpd',
  eff_rpd,
  'eff_concurrency',
  eff_conc,
  'rpm_bucket',
  rpm_key,
  'rpd_bucket',
  rpd_key,
  'conc_key',
  conc_key,
  'lease_ttl_ms',
  lease_ttl_ms,
  'renew_ms',
  renew_ms,
  'terminal_ttl_ms',
  terminal_ttl_ms,
  'bucket_ttl_ms',
  bucket_ttl_ms,
  'acquired_at_ms',
  now,
  'lease_until_ms',
  lease_until
)
redis.call('PEXPIRE', token_key, lease_until - now + terminal_ttl_ms)
return { 'allowed', lease_until, renew_ms }
