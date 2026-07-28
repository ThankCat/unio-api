ALTER TABLE public.routes
    ADD COLUMN concurrency_limit integer;

ALTER TABLE public.routes
    ADD CONSTRAINT routes_concurrency_limit_check
    CHECK (concurrency_limit IS NULL OR concurrency_limit >= 0);
