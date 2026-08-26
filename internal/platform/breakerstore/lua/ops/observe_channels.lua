-- 观测专用的批量只读快照：给管理台看「此刻各渠道的运行态」。
--
-- 与 snapshot_many 的区别是刻意的，不要向它对齐：
--   * snapshot_many 服务准入，任何 revision 漂移、control 缺失都必须整批失败，
--     因为按过期事实放行请求是错的；
--   * 这里只是展示，读到什么算什么。control 缺失或解析失败只把该行标成
--     capacity_unknown，不影响其他渠道，也不影响整批。
-- 因此本脚本不校验完整性 epoch、不校验任何 revision、不读 model permission
-- （permission 是 (channel, model) 维度，全局流量视图没有单一 model）。

local count = tonumber(ARGV[1])
if count == nil or count < 0 or count ~= math.floor(count) or #KEYS ~= 1 + count * 4 then
  return redis.error_reply('invalid observe batch shape')
end

local t = redis.call('TIME')
local now = tonumber(t[1]) * 1000 + math.floor(tonumber(t[2]) / 1000)

-- 全局并发上限用作渠道未单独设限时的继承值；读不到就当没有继承值。
local inherited_limit = nil
local global_ctl = KEYS[1]
if redis_key_type(global_ctl) == 'hash' then
  local payload = redis.call('HGET', global_ctl, 'active_payload')
  if payload then
    local parsed = parse_global_concurrency_payload(payload)
    if parsed ~= nil then inherited_limit = parsed.channel_limit end
  end
end

local function read_breaker(state_key)
  if redis_key_type(state_key) ~= 'hash' then return { 'absent', now } end
  local fields = redis.call('HGETALL', state_key)
  local open_until = tonumber(redis.call('HGET', state_key, 'open_until_ms')) or 0
  local remaining = 0
  if open_until > now then remaining = open_until - now end
  return { 'present', now, remaining, fields }
end

local rows = {}
for index = 1, count do
  local offset = 1 + (index - 1) * 4
  local channel_key = KEYS[offset + 1]
  local concurrency_key = KEYS[offset + 2]
  local cooldown_key = KEYS[offset + 3]
  local capacity_ctl = KEYS[offset + 4]

  -- active_zset_count 在 key 类型异常时返回 nil；观测把它记成 -1（未知）而不是失败。
  local used = active_zset_count(concurrency_key, now)
  if used == nil then used = -1 end

  local capacity_known = 0
  local limit = 0
  if redis_key_type(capacity_ctl) == 'hash' then
    local payload = redis.call('HGET', capacity_ctl, 'active_payload')
    if payload then
      local parsed = parse_channel_capacity_payload(payload)
      if parsed ~= nil then
        local resolved = resolve_channel_limit(parsed.concurrency, inherited_limit)
        if resolved ~= nil then limit = resolved end
        capacity_known = 1
      end
    end
  end

  local cooldown_remaining = 0
  if redis_key_type(cooldown_key) == 'hash' then
    local until_ms = tonumber(redis.call('HGET', cooldown_key, 'until_ms'))
    if until_ms ~= nil and until_ms > now then cooldown_remaining = until_ms - now end
  end

  rows[#rows + 1] = {
    used,
    limit,
    capacity_known,
    cooldown_remaining,
    read_breaker(channel_key),
  }
end

return { 'ok', now, rows }
