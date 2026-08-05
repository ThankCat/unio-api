-- correct_tpm_observation.lua 用可靠 usage 修正已写入的 TPM 观测（§8.4）。
--
-- 与 record 的三个关键区别：
--   1. 增量是有符号的（实际值减去先前的估算分配），可以为负；
--   2. 只修正**已经存在**的分钟桶——桶过期即放弃，绝不用一条负增量把过期分钟复活；
--   3. 写后把每个字段夹到 >= 0，保证 Admin 永远读不到负的观测值。
-- 幂等 marker 跨进程有效：recovery worker 重放同一 request/attempt 不得二次修正。
--
-- KEYS[1]      = 修正幂等 marker（obs:tpm:v1:corrected:{scope}）
-- KEYS[2..N+1] = N 个分钟桶 hash key（调用方已过滤掉超出回溯窗口的分钟）
-- ARGV[1]      = marker_ttl_ms
-- ARGV[1+5*(i-1)+1 .. 1+5*i] = 第 i 个桶的 5 个有符号增量，顺序固定为
--                input_tokens / output_tokens / provisional_tokens
--                / observed_attempts / missing_usage_count
--
-- 返回 { 'applied', applied_bucket_count, expired_bucket_count } 或 { 'duplicate' }。
local MAX_EXACT_INTEGER = 9007199254740991
local FIELD_COUNT = 5
local FIELDS = {
  'input_tokens',
  'output_tokens',
  'provisional_tokens',
  'observed_attempts',
  'missing_usage_count',
}

local bucket_count = #KEYS - 1
if bucket_count < 0 then return redis.error_reply('tpm correction requires a marker key') end
if #ARGV ~= 1 + bucket_count * FIELD_COUNT then
  return redis.error_reply('tpm correction argument count mismatch')
end

local marker_ttl = tonumber(ARGV[1])
if marker_ttl == nil or marker_ttl <= 0 then
  return redis.error_reply('tpm correction marker ttl must be positive')
end

-- 校验全部前置于第一次写入：脚本中途报错不会回滚已执行的命令。
for index = 1, bucket_count do
  local base = 1 + (index - 1) * FIELD_COUNT
  for offset = 1, FIELD_COUNT do
    local delta = tonumber(ARGV[base + offset])
    if
      delta == nil
      or delta > MAX_EXACT_INTEGER
      or delta < -MAX_EXACT_INTEGER
      or delta ~= math.floor(delta)
    then
      return redis.error_reply('tpm correction delta must be an exact integer')
    end
  end
end

if redis.call('SET', KEYS[1], '1', 'NX', 'PX', marker_ttl) == false then
  return { 'duplicate' }
end

local applied = 0
local expired = 0
for index = 1, bucket_count do
  local key = KEYS[index + 1]
  -- 桶不存在 = 已过保留期。放弃这一分钟的修正，绝不重建。
  if redis.call('EXISTS', key) == 0 then
    expired = expired + 1
  else
    local base = 1 + (index - 1) * FIELD_COUNT
    local touched = false
    for offset = 1, FIELD_COUNT do
      local delta = tonumber(ARGV[base + offset])
      if delta ~= 0 then
        local raw = redis.call('HGET', key, FIELDS[offset])
        local current = 0
        if raw ~= false then current = tonumber(raw) or 0 end
        local next_value = current + delta
        if next_value < 0 then next_value = 0 end
        redis.call('HSET', key, FIELDS[offset], next_value)
        touched = true
      end
    end
    if touched then applied = applied + 1 end
  end
end

return { 'applied', applied, expired }
