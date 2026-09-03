ALTER TABLE public.subscription_accounts
    DROP CONSTRAINT IF EXISTS subscription_accounts_proxy_id_fkey,
    DROP COLUMN IF EXISTS proxy_id;

ALTER TABLE public.channels
    DROP CONSTRAINT IF EXISTS channels_proxy_id_fkey,
    DROP COLUMN IF EXISTS proxy_id;

DROP TABLE IF EXISTS public.proxies;
