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
return { until_ms }
