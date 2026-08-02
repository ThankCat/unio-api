CREATE INDEX idx_request_records_created_at_id
    ON public.request_records USING btree (created_at DESC, id DESC);

CREATE INDEX idx_request_attempts_created_at_id
    ON public.request_attempts USING btree (created_at DESC, id DESC);

CREATE INDEX idx_usage_records_created_at_id
    ON public.usage_records USING btree (created_at DESC, id DESC);

CREATE INDEX idx_cost_snapshots_created_at_id
    ON public.cost_snapshots USING btree (created_at DESC, id DESC);

CREATE INDEX idx_ledger_entries_type_created_at_id
    ON public.ledger_entries USING btree (entry_type, created_at DESC, id DESC);

CREATE INDEX idx_ledger_billing_exceptions_created_at_id
    ON public.ledger_billing_exceptions USING btree (created_at DESC, id DESC);
