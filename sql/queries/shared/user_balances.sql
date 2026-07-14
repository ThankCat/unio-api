-- name: CreateUserBalance :one
-- CreateUserBalance 创建用户指定币种的余额投影。
INSERT INTO
    user_balances (user_id, currency, balance)
VALUES
    (
        sqlc.arg (user_id),
        sqlc.arg (currency),
        sqlc.arg (balance)
    )
RETURNING
    id,
    user_id,
    currency,
    balance,
    reserved_balance,
    created_at,
    updated_at;

-- name: GetUserBalance :one
-- GetUserBalance 读取用户指定币种的余额投影。
SELECT
    id,
    user_id,
    currency,
    balance,
    reserved_balance,
    created_at,
    updated_at
FROM
    user_balances
WHERE
    user_id = sqlc.arg (user_id)
  AND currency = sqlc.arg (currency);

-- name: GetUserBalanceForUpdate :one
-- GetUserBalanceForUpdate 锁定用户余额投影用于事务内余额变更。
SELECT
    id,
    user_id,
    currency,
    balance,
    reserved_balance,
    created_at,
    updated_at
FROM
    user_balances
WHERE
    user_id = sqlc.arg (user_id)
  AND currency = sqlc.arg (currency)
    FOR UPDATE;

-- name: UpdateUserBalance :one
-- UpdateUserBalance 直接更新用户余额投影的 balance 字段。
UPDATE user_balances
SET
    balance = sqlc.arg (balance),
    updated_at = now()
WHERE
    user_id = sqlc.arg (user_id)
  AND currency = sqlc.arg (currency)
RETURNING
    id,
    user_id,
    currency,
    balance,
    reserved_balance,
    created_at,
    updated_at;

-- name: EnsureUserBalance :exec
-- EnsureUserBalance 确保用户指定币种存在余额投影。
INSERT INTO user_balances(user_id, currency, balance)
VALUES (sqlc.arg(user_id), sqlc.arg(currency), 0)
ON CONFLICT (user_id, currency) DO NOTHING;

-- name: AddUserBalance :one
-- AddUserBalance 给用户余额投影增加可用余额。
UPDATE user_balances
SET
    balance = balance + sqlc.arg(amount),
    updated_at = now()
WHERE user_id = sqlc.arg(user_id)
AND currency = sqlc.arg(currency)
RETURNING
    id,
    user_id,
    currency,
    balance,
    reserved_balance,
    created_at,
    updated_at;

-- name: SubtractUserBalance :one
-- SubtractUserBalance 从用户余额投影扣减可用余额，要求未冻结余额足够。
UPDATE user_balances
SET
    balance = balance - sqlc.arg(amount),
    updated_at = now()
WHERE user_id = sqlc.arg(user_id)
  AND currency = sqlc.arg(currency)
  AND balance >= sqlc.arg(amount)
  AND balance - reserved_balance >= sqlc.arg(amount)
RETURNING
    id,
    user_id,
    currency,
    balance,
    reserved_balance,
    created_at,
    updated_at;

-- name: ReserveUserBalance :one
-- ReserveUserBalance 冻结指定金额的用户可用余额。
UPDATE user_balances
SET
    reserved_balance = reserved_balance + sqlc.arg(amount),
    updated_at = now()
WHERE user_id = sqlc.arg(user_id)
    AND currency = sqlc.arg(currency)
    AND balance - reserved_balance >= sqlc.arg(amount)
RETURNING
    id,
    user_id,
    currency,
    balance,
    reserved_balance,
    created_at,
    updated_at;

-- name: CaptureUserReservedBalance :one
-- CaptureUserReservedBalance 从冻结余额中确认扣费并释放剩余授权金额。
UPDATE user_balances
SET
    balance = balance - sqlc.arg(captured_amount),
    reserved_balance = reserved_balance - sqlc.arg(authorized_amount),
    updated_at = now()
WHERE user_id = sqlc.arg(user_id)
  AND currency = sqlc.arg(currency)
  AND reserved_balance >= sqlc.arg(authorized_amount)
  AND balance >= sqlc.arg(captured_amount)
RETURNING
    id,
    user_id,
    currency,
    balance,
    reserved_balance,
    created_at,
    updated_at;

-- name: ReleaseUserReservedBalance :one
-- ReleaseUserReservedBalance 释放指定金额的用户冻结余额。
UPDATE user_balances
SET
    reserved_balance = reserved_balance - sqlc.arg(amount),
    updated_at = now()
WHERE user_id = sqlc.arg(user_id)
  AND currency = sqlc.arg(currency)
  AND reserved_balance >= sqlc.arg(amount)
RETURNING
    id,
    user_id,
    currency,
    balance,
    reserved_balance,
    created_at,
    updated_at;

-- name: CollectUserBalanceOverage :one
-- CollectUserBalanceOverage 在 capture 之后，对「真实费用超过授权金额」的差额做二次补扣。
-- 实扣金额 = GREATEST(LEAST(可用余额(balance - reserved_balance), 真实费用 - 授权金额), 0)：
--   可用余额足够时全额补扣差额；不足时只扣到清空可用余额；不动用其它请求的冻结额，余额永不为负。
-- 返回本次实际补扣金额 collected_amount 与扣减后的余额投影，供调用方写超额扣费流水。
WITH locked_balance AS (
    SELECT
        ub.id,
        GREATEST(
            LEAST(
                ub.balance - ub.reserved_balance,
                sqlc.arg(actual_amount)::numeric(20, 10) - sqlc.arg(authorized_amount)::numeric(20, 10)
            ),
            0
        )::numeric(20, 10) AS collected_amount
    FROM user_balances ub
    WHERE ub.user_id = sqlc.arg(user_id)
      AND ub.currency = sqlc.arg(currency)
        FOR UPDATE
),
     updated AS (
         UPDATE user_balances ub
             SET
                 balance = ub.balance - lb.collected_amount,
                 updated_at = now()
             FROM locked_balance lb
             WHERE ub.id = lb.id
             RETURNING
                 ub.id,
                 ub.user_id,
                 ub.currency,
                 ub.balance,
                 ub.reserved_balance,
                 ub.created_at,
                 ub.updated_at,
                 lb.collected_amount
     )
SELECT id,
       user_id,
       currency,
       balance,
       reserved_balance,
       created_at,
       updated_at,
       collected_amount
FROM updated;

-- name: ReserveAvailableUserBalance :one
-- ReserveAvailableUserBalance 按估算金额冻结用户全部或部分可用余额，并返回本次实际授权金额。
WITH locked_balance AS (
    SELECT
        ub.id,
        LEAST(
            ub.balance - ub.reserved_balance,
            sqlc.arg(estimated_amount)::numeric(20, 10)
        )::numeric(20, 10) AS authorized_amount
    FROM user_balances ub
    WHERE ub.user_id = sqlc.arg(user_id)
      AND ub.currency = sqlc.arg(currency)
        FOR UPDATE
),
     updated AS (
         UPDATE user_balances ub
             SET
                 reserved_balance = ub.reserved_balance + lb.authorized_amount,
                 updated_at = now()
             FROM locked_balance lb
             WHERE ub.id = lb.id
                 AND lb.authorized_amount > 0
             RETURNING
                 ub.id,
                 ub.user_id,
                 ub.currency,
                 ub.balance,
                 ub.reserved_balance,
                 ub.created_at,
                 ub.updated_at,
                 lb.authorized_amount
     )
SELECT id,
       user_id,
       currency,
       balance,
       reserved_balance,
       created_at,
       updated_at,
       authorized_amount
FROM updated;
