-- M8 系统 / 任务 / 健康（横切）只读聚合。纯只读、不引入新业务事实。
-- channel 熔断为 gateway 进程内内存态（见 lifecycle/breaker.go），admin 跨进程读不到；
-- 此处的 channel 健康从区间内 request_attempts 成功率派生，作为运营可观测的近似。

-- name: SystemChannelHealth :many
-- SystemChannelHealth 按区间内 request_attempts 推导每个 channel 的健康明细（比 M9 看板更细：含失败数与最近尝试时间）。
-- LEFT JOIN + 时间过滤放在 ON 条件，保留区间内零尝试的 channel（service 视为 no_data）。
SELECT
    c.id AS channel_id,
    c.name,
    c.status,
    c.provider_id,
    COUNT(a.id) AS attempt_total,
    COUNT(a.id) FILTER (WHERE a.status = 'succeeded') AS attempt_succeeded,
    COUNT(a.id) FILTER (WHERE a.status = 'failed') AS attempt_failed,
    COUNT(a.id) FILTER (WHERE a.status = 'canceled') AS attempt_canceled,
    MAX(a.created_at)::timestamptz AS last_attempt_at
FROM channels c
LEFT JOIN request_attempts a
    ON a.channel_id = c.id
    AND (sqlc.narg('from_time')::timestamptz IS NULL OR a.created_at >= sqlc.narg('from_time')::timestamptz)
    AND (sqlc.narg('to_time')::timestamptz IS NULL OR a.created_at < sqlc.narg('to_time')::timestamptz)
GROUP BY c.id, c.name, c.status, c.provider_id
ORDER BY attempt_failed DESC, attempt_total DESC, c.id;
