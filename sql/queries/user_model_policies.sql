-- name: UserCanUseModel :one
-- UserCanUseModel 判断指定用户是否允许使用指定启用模型。
WITH user_scope AS (
    SELECT sqlc.arg(user_id)::BIGINT AS user_id
),
target_model AS (
    SELECT m.id
    FROM models m
    WHERE m.model_id = sqlc.arg(requested_model_id)
      AND m.status = 'enabled'
),
user_policy_mode AS (
    SELECT EXISTS (
        SELECT 1
        FROM user_model_policies ump
        JOIN user_scope us ON us.user_id = ump.user_id
        WHERE ump.visibility = 'allowed'
    ) AS has_allow_list
)
SELECT EXISTS (
    SELECT 1
    FROM target_model m
    JOIN user_scope us ON us.user_id > 0
    WHERE NOT EXISTS (
        SELECT 1
        FROM user_model_policies denied
        WHERE denied.user_id = us.user_id
          AND denied.model_id = m.id
          AND denied.visibility = 'denied'
    )
      AND (
        NOT (SELECT has_allow_list FROM user_policy_mode)
            OR EXISTS (
            SELECT 1
            FROM user_model_policies allowed
            WHERE allowed.user_id = us.user_id
              AND allowed.model_id = m.id
              AND allowed.visibility = 'allowed'
        )
        )
) AS allowed;
