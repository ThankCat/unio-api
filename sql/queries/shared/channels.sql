-- name: ApplyRuntime401CredentialInvalidation :one
-- ApplyRuntime401CredentialInvalidation 将达到阈值的运行时 401 按当次 Channel config 与 Endpoint
-- BaseURL/status 三类 expected revision 做原子 CAS。只有三类 revision 仍匹配且 credential_valid=true
-- 时才翻 false 并推进 config_revision；迟到结果只写 state_change_applied=false 的审计行。
WITH matching AS MATERIALIZED (
    SELECT c.id, c.credential_valid, c.config_revision
    FROM channels c
    JOIN provider_endpoints pe ON pe.id = c.provider_endpoint_id
    WHERE c.id = sqlc.arg(channel_id)
      AND c.config_revision = sqlc.arg(expected_config_revision)
      AND pe.base_url_revision = sqlc.arg(expected_endpoint_base_url_revision)
      AND pe.status_revision = sqlc.arg(expected_endpoint_status_revision)
    FOR UPDATE OF c
), applied AS (
    UPDATE channels c
    SET credential_valid = FALSE,
        config_revision = c.config_revision + 1,
        updated_at = now()
    FROM matching
    WHERE c.id = matching.id
      AND matching.credential_valid = TRUE
    RETURNING c.id, c.credential_valid, c.config_revision
), current_state AS (
    SELECT applied.id, applied.credential_valid, applied.config_revision, TRUE AS state_change_applied
    FROM applied
    UNION ALL
    SELECT c.id, c.credential_valid, c.config_revision, FALSE AS state_change_applied
    FROM channels c
    WHERE c.id = sqlc.arg(channel_id)
      AND NOT EXISTS (SELECT 1 FROM applied)
    LIMIT 1
), logged AS (
    INSERT INTO channel_test_logs (
        channel_id, source, success, error_code, credential_valid_after, message,
        tested_endpoint_base_url_revision, tested_endpoint_status_revision,
        tested_config_revision, state_change_applied
    )
    SELECT
        current_state.id, 'runtime_401', FALSE, 'credential_invalid', current_state.credential_valid,
        '连续 401 达阈值，按冻结版本尝试标记凭据失效',
        sqlc.arg(expected_endpoint_base_url_revision), sqlc.arg(expected_endpoint_status_revision),
        sqlc.arg(expected_config_revision), current_state.state_change_applied
    FROM current_state
    RETURNING channel_id
)
SELECT current_state.state_change_applied, current_state.credential_valid AS credential_valid_after,
       current_state.config_revision AS current_config_revision
FROM current_state
JOIN logged ON logged.channel_id = current_state.id;
