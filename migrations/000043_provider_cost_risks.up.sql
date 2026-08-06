-- Provider 成本风险：上游可能已扣费，但 Unio 没有可靠 usage，不能直接扣内部余额。
CREATE SEQUENCE public.provider_cost_risks_id_seq
    START WITH 1 INCREMENT BY 1 NO MINVALUE NO MAXVALUE CACHE 1;

CREATE TABLE public.provider_cost_risks (
    id bigint NOT NULL,
    provider_id bigint NOT NULL,
    request_record_id bigint,
    request_attempt_id bigint,
    provider_probe_record_id bigint,
    source_type text NOT NULL,
    estimated_amount numeric(20,10),
    currency text,
    reason_code text NOT NULL,
    reason text NOT NULL,
    status text NOT NULL DEFAULT 'unresolved',
    reconciliation_ledger_entry_id bigint,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    reconciled_at timestamp with time zone,
    CONSTRAINT provider_cost_risks_pkey PRIMARY KEY (id),
    CONSTRAINT provider_cost_risks_source_type_check CHECK (source_type IN ('request', 'probe')),
    CONSTRAINT provider_cost_risks_source_check CHECK (
        (source_type = 'request' AND request_record_id IS NOT NULL AND request_attempt_id IS NOT NULL AND provider_probe_record_id IS NULL)
        OR (source_type = 'probe' AND request_record_id IS NULL AND request_attempt_id IS NULL AND provider_probe_record_id IS NOT NULL)
    ),
    CONSTRAINT provider_cost_risks_amount_check CHECK (estimated_amount IS NULL OR estimated_amount >= 0),
    CONSTRAINT provider_cost_risks_reason_code_check CHECK (btrim(reason_code) <> ''),
    CONSTRAINT provider_cost_risks_reason_check CHECK (btrim(reason) <> ''),
    CONSTRAINT provider_cost_risks_status_check CHECK (status IN ('unresolved', 'reconciled')),
    CONSTRAINT provider_cost_risks_reconciled_check CHECK (
        (status = 'unresolved' AND reconciliation_ledger_entry_id IS NULL AND reconciled_at IS NULL)
        OR (status = 'reconciled' AND reconciliation_ledger_entry_id IS NOT NULL AND reconciled_at IS NOT NULL)
    )
);

ALTER SEQUENCE public.provider_cost_risks_id_seq OWNED BY public.provider_cost_risks.id;
ALTER TABLE ONLY public.provider_cost_risks
    ALTER COLUMN id SET DEFAULT nextval('public.provider_cost_risks_id_seq'::regclass);
ALTER TABLE ONLY public.provider_cost_risks
    ADD CONSTRAINT provider_cost_risks_provider_id_fkey FOREIGN KEY (provider_id) REFERENCES public.providers(id);
ALTER TABLE ONLY public.provider_cost_risks
    ADD CONSTRAINT provider_cost_risks_request_record_id_fkey FOREIGN KEY (request_record_id) REFERENCES public.request_records(id);
ALTER TABLE ONLY public.provider_cost_risks
    ADD CONSTRAINT provider_cost_risks_request_attempt_fkey FOREIGN KEY (request_attempt_id, request_record_id)
    REFERENCES public.request_attempts(id, request_record_id);
ALTER TABLE ONLY public.provider_cost_risks
    ADD CONSTRAINT provider_cost_risks_probe_record_id_fkey FOREIGN KEY (provider_probe_record_id)
    REFERENCES public.provider_probe_records(id);
ALTER TABLE ONLY public.provider_cost_risks
    ADD CONSTRAINT provider_cost_risks_reconciliation_ledger_entry_id_fkey FOREIGN KEY (reconciliation_ledger_entry_id)
    REFERENCES public.provider_ledger_entries(id);

CREATE UNIQUE INDEX uq_provider_cost_risks_request_attempt
    ON public.provider_cost_risks (request_attempt_id) WHERE request_attempt_id IS NOT NULL;
CREATE UNIQUE INDEX uq_provider_cost_risks_probe_record
    ON public.provider_cost_risks (provider_probe_record_id) WHERE provider_probe_record_id IS NOT NULL;
CREATE INDEX idx_provider_cost_risks_provider_status_created
    ON public.provider_cost_risks (provider_id, status, created_at DESC, id DESC);
