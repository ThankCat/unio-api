local control = KEYS[1]
if redis.call('EXISTS', control) == 0 then return { 0, 0, '', '', 'absent' } end
local active_rev = tonumber(redis.call('HGET', control, 'active_revision')) or 0
local pending_rev = tonumber(redis.call('HGET', control, 'pending_revision')) or 0
local payload = redis.call('HGET', control, 'active_payload') or ''
local pending_payload = redis.call('HGET', control, 'pending_payload') or ''
local sync = 'active'
if pending_rev ~= 0 then sync = 'pending' end
if ARGV[1] ~= '' then
  local expected = tonumber(ARGV[1])
  if pending_rev ~= 0 then
    sync = 'pending'
  elseif active_rev < expected then
    sync = 'stale'
  elseif active_rev > expected then
    sync = 'ahead'
  end
end
return { active_rev, pending_rev, payload, pending_payload, sync }
