-- Provider ledger entry 是内部 Provider 余额变化的不可变事实。
CREATE SEQUENCE public.provider_ledger_entries_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;

CREATE TABLE public.provider_ledger_entries (
    id bigint NOT NULL,
    provider_id bigint NOT NULL,
    request_record_id bigint,
    request_attempt_id bigint,
    provider_probe_record_id bigint,
    cost_snapshot_id bigint,
    channel_id bigint,
    request_id text,
    channel_name text,
    upstream_model text,
    entry_type text NOT NULL,
    amount numeric(20,10) NOT NULL,
    currency text NOT NULL,
    balance_before numeric(20,10) NOT NULL,
    balance_after numeric(20,10) NOT NULL,
    idempotency_key text NOT NULL,
    reason text NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT provider_ledger_entries_amount_check CHECK (amount > 0),
    CONSTRAINT provider_ledger_entries_currency_check CHECK (btrim(currency) <> ''),
    CONSTRAINT provider_ledger_entries_idempotency_key_check CHECK (btrim(idempotency_key) <> ''),
    CONSTRAINT provider_ledger_entries_reason_check CHECK (btrim(reason) <> ''),
    CONSTRAINT provider_ledger_entries_entry_type_check CHECK (
        entry_type = ANY (ARRAY['usage_debit'::text, 'probe_debit'::text, 'adjustment_credit'::text, 'adjustment_debit'::text])
    ),
    CONSTRAINT provider_ledger_entries_balance_math_check CHECK (
        (entry_type = 'adjustment_credit' AND balance_after = balance_before + amount)
        OR (entry_type IN ('usage_debit', 'probe_debit', 'adjustment_debit') AND balance_after = balance_before - amount)
    ),
    CONSTRAINT provider_ledger_entries_source_check CHECK (
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
    )
);

ALTER SEQUENCE public.provider_ledger_entries_id_seq OWNED BY public.provider_ledger_entries.id;

ALTER TABLE ONLY public.provider_ledger_entries
    ALTER COLUMN id SET DEFAULT nextval('public.provider_ledger_entries_id_seq'::regclass);

ALTER TABLE ONLY public.provider_ledger_entries
    ADD CONSTRAINT provider_ledger_entries_pkey PRIMARY KEY (id);

ALTER TABLE ONLY public.provider_ledger_entries
    ADD CONSTRAINT provider_ledger_entries_idempotency_key_key UNIQUE (idempotency_key);

CREATE UNIQUE INDEX uq_provider_ledger_entries_cost_snapshot_id
    ON public.provider_ledger_entries USING btree (cost_snapshot_id)
    WHERE cost_snapshot_id IS NOT NULL;

CREATE UNIQUE INDEX uq_provider_ledger_entries_probe_record_id
    ON public.provider_ledger_entries USING btree (provider_probe_record_id)
    WHERE provider_probe_record_id IS NOT NULL;

CREATE INDEX idx_provider_ledger_entries_provider_currency_created
    ON public.provider_ledger_entries USING btree (provider_id, currency, created_at DESC, id DESC);

CREATE INDEX idx_provider_ledger_entries_provider_type_created
    ON public.provider_ledger_entries USING btree (provider_id, entry_type, created_at DESC, id DESC);

CREATE INDEX idx_provider_ledger_entries_request_record_id
    ON public.provider_ledger_entries USING btree (request_record_id)
    WHERE request_record_id IS NOT NULL;

CREATE INDEX idx_provider_ledger_entries_request_id
    ON public.provider_ledger_entries USING btree (request_id)
    WHERE request_id IS NOT NULL;

ALTER TABLE ONLY public.provider_ledger_entries
    ADD CONSTRAINT provider_ledger_entries_provider_id_fkey FOREIGN KEY (provider_id) REFERENCES public.providers(id);

ALTER TABLE ONLY public.provider_ledger_entries
    ADD CONSTRAINT provider_ledger_entries_request_record_id_fkey FOREIGN KEY (request_record_id) REFERENCES public.request_records(id);

ALTER TABLE ONLY public.provider_ledger_entries
    ADD CONSTRAINT provider_ledger_entries_request_attempt_fkey
    FOREIGN KEY (request_attempt_id, request_record_id) REFERENCES public.request_attempts(id, request_record_id);

ALTER TABLE ONLY public.provider_ledger_entries
    ADD CONSTRAINT provider_ledger_entries_probe_record_id_fkey FOREIGN KEY (provider_probe_record_id)
    REFERENCES public.provider_probe_records(id);

ALTER TABLE ONLY public.provider_ledger_entries
    ADD CONSTRAINT provider_ledger_entries_cost_snapshot_id_fkey FOREIGN KEY (cost_snapshot_id) REFERENCES public.cost_snapshots(id);

ALTER TABLE ONLY public.provider_ledger_entries
    ADD CONSTRAINT provider_ledger_entries_channel_id_fkey FOREIGN KEY (channel_id) REFERENCES public.channels(id);
