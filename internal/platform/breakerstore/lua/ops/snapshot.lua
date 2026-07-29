local function now_ms()
  local t = redis.call('TIME')
  return tonumber(t[1]) * 1000 + math.floor(tonumber(t[2]) / 1000)
end
local state_key = KEYS[1]
local now = now_ms()
if redis.call('EXISTS', state_key) == 0 then return { 'absent', now } end
local h = redis.call('HGETALL', state_key)
local open_until = tonumber(redis.call('HGET', state_key, 'open_until_ms')) or 0
local remaining = 0
if open_until > now then remaining = open_until - now end
return { 'present', now, remaining, h }
