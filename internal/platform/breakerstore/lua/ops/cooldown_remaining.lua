local function now_ms()
  local t = redis.call('TIME')
  return tonumber(t[1]) * 1000 + math.floor(tonumber(t[2]) / 1000)
end
local cooldown_key = KEYS[1]
if redis.call('EXISTS', cooldown_key) == 0 then return { 0 } end
local until_ms = tonumber(redis.call('HGET', cooldown_key, 'until_ms')) or 0
local now = now_ms()
if until_ms <= now then return { 0 } end
return { until_ms - now }
