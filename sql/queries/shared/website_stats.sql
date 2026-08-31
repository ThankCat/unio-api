-- name: MinWebsiteFirstTokenMs :one
-- 营销首页「最快首字」：客户侧 TTFT = gateway_first_token_at − started_at（毫秒），
-- 与 console 请求明细 first_token_ms 同口径。只统计真正吐出过首 token 的成功流式请求；
-- 非正数样本排除（时钟回拨 / 未观测）。无样本时返回 0，由 HTTP 层转成 null。
SELECT COALESCE(MIN(EXTRACT(EPOCH FROM (gateway_first_token_at - started_at)) * 1000), 0)::float8 AS min_first_token_ms
FROM request_records
WHERE stream = TRUE
  AND status = 'succeeded'
  AND gateway_first_token_at IS NOT NULL
  AND started_at IS NOT NULL
  AND gateway_first_token_at > started_at;
