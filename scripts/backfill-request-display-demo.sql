-- 请求记录展示效果回填（仅测试库）：给最近的成功请求补齐
-- 缓存写/推理输出 tokens、成本/售价快照分项单价（并按 CHECK 重算金额）、
-- reasoning_effort/budget、client_ip、route 快照、多渠道尝试链。
-- 幂等性：可重复执行（更新为确定值）；请勿在生产执行。
\set ON_ERROR_STOP on
BEGIN;

-- 目标行：user 1 最近 12 条 succeeded（rn=1 最新）。
CREATE TEMP TABLE tgt ON COMMIT DROP AS
SELECT id, row_number() OVER (ORDER BY id DESC) AS rn
FROM request_records
WHERE user_id = 1 AND status = 'succeeded'
ORDER BY id DESC LIMIT 12;

-- 1) request_records：线路快照 / 客户端 IP 多样化 / 推理强度档位（含 2 行 Anthropic 式预算）。
UPDATE request_records r SET
    route_id = COALESCE(r.route_id, 75),
    client_ip = (ARRAY['203.0.113.7','198.51.100.23','192.0.2.45','203.0.113.99','172.104.66.10'])[(t.rn % 5) + 1],
    reasoning_effort = (ARRAY[NULL,'xhigh','high','medium','low','minimal'])[(t.rn % 6) + 1],
    reasoning_budget_tokens = CASE (t.rn % 6) + 1 WHEN 3 THEN 16000 WHEN 4 THEN 8000 ELSE NULL END
FROM tgt t WHERE r.id = t.id;

-- 2) usage_records：部分行补缓存写（5m/1h）与推理输出 tokens（非 0 值必须同时置 state=known）。
UPDATE usage_records ur SET
    cache_write_5m_input_tokens = CASE WHEN t.rn % 3 = 0 THEN 2048 ELSE ur.cache_write_5m_input_tokens END,
    cache_write_5m_input_tokens_state = CASE WHEN t.rn % 3 = 0 THEN 'known' ELSE ur.cache_write_5m_input_tokens_state END,
    cache_write_1h_input_tokens = CASE WHEN t.rn % 4 = 0 THEN 1024 ELSE ur.cache_write_1h_input_tokens END,
    cache_write_1h_input_tokens_state = CASE WHEN t.rn % 4 = 0 THEN 'known' ELSE ur.cache_write_1h_input_tokens_state END,
    reasoning_output_tokens = CASE WHEN t.rn % 2 = 0 AND ur.output_tokens_total > 0 THEN GREATEST(ur.output_tokens_total / 3, 1) ELSE ur.reasoning_output_tokens END,
    reasoning_output_tokens_state = CASE WHEN t.rn % 2 = 0 AND ur.output_tokens_total > 0 THEN 'known' ELSE ur.reasoning_output_tokens_state END
FROM tgt t WHERE ur.request_record_id = t.id;

-- 3) cost_snapshots：补缓存写/推理单价，并按「单价 × tokens ÷ 1e6」重算全部金额 + 总额（满足 total CHECK）。
WITH calc AS (
    SELECT cs.id AS cs_id,
           round(cs.uncached_input_cost * ur.uncached_input_tokens / 1000000::numeric, 10) AS a_in,
           round(COALESCE(cs.cache_read_input_cost, 0.07) * ur.cache_read_input_tokens / 1000000::numeric, 10) AS a_cr,
           round(0.875 * ur.cache_write_5m_input_tokens / 1000000::numeric, 10) AS a_w5,
           round(1.4 * ur.cache_write_1h_input_tokens / 1000000::numeric, 10) AS a_w1,
           round(cs.output_cost * (ur.output_tokens_total - ur.reasoning_output_tokens) / 1000000::numeric, 10) AS a_out,
           round(cs.output_cost * ur.reasoning_output_tokens / 1000000::numeric, 10) AS a_re
    FROM cost_snapshots cs
    JOIN usage_records ur ON ur.request_record_id = cs.request_record_id
    JOIN tgt t ON t.id = cs.request_record_id
)
UPDATE cost_snapshots cs SET
    cache_read_input_cost = COALESCE(cs.cache_read_input_cost, 0.07),
    cache_write_5m_input_cost = 0.875,
    cache_write_1h_input_cost = 1.4,
    reasoning_output_cost = cs.output_cost,
    uncached_input_cost_amount = c.a_in,
    cache_read_input_cost_amount = c.a_cr,
    cache_write_5m_input_cost_amount = c.a_w5,
    cache_write_1h_input_cost_amount = c.a_w1,
    output_cost_amount = c.a_out,
    reasoning_output_cost_amount = c.a_re,
    total_cost_amount = c.a_in + c.a_cr + c.a_w5 + c.a_w1 + c.a_out + c.a_re
FROM calc c WHERE cs.id = c.cs_id;

-- 4) price_snapshots：补缓存读/写与推理售价单价（无金额列，前端按单价×tokens 计算）。
UPDATE price_snapshots ps SET
    cache_read_input_price = COALESCE(ps.cache_read_input_price, round(ps.uncached_input_price * 0.1, 10)),
    cache_write_5m_input_price = COALESCE(ps.cache_write_5m_input_price, round(ps.uncached_input_price * 1.25, 10)),
    cache_write_1h_input_price = COALESCE(ps.cache_write_1h_input_price, round(ps.uncached_input_price * 2, 10)),
    reasoning_output_price = COALESCE(ps.reasoning_output_price, ps.output_price)
FROM tgt t WHERE ps.request_record_id = t.id;

-- 5) 多渠道尝试链（rn 1/2）：原成功尝试后移到 index 1，插入 index 0 的失败尝试（渠道 1，429 限流）。
--    效果：渠道链显示「渠道1名 → 原渠道名」，fault_party 由生成列自动派生。
UPDATE request_attempts a SET attempt_index = a.attempt_index + 1
FROM tgt t
WHERE a.request_record_id = t.id AND t.rn IN (1, 2)
  AND NOT EXISTS (
      SELECT 1 FROM request_attempts x
      WHERE x.request_record_id = t.id AND x.status = 'failed'
  );

INSERT INTO request_attempts (
    request_record_id, attempt_index, provider_id, channel_id, adapter_key,
    upstream_model, upstream_protocol, status, upstream_status_code,
    error_code, error_message, final_usage_received, started_at, completed_at, created_at
)
SELECT t.id, 0, c.provider_id, c.id, 'openai',
       r.requested_model_id, 'openai', 'failed', 429,
       'upstream_rate_limited', 'upstream returned 429 Too Many Requests', false,
       r.started_at, r.started_at + interval '350 milliseconds', r.started_at
FROM tgt t
JOIN request_records r ON r.id = t.id
JOIN channels c ON c.id = 1
WHERE t.rn IN (1, 2)
  AND NOT EXISTS (
      SELECT 1 FROM request_attempts x
      WHERE x.request_record_id = t.id AND x.attempt_index = 0 AND x.status = 'failed'
  );

COMMIT;

-- 校验输出
SELECT r.id, r.reasoning_effort, r.reasoning_budget_tokens, r.client_ip,
       ur.cache_write_5m_input_tokens AS w5, ur.reasoning_output_tokens AS re_tok,
       cs.total_cost_amount,
       (SELECT count(*) FROM request_attempts a WHERE a.request_record_id = r.id) AS attempts
FROM request_records r
JOIN usage_records ur ON ur.request_record_id = r.id
JOIN cost_snapshots cs ON cs.request_record_id = r.id
WHERE r.user_id = 1 AND r.status = 'succeeded'
ORDER BY r.id DESC LIMIT 12;
