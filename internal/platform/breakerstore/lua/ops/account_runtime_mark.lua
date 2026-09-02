-- 账号运行态：冷却、临时不可调度、用量暂停都表达为「到期毫秒」，到点自愈，不需要后台清理任务。
-- 三种状态共用一个 hash 且只由本脚本维护，是为了让键 TTL 只有一处计算：TTL 必须覆盖最晚的那个
-- 到期时刻，任一状态各自 PEXPIRE 都会把另一个状态提前抹掉。
local account_key = KEYS[1]
local state = ARGV[1]
local duration_ms = tonumber(ARGV[2]) or 0
local reason = ARGV[3]
local grace_ms = tonumber(ARGV[4]) or 0

-- extend=true 表示多个故障源叠加时取最晚到期：一次 401 刷新窗口不该把更长的代理故障隔离提前解除。
-- extend=false 表示以最近一次上游观测为准：冷却与用量暂停的到期都由上游 reset_at 推出，而官方支持
-- 付费即时重置，reset_at 会变小；只增不减会让账号在配额已经恢复之后继续被停用数小时。
local specs = {
  cooldown = { until_field = 'cooldown_until_ms', reason_field = 'cooldown_reason', extend = false },
  unschedulable = { until_field = 'unschedulable_until_ms', reason_field = 'unschedulable_reason', extend = true },
  usage_pause = { until_field = 'usage_pause_until_ms', reason_field = 'usage_pause_reason', extend = false },
}
local target = specs[state]
if target == nil then return redis.error_reply('unknown account runtime state') end

local key_type = redis.call('TYPE', account_key)
if type(key_type) == 'table' then key_type = key_type['ok'] end
if key_type ~= 'none' and key_type ~= 'hash' then
  return redis.error_reply('WRONGTYPE account runtime key must be a hash')
end

local t = redis.call('TIME')
local now = tonumber(t[1]) * 1000 + math.floor(tonumber(t[2]) / 1000)

local effective_until = 0
if duration_ms > 0 then
  effective_until = now + duration_ms
  local existing = tonumber(redis.call('HGET', account_key, target.until_field)) or 0
  if target.extend and existing > effective_until then
    -- 保留更晚的到期连同它的原因：展示出来的原因必须对应实际生效的那个到期时刻。
    effective_until = existing
  else
    redis.call('HSET', account_key, target.until_field, effective_until, target.reason_field, reason)
  end
else
  redis.call('HDEL', account_key, target.until_field, target.reason_field)
end

local latest = 0
for _, spec in pairs(specs) do
  local value = tonumber(redis.call('HGET', account_key, spec.until_field)) or 0
  if value > latest then latest = value end
end
if latest <= now then
  -- 三个状态都已清除或过期：直接删键，不留空 hash 占位。
  redis.call('DEL', account_key)
  return { 0 }
end
redis.call('PEXPIRE', account_key, latest - now + grace_ms)
return { effective_until }
