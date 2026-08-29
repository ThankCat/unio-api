-- providers.currency：供应商的结算币种（多货币支持 Phase 1，PLAN M2/D3）。
-- 语义 = 该供应商账单/余额以什么货币计价；channel_prices 成本与 provider 余额记账都跟随此币种。
-- 一渠道一币种（D3）：channel_prices.currency 必须等于 provider.currency（000057 触发器强制）；
-- 产生任何 channel_prices / provider_ledger_entries / provider_balances 引用后不可修改（应用层强制）。
-- 扩展新币种 = 扩 CHECK + exchange_rates 补该币种汇率 + 应用层白名单。
ALTER TABLE public.providers
    ADD COLUMN currency text DEFAULT 'USD' NOT NULL;

ALTER TABLE public.providers
    ADD CONSTRAINT providers_currency_check CHECK ((currency = ANY (ARRAY['USD'::text, 'CNY'::text])));

-- 存量盘点结论（PLAN §12.C.2，2026-08-29 业务确认）：现存 provider 全部为「直接 CNY 计价」
-- （报价数值即 CNY：上游按 1:1 兑名义 USD、充值均为 CNY），统一改标 CNY。
-- 全新数据库此 UPDATE 影响 0 行，无副作用。
UPDATE public.providers SET currency = 'CNY';
