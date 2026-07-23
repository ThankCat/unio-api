-- Ledger billing exception 是结算中的平台核销或风险敞口审计事实。
CREATE SEQUENCE public.ledger_billing_exceptions_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;

CREATE TABLE public.ledger_billing_exceptions (
    -- id: 主键。--
    id bigint NOT NULL,
    -- user_id: 异常所属用户 ID。--
    user_id bigint NOT NULL,
    -- request_record_id: 异常对应的请求记录 ID。--
    request_record_id bigint NOT NULL,
    -- reservation_id: 异常对应的预授权 ID。--
    reservation_id bigint NOT NULL,
    -- event_type: 异常事件类型。--
    event_type text NOT NULL,
    -- actual_amount: 真实应结算金额，风险敞口场景为空。--
    actual_amount numeric(20,10),
    -- captured_amount: 已从用户冻结金额中 capture 的金额。--
    captured_amount numeric(20,10) NOT NULL,
    -- platform_amount: 平台承担的金额。--
    platform_amount numeric(20,10) NOT NULL,
    -- currency: 异常金额币种。--
    currency text NOT NULL,
    -- reason_code: 稳定原因码。--
    reason_code text NOT NULL,
    -- reason: 可审计的业务原因。--
    reason text NOT NULL,
    -- created_at: 记录创建时间。--
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT ck_ledger_billing_exceptions_amounts CHECK ((((event_type = 'write_off'::text) AND (actual_amount IS NOT NULL) AND (captured_amount < actual_amount) AND (platform_amount = (actual_amount - captured_amount))) OR ((event_type = 'risk_exposure'::text) AND (actual_amount IS NULL) AND (captured_amount = (0)::numeric)))),
    CONSTRAINT ledger_billing_exceptions_actual_amount_check CHECK (((actual_amount IS NULL) OR (actual_amount > (0)::numeric))),
    CONSTRAINT ledger_billing_exceptions_captured_amount_check CHECK ((captured_amount >= (0)::numeric)),
    CONSTRAINT ledger_billing_exceptions_currency_check CHECK ((currency <> ''::text)),
    CONSTRAINT ledger_billing_exceptions_event_type_check CHECK ((event_type = ANY (ARRAY['write_off'::text, 'risk_exposure'::text]))),
    CONSTRAINT ledger_billing_exceptions_platform_amount_check CHECK ((platform_amount > (0)::numeric)),
    CONSTRAINT ledger_billing_exceptions_reason_check CHECK ((reason <> ''::text)),
    CONSTRAINT ledger_billing_exceptions_reason_code_check CHECK ((reason_code <> ''::text))
);

ALTER SEQUENCE public.ledger_billing_exceptions_id_seq OWNED BY public.ledger_billing_exceptions.id;

ALTER TABLE ONLY public.ledger_billing_exceptions ALTER COLUMN id SET DEFAULT nextval('public.ledger_billing_exceptions_id_seq'::regclass);

ALTER TABLE ONLY public.ledger_billing_exceptions
    ADD CONSTRAINT ledger_billing_exceptions_pkey PRIMARY KEY (id);

ALTER TABLE ONLY public.ledger_billing_exceptions
    ADD CONSTRAINT ledger_billing_exceptions_request_record_id_key UNIQUE (request_record_id);

ALTER TABLE ONLY public.ledger_billing_exceptions
    ADD CONSTRAINT ledger_billing_exceptions_reservation_id_key UNIQUE (reservation_id);

CREATE INDEX idx_ledger_billing_exceptions_user_created_at ON public.ledger_billing_exceptions USING btree (user_id, created_at DESC, id DESC);

ALTER TABLE ONLY public.ledger_billing_exceptions
    ADD CONSTRAINT fk_ledger_billing_exceptions_request_user FOREIGN KEY (request_record_id, user_id) REFERENCES public.request_records(id, user_id);

ALTER TABLE ONLY public.ledger_billing_exceptions
    ADD CONSTRAINT fk_ledger_billing_exceptions_reservation FOREIGN KEY (reservation_id, user_id, request_record_id) REFERENCES public.ledger_reservations(id, user_id, request_record_id);

ALTER TABLE ONLY public.ledger_billing_exceptions
    ADD CONSTRAINT ledger_billing_exceptions_user_id_fkey FOREIGN KEY (user_id) REFERENCES public.users(id);
