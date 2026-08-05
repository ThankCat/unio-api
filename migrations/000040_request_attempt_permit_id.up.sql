ALTER TABLE public.request_attempts
    ADD COLUMN permit_id text;

ALTER TABLE public.request_attempts
    ADD CONSTRAINT request_attempts_permit_id_check
        CHECK (permit_id IS NULL OR btrim(permit_id) <> '');

CREATE UNIQUE INDEX uq_request_attempts_permit_id
    ON public.request_attempts (permit_id)
    WHERE permit_id IS NOT NULL;
