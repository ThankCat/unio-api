ALTER TABLE public.routes
    DROP CONSTRAINT IF EXISTS routes_concurrency_limit_check,
    DROP COLUMN IF EXISTS concurrency_limit;
