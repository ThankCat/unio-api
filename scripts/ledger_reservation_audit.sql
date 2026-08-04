-- 账本预授权不变量巡检。
--
-- 用途：验证冻结余额没有泄漏。两条主检查都应恒为 0 行；任一非 0 都需要人工追查。
--
-- 用法：
--   psql "$DATABASE_URL" -f scripts/ledger_reservation_audit.sql
--   docker exec -i unio-postgres psql -U unio -d unio < scripts/ledger_reservation_audit.sql
--
-- 全部查询只读，走部分索引 idx_ledger_reservations_authorized_created_at，代价与在途请求数同阶。
--
-- 背景：网关失败路径「先 release 冻结、再写请求终态」两步非原子。release 自身失败而随后的审计写入成功时，
-- 请求变 failed/canceled 而冻结留在 authorized。worker 侧的 stranded_reservation_sweeper 会在年龄阈值
-- （WORKER_ORPHAN_RESERVATION_SWEEP_AGE_THRESHOLD，默认 15 分钟）后自动回收，因此本巡检是残余检查：
-- 持续非 0 说明 worker 没在跑、回收失败，或出现了新的泄漏形态。

\echo ''
\echo '=== [1] 终态请求不应存在 authorized 冻结（应为 0 行）==='
-- succeeded 配 authorized 最严重：capture 未发生却已告知客户成功，worker 不会自动回收，必须人工处置。
SELECT lr.id            AS reservation_id,
       lr.user_id,
       lr.request_record_id,
       lr.currency,
       lr.authorized_amount,
       lr.created_at,
       now() - lr.created_at AS age,
       r.status         AS request_status,
       r.error_code
FROM ledger_reservations lr
JOIN request_records r ON r.id = lr.request_record_id
WHERE lr.status = 'authorized'
  AND r.status IN ('succeeded', 'failed', 'canceled')
ORDER BY lr.created_at;

\echo ''
\echo '=== [2] reserved_balance 应等于该用户 authorized 冻结之和（应为 0 行）==='
-- 覆盖面比 [1] 更广：任何来源的账本漂移都会在这里显形。drift 持续为正会顶到
-- ck_user_balances_reserved_not_above_balance，表现为用户充值后仍发不出请求。
SELECT ub.user_id,
       ub.currency,
       ub.reserved_balance,
       coalesce(s.total, 0)                       AS authorized_sum,
       ub.reserved_balance - coalesce(s.total, 0) AS drift
FROM user_balances ub
LEFT JOIN (
    SELECT user_id, currency, sum(authorized_amount) AS total
    FROM ledger_reservations
    WHERE status = 'authorized'
    GROUP BY user_id, currency
) s ON s.user_id = ub.user_id AND s.currency = ub.currency
WHERE ub.reserved_balance <> coalesce(s.total, 0)
ORDER BY abs(ub.reserved_balance - coalesce(s.total, 0)) DESC;

\echo ''
\echo '=== [3] 参考：全部 authorized 冻结按请求状态分布（running 属正常在途）==='
SELECT r.status                               AS request_status,
       count(*)                               AS rows,
       min(lr.created_at)                     AS oldest,
       coalesce(sum(lr.authorized_amount), 0) AS amount
FROM ledger_reservations lr
JOIN request_records r ON r.id = lr.request_record_id
WHERE lr.status = 'authorized'
GROUP BY r.status
ORDER BY r.status;

\echo ''
\echo '=== [4] 参考：请求永久停留 running 超过 15 分钟（孤儿清扫的目标，应趋近 0）==='
SELECT count(*)                    AS stuck_running,
       min(r.created_at)           AS oldest
FROM request_records r
WHERE r.status = 'running'
  AND r.created_at < now() - interval '15 minutes';
