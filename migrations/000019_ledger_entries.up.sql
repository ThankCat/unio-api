-- Ledger entry 是用户余额变化的账本事实。
CREATE SEQUENCE public.ledger_entries_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;

CREATE TABLE public.ledger_entries (
    -- id: 主键。--
    id bigint NOT NULL,
    -- user_id: 流水所属用户 ID。--
    user_id bigint NOT NULL,
    -- request_record_id: 触发该流水的请求记录 ID，非请求流水为空。--
    request_record_id bigint,
    -- entry_type: 流水类型。--
    entry_type text NOT NULL,
    amount numeric(20,10) NOT NULL,
    currency text NOT NULL,
    balance_before numeric(20,10) NOT NULL,
    balance_after numeric(20,10) NOT NULL,
    idempotency_key text NOT NULL,
    reason text NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT ck_ledger_entries_balance_math CHECK ((((entry_type = ANY (ARRAY['credit'::text, 'refund'::text, 'adjustment_credit'::text])) AND (balance_after = (balance_before + amount))) OR ((entry_type = ANY (ARRAY['debit'::text, 'adjustment_debit'::text])) AND (balance_after = (balance_before - amount))))),
    CONSTRAINT ledger_entries_amount_check CHECK ((amount > (0)::numeric)),
    CONSTRAINT ledger_entries_balance_after_check CHECK ((balance_after >= (0)::numeric)),
    CONSTRAINT ledger_entries_balance_before_check CHECK ((balance_before >= (0)::numeric)),
    CONSTRAINT ledger_entries_currency_check CHECK ((currency <> ''::text)),
    CONSTRAINT ledger_entries_entry_type_check CHECK ((entry_type = ANY (ARRAY['credit'::text, 'debit'::text, 'refund'::text, 'adjustment_credit'::text, 'adjustment_debit'::text]))),
    CONSTRAINT ledger_entries_idempotency_key_check CHECK ((idempotency_key <> ''::text)),
    CONSTRAINT ledger_entries_reason_check CHECK ((reason <> ''::text))
);

ALTER SEQUENCE public.ledger_entries_id_seq OWNED BY public.ledger_entries.id;

ALTER TABLE ONLY public.ledger_entries ALTER COLUMN id SET DEFAULT nextval('public.ledger_entries_id_seq'::regclass);

ALTER TABLE ONLY public.ledger_entries
    ADD CONSTRAINT ledger_entries_idempotency_key_key UNIQUE (idempotency_key);

ALTER TABLE ONLY public.ledger_entries
    ADD CONSTRAINT ledger_entries_pkey PRIMARY KEY (id);

ALTER TABLE ONLY public.ledger_entries
    ADD CONSTRAINT uq_ledger_entries_id_user_request UNIQUE (id, user_id, request_record_id);

CREATE INDEX idx_ledger_entries_request_record_id ON public.ledger_entries USING btree (request_record_id) WHERE (request_record_id IS NOT NULL);

CREATE INDEX idx_ledger_entries_user_created_at ON public.ledger_entries USING btree (user_id, created_at DESC, id DESC);

ALTER TABLE ONLY public.ledger_entries
    ADD CONSTRAINT fk_ledger_entries_request_user FOREIGN KEY (request_record_id, user_id) REFERENCES public.request_records(id, user_id);

ALTER TABLE ONLY public.ledger_entries
    ADD CONSTRAINT ledger_entries_user_id_fkey FOREIGN KEY (user_id) REFERENCES public.users(id);
