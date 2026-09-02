DROP INDEX IF EXISTS idx_routing_traces_selected_account;

ALTER TABLE public.routing_decision_traces
    DROP COLUMN IF EXISTS attempted_account_ids,
    DROP COLUMN IF EXISTS selected_account_id;
