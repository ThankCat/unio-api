CREATE SEQUENCE IF NOT EXISTS public.schema_health_checks_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;

CREATE TABLE IF NOT EXISTS public.schema_health_checks (
    id bigint NOT NULL DEFAULT nextval('public.schema_health_checks_id_seq'::regclass),
    name text NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT schema_health_checks_pkey PRIMARY KEY (id),
    CONSTRAINT schema_health_checks_name_key UNIQUE (name)
);

ALTER SEQUENCE public.schema_health_checks_id_seq OWNED BY public.schema_health_checks.id;
