-- Provider balance 是 Unio 按成本快照维护的内部余额投影；允许负数，不参与路由或运行态判断。
CREATE SEQUENCE public.provider_balances_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;

CREATE TABLE public.provider_balances (
    id bigint NOT NULL,
    provider_id bigint NOT NULL,
    currency text NOT NULL,
    balance numeric(20,10) DEFAULT 0 NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT provider_balances_currency_check CHECK (btrim(currency) <> '')
);

ALTER SEQUENCE public.provider_balances_id_seq OWNED BY public.provider_balances.id;

ALTER TABLE ONLY public.provider_balances
    ALTER COLUMN id SET DEFAULT nextval('public.provider_balances_id_seq'::regclass);

ALTER TABLE ONLY public.provider_balances
    ADD CONSTRAINT provider_balances_pkey PRIMARY KEY (id);

ALTER TABLE ONLY public.provider_balances
    ADD CONSTRAINT provider_balances_provider_currency_key UNIQUE (provider_id, currency);

ALTER TABLE ONLY public.provider_balances
    ADD CONSTRAINT provider_balances_provider_id_fkey FOREIGN KEY (provider_id) REFERENCES public.providers(id);
