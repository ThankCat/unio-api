-- Ledger reservation 是一次请求的余额预授权事实。
CREATE SEQUENCE public.ledger_reservations_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;

CREATE TABLE public.ledger_reservations (
    -- id: 主键。--
    id bigint NOT NULL,
    -- user_id: 预授权所属用户 ID。--
    user_id bigint NOT NULL,
    -- request_record_id: 预授权对应的请求记录 ID。--
    request_record_id bigint NOT NULL,
    -- currency: 预授权币种。--
    currency text NOT NULL,
    -- status: 预授权状态。--
    status text NOT NULL,
    -- authorized_amount: 实际冻结金额。--
    authorized_amount numeric(20,10) NOT NULL,
    -- captured_amount: 已确认扣费金额。--
    captured_amount numeric(20,10) DEFAULT 0 NOT NULL,
    -- released_amount: 已释放冻结金额。--
    released_amount numeric(20,10) DEFAULT 0 NOT NULL,
    -- estimated_amount: 本次请求的风险估算金额。--
    estimated_amount numeric(20,10) NOT NULL,
    -- capture_ledger_entry_id: capture 成功后对应的扣费流水 ID。--
    capture_ledger_entry_id bigint,
    -- idempotency_key: 预授权幂等键。--
    idempotency_key text NOT NULL,
    -- reason: 预授权业务原因。--
    reason text NOT NULL,
    -- created_at: 记录创建时间。--
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    -- updated_at: 记录更新时间。--
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    -- captured_at: capture 完成时间。--
    captured_at timestamp with time zone,
    -- released_at: release 完成时间。--
    released_at timestamp with time zone,
    CONSTRAINT ck_ledger_reservations_amount_sum CHECK (((captured_amount + released_amount) <= authorized_amount)),
    CONSTRAINT ck_ledger_reservations_authorized_not_above_estimated CHECK ((authorized_amount <= estimated_amount)),
    CONSTRAINT ck_ledger_reservations_status_amounts CHECK ((((status = 'authorized'::text) AND (captured_amount = (0)::numeric) AND (released_amount = (0)::numeric) AND (capture_ledger_entry_id IS NULL) AND (captured_at IS NULL) AND (released_at IS NULL)) OR ((status = 'captured'::text) AND (captured_amount > (0)::numeric) AND ((captured_amount + released_amount) = authorized_amount) AND (capture_ledger_entry_id IS NOT NULL) AND (captured_at IS NOT NULL) AND (((released_amount = (0)::numeric) AND (released_at IS NULL)) OR ((released_amount > (0)::numeric) AND (released_at IS NOT NULL)))) OR ((status = 'released'::text) AND (captured_amount = (0)::numeric) AND (released_amount = authorized_amount) AND (capture_ledger_entry_id IS NULL) AND (captured_at IS NULL) AND (released_at IS NOT NULL)))),
    CONSTRAINT ledger_reservations_authorized_amount_check CHECK ((authorized_amount > (0)::numeric)),
    CONSTRAINT ledger_reservations_captured_amount_check CHECK ((captured_amount >= (0)::numeric)),
    CONSTRAINT ledger_reservations_currency_check CHECK ((currency <> ''::text)),
    CONSTRAINT ledger_reservations_estimated_amount_check CHECK ((estimated_amount > (0)::numeric)),
    CONSTRAINT ledger_reservations_idempotency_key_check CHECK ((idempotency_key <> ''::text)),
    CONSTRAINT ledger_reservations_reason_check CHECK ((reason <> ''::text)),
    CONSTRAINT ledger_reservations_released_amount_check CHECK ((released_amount >= (0)::numeric)),
    CONSTRAINT ledger_reservations_status_check CHECK ((status = ANY (ARRAY['authorized'::text, 'captured'::text, 'released'::text])))
);

ALTER SEQUENCE public.ledger_reservations_id_seq OWNED BY public.ledger_reservations.id;

ALTER TABLE ONLY public.ledger_reservations ALTER COLUMN id SET DEFAULT nextval('public.ledger_reservations_id_seq'::regclass);

ALTER TABLE ONLY public.ledger_reservations
    ADD CONSTRAINT ledger_reservations_capture_ledger_entry_id_key UNIQUE (capture_ledger_entry_id);

ALTER TABLE ONLY public.ledger_reservations
    ADD CONSTRAINT ledger_reservations_idempotency_key_key UNIQUE (idempotency_key);

ALTER TABLE ONLY public.ledger_reservations
    ADD CONSTRAINT ledger_reservations_pkey PRIMARY KEY (id);

ALTER TABLE ONLY public.ledger_reservations
    ADD CONSTRAINT ledger_reservations_request_record_id_key UNIQUE (request_record_id);

ALTER TABLE ONLY public.ledger_reservations
    ADD CONSTRAINT uq_ledger_reservations_id_user_request UNIQUE (id, user_id, request_record_id);

CREATE INDEX idx_ledger_reservations_authorized_created_at ON public.ledger_reservations USING btree (created_at, id) WHERE (status = 'authorized'::text);

CREATE INDEX idx_ledger_reservations_user_created_at ON public.ledger_reservations USING btree (user_id, created_at DESC, id DESC);

ALTER TABLE ONLY public.ledger_reservations
    ADD CONSTRAINT fk_ledger_reservations_capture_entry FOREIGN KEY (capture_ledger_entry_id, user_id, request_record_id) REFERENCES public.ledger_entries(id, user_id, request_record_id);

ALTER TABLE ONLY public.ledger_reservations
    ADD CONSTRAINT fk_ledger_reservations_request_user FOREIGN KEY (request_record_id, user_id) REFERENCES public.request_records(id, user_id);

ALTER TABLE ONLY public.ledger_reservations
    ADD CONSTRAINT ledger_reservations_user_id_fkey FOREIGN KEY (user_id) REFERENCES public.users(id);
