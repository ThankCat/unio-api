-- Settlement recovery 必须携带与首次结算相同的终态、错误和长上下文策略，不能在 worker 重放时依赖默认值
-- 或通过成本来源 ID 间接推断。
DO $$
BEGIN
    IF EXISTS (
        SELECT 1
        FROM public.settlement_recovery_jobs
        WHERE status IN ('pending', 'running')
    ) THEN
        RAISE EXCEPTION
            'migration 000041 requires pending/running settlement recovery jobs to be drained before upgrade';
    END IF;
END
$$;

ALTER TABLE public.settlement_recovery_jobs
    ADD COLUMN request_final_status text NOT NULL DEFAULT 'succeeded',
    ADD COLUMN attempt_final_status text NOT NULL DEFAULT 'succeeded',
    ADD COLUMN settlement_error_code text NOT NULL DEFAULT '',
    ADD COLUMN settlement_error_message text NOT NULL DEFAULT '',
    ADD COLUMN settlement_internal_error_detail text NOT NULL DEFAULT '',
    ADD COLUMN long_context_enabled boolean NOT NULL DEFAULT false,
    ADD COLUMN long_context_threshold bigint,
    ADD COLUMN long_context_input_multiplier numeric(20,10),
    ADD COLUMN long_context_output_multiplier numeric(20,10),
    ADD CONSTRAINT ck_settlement_recovery_jobs_request_final_status
        CHECK (request_final_status IN ('succeeded', 'failed', 'canceled')),
    ADD CONSTRAINT ck_settlement_recovery_jobs_attempt_final_status
        CHECK (attempt_final_status IN ('succeeded', 'failed', 'canceled')),
    ADD CONSTRAINT ck_settlement_recovery_jobs_error_facts
        CHECK (
            (
                request_final_status = 'succeeded'
                AND attempt_final_status = 'succeeded'
                AND settlement_error_code = ''
                AND settlement_error_message = ''
                AND settlement_internal_error_detail = ''
            )
            OR
            (
                (request_final_status <> 'succeeded' OR attempt_final_status <> 'succeeded')
                AND settlement_error_code <> ''
                AND settlement_error_message <> ''
            )
        ),
    ADD CONSTRAINT ck_settlement_recovery_jobs_long_context
        CHECK (
            NOT long_context_enabled
            OR (
                long_context_threshold IS NOT NULL
                AND long_context_threshold > 0
                AND long_context_input_multiplier IS NOT NULL
                AND long_context_input_multiplier > 0
                AND long_context_output_multiplier IS NOT NULL
                AND long_context_output_multiplier > 0
            )
        );

-- DEFAULT 只用于兼容已关闭的历史 job；新 job 必须由唯一写入口显式提供全部重放事实。
ALTER TABLE public.settlement_recovery_jobs
    ALTER COLUMN request_final_status DROP DEFAULT,
    ALTER COLUMN attempt_final_status DROP DEFAULT,
    ALTER COLUMN settlement_error_code DROP DEFAULT,
    ALTER COLUMN settlement_error_message DROP DEFAULT,
    ALTER COLUMN settlement_internal_error_detail DROP DEFAULT,
    ALTER COLUMN long_context_enabled DROP DEFAULT;
