-- User balance 是用户当前余额投影，最终事实仍以 ledger_entries 为准。
CREATE SEQUENCE public.user_balances_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;

CREATE TABLE public.user_balances (
    -- id: 主键。--
    id bigint NOT NULL,
    -- user_id: 余额所属用户 ID。--
    user_id bigint NOT NULL,
    -- currency: 余额币种。--
    currency text NOT NULL,
    -- balance: 用户总余额。--
    balance numeric(20,10) DEFAULT 0 NOT NULL,
    -- reserved_balance: 已冻结但尚未 capture/release 的余额。--
    reserved_balance numeric(20,10) DEFAULT 0 NOT NULL,
    -- created_at: 记录创建时间。--
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    -- updated_at: 记录更新时间。--
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT ck_user_balances_reserved_not_above_balance CHECK ((reserved_balance <= balance)),
    CONSTRAINT user_balances_balance_check CHECK ((balance >= (0)::numeric)),
    CONSTRAINT user_balances_currency_check CHECK ((currency <> ''::text)),
    CONSTRAINT user_balances_reserved_balance_check CHECK ((reserved_balance >= (0)::numeric))
);

ALTER SEQUENCE public.user_balances_id_seq OWNED BY public.user_balances.id;

ALTER TABLE ONLY public.user_balances ALTER COLUMN id SET DEFAULT nextval('public.user_balances_id_seq'::regclass);

ALTER TABLE ONLY public.user_balances
    ADD CONSTRAINT user_balances_pkey PRIMARY KEY (id);

ALTER TABLE ONLY public.user_balances
    ADD CONSTRAINT user_balances_user_id_currency_key UNIQUE (user_id, currency);

ALTER TABLE ONLY public.user_balances
    ADD CONSTRAINT user_balances_user_id_fkey FOREIGN KEY (user_id) REFERENCES public.users(id);
