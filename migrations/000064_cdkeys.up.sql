-- CDKEY 充值券。明文保留在数据库，仅导出 service 可读取；普通查询永不返回该列。
CREATE SEQUENCE public.cdkeys_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;

CREATE TABLE public.cdkeys (
    id bigint NOT NULL,
    batch_id uuid NOT NULL,
    code_plaintext text NOT NULL,
    code_hash text NOT NULL,
    code_prefix text NOT NULL,
    code_suffix text NOT NULL,
    amount numeric(20,10) NOT NULL,
    currency text DEFAULT 'USD'::text NOT NULL,
    status text DEFAULT 'unused'::text NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    redeemed_at timestamp with time zone,
    revoked_at timestamp with time zone,
    CONSTRAINT cdkeys_amount_check CHECK ((amount = ANY (ARRAY[(5)::numeric, (10)::numeric, (30)::numeric, (50)::numeric, (100)::numeric, (200)::numeric, (500)::numeric]))),
    CONSTRAINT cdkeys_currency_check CHECK ((currency = 'USD'::text)),
    CONSTRAINT cdkeys_status_check CHECK ((status = ANY (ARRAY['unused'::text, 'redeemed'::text, 'revoked'::text]))),
    CONSTRAINT cdkeys_state_timestamps_check CHECK (((status = 'unused'::text AND redeemed_at IS NULL AND revoked_at IS NULL) OR (status = 'redeemed'::text AND redeemed_at IS NOT NULL AND revoked_at IS NULL) OR (status = 'revoked'::text AND redeemed_at IS NULL AND revoked_at IS NOT NULL))),
    CONSTRAINT cdkeys_code_plaintext_check CHECK ((btrim(code_plaintext) <> ''::text)),
    CONSTRAINT cdkeys_code_hash_check CHECK ((btrim(code_hash) <> ''::text))
);

ALTER SEQUENCE public.cdkeys_id_seq OWNED BY public.cdkeys.id;

ALTER TABLE ONLY public.cdkeys
    ALTER COLUMN id SET DEFAULT nextval('public.cdkeys_id_seq'::regclass);

ALTER TABLE ONLY public.cdkeys
    ADD CONSTRAINT cdkeys_pkey PRIMARY KEY (id);

ALTER TABLE ONLY public.cdkeys
    ADD CONSTRAINT cdkeys_code_hash_key UNIQUE (code_hash);

CREATE INDEX idx_cdkeys_batch_id ON public.cdkeys USING btree (batch_id);
CREATE INDEX idx_cdkeys_status_created_at ON public.cdkeys USING btree (status, created_at DESC, id DESC);
CREATE INDEX idx_cdkeys_amount_status ON public.cdkeys USING btree (amount, status);
CREATE INDEX idx_cdkeys_prefix_suffix ON public.cdkeys USING btree (code_prefix, code_suffix);

CREATE SEQUENCE public.cdkey_redemptions_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;

CREATE TABLE public.cdkey_redemptions (
    id bigint NOT NULL,
    cdkey_id bigint NOT NULL,
    user_id bigint NOT NULL,
    amount numeric(20,10) NOT NULL,
    currency text DEFAULT 'USD'::text NOT NULL,
    ledger_entry_id bigint NOT NULL,
    idempotency_key text NOT NULL,
    redeemed_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT cdkey_redemptions_amount_check CHECK ((amount = ANY (ARRAY[(5)::numeric, (10)::numeric, (30)::numeric, (50)::numeric, (100)::numeric, (200)::numeric, (500)::numeric]))),
    CONSTRAINT cdkey_redemptions_currency_check CHECK ((currency = 'USD'::text)),
    CONSTRAINT cdkey_redemptions_idempotency_key_check CHECK ((btrim(idempotency_key) <> ''::text))
);

ALTER SEQUENCE public.cdkey_redemptions_id_seq OWNED BY public.cdkey_redemptions.id;

ALTER TABLE ONLY public.cdkey_redemptions
    ALTER COLUMN id SET DEFAULT nextval('public.cdkey_redemptions_id_seq'::regclass);

ALTER TABLE ONLY public.cdkey_redemptions
    ADD CONSTRAINT cdkey_redemptions_pkey PRIMARY KEY (id);

ALTER TABLE ONLY public.cdkey_redemptions
    ADD CONSTRAINT cdkey_redemptions_cdkey_id_key UNIQUE (cdkey_id);

ALTER TABLE ONLY public.cdkey_redemptions
    ADD CONSTRAINT cdkey_redemptions_ledger_entry_id_key UNIQUE (ledger_entry_id);

ALTER TABLE ONLY public.cdkey_redemptions
    ADD CONSTRAINT cdkey_redemptions_idempotency_key_key UNIQUE (idempotency_key);

ALTER TABLE ONLY public.cdkey_redemptions
    ADD CONSTRAINT cdkey_redemptions_cdkey_id_fkey FOREIGN KEY (cdkey_id) REFERENCES public.cdkeys(id) ON DELETE RESTRICT;

ALTER TABLE ONLY public.cdkey_redemptions
    ADD CONSTRAINT cdkey_redemptions_user_id_fkey FOREIGN KEY (user_id) REFERENCES public.users(id) ON DELETE RESTRICT;

ALTER TABLE ONLY public.cdkey_redemptions
    ADD CONSTRAINT cdkey_redemptions_ledger_entry_id_fkey FOREIGN KEY (ledger_entry_id) REFERENCES public.ledger_entries(id) ON DELETE RESTRICT;

CREATE INDEX idx_cdkey_redemptions_user_redeemed_at ON public.cdkey_redemptions USING btree (user_id, redeemed_at DESC, id DESC);
CREATE INDEX idx_cdkey_redemptions_redeemed_at ON public.cdkey_redemptions USING btree (redeemed_at DESC, id DESC);

-- CDKEY 充值是正向余额变更，和普通 credit/refund 一样满足 balance_after = before + amount。
ALTER TABLE public.ledger_entries
    DROP CONSTRAINT IF EXISTS ck_ledger_entries_balance_math;
ALTER TABLE public.ledger_entries
    DROP CONSTRAINT IF EXISTS ledger_entries_entry_type_check;
ALTER TABLE public.ledger_entries
    ADD CONSTRAINT ck_ledger_entries_balance_math CHECK (
        ((entry_type = ANY (ARRAY['credit'::text, 'refund'::text, 'adjustment_credit'::text, 'cdkey_credit'::text])) AND (balance_after = (balance_before + amount)))
        OR ((entry_type = ANY (ARRAY['debit'::text, 'adjustment_debit'::text])) AND (balance_after = (balance_before - amount)))
    );
ALTER TABLE public.ledger_entries
    ADD CONSTRAINT ledger_entries_entry_type_check CHECK (
        entry_type = ANY (ARRAY['credit'::text, 'debit'::text, 'refund'::text, 'adjustment_credit'::text, 'adjustment_debit'::text, 'cdkey_credit'::text])
    );
