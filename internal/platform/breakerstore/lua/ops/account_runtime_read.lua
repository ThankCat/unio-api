-- 批量读取账号运行态与在途并发：路由为池型渠道产出候选快照时，一次拿全池所有账号的事实。
-- 只读，不做任何自愈写入；过期状态在这里表现为剩余 0，由下一次 mark 或键 TTL 真正清除。
local count = tonumber(ARGV[1])
if count == nil or count < 1 or count ~= math.floor(count) or #KEYS ~= count * 2 then
  return redis.error_reply('invalid account runtime batch shape')
end

local t = redis.call('TIME')
local now = tonumber(t[1]) * 1000 + math.floor(tonumber(t[2]) / 1000)

local function key_type(key)
  local reply = redis.call('TYPE', key)
  if type(reply) == 'table' then return reply['ok'] end
  return reply
end

local rows = {}
for index = 1, count do
  local account_key = KEYS[(index - 1) * 2 + 1]
  local conc_key = KEYS[(index - 1) * 2 + 2]

  local account_type = key_type(account_key)
  if account_type ~= 'none' and account_type ~= 'hash' then
    return redis.error_reply('WRONGTYPE account runtime key must be a hash')
  end
  local conc_type = key_type(conc_key)
  if conc_type ~= 'none' and conc_type ~= 'zset' then
    return redis.error_reply('WRONGTYPE account concurrency key must be a zset')
  end

  local function remaining(until_field, reason_field)
    if account_type ~= 'hash' then return 0, '' end
    local until_ms = tonumber(redis.call('HGET', account_key, until_field)) or 0
    if until_ms <= now then return 0, '' end
    return until_ms - now, redis.call('HGET', account_key, reason_field) or ''
  end

  -- 只统计租约未到期的成员：崩溃残留的 permit 靠 score 过期自然退出在途计数（与渠道并发同口径）。
  local in_flight = 0
  if conc_type == 'zset' then in_flight = tonumber(redis.call('ZCOUNT', conc_key, '(' .. now, '+inf')) end

  local cooldown_remaining, cooldown_reason = remaining('cooldown_until_ms', 'cooldown_reason')
  local unschedulable_remaining, unschedulable_reason = remaining('unschedulable_until_ms', 'unschedulable_reason')
  local usage_pause_remaining, usage_pause_reason = remaining('usage_pause_until_ms', 'usage_pause_reason')

  rows[#rows + 1] = {
    cooldown_remaining,
    cooldown_reason,
    unschedulable_remaining,
    unschedulable_reason,
    usage_pause_remaining,
    usage_pause_reason,
    in_flight,
  }
end
return { now, rows }
