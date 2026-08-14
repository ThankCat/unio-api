-- ADR-0019 distinguishes an explicit Model delist from a temporary global pause.
ALTER TABLE public.route_model_offerings
    DROP CONSTRAINT route_model_offerings_disabled_reason_check;

ALTER TABLE public.route_model_offerings
    ADD CONSTRAINT route_model_offerings_disabled_reason_check CHECK (
        disabled_reason IS NULL
        OR disabled_reason = ANY (ARRAY[
            'manual_unselected'::text,
            'model_disabled'::text,
            'model_delisted'::text,
            'binding_disabled'::text,
            'channel_disabled'::text,
            'route_channel_removed'::text,
            'channel_protocol_changed'::text,
            'migration_backfill'::text
        ])
    );
