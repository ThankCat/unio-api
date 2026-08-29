ALTER TABLE public.cost_snapshots DROP CONSTRAINT ck_cost_snapshots_fx;
ALTER TABLE public.cost_snapshots
    DROP COLUMN fx_rate,
    DROP COLUMN fx_rate_date,
    DROP COLUMN total_cost_amount_usd;
