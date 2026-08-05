-- record_sample.lua 幂等写入评分样本分钟桶 + RPD 日桶（§12 观测，独立于 admission 硬门槛）。
-- 用 attempt 维度 marker 保证「重复 Finish / Worker 重放 / 网络重试」不重复计数（§12.5）。
--
-- KEYS[1] = 幂等 marker key（sample:v1:written:{attempt_id}）
-- KEYS[2] = 分钟桶 hash key（sample:v1:ch:{channel}:min:{minute}）
-- KEYS[3] = UTC 日桶 counter key（sample:v1:ch:{channel}:day:{day}）
-- ARGV[1] = marker_ttl_ms
-- ARGV[2] = minute_ttl_ms（>=35min，覆盖时钟边界与短暂消费延迟）
-- ARGV[3] = day_ttl_ms
-- ARGV[4] = ttft_ms（'' 表示无 TTFT 样本）
-- ARGV[5] = error_eligible（'1' 计入错误率分母）
-- ARGV[6] = is_error（'1' 计入错误率分子；要求 error_eligible=1）
-- ARGV[7] = observed_request（'1' 真实发起上游调用，计入 RPM/RPD）
-- 返回 1=已写入；0=幂等跳过。
-- token 观测不在这里：TPM 由独立的 obs:tpm 分钟桶按真实 chunk 时间记录（§8）。
if redis.call('SET', KEYS[1], '1', 'NX', 'PX', tonumber(ARGV[1])) == false then
  return 0
end

local mkey = KEYS[2]
local touched = false

if ARGV[4] ~= '' then
  local ttft = tonumber(ARGV[4])
  if ttft ~= nil and ttft >= 0 then
    redis.call('HINCRBY', mkey, 'ttft_sum_ms', ttft)
    redis.call('HINCRBY', mkey, 'ttft_count', 1)
    touched = true
  end
end

if ARGV[5] == '1' then
  redis.call('HINCRBY', mkey, 'error_attempt_count', 1)
  if ARGV[6] == '1' then
    redis.call('HINCRBY', mkey, 'error_count', 1)
  end
  touched = true
end

if ARGV[7] == '1' then
  redis.call('HINCRBY', mkey, 'observed_request_count', 1)
  touched = true
  redis.call('INCR', KEYS[3])
  redis.call('PEXPIRE', KEYS[3], tonumber(ARGV[3]))
end

if touched then
  redis.call('PEXPIRE', mkey, tonumber(ARGV[2]))
end
return 1
