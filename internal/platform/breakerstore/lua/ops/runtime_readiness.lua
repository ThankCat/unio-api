if redis.call('EXISTS', KEYS[6]) == 1 then return { 'breaker_store_unavailable' } end
local instance_matches = redis_instance_proof_matches(KEYS[7])
if instance_matches == nil then return redis.error_reply('invalid Redis instance reconciliation proof') end
if not instance_matches then return { 'redis_instance_changed' } end
local marker = KEYS[1]
if redis.call('EXISTS', marker) == 0 then return { 'marker_absent' } end
if redis.call('HGET', marker, 'state') ~= 'ready' then return { 'marker_not_ready' } end
if
  redis.call('HGET', marker, 'epoch') ~= ARGV[1]
  or redis.call('HGET', marker, 'revision') ~= ARGV[2]
  or redis.call('HGET', marker, 'marker_hash') ~= ARGV[7]
then
  return { 'marker_mismatch' }
end

local payloads = {}
for index = 2, 5 do
  local control = KEYS[index]
  local expected_revision = ARGV[index + 1]
  if redis.call('EXISTS', control) == 0 then return { 'control_absent', index - 1 } end
  local pending = tonumber(redis.call('HGET', control, 'pending_revision')) or 0
  if pending ~= 0 then return { 'control_pending', index - 1 } end
  if redis.call('HGET', control, 'active_revision') ~= expected_revision then
    return { 'control_revision_mismatch', index - 1 }
  end
  local payload = redis.call('HGET', control, 'active_payload') or ''
  local payload_hash = redis.call('HGET', control, 'active_payload_hash') or ''
  if payload == '' or payload_hash == '' then return { 'control_invalid', index - 1 } end
  table.insert(payloads, payload)
  table.insert(payloads, payload_hash)
end
return { 'ready', unpack(payloads) }
