DROP TABLE IF EXISTS public.runtime_control_operations CASCADE;
DROP FUNCTION IF EXISTS public.enforce_runtime_control_operation_transition();
DROP FUNCTION IF EXISTS public.runtime_control_release_evidence_valid(jsonb, jsonb, text);
DROP FUNCTION IF EXISTS public.runtime_control_recovery_evidence_valid(jsonb, jsonb, bigint, text);
DROP FUNCTION IF EXISTS public.runtime_control_epoch_transition_valid(jsonb, bigint, bigint);
