ALTER TABLE public.channels
    ADD COLUMN upstream_bills_on_disconnect boolean DEFAULT false NOT NULL;

CREATE SEQUENCE public.channel_cost_exposures_id_seq
    START WITH 1 INCREMENT BY 1 NO MINVALUE NO MAXVALUE CACHE 1;

CREATE TABLE public.channel_cost_exposures (
    id bigint NOT NULL,
    request_record_id bigint NOT NULL,
    attempt_id bigint NOT NULL,
    channel_id bigint NOT NULL,
    provider_id bigint NOT NULL,
    reason text NOT NULL,
    estimated_input_tokens bigint NOT NULL,
    assumed_output_tokens bigint NOT NULL,
    estimated_cost_amount numeric NOT NULL,
    currency text NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT channel_cost_exposures_assumed_output_tokens_check CHECK (assumed_output_tokens >= 0),
    CONSTRAINT channel_cost_exposures_estimated_cost_amount_check CHECK (estimated_cost_amount >= 0),
    CONSTRAINT channel_cost_exposures_estimated_input_tokens_check CHECK (estimated_input_tokens >= 0),
    CONSTRAINT channel_cost_exposures_reason_check CHECK (reason IN ('upstream_timeout', 'upstream_error', 'client_canceled'))
);

ALTER SEQUENCE public.channel_cost_exposures_id_seq OWNED BY public.channel_cost_exposures.id;
ALTER TABLE ONLY public.channel_cost_exposures
    ALTER COLUMN id SET DEFAULT nextval('public.channel_cost_exposures_id_seq'::regclass);
ALTER TABLE ONLY public.channel_cost_exposures
    ADD CONSTRAINT channel_cost_exposures_pkey PRIMARY KEY (id);
CREATE INDEX idx_channel_cost_exposures_channel_created_at
    ON public.channel_cost_exposures (channel_id, created_at DESC);
CREATE INDEX idx_channel_cost_exposures_request
    ON public.channel_cost_exposures (request_record_id);
ALTER TABLE ONLY public.channel_cost_exposures
    ADD CONSTRAINT channel_cost_exposures_attempt_id_fkey FOREIGN KEY (attempt_id) REFERENCES public.request_attempts(id);
ALTER TABLE ONLY public.channel_cost_exposures
    ADD CONSTRAINT channel_cost_exposures_channel_id_fkey FOREIGN KEY (channel_id) REFERENCES public.channels(id);
ALTER TABLE ONLY public.channel_cost_exposures
    ADD CONSTRAINT channel_cost_exposures_provider_id_fkey FOREIGN KEY (provider_id) REFERENCES public.providers(id);
ALTER TABLE ONLY public.channel_cost_exposures
    ADD CONSTRAINT channel_cost_exposures_request_record_id_fkey FOREIGN KEY (request_record_id) REFERENCES public.request_records(id);

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
    status text DEFAULT 'unresolved' NOT NULL,
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

ALTER TABLE public.provider_probe_records
    DROP CONSTRAINT provider_probe_records_cost_details_check,
    DROP CONSTRAINT provider_probe_records_usage_check;

UPDATE public.provider_probe_records
SET usage_reliable = false
WHERE usage_reliable = true
  AND (cost_amount IS NULL OR currency IS NULL);

ALTER TABLE public.provider_probe_records
    ADD CONSTRAINT provider_probe_records_usage_check CHECK (
        usage_reliable = false
        OR (usage_source IS NOT NULL AND usage_facts IS NOT NULL AND cost_amount IS NOT NULL AND currency IS NOT NULL)
    );

ALTER TABLE public.provider_ledger_entries
    DROP CONSTRAINT provider_ledger_entries_usage_source_check,
    DROP CONSTRAINT provider_ledger_entries_source_check;

ALTER TABLE public.provider_ledger_entries
    ADD CONSTRAINT provider_ledger_entries_source_check CHECK (
        (
            entry_type = 'usage_debit'
            AND provider_probe_record_id IS NULL
            AND request_record_id IS NOT NULL
            AND request_attempt_id IS NOT NULL
            AND cost_snapshot_id IS NOT NULL
            AND channel_id IS NOT NULL
            AND request_id IS NOT NULL AND btrim(request_id) <> ''
            AND channel_name IS NOT NULL AND btrim(channel_name) <> ''
            AND upstream_model IS NOT NULL AND btrim(upstream_model) <> ''
        )
        OR (
            entry_type = 'probe_debit'
            AND provider_probe_record_id IS NOT NULL
            AND request_record_id IS NULL
            AND request_attempt_id IS NULL
            AND cost_snapshot_id IS NULL
            AND channel_id IS NULL
            AND request_id IS NULL
            AND channel_name IS NULL
            AND upstream_model IS NULL
        )
        OR (
            entry_type IN ('adjustment_credit', 'adjustment_debit')
            AND provider_probe_record_id IS NULL
            AND request_record_id IS NULL
            AND request_attempt_id IS NULL
            AND cost_snapshot_id IS NULL
            AND channel_id IS NULL
            AND request_id IS NULL
            AND channel_name IS NULL
            AND upstream_model IS NULL
        )
    );

ALTER TABLE public.provider_ledger_entries DROP COLUMN usage_source;
