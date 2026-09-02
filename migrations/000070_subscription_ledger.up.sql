-- 订阅费用台账（docs/changes/2026-09-02-account-pool 批次一）。
--
-- 订阅是「固定周期费用」，与按 token 计费的成本模型不同：池型渠道的每请求成本快照恒为 0，
-- 真实支出记在这里。摊销单价（订阅费 ÷ 该号产出 token）与利用率由离线查询计算，
-- 不进实时结算链路——避免把估算值写进不可改写的结算快照。
--
-- 台账挂在账号维度而非渠道：号是逐个购买、逐个续费的，续费一次写一行新记录。

CREATE SEQUENCE public.subscription_ledger_entries_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;

CREATE TABLE public.subscription_ledger_entries (
    -- id: 主键。--
    id bigint NOT NULL,
    -- account_id: 费用归属账号。账号归档后台账仍需保留，故不做级联删除。--
    account_id bigint NOT NULL,
    -- amount: 本期支付金额（原币）。允许 0，表示赠送或试用期。--
    amount numeric(20,10) NOT NULL,
    -- currency: 支付币种（ISO 4217）。与服务商结算币种可以不同，离线折算时按当期汇率处理。--
    currency text NOT NULL,
    -- period_start: 计费周期开始。--
    period_start timestamp with time zone NOT NULL,
    -- period_end: 计费周期结束；摊销按 [period_start, period_end) 内的请求量分摊。--
    period_end timestamp with time zone NOT NULL,
    -- note: 备注（订单号、支付渠道等），审计用。--
    note text,
    -- created_by: 录入者标识（审计）。--
    created_by text,
    -- created_at: 记录创建时间。--
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    -- updated_at: 记录更新时间。--
    updated_at timestamp with time zone DEFAULT now() NOT NULL
);

ALTER TABLE public.subscription_ledger_entries
    ALTER COLUMN id SET DEFAULT nextval('public.subscription_ledger_entries_id_seq');

ALTER SEQUENCE public.subscription_ledger_entries_id_seq
    OWNED BY public.subscription_ledger_entries.id;

ALTER TABLE ONLY public.subscription_ledger_entries
    ADD CONSTRAINT subscription_ledger_entries_pkey PRIMARY KEY (id);

ALTER TABLE ONLY public.subscription_ledger_entries
    ADD CONSTRAINT subscription_ledger_entries_account_id_fkey
        FOREIGN KEY (account_id) REFERENCES public.subscription_accounts(id);

ALTER TABLE public.subscription_ledger_entries
    ADD CONSTRAINT subscription_ledger_entries_amount_check
        CHECK (amount >= 0),
    ADD CONSTRAINT subscription_ledger_entries_period_check
        CHECK (period_end > period_start);

-- 按账号查台账（详情页与离线摊销都按此维度聚合）。
CREATE INDEX subscription_ledger_entries_account_period_idx
    ON public.subscription_ledger_entries (account_id, period_start DESC);
