---@diagnostic disable: unused-function, unused-local

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
  if marker_state ~= 'ready' or marker_epoch == false or marker_revision == false then return 'runtime_state_lost' end
  if marker_epoch ~= ARGV[2] or marker_revision ~= ARGV[3] then return 'stale_integrity_epoch' end

  local permit_type = attempt_key_type(permit_key)
  if permit_type == 'none' then return 'unknown_permit' end
  if permit_type ~= 'hash' then return 'runtime_sync_required' end

  local permit_epoch = redis.call('HGET', permit_key, 'runtime_integrity_epoch')
  local permit_revision = redis.call('HGET', permit_key, 'runtime_integrity_revision')
  if permit_epoch == false or permit_revision == false then return 'runtime_sync_required' end
  if permit_epoch ~= ARGV[2] or permit_revision ~= ARGV[3] then return 'stale_integrity_epoch' end

  local identities = {
    { 'permit_id', 1 },
    { 'request_admission_id', 4 },
    { 'provider_id', 5 },
    { 'channel_id', 6 },
    { 'origin_revision', 7 },
    { 'status_revision', 8 },
    { 'channel_config_revision', 9 },
    { 'model_id', 10 },
    { 'upstream_endpoint', 11 },
    { 'request_mode', 12 },
    { 'provider_state_generation', 13 },
    { 'channel_state_generation', 14 },
    { 'provider_half_open_probe', 15 },
    { 'channel_half_open_probe', 16 },
    { 'route_id', 17 },
    { 'concurrency_channel_id', 6 },
  }
  for _, identity in ipairs(identities) do
    local stored = redis.call('HGET', permit_key, identity[1])
    if stored == false then return 'runtime_sync_required' end
    if stored ~= ARGV[identity[2]] then return 'conflict' end
  end

  local status = redis.call('HGET', permit_key, 'status')
  if status ~= 'active' and status ~= 'finished' and status ~= 'aborted' then return 'runtime_sync_required' end
  -- Terminal permits are tombstones: frozen identity is still checked above, but expired resource
  -- buckets must not turn an idempotent retry into a runtime fault.
  if status == 'finished' or status == 'aborted' then return nil end

  local origin_control_enforced = redis.call('HGET', permit_key, 'origin_control_enforced')
  local origin_base_fence = redis.call('HGET', permit_key, 'origin_fence_generation')
  local origin_status_fence = redis.call('HGET', permit_key, 'status_fence_generation')
  if
    (origin_control_enforced ~= '0' and origin_control_enforced ~= '1')
    or type(origin_base_fence) ~= 'string'
    or string.match(origin_base_fence, '^%d+$') == nil
    or type(origin_status_fence) ~= 'string'
    or string.match(origin_status_fence, '^%d+$') == nil
  then
    return 'runtime_sync_required'
  end

  local origin_type = attempt_key_type(KEYS[3])
  local channel_type = attempt_key_type(KEYS[4])
  local concurrency_type = attempt_key_type(KEYS[5])
  if
    (origin_type ~= 'none' and origin_type ~= 'hash')
    or (channel_type ~= 'none' and channel_type ~= 'hash')
    or (concurrency_type ~= 'none' and concurrency_type ~= 'zset')
  then
    return 'runtime_sync_required'
  end

  if redis.call('HGET', permit_key, 'admission_enforced') ~= '1' then return 'runtime_sync_required' end
  local bucket_fields = { 'ch_rpm_bucket', 'ch_rpd_bucket', 'ch_tpm_bucket' }
  for _, field in ipairs(bucket_fields) do
    local bucket_key = redis.call('HGET', permit_key, field)
    if type(bucket_key) ~= 'string' or bucket_key == '' then return 'runtime_sync_required' end
    local bucket_type = attempt_key_type(bucket_key)
    if bucket_type ~= 'none' and bucket_type ~= 'string' then return 'runtime_sync_required' end
    -- Channel RPD 是 permit 冻结的当日容量事实。它在 active permit 生命周期中丢失时，
    -- 继续 Finish/Abort/Renew 会静默重置日限额，因此必须 fail closed。
    if field == 'ch_rpd_bucket' and bucket_type == 'none' then return 'runtime_sync_required' end
    if bucket_type == 'string' then
      local raw = redis.call('GET', bucket_key)
      local value = tonumber(raw)
      if
        raw == false
        or string.match(raw, '^%d+$') == nil
        or value == nil
        or value < 0
        or value > 9007199254740991
        or value ~= math.floor(value)
      then
        return 'runtime_sync_required'
      end
    end
  end
  local input_estimate = redis.call('HGET', permit_key, 'tpm_input_estimate')
  local tpm_state = redis.call('HGET', permit_key, 'tpm_state')
  if type(input_estimate) ~= 'string' or string.match(input_estimate, '^%d+$') == nil or tpm_state ~= 'held' then
    return 'runtime_sync_required'
  end
  return nil
end
