-- record_tpm_observation.lua 原子写入一个 flush 批次的 TPM 观测（§8.2/§8.3）。
--
-- 批次 dedup marker 与全部 HINCRBY 必须在同一次执行内完成：Redis Pipeline 不是原子的，
-- 部分成功后按同一 operation id 重试会重复累加，因此观测批次整体幂等只能靠脚本保证。
--
-- KEYS[1]      = flush 幂等 marker（obs:tpm:v1:flush:{operation_id}）
-- KEYS[2..N+1] = N 个分钟桶 hash key
-- ARGV[1]      = marker_ttl_ms
-- ARGV[2]      = bucket_ttl_ms
-- ARGV[2+5*(i-1)+1 .. 2+5*i] = 第 i 个桶的 5 个非负增量，顺序固定为
--                input_tokens / output_tokens / provisional_tokens
--                / observed_attempts / missing_usage_count
--
-- 返回 1=已写入；0=幂等跳过。
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
if bucket_count < 0 then return redis.error_reply('tpm observation batch requires a marker key') end
if #ARGV ~= 2 + bucket_count * FIELD_COUNT then
  return redis.error_reply('tpm observation batch argument count mismatch')
end

local marker_ttl = tonumber(ARGV[1])
local bucket_ttl = tonumber(ARGV[2])
if
  marker_ttl == nil
  or marker_ttl <= 0
  or bucket_ttl == nil
  or bucket_ttl <= 0
then
  return redis.error_reply('tpm observation ttl must be positive')
end

-- Redis 不会回滚脚本中已执行的写入，因此所有校验必须在第一次写之前全部完成。
for index = 1, bucket_count do
  local base = 2 + (index - 1) * FIELD_COUNT
  for offset = 1, FIELD_COUNT do
    local delta = tonumber(ARGV[base + offset])
    if
      delta == nil
      or delta < 0
      or delta > MAX_EXACT_INTEGER
      or delta ~= math.floor(delta)
    then
      return redis.error_reply('tpm observation delta must be a non-negative exact integer')
    end
  end
end

if redis.call('SET', KEYS[1], '1', 'NX', 'PX', marker_ttl) == false then
  return 0
end

for index = 1, bucket_count do
  local key = KEYS[index + 1]
  local base = 2 + (index - 1) * FIELD_COUNT
  local touched = false
  for offset = 1, FIELD_COUNT do
    local delta = tonumber(ARGV[base + offset])
    if delta > 0 then
      redis.call('HINCRBY', key, FIELDS[offset], delta)
      touched = true
    end
  end
  if touched then
    redis.call('PEXPIRE', key, bucket_ttl)
  end
end

return 1
